// Package testserver implements a minimal mock Minecraft server used by the
// end-to-end tests. It speaks just enough of the protocol to be a believable
// backend: status responses, login (offline mode), compression, join game
// and keep-alives.
package testserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"lazymc/nbt"
	"lazymc/proto"
)

// State records observations the mock server makes.
type State struct {
	mu       sync.Mutex
	Statuses int     `json:"status_queries"`
	Logins   []Login `json:"logins"`
	Shutdown bool    `json:"shutdown"`
}

// Login records a login attempt.
type Login struct {
	Username string `json:"username"`
	Protocol int32  `json:"protocol"`
	Addr     string `json:"addr"`
	Packets  int    `json:"packets_from_client"`
}

// NewState creates an empty state.
func NewState() *State { return &State{Logins: []Login{}} }

// Snapshot returns a copy of the state.
func (s *State) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{Statuses: s.Statuses, Logins: append([]Login(nil), s.Logins...), Shutdown: s.Shutdown}
}

func (s *State) addStatus() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Statuses++
}

func (s *State) addLogin(l Login) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Logins = append(s.Logins, l)
}

func (s *State) setShutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Shutdown = true
}

// Server is the mock Minecraft server.
type Server struct {
	Addr string
	// State file path, written on changes.
	StateFile string

	state *State
	ln    net.Listener

	// Serializes state file writes.
	writeMu sync.Mutex

	// CaptureDir, when set, records the first 16 bytes of each accepted
	// connection (for proxy header verification in tests).
	CaptureDir string
	captureMu  sync.Mutex
	captureN   int

	connsMu    sync.Mutex
	conns      map[net.Conn]struct{}
	wg         sync.WaitGroup
	acceptDone chan struct{}
}

// NewServer creates and starts the mock server on the given address.
func NewServer(addr, stateFile string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{Addr: ln.Addr().String(), StateFile: stateFile, state: NewState(), ln: ln, conns: map[net.Conn]struct{}{}, acceptDone: make(chan struct{})}
	s.writeState()
	go s.acceptLoop()
	return s, nil
}

// Stop shuts the server down, closing active connections.
func (s *Server) Stop() {
	s.ln.Close()
	// Finish accepting and writing shutdown state before callers remove fixtures.
	<-s.acceptDone
	s.connsMu.Lock()
	for conn := range s.conns {
		conn.Close()
	}
	s.connsMu.Unlock()
	s.wg.Wait()
}

// saveCapture records a proxy header.
func (s *Server) saveCapture(header []byte) {
	s.captureMu.Lock()
	n := s.captureN
	s.captureN++
	s.captureMu.Unlock()
	_ = os.WriteFile(fmt.Sprintf("%s/conn-%d.bin", s.CaptureDir, n), header, 0o644)
}

// peekProxyHeader peeks at a HAProxy v2 header from the buffered reader,
// consuming and returning it if present.
func peekProxyHeader(br *bufio.Reader) ([]byte, bool) {
	sig, err := br.Peek(12)
	if err != nil {
		return nil, false
	}
	expected := []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}
	if !bytes.Equal(sig, expected) {
		return nil, false
	}
	// Signature matches: consume the full header (16 bytes + body length)
	full, err := br.Peek(16)
	if err != nil {
		return nil, false
	}
	length := int(full[14])<<8 | int(full[15])
	header := make([]byte, 16+length)
	if _, err := io.ReadFull(br, header); err != nil {
		return nil, false
	}
	return header, true
}

// bufferedConn is a net.Conn that reads from a bufio.Reader (for peeking).
type bufferedConn struct {
	*bufio.Reader
	net.Conn
}

// Read reads from the buffered reader.
func (c *bufferedConn) Read(b []byte) (int, error) {
	return c.Reader.Read(b)
}

// Shutdown marks the server as shut down and stops it.
func (s *Server) Shutdown() {
	s.state.setShutdown()
	s.writeState()
	s.Stop()
}

