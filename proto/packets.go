package proto

// Minecraft protocol packet definitions and helpers, mirroring the
// minecraft-protocol crate's v1_14_4 / v1_20_3 modules.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
)

// Packet IDs for client-bound/server-bound packets.
const (
	// Handshake (server-bound)
	PacketHandshake = 0x00

	// Status (server-bound)
	PacketServerStatusRequest = 0x00
	PacketServerPing          = 0x01

	// Status (client-bound)
	PacketClientStatusResponse = 0x00
	PacketClientPing           = 0x01

	// Login (server-bound)
	PacketServerLoginStart          = 0x00
	PacketServerEncryptionResponse  = 0x01
	PacketServerLoginPluginResponse = 0x02

	// Login (client-bound)
	PacketClientLoginDisconnect    = 0x00
	PacketClientEncryptionRequest  = 0x01
	PacketClientLoginSuccess       = 0x02
	PacketClientSetCompression     = 0x03
	PacketClientLoginPluginRequest = 0x04

	// Play (client-bound, v1_14_4)
	PacketGameDisconnect = 0x1A
)

// Handshake is the server-bound handshake packet.
type Handshake struct {
	ProtocolVersion int32
	ServerAddr      string
	ServerPort      uint16
	NextState       int32
}

// Encode writes the handshake data (without packet id).
func (h Handshake) Encode(w *PacketWriter) {
	w.WriteVarInt(h.ProtocolVersion)
	w.WriteString(h.ServerAddr)
	w.WriteUint16(h.ServerPort)
	w.WriteVarInt(h.NextState)
}

// DecodeHandshake parses a handshake from data.
func DecodeHandshake(data []byte) (Handshake, error) {
	r := &packetReader{data: data}
	h := Handshake{}
	var ok bool
	h.ProtocolVersion, ok = r.varInt()
	if !ok {
		return h, fmt.Errorf("malformed handshake")
	}
	h.ServerAddr, ok = r.str(255)
	if !ok {
		return h, fmt.Errorf("malformed handshake")
	}
	h.ServerPort, ok = r.u16()
	if !ok {
		return h, fmt.Errorf("malformed handshake")
	}
	h.NextState, ok = r.varInt()
	if !ok {
		return h, fmt.Errorf("malformed handshake")
	}
	return h, nil
}

// LoginStart is the server-bound login start packet.
type LoginStart struct {
	Name string
}

// Encode writes login start data.
func (l LoginStart) Encode(w *PacketWriter) {
	w.WriteString(l.Name)
}

// DecodeLoginStart parses login start from data.
func DecodeLoginStart(data []byte) (LoginStart, error) {
	r := &packetReader{data: data}
	name, ok := r.str(16)
	if !ok {
		return LoginStart{}, fmt.Errorf("malformed login start")
	}
	return LoginStart{Name: name}, nil
}

// LoginPluginResponse is the server-bound login plugin response packet.
type LoginPluginResponse struct {
	MessageID  int32
	Successful bool
	Data       []byte
}

// Encode writes login plugin response data.
func (l LoginPluginResponse) Encode(w *PacketWriter) {
	w.WriteVarInt(l.MessageID)
	w.WriteBool(l.Successful)
	w.Write(l.Data)
}

// DecodeLoginPluginResponse parses login plugin response from data.
func DecodeLoginPluginResponse(data []byte) (LoginPluginResponse, error) {
	r := &packetReader{data: data}
	msgID, ok := r.varInt()
	if !ok {
		return LoginPluginResponse{}, fmt.Errorf("malformed login plugin response")
	}
	successful, ok := r.bool()
	if !ok {
		return LoginPluginResponse{}, fmt.Errorf("malformed login plugin response")
	}
	return LoginPluginResponse{MessageID: msgID, Successful: successful, Data: r.rest()}, nil
}

// LoginPluginRequest is the client-bound login plugin request packet.
type LoginPluginRequest struct {
	MessageID int32
	Channel   string
	Data      []byte
}

// DecodeLoginPluginRequest parses login plugin request from data.
func DecodeLoginPluginRequest(data []byte) (LoginPluginRequest, error) {
	r := &packetReader{data: data}
	msgID, ok := r.varInt()
	if !ok {
		return LoginPluginRequest{}, fmt.Errorf("malformed login plugin request")
	}
	channel, ok := r.str(32767)
	if !ok {
		return LoginPluginRequest{}, fmt.Errorf("malformed login plugin request")
	}
	return LoginPluginRequest{MessageID: msgID, Channel: channel, Data: r.rest()}, nil
}

// SetCompression is the client-bound set compression packet.
type SetCompression struct {
	Threshold int32
}

// DecodeSetCompression parses set compression from data.
func DecodeSetCompression(data []byte) (SetCompression, error) {
	r := &packetReader{data: data}
	threshold, ok := r.varInt()
	if !ok {
		return SetCompression{}, fmt.Errorf("malformed set compression")
	}
	return SetCompression{Threshold: threshold}, nil
}

// PingRequest is the server-bound ping packet.
type PingRequest struct {
	Time uint64
}

// DecodePingRequest parses ping from data.
func DecodePingRequest(data []byte) (PingRequest, error) {
	r := &packetReader{data: data}
	t, ok := r.u64()
	if !ok {
		return PingRequest{}, fmt.Errorf("malformed ping request")
	}
	return PingRequest{Time: t}, nil
}

// PingResponse is the client-bound pong packet.
type PingResponse struct {
	Time uint64
}

