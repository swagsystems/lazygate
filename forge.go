package main

// Forge (FML2) login wrapper handling, mirroring lazymc's forge.rs.

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"lazymc/proto"
	"lazymc/proto/packets/play"
)

// Forge handshake/status magic. Forge 1.20.1 uses network version 3; older
// supported branches use FML2.
const (
	forgeStatusMagicV2 = "\x00FML2\x00"
	forgeStatusMagicV3 = "\x00FML3\x00"
)

func forgeStatusMagic(protocol uint32) string {
	if protocol >= play.ProtocolV1_20_1 {
		return forgeStatusMagicV3
	}
	return forgeStatusMagicV2
}

// Forge plugin wrapper login plugin request channel.
const forgeChannelLoginWrapper = "fml:loginwrapper"

// Forge handshake channel.
const forgeChannelHandshake = "fml:handshake"

// Timeout for draining Forge plugin responses from client.
var clientDrainForgeTimeout = 5 * time.Second

// A large modpack may spend substantially longer than the hostile-edge LOGIN
// framing budget constructing its authenticated FML3 mod-list response.
var clientRelayForgeTimeout = 2 * time.Minute

const (
	// Large Forge 1.20.1 packs may negotiate hundreds of registry/config
	// fragments. Keep a finite exchange-count guard, but let the independent
	// 8 MiB aggregate byte limit remain the primary memory bound.
	maxForgeLoginExchanges  = 512
	maxForgeLoginCacheBytes = 8 << 20
)

// respondForgeLoginPacket responds with a Forge login wrapper packet.
func respondForgeLoginPacket(client *proto.Client, conn net.Conn, messageID int32, forgeChannel string, forgePacketID byte, forgeData []byte) error {
	// Encode Forge packet with the client's compression preference
	forgePayload, ok := proto.NewRawPacket(forgePacketID, forgeData).EncodeWithoutLen(client)
	if !ok {
		return proto.ErrMalformedPacket
	}

	// Wrap Forge payload in login wrapper
	w := proto.NewPacketWriter()
	w.WriteString(forgeChannel)
	w.Write(forgePayload)

	// Write login plugin response with forge payload
	response := proto.NewRawPacket(proto.PacketServerLoginPluginResponse, w.Bytes())
	return writePacketTo(client, conn, response)
}

// writePacketTo encodes and writes a packet to the connection.
func writePacketTo(client *proto.Client, conn net.Conn, packet proto.RawPacket) error {
	encoded, ok := packet.EncodeWithLen(client)
	if !ok {
		return proto.ErrMalformedPacket
	}
	_, err := conn.Write(encoded)
	return err
}

// forgeLoginWrapper is a decoded Forge login wrapper packet.
type forgeLoginWrapper struct {
	messageID int32
	channel   string
	packetID  byte
	data      []byte
}

// forgeLoginRelay holds the client-side state for a live FML3 bootstrap. The
// backend login loop is synchronous, so each request is sent to the still
// LOGIN-state client and its response is immediately returned to the backend.
type forgeLoginRelay struct {
	client     *proto.Client
	inbound    net.Conn
	inboundBuf *[]byte
	server     *ServerState
	exchanges  []ForgeLoginExchange
	clientID   int32
	cacheBytes int
}

func newForgeLoginRelay(client *proto.Client, inbound net.Conn, inboundBuf *[]byte, server *ServerState) *forgeLoginRelay {
	return &forgeLoginRelay{client: client, inbound: inbound, inboundBuf: inboundBuf, server: server}
}

