package main

// Server probing, mirroring lazymc's probe.rs.

import (
	"errors"
	"net"
	"time"

	"lazymc/mc"
	"lazymc/proto"
	"lazymc/proto/packets/play"
)

// Minecraft username to use for probing the server.
const probeUser = "_lazymc_probe"

// Timeout for probe user connecting to the server.
const probeConnectTimeout = 30 * time.Second

// Maximum time the probe may wait for the server to come online.
const probeOnlineTimeout = 10 * time.Minute

// Timeout for receiving join game packet.
const probeJoinGameTimeout = 20 * time.Second

// Probe connects to the Minecraft server and probes useful details from it.
func Probe(config *Config, server *ServerState) error {
	DebugLog(TargetProbe, "Starting server probe...")

	// Start server if not starting already
	if server.Start(config, nil) {
		InfoLog(TargetProbe, "Starting server to probe...")
	}

	// Wait for server to come online
	if !waitUntilOnline(server) {
		WarnLog(TargetProbe, "Couldn't probe server, failed to wait for server to come online")
		return errors.New("failed to wait for server to come online")
	}

	DebugLog(TargetProbe, "Connecting to server to probe details...")

	// Connect to server, record Forge payload
	forgePayload, err := probeConnectToServer(config, server)
	if err != nil {
		return err
	}
	server.SetForgePayload(forgePayload)

	return nil
}

// waitUntilOnline waits for the server to come online.
//
// Returns true when it is online.
func waitUntilOnline(server *ServerState) bool {
	TraceLog(TargetProbe, "Waiting for server to come online...")

	// A task to wait for suitable server state
	done := make(chan bool, 1)
	go func() {
		stateCh := server.Subscribe()
		for state := range stateCh {
			switch state {
			// Still waiting on server start
			case StateStarting:
				continue

			// Server started, start relaying and proxy
			case StateStarted:
				done <- true
				return

			// Server stopping, this shouldn't happen, skip
			case StateStopping:
				WarnLog(TargetProbe, "Server stopping while trying to probe, skipping")
				done <- false
				return

			// Server stopped, this shouldn't happen, skip
			case StateStopped:
				ErrorLog(TargetProbe, "Server stopped while trying to probe, skipping")
				done <- false
				return
			}
		}
		done <- false
	}()

	// Wait for server state with timeout
	select {
	case online := <-done:
		return online
	case <-time.After(probeOnlineTimeout):
		WarnLog(TargetProbe, "Probe waited for server to come online but timed out after %ds", int(probeOnlineTimeout.Seconds()))
		return false
	}
}

// connectToServer creates a connection to the server, with timeout.
//
// This initializes the connection to the play state. Returns the recorded
// Forge login payload if any.
func probeConnectToServer(config *Config, server *ServerState) ([][]byte, error) {
	type result struct {
		payload [][]byte
		err     error
	}
	done := make(chan result, 1)
	go func() {
		payload, err := probeConnectToServerNoTimeout(config, server)
		done <- result{payload, err}
	}()

	select {
	case r := <-done:
		return r.payload, r.err
	case <-time.After(probeConnectTimeout):
		ErrorLog(TargetProbe, "Probe tried to connect to server but timed out after %ds", int(probeConnectTimeout.Seconds()))
		return nil, errors.New("probe connect timeout")
	}
}