// DecodePingResponse parses pong from data.
func DecodePingResponse(data []byte) (PingResponse, error) {
	r := &packetReader{data: data}
	t, ok := r.u64()
	if !ok {
		return PingResponse{}, fmt.Errorf("malformed ping response")
	}
	return PingResponse{Time: t}, nil
}

// packetReader reads protocol fields from a byte slice.
type packetReader struct {
	data []byte
	pos  int
}

func (r *packetReader) remaining() int { return len(r.data) - r.pos }

func (r *packetReader) varInt() (int32, bool) {
	var result int32
	for i := 0; i < 5; i++ {
		if r.pos >= len(r.data) {
			return 0, false
		}
		b := r.data[r.pos]
		r.pos++
		result |= int32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return result, true
		}
	}
	return 0, false
}

func (r *packetReader) bool() (bool, bool) {
	if r.remaining() < 1 {
		return false, false
	}
	v := r.data[r.pos]
	r.pos++
	return v != 0, true
}

func (r *packetReader) u8() (byte, bool) {
	if r.remaining() < 1 {
		return 0, false
	}
	v := r.data[r.pos]
	r.pos++
	return v, true
}

func (r *packetReader) u16() (uint16, bool) {
	if r.remaining() < 2 {
		return 0, false
	}
	v := binary.BigEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return v, true
}

func (r *packetReader) i64() (int64, bool) {
	if r.remaining() < 8 {
		return 0, false
	}
	v := int64(binary.BigEndian.Uint64(r.data[r.pos:]))
	r.pos += 8
	return v, true
}

func (r *packetReader) u64() (uint64, bool) {
	if r.remaining() < 8 {
		return 0, false
	}
	v := binary.BigEndian.Uint64(r.data[r.pos:])
	r.pos += 8
	return v, true
}

func (r *packetReader) str(maxLen int) (string, bool) {
	n, ok := r.varInt()
	if !ok || n < 0 || int(n) > r.remaining() || int(n) > maxLen {
		return "", false
	}
	s := string(r.data[r.pos : r.pos+int(n)])
	r.pos += int(n)
	return s, true
}

func (r *packetReader) rest() []byte {
	out := append([]byte(nil), r.data[r.pos:]...)
	r.pos = len(r.data)
	return out
}

// ServerStatus is the status response payload, mirroring v1_20_3
// status::ServerStatus where description is a raw string.
type ServerStatus struct {
	Version     ServerVersion
	Players     OnlinePlayers
	Description string
	Favicon     *string
	// ForgeData is the opaque Forge server-list metadata. It must survive a
	// status proxy unchanged: Forge clients use it to decide whether to enable
	// their login-query handlers before opening the login connection.
	ForgeData json.RawMessage
}

// ServerVersion is the version block of a server status.
type ServerVersion struct {
	Name     string
	Protocol uint32
}

// OnlinePlayers is the players block of a server status.
type OnlinePlayers struct {
	Max    uint32
	Online uint32
	Sample []OnlinePlayer
}

// OnlinePlayer is a single player sample entry.
type OnlinePlayer struct {
	Name string
	ID   string
}

// StatusResponseJSON renders the status response JSON in serde field order:
// version, players, description, favicon.
func (s ServerStatus) StatusResponseJSON() string {
	var sb strings.Builder
	sb.WriteString(`{"version":{"name":`)
	sb.WriteString(serdeJSONString(s.Version.Name))
	sb.WriteString(`,"protocol":`)
	sb.WriteString(itoa(s.Version.Protocol))
	sb.WriteString(`},"players":{"max":`)
	sb.WriteString(itoa(s.Players.Max))
	sb.WriteString(`,"online":`)
	sb.WriteString(itoa(s.Players.Online))
	sb.WriteString(`,"sample":[`)
	for i, p := range s.Players.Sample {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"name":`)
		sb.WriteString(serdeJSONString(p.Name))
		sb.WriteString(`,"id":`)
		sb.WriteString(serdeJSONString(p.ID))
		sb.WriteString("}")
	}
	sb.WriteString(`]},"description":`)
	sb.WriteString(serdeJSONString(s.Description))
	sb.WriteString(`,"favicon":`)
	if s.Favicon == nil {
		sb.WriteString("null")
	} else {
		sb.WriteString(serdeJSONString(*s.Favicon))
	}
	if len(s.ForgeData) != 0 && json.Valid(s.ForgeData) {
		sb.WriteString(`,"forgeData":`)
		sb.Write(s.ForgeData)
	}
	sb.WriteString("}")
	return sb.String()
}

// ChatMessageJSON renders a chat message as {"text":"..."}.
func ChatMessageJSON(text string) string {
	return `{"text":` + serdeJSONString(text) + `}`
}

// StringBytes renders a string as length-prefixed protocol bytes.
func StringBytes(s string) []byte {
	out := EncodeVarInt(int32(len(s)))
	out = append(out, []byte(s)...)
	return out
}

// serdeJSONString escapes a string like serde_json does: quotes, backslash
// and control characters are escaped, everything else is emitted raw UTF-8.
func serdeJSONString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			sb.WriteString(`\"`)
		case c == '\\':
			sb.WriteString(`\\`)
		case c == '\b':
			sb.WriteString(`\b`)
		case c == '\f':
			sb.WriteString(`\f`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c < 0x20:
			fmt.Fprintf(&sb, `\u%04x`, c)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// itoa renders an unsigned integer in decimal.
func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