func (s *Server) writeState() {
	if s.StateFile == "" {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data, _ := json.MarshalIndent(s.state.Snapshot(), "", "  ")
	// Write atomically so readers never see a truncated file. The temp
	// name is unique per process so concurrent writers (e.g. a previous
	// mock process still shutting down) can't interleave writes.
	tmp := fmt.Sprintf("%s.%d.tmp", s.StateFile, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.StateFile)
}

func (s *Server) acceptLoop() {
	defer close(s.acceptDone)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.state.setShutdown()
			s.writeState()
			return
		}
		s.connsMu.Lock()
		s.conns[conn] = struct{}{}
		s.connsMu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.connsMu.Lock()
				delete(s.conns, conn)
				s.connsMu.Unlock()
			}()
			s.handle(conn)
			s.writeState()
		}()
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()

	// Wrap in a buffered reader so we can peek at a potential proxy header
	// without consuming protocol bytes.
	br := bufio.NewReader(conn)
	conn = &bufferedConn{Reader: br, Conn: conn}

	client := proto.DummyClient()
	var buf []byte

	// Detect and skip a HAProxy v2 proxy header if present, capturing it
	// (like a real server with a proxy plugin installed).
	if header, ok := peekProxyHeader(br); ok {
		if s.CaptureDir != "" {
			s.saveCapture(header)
		}
	}

	// Read handshake
	packet, _, more, err := proto.ReadPacket(client, &buf, conn)
	if err != nil || !more || packet.ID != proto.PacketHandshake {
		return
	}
	handshake, err := proto.DecodeHandshake(packet.Data)
	if err != nil {
		return
	}

	if handshake.NextState == 1 {
		// Status flow
		s.state.addStatus()
		// Read status request
		req, _, more, err := proto.ReadPacket(client, &buf, conn)
		if err != nil || !more || req.ID != proto.PacketServerStatusRequest {
			return
		}
		status := proto.ServerStatus{
			Version:     proto.ServerVersion{Name: "Mock 1.20.4", Protocol: 765},
			Players:     proto.OnlinePlayers{Max: 20, Online: 0, Sample: []proto.OnlinePlayer{}},
			Description: "Mock MOTD",
			Favicon:     nil,
		}
		data := proto.StringBytes(status.StatusResponseJSON())
		writePacket(conn, client, proto.PacketClientStatusResponse, data)

		// Read ping, echo back
		ping, _, more, err := proto.ReadPacket(client, &buf, conn)
		if err != nil || !more || ping.ID != proto.PacketServerPing {
			return
		}
		writePacket(conn, client, proto.PacketClientPing, ping.Data)
		return
	}

	if handshake.NextState == 2 {
		// Login flow (offline mode)
		login := Login{Protocol: handshake.ProtocolVersion, Addr: conn.RemoteAddr().String()}

		// Read login start
		start, _, more, err := proto.ReadPacket(client, &buf, conn)
		if err != nil || !more || start.ID != proto.PacketServerLoginStart {
			return
		}
		ls, err := proto.DecodeLoginStart(start.Data)
		if err != nil {
			return
		}
		login.Username = ls.Name
		s.state.addLogin(login)

		// Set compression
		w := proto.NewPacketWriter()
		w.WriteVarInt(proto.CompressionThreshold)
		writePacket(conn, client, proto.PacketClientSetCompression, w.Bytes())
		client.SetCompression(proto.CompressionThreshold)

		// Login success: send a hyphenated UUID string (version 1 style)
		sw := proto.NewPacketWriter()
		sw.WriteString("9e5f3c9a-2b1d-4e6f-8a7c-0d1e2f3a4b5c")
		sw.WriteString(ls.Name)
		writePacket(conn, client, proto.PacketClientLoginSuccess, sw.Bytes())
		client.SetState(proto.ClientStatePlay)

		// Join game (v1.17)
		codec := minimalCodec()
		dim := nbt.NewCompound()
		jw := proto.NewPacketWriter()
		jw.WriteUint32(1) // entity_id
		jw.WriteBool(false)
		jw.WriteU8(0)   // game_mode survival
		jw.WriteU8(255) // previous game mode
		worldNames := []string{"minecraft:overworld", "minecraft:the_nether", "minecraft:the_end"}
		jw.WriteVarInt(int32(len(worldNames)))
		for _, n := range worldNames {
			jw.WriteString(n)
		}
		nbt.WriteCompoundTag(jw, codec)
		nbt.WriteCompoundTag(jw, dim)
		jw.WriteString("minecraft:overworld")
		jw.WriteInt64(0)
		jw.WriteVarInt(20) // max_players
		jw.WriteVarInt(10) // view_distance
		jw.WriteBool(false)
		jw.WriteBool(true)
		jw.WriteBool(false)
		jw.WriteBool(false)
		writePacket(conn, client, 0x26, jw.Bytes())

		// Keep the connection alive; record client packets
		keepAliveID := uint64(0)
		lastKeepAlive := time.Now()
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		for {
			_, _, more, err := proto.ReadPacket(client, &buf, conn)
			if err == nil && more {
				login.Packets++
				s.state.mu.Lock()
				if len(s.state.Logins) > 0 {
					s.state.Logins[len(s.state.Logins)-1] = login
				}
				s.state.mu.Unlock()
			}
			if time.Since(lastKeepAlive) >= 2*time.Second {
				kw := proto.NewPacketWriter()
				kw.WriteUint64(keepAliveID)
				keepAliveID++
				writePacket(conn, client, 0x21, kw.Bytes())
				lastKeepAlive = time.Now()
			}
			if err != nil || !more {
				return
			}
			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		}
	}
}

// minimalCodec builds a tiny valid dimension codec for the join game packet.
func minimalCodec() *nbt.Compound {
	codec := nbt.NewCompound()

	dimTypes := nbt.NewCompound()
	dimTypes.InsertString("type", "minecraft:dimension_type")
	dim := nbt.NewCompound()
	dim.InsertString("name", "minecraft:overworld")
	dim.InsertInt("id", 0)
	element := nbt.NewCompound()
	element.InsertByte("piglin_safe", 0)
	element.InsertByte("natural", 1)
	element.InsertFloat("ambient_light", 0.0)
	element.InsertString("infiniburn", "minecraft:infiniburn_overworld")
	element.InsertByte("respawn_anchor_works", 0)
	element.InsertByte("has_skylight", 1)
	element.InsertByte("bed_works", 1)
	element.InsertString("effects", "minecraft:overworld")
	element.InsertByte("has_raids", 1)
	element.InsertInt("min_y", 0)
	element.InsertInt("height", 256)
	element.InsertInt("logical_height", 256)
	element.InsertDouble("coordinate_scale", 1.0)
	element.InsertByte("ultrawarm", 0)
	element.InsertByte("has_ceiling", 0)
	dim.Insert("element", &nbt.Tag{Type: nbt.TagCompound, Compound: element})
	dimTypes.Insert("value", &nbt.Tag{Type: nbt.TagList, ListType: nbt.TagCompound, List: []nbt.Tag{{Type: nbt.TagCompound, Compound: dim}}})
	codec.Insert("minecraft:dimension_type", &nbt.Tag{Type: nbt.TagCompound, Compound: dimTypes})

	return codec
}

func writePacket(conn net.Conn, client *proto.Client, id byte, data []byte) {
	packet := proto.NewRawPacket(id, data)
	encoded, ok := packet.EncodeWithLen(client)
	if !ok {
		return
	}
	_, _ = conn.Write(encoded)
}
