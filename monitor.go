package main

// Server status monitoring, mirroring lazymc's monitor.rs.

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"strings"
	"time"

	"lazymc/proto"
)

// Monitor poll interval in seconds.
const monitorPollInterval = 2 * time.Second

// Status request timeout in seconds.
const statusTimeout = 20 * time.Second

// Ping request timeout in seconds.
const pingTimeout = 10 * time.Second

// MonitorServer polls the server status and sleeps/kills the server as
// needed.
func MonitorServer(config *Config, server *ServerState) {
	addr := config.Server.Address

	ticker := time.NewTicker(monitorPollInterval)
	defer ticker.Stop()

	for range ticker.C {
		// Poll server state and update internal status
		TraceLog(TargetMonitor, "Fetching status for %s ... ", addr)
		status, ok, pingFallback := pollServer(config, server, addr)
		switch {
		case ok && status != nil:
			server.UpdateStatus(config, status)
			if config.Server.ForgeStatusFile != "" && len(status.ForgeData) != 0 {
				if err := persistForgeStatusSnapshot(config, status.ForgeData); err != nil {
					WarnLog(TargetMonitor, "Failed to persist Forge status metadata: %v", err)
				}
			}

		// Error, reset status
		case !ok:
			server.UpdateStatus(config, nil)

		// Didn't get status, but ping fallback worked, leave as-is
		case pingFallback:
			WarnLog(TargetMonitor, "Failed to poll server status, ping fallback succeeded")
		}

		// Sleep server when it's bedtime
		if server.ShouldSleep(config) {
			InfoLog(TargetMonitor, "Server has been idle, sleeping...")
			server.Stop(config)
		}

		// Check whether we should force kill server
		if server.ShouldKill() {
			if config.Server.ControllerManaged {
				if server.ExpireControllerManagedTransition(config) {
					WarnLog(TargetMonitor, "Controller-managed server did not become ready before the start timeout")
				}
				continue
			}
			ErrorLog(TargetMonitor, "Force killing server, took too long to start or stop")
			if !server.ForceKill(config) {
				WarnLog(Target, "Failed to force kill server")
			}
		}
	}
}

// pollServer polls server state.
//
// Returns (status, ok, pingFallback): ok=false on error, pingFallback=true
// when only the ping fallback succeeded.
func pollServer(config *Config, server *ServerState, addr SocketAddr) (*proto.ServerStatus, bool, bool) {
	// Fetch status
	if status, ok := fetchStatus(config, addr); ok {
		return status, true, false
	}

	// Try ping fallback if server is currently started
	if server.GetState() == StateStarted {
		DebugLog(TargetMonitor, "Failed to get status from started server, trying ping...")
		if doPing(config, addr) {
			return nil, true, true
		}
	}

	return nil, false, false
}

// fetchStatus attempts to fetch the server status.
func fetchStatus(config *Config, addr SocketAddr) (*proto.ServerStatus, bool) {
	conn, err := net.DialTimeout("tcp", addr.String(), 10*time.Second)
	if err != nil {
		return nil, false
	}
	defer conn.Close()

	// Add proxy header
	if config.Server.SendProxyV2 {
		TraceLog(TargetMonitor, "Sending local proxy header for server connection")
		header, err := LocalProxyHeader()
		if err != nil {
			return nil, false
		}
		if _, err := conn.Write(header); err != nil {
			return nil, false
		}
	}

	// Dummy client
	client := proto.DummyClient()

	if !sendHandshake(client, conn, config, addr) {
		return nil, false
	}
	if !requestStatus(client, conn) {
		return nil, false
	}
	return waitForStatusTimeout(client, conn)
}

// doPing attempts to ping the server.
func doPing(config *Config, addr SocketAddr) bool {
	conn, err := net.DialTimeout("tcp", addr.String(), 10*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()

	// Add proxy header
	if config.Server.SendProxyV2 {
		TraceLog(TargetMonitor, "Sending local proxy header for server connection")
		header, err := LocalProxyHeader()
		if err != nil {
			return false
		}
		if _, err := conn.Write(header); err != nil {
			return false
		}
	}

	// Dummy client
	client := proto.DummyClient()

	if !sendHandshake(client, conn, config, addr) {
		return false
	}
	token, ok := sendPing(client, conn)
	if !ok {
		return false
	}
	return waitForPingTimeout(client, conn, token)
}

// sendHandshake sends a status handshake.
func sendHandshake(client *proto.Client, conn net.Conn, config *Config, addr SocketAddr) bool {
	w := proto.NewPacketWriter()
	w.WriteVarInt(int32(config.Public.Protocol))
	w.WriteString(addr.IP.String())
	w.WriteUint16(uint16(addr.Port))
	w.WriteVarInt(proto.ClientStateStatus.ToID())

	packet := proto.NewRawPacket(proto.PacketHandshake, w.Bytes())
	encoded, ok := packet.EncodeWithLen(client)
	if !ok {
		return false
	}
	_, err := conn.Write(encoded)
	return err == nil
}

// requestStatus sends a status request packet.
func requestStatus(client *proto.Client, conn net.Conn) bool {
	packet := proto.NewRawPacket(proto.PacketServerStatusRequest, nil)
	encoded, ok := packet.EncodeWithLen(client)
	if !ok {
		return false
	}
	_, err := conn.Write(encoded)
	return err == nil
}

// sendPing sends a ping request, returning the token.
func sendPing(client *proto.Client, conn net.Conn) (uint64, bool) {
	token := randomToken()
	w := proto.NewPacketWriter()
	w.WriteUint64(token)

	packet := proto.NewRawPacket(proto.PacketServerPing, w.Bytes())
	encoded, ok := packet.EncodeWithLen(client)
	if !ok {
		return 0, false
	}
	if _, err := conn.Write(encoded); err != nil {
		return 0, false
	}
	return token, true
}

// randomToken generates a random u64 ping token.
func randomToken() uint64 {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return uint64(time.Now().UnixNano())
	}
	return n.Uint64()
}