func (r *forgeLoginRelay) relay(tmpClient *proto.Client, outbound net.Conn, request proto.LoginPluginRequest) error {
	if len(r.exchanges) >= maxForgeLoginExchanges {
		return errors.New("forge login exchange count exceeded")
	}
	// Mod-specific channels may reserve negative transaction IDs for client
	// dispatch (EveryCompat uses MinInt32). Preserve those IDs end to end.
	// Only Forge's generic login wrapper is remapped to a small local ID and
	// restored on the backend response.
	clientMessageID := request.MessageID
	if request.Channel == forgeChannelLoginWrapper {
		r.clientID++
		clientMessageID = r.clientID
	}
	clientRequest := proto.LoginPluginRequest{
		MessageID: clientMessageID,
		Channel:   request.Channel,
		Data:      request.Data,
	}
	encodedRequest := encodeLoginPluginRequest(clientRequest)
	if len(encodedRequest) > proto.MaxPacketLength || r.cacheBytes+len(encodedRequest) > maxForgeLoginCacheBytes {
		return errors.New("forge login request cache bound exceeded")
	}

	// Preserve the backend's request payload while encoding it for the actual
	// client using the client's current compression state.
	if err := writePacketTo(r.client, r.inbound, proto.NewRawPacket(proto.PacketClientLoginPluginRequest, encodedRequest)); err != nil {
		return err
	}
	noResponse := false
	if innerChannel, innerPacketID, ok := inspectForgeLoginWrapper(request); ok {
		TraceLog(TargetForge, "Relayed FML wrapper inner_channel=%q inner_packet_id=%d", innerChannel, innerPacketID)
		// FML3 ModData (inner packet 5) is a one-way login message. Ambassador
		// deliberately does not register a response context for it.
		noResponse = innerChannel == forgeChannelHandshake && innerPacketID == 5
	}
	if noResponse {
		r.exchanges = append(r.exchanges, ForgeLoginExchange{Request: append([]byte(nil), encodedRequest...), NoResponse: true})
		r.cacheBytes += len(encodedRequest)
		return nil
	}

	responsePacket, _, more, err := proto.ReadPacketWithTimeout(r.client, r.inboundBuf, r.inbound, clientRelayForgeTimeout)
	if err != nil {
		return err
	}
	if !more || responsePacket.ID != proto.PacketServerLoginPluginResponse {
		return fmt.Errorf("client did not answer forge login request: more=%t packet_id=0x%02x bytes=%d", more, responsePacket.ID, len(responsePacket.Data))
	}
	response, err := proto.DecodeLoginPluginResponse(responsePacket.Data)
	if err != nil {
		return err
	}
	if response.MessageID != clientRequest.MessageID {
		return errors.New("forge login response message id mismatch")
	}
	if len(response.Data) > proto.MaxPacketLength || r.cacheBytes+len(encodedRequest)+len(response.Data) > maxForgeLoginCacheBytes {
		return errors.New("forge login response cache bound exceeded")
	}
	TraceLog(TargetForge, "Client login plugin response channel=%q client_id=%d backend_id=%d success=%t bytes=%d",
		request.Channel, response.MessageID, request.MessageID, response.Successful, len(response.Data))

	exchange := ForgeLoginExchange{
		Request: append([]byte(nil), encodedRequest...),
		Success: response.Successful,
		Data:    append([]byte(nil), response.Data...),
	}
	r.exchanges = append(r.exchanges, exchange)
	r.cacheBytes += len(encodedRequest) + len(response.Data)

	return writeForgeLoginResponse(tmpClient, outbound, request.MessageID, response.Successful, response.Data)
}

// inspectForgeLoginWrapper returns the inner Forge channel and packet ID used
// by the FML2/FML3 login wrapper. The wrapper layout is inner channel, encoded
// packet length, then encoded packet ID and payload.
func inspectForgeLoginWrapper(request proto.LoginPluginRequest) (string, int32, bool) {
	if request.Channel != forgeChannelLoginWrapper {
		return "", 0, false
	}
	r := newPacketDecoder(request.Data)
	innerChannel, ok := r.str(32767)
	if !ok || innerChannel != forgeChannelHandshake {
		return innerChannel, 0, false
	}
	innerLength, ok := r.varInt()
	if !ok || innerLength <= 0 || int(innerLength) > len(r.data)-r.pos {
		return innerChannel, 0, false
	}
	innerPacketID, ok := r.varInt()
	if !ok {
		return innerChannel, 0, false
	}
	return innerChannel, innerPacketID, true
}

// relayForgeLoginRequest mirrors the modern Forge proxy contract for 1.13-
// 1.20.1: only Forge's login wrapper is relayed to the client. Other backend
// login queries are proxy/plugin concerns and must be rejected directly. In
// particular, forwarding EveryCompat's MinInt32 query stalls the Forge client,
// which does not produce a response for the proxy to relay.
func relayForgeLoginRequest(relay *forgeLoginRelay, tmpClient *proto.Client, outbound net.Conn, request proto.LoginPluginRequest) error {
	if request.Channel != forgeChannelLoginWrapper {
		TraceLog(TargetForge, "Rejecting non-FML login plugin channel %q during authenticated Forge bootstrap", request.Channel)
		return writeForgeLoginResponse(tmpClient, outbound, request.MessageID, false, nil)
	}
	return relay.relay(tmpClient, outbound, request)
}

func cachedForgeRequestMatches(exchange ForgeLoginExchange, request proto.LoginPluginRequest) bool {
	cached, err := proto.DecodeLoginPluginRequest(exchange.Request)
	return err == nil && cached.Channel == request.Channel && bytes.Equal(cached.Data, request.Data)
}

func (r *forgeLoginRelay) commit() {
	if len(r.exchanges) != 0 {
		r.server.SetForgeLoginCache(r.exchanges)
	}
}

func encodeLoginPluginRequest(request proto.LoginPluginRequest) []byte {
	w := proto.NewPacketWriter()
	w.WriteVarInt(request.MessageID)
	w.WriteString(request.Channel)
	w.Write(request.Data)
	return w.Bytes()
}