// connectToServerNoTimeout initializes a connection to the play state.
func probeConnectToServerNoTimeout(config *Config, server *ServerState) ([][]byte, error) {
	// Open connection
	outbound, err := net.DialTimeout("tcp", config.Server.Address.String(), 10*time.Second)
	if err != nil {
		return nil, err
	}

	// Construct temporary server client
	tmpClient := proto.DummyClient()
	tmpClient.SetState(proto.ClientStateLogin)

	// Construct client info
	p := config.Public.Protocol
	tmpClientInfo := proto.EmptyClientInfo()
	tmpClientInfo.Protocol = &p
	probeUUID := mc.OfflinePlayerUUID(probeUser)
	probeProfile := &proto.GameProfile{UUID: probeUUID, Name: probeUser}
	tmpClientInfo.Profile = probeProfile

	// Select server address to use, add magic if Forge
	serverAddr := config.Server.Address.IP.String()
	if config.Server.Forge {
		serverAddr += forgeStatusMagic(config.Public.Protocol)
	}

	// Send handshake packet
	w := proto.NewPacketWriter()
	w.WriteVarInt(int32(config.Public.Protocol))
	w.WriteString(serverAddr)
	w.WriteUint16(uint16(config.Server.Address.Port))
	w.WriteVarInt(proto.ClientStateLogin.ToID())
	handshake := proto.NewRawPacket(proto.PacketHandshake, w.Bytes())
	if err := writePacketTo(tmpClient, outbound, handshake); err != nil {
		outbound.Close()
		return nil, err
	}

	// Request login start
	lw := proto.NewPacketWriter()
	lw.WriteString(probeUser)
	if config.Public.Protocol >= 759 {
		lw.WriteBool(true)
		lw.Write(probeUUID[:])
	}
	loginStart := proto.NewRawPacket(proto.PacketServerLoginStart, lw.Bytes())
	if err := writePacketTo(tmpClient, outbound, loginStart); err != nil {
		outbound.Close()
		return nil, err
	}

	// Incoming buffer, record Forge plugin request payload
	var buf []byte
	var forgePayload [][]byte
	profileForwarded := false

	for {
		// Read packet from stream
		packet, raw, more, err := proto.ReadPacket(tmpClient, &buf, outbound)
		if err != nil {
			if errors.Is(err, proto.ErrMalformedPacket) {
				ErrorLog(TargetForge, "Closing connection, error occurred")
			}
			break
		}
		if !more {
			break
		}

		// Grab client state
		clientState := tmpClient.State()

		// Catch set compression
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketClientSetCompression {
			// Decode compression packet
			setCompression, err := proto.DecodeSetCompression(packet.Data)
			if err != nil {
				break
			}

			// Client and server compression threshold should match
			if setCompression.Threshold != proto.CompressionThreshold {
				ErrorLog(TargetForge, "Compression threshold sent to lobby client does not match threshold from server, this may cause errors (client: %d, server: %d)", proto.CompressionThreshold, setCompression.Threshold)
			}

			// Set client compression
			tmpClient.SetCompression(setCompression.Threshold)
			continue
		}

		// Catch login plugin request
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketClientLoginPluginRequest {
			// Decode login plugin request packet
			pluginRequest, err := proto.DecodeLoginPluginRequest(packet.Data)
			if err != nil {
				ErrorLog(TargetProbe, "Failed to decode login plugin request from server, cannot respond properly: %v", err)
				continue
			}

			// A proxy-compatible offline backend authenticates the probe through
			// the same signed profile-forwarding boundary as real lobby clients.
			// This request is not Forge lobby payload and must not be replayed to
			// clients.
			if pluginRequest.Channel == velocityPlayerInfoChannel {
				if err := respondVelocityForwarding(tmpClient, outbound, pluginRequest, probeProfile, outbound.LocalAddr(), config); err != nil {
					outbound.Close()
					return nil, err
				}
				profileForwarded = true
				continue
			}

			// Handle and record only the Forge FML2 login wrapper payload.
			if config.Server.Forge && pluginRequest.Channel == forgeChannelLoginWrapper {
				// Record Forge login payload
				forgePayload = append(forgePayload, raw)

				// Respond to Forge login plugin request
				if err := respondLoginPluginRequest(tmpClient, outbound, pluginRequest); err != nil {
					outbound.Close()
					return nil, err
				}
				continue
			}

			WarnLog(TargetProbe, "Got unexpected login plugin request, responding with error")

			// Respond with plugin response failure
			rw := proto.NewPacketWriter()
			rw.WriteVarInt(pluginRequest.MessageID)
			rw.WriteBool(false)
			response := proto.NewRawPacket(proto.PacketServerLoginPluginResponse, rw.Bytes())
			if err := writePacketTo(tmpClient, outbound, response); err != nil {
				outbound.Close()
				return nil, err
			}

			continue
		}

		// Hijack login success
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketClientLoginSuccess {
			if config.Auth.OnlineMode && !profileForwarded {
				outbound.Close()
				return nil, errors.New("backend accepted probe without requesting authenticated profile forwarding")
			}
			TraceLog(TargetProbe, "Got login success from server connection, change to play mode")

			// Switch to play state
			tmpClient.SetState(proto.ClientStatePlay)

			// Wait to catch join game packet
			joinGameData, err := waitForServerJoinGame(tmpClient, &tmpClientInfo, outbound, &buf, TargetProbe)
			if err != nil {
				outbound.Close()
				return nil, err
			}
			server.SetProbedJoinGame(joinGameData)

			// Gracefully close connection
			CloseTCPStream(outbound)

			return forgePayload, nil
		}

		// Show unhandled packet warning
		DebugLog(TargetForge, "Got unhandled packet from server in connect_to_server:")
		DebugLog(TargetForge, "- State: %v", clientState)
		DebugLog(TargetForge, "- Packet ID: 0x%02X (%d)", packet.ID, packet.ID)
	}

	// Gracefully close connection
	CloseTCPStream(outbound)

	return nil, errors.New("probe connection ended unexpectedly")
}

