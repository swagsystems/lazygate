package main

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"

	"lazymc/proto"
)

func TestForgeFML3RelayCachesAndForwardsResponse(t *testing.T) {
	oldLoginTimeout := proto.LoginReadTimeout
	oldRelayTimeout := clientRelayForgeTimeout
	proto.LoginReadTimeout = 5 * time.Millisecond
	clientRelayForgeTimeout = 250 * time.Millisecond
	defer func() {
		proto.LoginReadTimeout = oldLoginTimeout
		clientRelayForgeTimeout = oldRelayTimeout
	}()
	proxyClientConn, clientConn := net.Pipe()
	backendProxyConn, backendConn := net.Pipe()
	defer proxyClientConn.Close()
	defer clientConn.Close()
	defer backendProxyConn.Close()
	defer backendConn.Close()

	client := proto.DummyClient()
	client.SetState(proto.ClientStateLogin)
	clientBuf := []byte(nil)
	server := NewServerState()
	relay := newForgeLoginRelay(client, proxyClientConn, &clientBuf, server)
	request := proto.LoginPluginRequest{
		MessageID: -2147483648,
		Channel:   forgeChannelLoginWrapper,
		Data:      []byte{0x03, 0x01, 0x02},
	}

	clientDone := make(chan error, 1)
	go func() {
		peer := proto.DummyClient()
		peer.SetState(proto.ClientStateLogin)
		buf := []byte(nil)
		packet, _, more, err := proto.ReadPacket(peer, &buf, clientConn)
		if err != nil || !more || packet.ID != proto.PacketClientSetCompression {
			clientDone <- testPacketError("client compression", packet, more, err)
			return
		}
		compression, err := proto.DecodeSetCompression(packet.Data)
		if err != nil {
			clientDone <- err
			return
		}
		peer.SetCompression(compression.Threshold)

		packet, _, more, err = proto.ReadPacket(peer, &buf, clientConn)
		if err != nil || !more || packet.ID != proto.PacketClientLoginPluginRequest {
			clientDone <- testPacketError("client request", packet, more, err)
			return
		}
		got, err := proto.DecodeLoginPluginRequest(packet.Data)
		if err != nil {
			clientDone <- err
			return
		}
		want := request
		want.MessageID = 1
		if got.MessageID != want.MessageID || got.Channel != want.Channel || !bytes.Equal(got.Data, want.Data) {
			clientDone <- testMismatch("relayed request", got, want)
			return
		}
		response := proto.NewPacketWriter()
		(proto.LoginPluginResponse{MessageID: got.MessageID, Successful: true, Data: []byte{0x09, 0x08}}).Encode(response)
		// Exceed the ordinary LOGIN edge timeout. The authenticated Forge relay
		// retains its separate bounded processing window.
		time.Sleep(25 * time.Millisecond)
		clientDone <- writePacketTo(peer, clientConn, proto.NewRawPacket(proto.PacketServerLoginPluginResponse, response.Bytes()))
	}()
	compressed, err := ensureClientCompression(client, proxyClientConn)
	if err != nil || !compressed {
		t.Fatalf("hot client compression: compressed=%v err=%v", compressed, err)
	}

	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.relay(proto.DummyClient(), backendProxyConn, request) }()

	backend := proto.DummyClient()
	backend.SetState(proto.ClientStateLogin)
	backendBuf := []byte(nil)
	packet, _, more, err := proto.ReadPacketWithTimeout(backend, &backendBuf, backendConn, 250*time.Millisecond)
	if err != nil || !more || packet.ID != proto.PacketServerLoginPluginResponse {
		t.Fatalf("backend response: packet=%#v more=%v err=%v", packet, more, err)
	}
	response, err := proto.DecodeLoginPluginResponse(packet.Data)
	if err != nil {
		t.Fatal(err)
	}
	if response.MessageID != request.MessageID || !response.Successful || !bytes.Equal(response.Data, []byte{0x09, 0x08}) {
		t.Fatalf("unexpected backend response: %#v", response)
	}
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	if err := <-relayDone; err != nil {
		t.Fatal(err)
	}

	relay.commit()
	cache := server.ForgeLoginCache()
	cachedRequest := request
	cachedRequest.MessageID = 1
	if len(cache) != 1 || !bytes.Equal(cache[0].Request, encodeLoginPluginRequest(cachedRequest)) || !cache[0].Success || !bytes.Equal(cache[0].Data, []byte{0x09, 0x08}) {
		t.Fatalf("unexpected FML3 cache: %#v", cache)
	}
}