// waitForStatus waits for a status response, returning the parsed status.
func waitForStatus(client *proto.Client, conn net.Conn) (*proto.ServerStatus, bool) {
	var buf []byte
	for {
		packet, _, more, err := proto.ReadPacket(client, &buf, conn)
		if err != nil {
			// Malformed packet, skip it and continue
			if errors.Is(err, proto.ErrMalformedPacket) {
				continue
			}
			return nil, false
		}
		if !more {
			return nil, false
		}

		// Catch status response
		if packet.ID == proto.PacketClientStatusResponse {
			status, ok := decodeStatusResponse(packet.Data)
			if !ok {
				DebugLog(Target, "Failed to decode status response from backend server, ignoring")
				continue
			}
			return status, true
		}
	}
}

// waitForStatusTimeout waits for a status response with a timeout.
func waitForStatusTimeout(client *proto.Client, conn net.Conn) (*proto.ServerStatus, bool) {
	type result struct {
		status *proto.ServerStatus
		ok     bool
	}
	done := make(chan result, 1)
	go func() {
		status, ok := waitForStatus(client, conn)
		done <- result{status, ok}
	}()

	select {
	case r := <-done:
		return r.status, r.ok
	case <-time.After(statusTimeout):
		return nil, false
	}
}

// waitForPing waits for a ping response matching the token.
func waitForPing(client *proto.Client, conn net.Conn, token uint64) bool {
	var buf []byte
	for {
		packet, _, more, err := proto.ReadPacket(client, &buf, conn)
		if err != nil {
			// Malformed packet, skip it and continue
			if errors.Is(err, proto.ErrMalformedPacket) {
				continue
			}
			return false
		}
		if !more {
			return false
		}

		// Catch ping response
		if packet.ID == proto.PacketClientPing {
			if len(packet.Data) >= 8 {
				got := binary.BigEndian.Uint64(packet.Data[:8])
				if got == token {
					return true
				}
				DebugLog(Target, "Got unmatched ping response when polling server status by ping")
			}
		}
	}
}

// waitForPingTimeout waits for a ping response with a timeout.
func waitForPingTimeout(client *proto.Client, conn net.Conn, token uint64) bool {
	done := make(chan bool, 1)
	go func() {
		done <- waitForPing(client, conn, token)
	}()

	select {
	case r := <-done:
		return r
	case <-time.After(pingTimeout):
		return false
	}
}

// decodeStatusResponse parses a status response payload (length-prefixed
// JSON string).
func decodeStatusResponse(data []byte) (*proto.ServerStatus, bool) {
	// Strip the string length prefix
	consumed, strLen, ok := proto.ReadVarInt(data)
	if !ok || strLen < 0 || consumed+int(strLen) > len(data) {
		return nil, false
	}
	jsonData := data[consumed : consumed+int(strLen)]

	var raw struct {
		Version struct {
			Name     string `json:"name"`
			Protocol uint32 `json:"protocol"`
		} `json:"version"`
		Players struct {
			Max    uint32 `json:"max"`
			Online uint32 `json:"online"`
			Sample []struct {
				Name string `json:"name"`
				ID   string `json:"id"`
			} `json:"sample"`
		} `json:"players"`
		Description json.RawMessage `json:"description"`
		Favicon     *string         `json:"favicon"`
		ForgeData   json.RawMessage `json:"forgeData"`
	}

	if err := json.Unmarshal(jsonData, &raw); err != nil {
		return nil, false
	}
	description, ok := decodeStatusDescription(raw.Description, 0)
	if !ok {
		return nil, false
	}

	status := &proto.ServerStatus{
		Version: proto.ServerVersion{
			Name:     raw.Version.Name,
			Protocol: raw.Version.Protocol,
		},
		Players: proto.OnlinePlayers{
			Max:    raw.Players.Max,
			Online: raw.Players.Online,
		},
		Description: description,
		Favicon:     raw.Favicon,
		ForgeData:   append(json.RawMessage(nil), raw.ForgeData...),
	}
	for _, p := range raw.Players.Sample {
		status.Players.Sample = append(status.Players.Sample, proto.OnlinePlayer{Name: p.Name, ID: p.ID})
	}

	return status, true
}

// decodeStatusDescription accepts both the legacy string MOTD and the modern
// chat-component form emitted by Forge 1.20.1. The proxy stores a bounded plain
// text rendering because its own status response model is intentionally small.
func decodeStatusDescription(raw json.RawMessage, depth int) (string, bool) {
	if depth > 16 || len(raw) == 0 {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, true
	}
	var component struct {
		Text  string            `json:"text"`
		Extra []json.RawMessage `json:"extra"`
	}
	if err := json.Unmarshal(raw, &component); err == nil && (component.Text != "" || component.Extra != nil || string(raw) == "{}") {
		var out strings.Builder
		out.WriteString(component.Text)
		for _, child := range component.Extra {
			value, ok := decodeStatusDescription(child, depth+1)
			if !ok {
				return "", false
			}
			out.WriteString(value)
		}
		return out.String(), true
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err == nil {
		var out strings.Builder
		for _, child := range parts {
			value, ok := decodeStatusDescription(child, depth+1)
			if !ok {
				return "", false
			}
			out.WriteString(value)
		}
		return out.String(), true
	}
	return "", false
}