// waitForServerJoinGame waits for the join game packet on the server
// connection, with timeout.
func waitForServerJoinGame(client *proto.Client, clientInfo *proto.ClientInfo, outbound net.Conn, buf *[]byte, target string) (*play.JoinGameData, error) {
	type result struct {
		data *play.JoinGameData
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := waitForServerJoinGameNoTimeout(client, clientInfo, outbound, buf, target)
		done <- result{data, err}
	}()

	select {
	case r := <-done:
		return r.data, r.err
	case <-time.After(probeJoinGameTimeout):
		ErrorLog(target, "Waiting for for game data from server for probe client timed out after %ds", int(probeJoinGameTimeout.Seconds()))
		return nil, errors.New("join game timeout")
	}
}

// waitForServerJoinGameNoTimeout parses, consumes and returns the join game
// packet. Any PLAY packets that precede JoinGame are preserved in buf so the
// lobby handoff can forward them after its synthetic Respawn instead of
// silently dropping backend registration or custom-payload state.
func waitForServerJoinGameNoTimeout(client *proto.Client, clientInfo *proto.ClientInfo, outbound net.Conn, buf *[]byte, target string) (*play.JoinGameData, error) {
	var queued []byte
	for {
		// Read packet from stream
		packet, raw, more, err := proto.ReadPacket(client, buf, outbound)
		if err != nil {
			if errors.Is(err, proto.ErrMalformedPacket) {
				ErrorLog(target, "Closing connection, error occurred")
			}
			return nil, err
		}
		if !more {
			break
		}

		// Catch join game
		if play.JoinGameIsPacket(clientInfo, packet.ID) {
			// Parse join game data
			joinGameData, err := play.JoinGameDataFromPacket(clientInfo, packet)
			if err != nil {
				WarnLog(target, "Failed to parse join game packet: %v", err)
				return nil, err
			}
			if len(queued) > 0 {
				remaining := append([]byte(nil), (*buf)...)
				*buf = append(queued, remaining...)
			}
			return joinGameData, nil
		}

		if len(queued)+len(raw) > proto.MaxBufferedBytes {
			return nil, proto.ErrBufferLimit
		}
		queued = append(queued, raw...)

		// Show unhandled packet warning
		DebugLog(target, "Got unhandled packet from server in wait_for_server_join_game:")
		DebugLog(target, "- Packet ID: 0x%02X (%d)", packet.ID, packet.ID)
	}

	// Gracefully close connection
	CloseTCPStream(outbound)

	return nil, errors.New("connection ended before join game")
}