func writeForgeLoginResponse(client *proto.Client, conn net.Conn, messageID int32, success bool, data []byte) error {
	w := proto.NewPacketWriter()
	(proto.LoginPluginResponse{MessageID: messageID, Successful: success, Data: data}).Encode(w)
	return writePacketTo(client, conn, proto.NewRawPacket(proto.PacketServerLoginPluginResponse, w.Bytes()))
}

func isForgeFML3(clientInfo *proto.ClientInfo) bool {
	protocol := clientInfo.GetProtocol()
	return protocol != nil && *protocol >= play.ProtocolV1_20_1
}

func forgeHandshakeMarker(clientInfo *proto.ClientInfo) string {
	if clientInfo == nil || clientInfo.Handshake == nil {
		return "absent"
	}
	switch {
	case strings.Contains(clientInfo.Handshake.ServerAddr, forgeStatusMagicV3):
		return "FML3"
	case strings.Contains(clientInfo.Handshake.ServerAddr, forgeStatusMagicV2):
		return "FML2"
	default:
		return "absent"
	}
}

// replayForgeLoginPayload replays cached FML3 requests to a cold lobby
// client. The client remains in LOGIN until the caller sends LoginSuccess.
func replayForgeLoginPayload(client *proto.Client, inbound net.Conn, server *ServerState, inboundBuf *[]byte) error {
	cache := server.ForgeLoginCache()
	responsesExpected := 0
	for _, exchange := range cache {
		if err := writePacketTo(client, inbound, proto.NewRawPacket(proto.PacketClientLoginPluginRequest, exchange.Request)); err != nil {
			return err
		}
		if !exchange.NoResponse {
			responsesExpected++
		}
	}
	return drainForgeResponses(client, inbound, inboundBuf, responsesExpected)
}

func replayForgeLoginResponse(client *proto.Client, conn net.Conn, exchange *ForgeLoginExchange, messageID int32) error {
	return writeForgeLoginResponse(client, conn, messageID, exchange.Success, exchange.Data)
}

// respondLoginPluginRequest responds to a Forge login plugin request.
func respondLoginPluginRequest(client *proto.Client, conn net.Conn, request proto.LoginPluginRequest) error {
	// Decode Forge login wrapper packet
	wrapper, err := decodeForgeLoginPacket(client, request)
	if err != nil {
		return err
	}

	// Determine whether we received the mod list
	isUnknownHeader := wrapper.channel != forgeChannelHandshake
	isModList := !isUnknownHeader && wrapper.packetID == play.ForgeModList

	// If not the mod list, just acknowledge
	if !isModList {
		TraceLog(TargetForge, "Acknowledging login plugin request")
		err := respondForgeLoginPacket(client, conn, wrapper.messageID, wrapper.channel, play.ForgeAcknowledgement, nil)
		if err != nil {
			ErrorLog(TargetForge, "Failed to send Forge login plugin request acknowledgement")
			return err
		}
		return nil
	}

	TraceLog(TargetForge, "Sending mod list reply to server with same contents")

	// Parse mod list, transform into reply
	reply, err := forgeModListReply(wrapper.data)
	if err != nil {
		ErrorLog(TargetForge, "Failed to decode Forge mod list: %v", err)
		return err
	}

	// We got mod list, respond with reply
	err = respondForgeLoginPacket(client, conn, wrapper.messageID, wrapper.channel, play.ForgeModListReply, reply)
	if err != nil {
		ErrorLog(TargetForge, "Failed to send Forge login plugin mod list reply")
		return err
	}

	return nil
}

// decodeForgeLoginPacket decodes a Forge login wrapper from a plugin request.
func decodeForgeLoginPacket(client *proto.Client, request proto.LoginPluginRequest) (forgeLoginWrapper, error) {
	// Validate channel
	if request.Channel != forgeChannelLoginWrapper {
		return forgeLoginWrapper{}, errors.New("unexpected channel")
	}

	// Decode login wrapped packet: string channel, rest packet
	r := newPacketDecoder(request.Data)
	channel, ok := r.str(32767)
	if !ok {
		return forgeLoginWrapper{}, errors.New("malformed LoginWrapper")
	}

	// Decode the wrapped packet with the client's compression preference
	inner, ok := proto.DecodeWithoutLen(client, r.data[r.pos:])
	if !ok {
		ErrorLog(TargetForge, "Failed to decode Forge LoginWrapper packet contents")
		return forgeLoginWrapper{}, errors.New("malformed LoginWrapper packet")
	}

	return forgeLoginWrapper{messageID: request.MessageID, channel: channel, packetID: inner.ID, data: inner.Data}, nil
}