func TestForgeFML3RejectsNonFMLLoginQueryWithoutClientRelay(t *testing.T) {
	backendProxyConn, backendConn := net.Pipe()
	defer backendProxyConn.Close()
	defer backendConn.Close()

	request := proto.LoginPluginRequest{
		MessageID: -2147483648,
		Channel:   "everycomp:channel",
		Data:      []byte{0x03, 0x01, 0x02},
	}
	relay := newForgeLoginRelay(proto.DummyClient(), nil, nil, NewServerState())
	done := make(chan error, 1)
	go func() {
		done <- relayForgeLoginRequest(relay, proto.DummyClient(), backendProxyConn, request)
	}()

	backend := proto.DummyClient()
	backend.SetState(proto.ClientStateLogin)
	buf := []byte(nil)
	packet, _, more, err := proto.ReadPacketWithTimeout(backend, &buf, backendConn, 250*time.Millisecond)
	if err != nil || !more || packet.ID != proto.PacketServerLoginPluginResponse {
		t.Fatalf("backend rejection: packet=%#v more=%v err=%v", packet, more, err)
	}
	response, err := proto.DecodeLoginPluginResponse(packet.Data)
	if err != nil {
		t.Fatal(err)
	}
	if response.MessageID != request.MessageID || response.Successful || len(response.Data) != 0 {
		t.Fatalf("unexpected backend rejection: %#v", response)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(relay.exchanges) != 0 {
		t.Fatalf("non-FML query was cached: %#v", relay.exchanges)
	}
}

func TestInspectForgeLoginWrapperRecognizesResponseLessModData(t *testing.T) {
	inner := proto.NewPacketWriter()
	inner.WriteVarInt(5)
	inner.Write([]byte{0xaa, 0xbb})
	data := proto.NewPacketWriter()
	data.WriteString(forgeChannelHandshake)
	data.WriteVarInt(int32(len(inner.Bytes())))
	data.Write(inner.Bytes())

	channel, packetID, ok := inspectForgeLoginWrapper(proto.LoginPluginRequest{
		MessageID: 9,
		Channel:   forgeChannelLoginWrapper,
		Data:      data.Bytes(),
	})
	if !ok || channel != forgeChannelHandshake || packetID != 5 {
		t.Fatalf("wrapper metadata: ok=%v channel=%q packetID=%d", ok, channel, packetID)
	}
}

func TestForgeFML3ModDataIsCachedWithoutAwaitingResponse(t *testing.T) {
	proxyConn, clientConn := net.Pipe()
	defer proxyConn.Close()
	defer clientConn.Close()

	inner := proto.NewPacketWriter()
	inner.WriteVarInt(5)
	inner.Write([]byte{0xaa, 0xbb})
	data := proto.NewPacketWriter()
	data.WriteString(forgeChannelHandshake)
	data.WriteVarInt(int32(len(inner.Bytes())))
	data.Write(inner.Bytes())
	request := proto.LoginPluginRequest{MessageID: 17, Channel: forgeChannelLoginWrapper, Data: data.Bytes()}

	client := proto.DummyClient()
	client.SetState(proto.ClientStateLogin)
	clientBuf := []byte(nil)
	server := NewServerState()
	relay := newForgeLoginRelay(client, proxyConn, &clientBuf, server)
	peerDone := make(chan error, 1)
	go func() {
		peer := proto.DummyClient()
		peer.SetState(proto.ClientStateLogin)
		buf := []byte(nil)
		packet, _, more, err := proto.ReadPacketWithTimeout(peer, &buf, clientConn, 250*time.Millisecond)
		if err != nil || !more || packet.ID != proto.PacketClientLoginPluginRequest {
			peerDone <- testPacketError("response-less ModData request", packet, more, err)
			return
		}
		peerDone <- nil
	}()

	if err := relay.relay(proto.DummyClient(), nil, request); err != nil {
		t.Fatal(err)
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
	relay.commit()
	cache := server.ForgeLoginCache()
	if len(cache) != 1 || !cache[0].NoResponse || cache[0].Success || len(cache[0].Data) != 0 {
		t.Fatalf("unexpected response-less cache: %#v", cache)
	}
}

func TestForgeFML3ReplayDoesNotDrainResponseLessModData(t *testing.T) {
	proxyConn, clientConn := net.Pipe()
	defer proxyConn.Close()
	defer clientConn.Close()

	server := NewServerState()
	server.SetForgeLoginCache([]ForgeLoginExchange{{
		Request:    encodeLoginPluginRequest(proto.LoginPluginRequest{MessageID: 4, Channel: forgeChannelLoginWrapper, Data: []byte{0x01}}),
		NoResponse: true,
	}})
	peerDone := make(chan error, 1)
	go func() {
		peer := proto.DummyClient()
		peer.SetState(proto.ClientStateLogin)
		buf := []byte(nil)
		packet, _, more, err := proto.ReadPacketWithTimeout(peer, &buf, clientConn, 250*time.Millisecond)
		if err != nil || !more || packet.ID != proto.PacketClientLoginPluginRequest {
			peerDone <- testPacketError("response-less replay request", packet, more, err)
			return
		}
		peerDone <- nil
	}()

	client := proto.DummyClient()
	client.SetState(proto.ClientStateLogin)
	if err := replayForgeLoginPayload(client, proxyConn, server, new([]byte)); err != nil {
		t.Fatal(err)
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}

func TestForgeFML3UsesProxyLocalClientIDAndRestoresBackendID(t *testing.T) {
	proxyClientConn, clientConn := net.Pipe()
	backendProxyConn, backendConn := net.Pipe()
	defer proxyClientConn.Close()
	defer clientConn.Close()
	defer backendProxyConn.Close()
	defer backendConn.Close()

	client := proto.DummyClient()
	client.SetState(proto.ClientStateLogin)
	clientBuf := []byte(nil)
	relay := newForgeLoginRelay(client, proxyClientConn, &clientBuf, NewServerState())
	request := proto.LoginPluginRequest{MessageID: 0, Channel: forgeChannelLoginWrapper, Data: []byte{0x01}}

	clientDone := make(chan error, 1)
	go func() {
		peer := proto.DummyClient()
		peer.SetState(proto.ClientStateLogin)
		buf := []byte(nil)
		packet, _, more, err := proto.ReadPacket(peer, &buf, clientConn)
		if err != nil || !more || packet.ID != proto.PacketClientLoginPluginRequest {
			clientDone <- testPacketError("client FML request", packet, more, err)
			return
		}
		got, err := proto.DecodeLoginPluginRequest(packet.Data)
		if err != nil {
			clientDone <- err
			return
		}
		if got.MessageID != 1 || got.Channel != request.Channel || !bytes.Equal(got.Data, request.Data) {
			clientDone <- testMismatch("client FML request", got, proto.LoginPluginRequest{MessageID: 1, Channel: request.Channel, Data: request.Data})
			return
		}
		response := proto.NewPacketWriter()
		(proto.LoginPluginResponse{MessageID: got.MessageID, Successful: true, Data: []byte{0x02}}).Encode(response)
		clientDone <- writePacketTo(peer, clientConn, proto.NewRawPacket(proto.PacketServerLoginPluginResponse, response.Bytes()))
	}()

	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.relay(proto.DummyClient(), backendProxyConn, request) }()

	backend := proto.DummyClient()
	backend.SetState(proto.ClientStateLogin)
	buf := []byte(nil)
	packet, _, more, err := proto.ReadPacket(backend, &buf, backendConn)
	if err != nil || !more || packet.ID != proto.PacketServerLoginPluginResponse {
		t.Fatalf("backend FML response: packet=%#v more=%v err=%v", packet, more, err)
	}
	response, err := proto.DecodeLoginPluginResponse(packet.Data)
	if err != nil {
		t.Fatal(err)
	}
	if response.MessageID != request.MessageID || !response.Successful || !bytes.Equal(response.Data, []byte{0x02}) {
		t.Fatalf("backend FML response = %#v", response)
	}
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	if err := <-relayDone; err != nil {
		t.Fatal(err)
	}
}

func TestForgeFML3CacheBoundsAndRequestMatching(t *testing.T) {
	relay := newForgeLoginRelay(proto.DummyClient(), nil, nil, NewServerState())
	relay.exchanges = make([]ForgeLoginExchange, maxForgeLoginExchanges)
	if err := relay.relay(proto.DummyClient(), nil, proto.LoginPluginRequest{}); err == nil {
		t.Fatal("exchange count bound was not enforced")
	}

	exchange := ForgeLoginExchange{Request: encodeLoginPluginRequest(proto.LoginPluginRequest{
		MessageID: 1,
		Channel:   "everycomp:channel",
		Data:      []byte{0x01, 0x02},
	})}
	if !cachedForgeRequestMatches(exchange, proto.LoginPluginRequest{MessageID: -2147483648, Channel: "everycomp:channel", Data: []byte{0x01, 0x02}}) {
		t.Fatal("equivalent request with a new backend id did not match")
	}
	if cachedForgeRequestMatches(exchange, proto.LoginPluginRequest{MessageID: 2, Channel: "everycomp:channel", Data: []byte{0x01, 0x03}}) {
		t.Fatal("changed request data matched cached exchange")
	}
}

func TestForgeFML3ReplaySendsCachedRequestAndRewritesBackendID(t *testing.T) {
	proxyConn, clientConn := net.Pipe()
	defer proxyConn.Close()
	defer clientConn.Close()

	client := proto.DummyClient()
	client.SetState(proto.ClientStateLogin)
	client.SetCompression(0)
	server := NewServerState()
	server.SetForgeLoginCache([]ForgeLoginExchange{{
		Request: encodeLoginPluginRequest(proto.LoginPluginRequest{MessageID: 11, Channel: forgeChannelLoginWrapper, Data: []byte{0x04}}),
		Success: true,
		Data:    []byte{0xaa},
	}})

	clientDone := make(chan error, 1)
	go func() {
		peer := proto.DummyClient()
		peer.SetState(proto.ClientStateLogin)
		peer.SetCompression(0)
		buf := []byte(nil)
		packet, _, more, err := proto.ReadPacket(peer, &buf, clientConn)
		if err != nil || !more || packet.ID != proto.PacketClientLoginPluginRequest {
			clientDone <- testPacketError("replay request", packet, more, err)
			return
		}
		request, err := proto.DecodeLoginPluginRequest(packet.Data)
		if err == nil && request.MessageID != 11 {
			err = testMismatch("replayed message id", request.MessageID, int32(11))
		}
		if err != nil {
			clientDone <- err
			return
		}
		response := proto.NewPacketWriter()
		(proto.LoginPluginResponse{MessageID: request.MessageID, Successful: true, Data: []byte{0x01}}).Encode(response)
		clientDone <- writePacketTo(peer, clientConn, proto.NewRawPacket(proto.PacketServerLoginPluginResponse, response.Bytes()))
	}()

	buf := []byte(nil)
	if err := replayForgeLoginPayload(client, proxyConn, server, &buf); err != nil {
		t.Fatal(err)
	}
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}

	backendConn, backendPeer := net.Pipe()
	defer backendConn.Close()
	defer backendPeer.Close()
	backend := proto.DummyClient()
	backend.SetState(proto.ClientStateLogin)
	cache := server.ForgeLoginCache()
	backendDone := make(chan error, 1)
	go func() {
		backendDone <- replayForgeLoginResponse(backend, backendConn, &cache[0], 91)
	}()
	backendBuf := []byte(nil)
	packet, _, more, err := proto.ReadPacket(backend, &backendBuf, backendPeer)
	if err != nil || !more || packet.ID != proto.PacketServerLoginPluginResponse {
		t.Fatalf("rewritten backend response: packet=%#v more=%v err=%v", packet, more, err)
	}
	response, err := proto.DecodeLoginPluginResponse(packet.Data)
	if err != nil {
		t.Fatal(err)
	}
	if response.MessageID != 91 || !response.Successful || !bytes.Equal(response.Data, []byte{0xaa}) {
		t.Fatalf("unexpected rewritten response: %#v", response)
	}
	if err := <-backendDone; err != nil {
		t.Fatal(err)
	}
}

func TestColdForgeReplaySendsCompressionBeforeCachedRequests(t *testing.T) {
	proxyConn, clientConn := net.Pipe()
	defer proxyConn.Close()
	defer clientConn.Close()

	client := proto.DummyClient()
	client.SetState(proto.ClientStateLogin)
	server := NewServerState()
	server.SetForgeLoginCache([]ForgeLoginExchange{{
		Request: encodeLoginPluginRequest(proto.LoginPluginRequest{MessageID: 23, Channel: forgeChannelLoginWrapper, Data: []byte{0x07}}),
		Success: true,
		Data:    []byte{0x08},
	}})

	peerDone := make(chan error, 1)
	go func() {
		peer := proto.DummyClient()
		peer.SetState(proto.ClientStateLogin)
		buf := []byte(nil)
		fail := func(err error) {
			_ = clientConn.Close()
			peerDone <- err
		}

		packet, _, more, err := proto.ReadPacket(peer, &buf, clientConn)
		if err != nil || !more || packet.ID != proto.PacketClientSetCompression {
			fail(fmt.Errorf("first cold replay packet: id=%d more=%v err=%v", packet.ID, more, err))
			return
		}
		compression, err := proto.DecodeSetCompression(packet.Data)
		if err != nil {
			fail(err)
			return
		}
		peer.SetCompression(compression.Threshold)

		packet, _, more, err = proto.ReadPacket(peer, &buf, clientConn)
		if err != nil || !more || packet.ID != proto.PacketClientLoginPluginRequest {
			fail(fmt.Errorf("second cold replay packet: id=%d more=%v err=%v", packet.ID, more, err))
			return
		}
		request, err := proto.DecodeLoginPluginRequest(packet.Data)
		if err != nil {
			fail(err)
			return
		}
		if request.MessageID != 23 {
			fail(fmt.Errorf("replayed message id = %d, want 23", request.MessageID))
			return
		}
		response := proto.NewPacketWriter()
		(proto.LoginPluginResponse{MessageID: request.MessageID, Successful: true, Data: []byte{0x01}}).Encode(response)
		peerDone <- writePacketTo(peer, clientConn, proto.NewRawPacket(proto.PacketServerLoginPluginResponse, response.Bytes()))
	}()

	replayDone := make(chan struct {
		compressed bool
		err        error
	}, 1)
	go func() {
		compressed, err := replayColdForgeLogin(client, proxyConn, server, new([]byte))
		replayDone <- struct {
			compressed bool
			err        error
		}{compressed: compressed, err: err}
	}()

	result := <-replayDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.compressed || !client.IsCompressed() || client.Compressed() != proto.CompressionThreshold {
		t.Fatalf("cold replay compression state: sent=%v threshold=%d", result.compressed, client.Compressed())
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}

func TestForgeFML2ModListReplyRemainsAvailable(t *testing.T) {
	reply, err := forgeModListReply([]byte{0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(reply) == 0 {
		t.Fatal("FML2 reply is empty")
	}
}

func testPacketError(label string, packet proto.RawPacket, more bool, err error) error {
	return &forgeTestError{label: label, packet: packet, more: more, err: err}
}

func testMismatch(label string, got, want any) error {
	return &forgeTestError{label: label, got: got, want: want}
}

type forgeTestError struct {
	label     string
	packet    proto.RawPacket
	more      bool
	err       error
	got, want any
}

func (e *forgeTestError) Error() string {
	return e.label
}