// forgeModListReply parses a Forge mod list and builds the reply.
//
// ModList: vec<string> mod_names, vec<(string,string)> channels, vec<string>
// registries. Reply: registries become (string, "") pairs.
func forgeModListReply(data []byte) ([]byte, error) {
	r := newPacketDecoder(data)

	modNames, ok := r.strVec()
	if !ok {
		return nil, errors.New("malformed mod list")
	}
	channels, ok := r.strPairVec()
	if !ok {
		return nil, errors.New("malformed mod list")
	}
	registries, ok := r.strVec()
	if !ok {
		return nil, errors.New("malformed mod list")
	}

	w := proto.NewPacketWriter()
	w.WriteVarInt(int32(len(modNames)))
	for _, s := range modNames {
		w.WriteString(s)
	}
	w.WriteVarInt(int32(len(channels)))
	for _, p := range channels {
		w.WriteString(p[0])
		w.WriteString(p[1])
	}
	w.WriteVarInt(int32(len(registries)))
	for _, s := range registries {
		w.WriteString(s)
		w.WriteString("")
	}

	return w.Bytes(), nil
}

// packetDecoder reads protocol fields from a byte slice.
type packetDecoder struct {
	data []byte
	pos  int
}

func newPacketDecoder(data []byte) *packetDecoder {
	return &packetDecoder{data: data}
}

func (r *packetDecoder) str(maxLen int) (string, bool) {
	n, ok := r.varInt()
	if !ok || n < 0 || int(n) > len(r.data)-r.pos || int(n) > maxLen {
		return "", false
	}
	s := string(r.data[r.pos : r.pos+int(n)])
	r.pos += int(n)
	return s, true
}

func (r *packetDecoder) varInt() (int32, bool) {
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

func (r *packetDecoder) strVec() ([]string, bool) {
	n, ok := r.varInt()
	if !ok || n < 0 {
		return nil, false
	}
	var out []string
	for i := int32(0); i < n; i++ {
		s, ok := r.str(32767)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func (r *packetDecoder) strPairVec() ([][2]string, bool) {
	n, ok := r.varInt()
	if !ok || n < 0 {
		return nil, false
	}
	var out [][2]string
	for i := int32(0); i < n; i++ {
		a, ok := r.str(32767)
		if !ok {
			return nil, false
		}
		b, ok := r.str(32767)
		if !ok {
			return nil, false
		}
		out = append(out, [2]string{a, b})
	}
	return out, true
}

// replayLoginPayload replays the Forge login payload for a client.
func replayLoginPayload(client *proto.Client, inbound net.Conn, server *ServerState, inboundBuf *[]byte) error {
	DebugLog(TargetLobby, "Replaying Forge login procedure for lobby client...")

	// Replay each Forge packet
	for _, packet := range server.ForgePayload() {
		if _, err := inbound.Write(packet); err != nil {
			ErrorLog(TargetLobby, "Failed to send Forge join payload to lobby client, will likely cause issues: %v", err)
		}
	}

	// Drain all responses
	count := len(server.ForgePayload())
	err := drainForgeResponses(client, inbound, inboundBuf, count)
	if err != nil {
		return err
	}

	TraceLog(TargetLobby, "Forge join payload replayed")
	return nil
}

// drainForgeResponses drains Forge login plugin response packets from the
// stream.
func drainForgeResponses(client *proto.Client, inbound net.Conn, buf *[]byte, count int) error {
	for {
		// We're done if count is zero
		if count == 0 {
			TraceLog(TargetForge, "Drained all plugin responses from client")
			return nil
		}

		// Read synchronously with a connection deadline. A goroutine around a
		// blocking read would survive the timeout and continue consuming the
		// client stream after this function returns.
		if err := inbound.SetReadDeadline(time.Now().Add(clientDrainForgeTimeout)); err != nil {
			return err
		}
		packet, _, more, err := proto.ReadPacket(client, buf, inbound)
		clearErr := inbound.SetReadDeadline(time.Time{})
		if err == nil && clearErr != nil {
			return clearErr
		}
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				ErrorLog(TargetForge, "Expected more plugin responses from client, but didn't receive anything in a while, may be problematic")
				return nil
			}
			if errors.Is(err, proto.ErrMalformedPacket) {
				ErrorLog(TargetForge, "Closing connection, error occurred")
				return err
			}
			return err
		}

		if !more {
			return nil
		}

		// Grab client state
		clientState := client.State()

		// Catch login plugin response
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketServerLoginPluginResponse {
			TraceLog(TargetForge, "Voiding plugin response from client")
			count--
			continue
		}

		// Show unhandled packet warning
		DebugLog(TargetForge, "Got unhandled packet from server in record_forge_response:")
		DebugLog(TargetForge, "- State: %v", clientState)
		DebugLog(TargetForge, "- Packet ID: 0x%02X (%d)", packet.ID, packet.ID)
	}
}
