package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"lazymc/proto"
	"lazymc/proto/packets/play"
)

func integrationFMLWrapper(innerChannel string, innerID int32, data []byte) []byte {
	inner := proto.NewPacketWriter()
	inner.WriteVarInt(innerID)
	inner.Write(data)

	w := proto.NewPacketWriter()
	w.WriteString(innerChannel)
	w.WriteVarInt(int32(len(inner.Bytes())))
	w.Write(inner.Bytes())
	return w.Bytes()
}

func integrationReadPacket(t *testing.T, client *proto.Client, buf *[]byte, conn net.Conn, label string) proto.RawPacket {
	t.Helper()
	packet, _, more, err := proto.ReadPacketWithTimeout(client, buf, conn, 500*time.Millisecond)
	if err != nil || !more {
		t.Fatalf("%s: packet=%#v more=%v err=%v", label, packet, more, err)
	}
	return packet
}

func integrationReadLoginPluginResponse(t *testing.T, client *proto.Client, buf *[]byte, conn net.Conn, label string) proto.LoginPluginResponse {
	t.Helper()
	packet := integrationReadPacket(t, client, buf, conn, label)
	if packet.ID != proto.PacketServerLoginPluginResponse {
		t.Fatalf("%s: packet id=0x%02x, want login-plugin response", label, packet.ID)
	}
	response, err := proto.DecodeLoginPluginResponse(packet.Data)
	if err != nil {
		t.Fatalf("%s: decode response: %v", label, err)
	}
	return response
}

func integrationSendLoginPluginRequest(t *testing.T, client *proto.Client, conn net.Conn, request proto.LoginPluginRequest) {
	t.Helper()
	if err := writePacketTo(client, conn, proto.NewRawPacket(proto.PacketClientLoginPluginRequest, encodeLoginPluginRequest(request))); err != nil {
		t.Fatalf("send login-plugin request: %v", err)
	}
}

func integrationAssertNoClientPacket(t *testing.T, conn net.Conn, label string) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("%s: set deadline: %v", label, err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var one [1]byte
	n, err := conn.Read(one[:])
	if err == nil || n != 0 {
		t.Fatalf("%s: unexpected client wire bytes n=%d err=%v", label, n, err)
	}
	if networkErr, ok := err.(net.Error); !ok || !networkErr.Timeout() {
		t.Fatalf("%s: expected read timeout, got %T %v", label, err, err)
	}
}

func TestConnectToServerNoTimeoutHotFML3OrderedFlow(t *testing.T) {
	oldLoginTimeout := proto.LoginReadTimeout
	oldRelayTimeout := clientRelayForgeTimeout
	proto.LoginReadTimeout = 500 * time.Millisecond
	clientRelayForgeTimeout = 500 * time.Millisecond
	defer func() {
		proto.LoginReadTimeout = oldLoginTimeout
		clientRelayForgeTimeout = oldRelayTimeout
	}()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	secretPath := t.TempDir() + "/forwarding-secret"
	if err := os.WriteFile(secretPath, []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}

	protocol := uint32(play.ProtocolV1_20_1)
	name := "ForgePlayer"
	profile := &proto.GameProfile{Name: name, UUID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}
	clientInfo := &proto.ClientInfo{
		Protocol: &protocol,
		Handshake: &proto.Handshake{
			ProtocolVersion: int32(protocol),
			ServerAddr:      "example.test" + forgeStatusMagicV3,
			ServerPort:      25565,
			NextState:       2,
		},
		Username: &name,
		Profile:  profile,
	}
	config := &Config{
		Server: Server{
			Address: SocketAddr{IP: net.ParseIP("127.0.0.1"), Port: listener.Addr().(*net.TCPAddr).Port},
			Forge:   true,
		},
		Auth: Auth{
			OnlineMode:           true,
			ForwardingSecretFile: secretPath,
		},
	}

	proxyClientConn, clientPeer := net.Pipe()
	secret := []byte("0123456789abcdef")
	encryptedProxyClientConn, err := newMinecraftEncryptedConn(proxyClientConn, secret)
	if err != nil {
		t.Fatal(err)
	}
	encryptedClientPeer, err := newMinecraftEncryptedConn(clientPeer, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer encryptedProxyClientConn.Close()
	defer encryptedClientPeer.Close()
	client := proto.DummyClient()
	client.SetState(proto.ClientStateLogin)
	client.SetCompression(proto.CompressionThreshold)
	var clientBuf []byte
	server := NewServerState()
	relay := newForgeLoginRelay(client, encryptedProxyClientConn, &clientBuf, server)

	type connectResult struct {
		client   *proto.Client
		outbound net.Conn
		buf      []byte
		err      error
	}
	connectDone := make(chan connectResult, 1)
	go func() {
		backendClient, outbound, serverBuf, connectErr := connectToServerNoTimeout(clientInfo, encryptedProxyClientConn, config, server, relay)
		connectDone <- connectResult{client: backendClient, outbound: outbound, buf: serverBuf, err: connectErr}
	}()

	backendConn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer backendConn.Close()
	backend := proto.DummyClient()
	backend.SetState(proto.ClientStateLogin)
	var backendBuf []byte

	handshake := integrationReadPacket(t, backend, &backendBuf, backendConn, "backend handshake")
	if handshake.ID != proto.PacketHandshake {
		t.Fatalf("backend handshake id=0x%02x", handshake.ID)
	}
	gotHandshake, err := proto.DecodeHandshake(handshake.Data)
	if err != nil {
		t.Fatal(err)
	}
	if gotHandshake.ProtocolVersion != int32(protocol) || gotHandshake.ServerAddr != clientInfo.Handshake.ServerAddr || gotHandshake.NextState != 2 {
		t.Fatalf("backend handshake=%+v", gotHandshake)
	}
	loginStart := integrationReadPacket(t, backend, &backendBuf, backendConn, "backend login start")
	if loginStart.ID != proto.PacketServerLoginStart {
		t.Fatalf("backend login start id=0x%02x", loginStart.ID)
	}

	integrationSendLoginPluginRequest(t, backend, backendConn, proto.LoginPluginRequest{
		MessageID: -722627311,
		Channel:   velocityPlayerInfoChannel,
		Data:      []byte{4},
	})
	velocityResponse := integrationReadLoginPluginResponse(t, backend, &backendBuf, backendConn, "velocity response")
	if velocityResponse.MessageID != -722627311 || !velocityResponse.Successful || len(velocityResponse.Data) <= 32 {
		t.Fatalf("velocity response=%+v", velocityResponse)
	}

	compressionWriter := proto.NewPacketWriter()
	compressionWriter.WriteVarInt(proto.CompressionThreshold)
	if err := writePacketTo(backend, backendConn, proto.NewRawPacket(proto.PacketClientSetCompression, compressionWriter.Bytes())); err != nil {
		t.Fatal(err)
	}
	backend.SetCompression(proto.CompressionThreshold)
	integrationAssertNoClientPacket(t, encryptedClientPeer, "duplicate hot SetCompression")

	integrationSendLoginPluginRequest(t, backend, backendConn, proto.LoginPluginRequest{
		MessageID: -2147483648,
		Channel:   "everycomp:channel",
		Data:      []byte{0x01, 0x02},
	})
	everycompResponse := integrationReadLoginPluginResponse(t, backend, &backendBuf, backendConn, "EveryCompat rejection")
	if everycompResponse.MessageID != -2147483648 || everycompResponse.Successful || len(everycompResponse.Data) != 0 {
		t.Fatalf("EveryCompat response=%+v", everycompResponse)
	}
	integrationAssertNoClientPacket(t, encryptedClientPeer, "EveryCompat client relay")

	modData := integrationFMLWrapper(forgeChannelHandshake, 5, []byte{0xaa, 0xbb})
	integrationSendLoginPluginRequest(t, backend, backendConn, proto.LoginPluginRequest{
		MessageID: 0,
		Channel:   forgeChannelLoginWrapper,
		Data:      modData,
	})
	clientPeerState := proto.DummyClient()
	clientPeerState.SetState(proto.ClientStateLogin)
	clientPeerState.SetCompression(proto.CompressionThreshold)
	var clientPeerBuf []byte
	modDataPacket := integrationReadPacket(t, clientPeerState, &clientPeerBuf, encryptedClientPeer, "FML ModData request")
	if modDataPacket.ID != proto.PacketClientLoginPluginRequest {
		t.Fatalf("ModData packet id=0x%02x", modDataPacket.ID)
	}
	modDataRequest, err := proto.DecodeLoginPluginRequest(modDataPacket.Data)
	if err != nil {
		t.Fatal(err)
	}
	if modDataRequest.MessageID != 1 || modDataRequest.Channel != forgeChannelLoginWrapper || !bytes.Equal(modDataRequest.Data, modData) {
		t.Fatalf("ModData request=%+v", modDataRequest)
	}

	ordinary := integrationFMLWrapper(forgeChannelHandshake, 6, []byte{0xcc})
	integrationSendLoginPluginRequest(t, backend, backendConn, proto.LoginPluginRequest{
		MessageID: 77,
		Channel:   forgeChannelLoginWrapper,
		Data:      ordinary,
	})
	ordinaryPacket := integrationReadPacket(t, clientPeerState, &clientPeerBuf, encryptedClientPeer, "ordinary FML request")
	ordinaryRequest, err := proto.DecodeLoginPluginRequest(ordinaryPacket.Data)
	if err != nil {
		t.Fatal(err)
	}
	if ordinaryRequest.MessageID != 2 || ordinaryRequest.MessageID <= 0 || ordinaryRequest.Channel != forgeChannelLoginWrapper || !bytes.Equal(ordinaryRequest.Data, ordinary) {
		t.Fatalf("ordinary FML request=%+v", ordinaryRequest)
	}
	responseWriter := proto.NewPacketWriter()
	(proto.LoginPluginResponse{MessageID: ordinaryRequest.MessageID, Successful: true, Data: []byte{0x99}}).Encode(responseWriter)
	if err := writePacketTo(clientPeerState, encryptedClientPeer, proto.NewRawPacket(proto.PacketServerLoginPluginResponse, responseWriter.Bytes())); err != nil {
		t.Fatal(err)
	}
	ordinaryResponse := integrationReadLoginPluginResponse(t, backend, &backendBuf, backendConn, "ordinary FML response")
	if ordinaryResponse.MessageID != 77 || !ordinaryResponse.Successful || !bytes.Equal(ordinaryResponse.Data, []byte{0x99}) {
		t.Fatalf("ordinary FML response=%+v", ordinaryResponse)
	}

	loginSuccess := proto.NewPacketWriter()
	loginSuccess.Write(profile.UUID[:])
	loginSuccess.WriteString(name)
	loginSuccess.WriteVarInt(0)
	loginSuccessWire, ok := proto.NewRawPacket(proto.PacketClientLoginSuccess, loginSuccess.Bytes()).EncodeWithLen(backend)
	if !ok {
		t.Fatal("encode LoginSuccess")
	}
	backend.SetState(proto.ClientStatePlay)
	playPayload := bytes.Repeat([]byte{0x5a}, 600)
	playWire, ok := proto.NewRawPacket(0x17, playPayload).EncodeWithLen(backend)
	if !ok {
		t.Fatal("encode queued PLAY packet")
	}
	if _, err := backendConn.Write(append(loginSuccessWire, playWire...)); err != nil {
		t.Fatal(err)
	}
	successPacket := integrationReadPacket(t, clientPeerState, &clientPeerBuf, encryptedClientPeer, "LoginSuccess")
	if successPacket.ID != proto.PacketClientLoginSuccess {
		t.Fatalf("LoginSuccess id=0x%02x", successPacket.ID)
	}

	result := <-connectDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.client == nil || result.client.State() != proto.ClientStatePlay || client.State() != proto.ClientStatePlay {
		t.Fatalf("states: backend=%v client=%v", result.client.State(), client.State())
	}
	if result.outbound == nil {
		t.Fatal("missing backend connection")
	}
	clientPeerState.SetState(proto.ClientStatePlay)
	proxyDone := make(chan struct{})
	go func() {
		lobbyRouteProxy(encryptedProxyClientConn, result.outbound, result.buf)
		close(proxyDone)
	}()
	queuedPlay := integrationReadPacket(t, clientPeerState, &clientPeerBuf, encryptedClientPeer, "queued encrypted PLAY packet")
	if queuedPlay.ID != 0x17 || !bytes.Equal(queuedPlay.Data, playPayload) {
		t.Fatalf("queued PLAY packet id=0x%02x bytes=%d", queuedPlay.ID, len(queuedPlay.Data))
	}
	streamPayload := bytes.Repeat([]byte{0xa5}, 900)
	if err := writePacketTo(backend, backendConn, proto.NewRawPacket(0x3f, streamPayload)); err != nil {
		t.Fatal(err)
	}
	streamedPlay := integrationReadPacket(t, clientPeerState, &clientPeerBuf, encryptedClientPeer, "streamed encrypted PLAY packet")
	if streamedPlay.ID != 0x3f || !bytes.Equal(streamedPlay.Data, streamPayload) {
		t.Fatalf("streamed PLAY packet id=0x%02x bytes=%d", streamedPlay.ID, len(streamedPlay.Data))
	}
	_ = encryptedClientPeer.Close()
	_ = backendConn.Close()
	select {
	case <-proxyDone:
	case <-time.After(time.Second):
		t.Fatal("proxy did not close after encrypted PLAY test")
	}

	cache := server.ForgeLoginCache()
	if len(cache) != 2 {
		t.Fatalf("cached exchanges=%d, want 2: %#v", len(cache), cache)
	}
	first, err := proto.DecodeLoginPluginRequest(cache[0].Request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := proto.DecodeLoginPluginRequest(cache[1].Request)
	if err != nil {
		t.Fatal(err)
	}
	if first.MessageID != 1 || !cache[0].NoResponse || first.Channel != forgeChannelLoginWrapper || !bytes.Equal(first.Data, modData) || cache[0].Success || len(cache[0].Data) != 0 {
		t.Fatalf("cached ModData exchange=%+v cache=%#v", first, cache[0])
	}
	if second.MessageID != 2 || second.MessageID <= 0 || cache[1].NoResponse || second.Channel != forgeChannelLoginWrapper || !bytes.Equal(second.Data, ordinary) || !cache[1].Success || !bytes.Equal(cache[1].Data, []byte{0x99}) {
		t.Fatalf("cached ordinary exchange=%+v cache=%#v", second, cache[1])
	}
}

func TestColdForgeReplayMixedOrderHasOneCompressionAndNoResponseGap(t *testing.T) {
	proxyConn, clientConn := net.Pipe()
	defer proxyConn.Close()
	defer clientConn.Close()

	client := proto.DummyClient()
	client.SetState(proto.ClientStateLogin)
	server := NewServerState()
	firstData := integrationFMLWrapper(forgeChannelHandshake, 5, []byte{0x01})
	secondData := integrationFMLWrapper(forgeChannelHandshake, 6, []byte{0x02})
	server.SetForgeLoginCache([]ForgeLoginExchange{
		{Request: encodeLoginPluginRequest(proto.LoginPluginRequest{MessageID: 1, Channel: forgeChannelLoginWrapper, Data: firstData}), NoResponse: true},
		{Request: encodeLoginPluginRequest(proto.LoginPluginRequest{MessageID: 2, Channel: forgeChannelLoginWrapper, Data: secondData}), Success: true, Data: []byte{0x03}},
	})

	peerDone := make(chan error, 1)
	go func() {
		peer := proto.DummyClient()
		peer.SetState(proto.ClientStateLogin)
		var buf []byte
		packet, _, more, err := proto.ReadPacketWithTimeout(peer, &buf, clientConn, 500*time.Millisecond)
		if err != nil || !more || packet.ID != proto.PacketClientSetCompression {
			peerDone <- fmt.Errorf("cold compression packet: id=0x%02x more=%v err=%v", packet.ID, more, err)
			return
		}
		compression, err := proto.DecodeSetCompression(packet.Data)
		if err != nil {
			peerDone <- err
			return
		}
		peer.SetCompression(compression.Threshold)

		packet, _, more, err = proto.ReadPacketWithTimeout(peer, &buf, clientConn, 500*time.Millisecond)
		if err != nil || !more || packet.ID != proto.PacketClientLoginPluginRequest {
			peerDone <- fmt.Errorf("cold ModData packet: id=0x%02x more=%v err=%v", packet.ID, more, err)
			return
		}
		first, err := proto.DecodeLoginPluginRequest(packet.Data)
		if err != nil {
			peerDone <- err
			return
		}
		if first.MessageID != 1 || !bytes.Equal(first.Data, firstData) {
			peerDone <- fmt.Errorf("cold first request=%+v", first)
			return
		}

		packet, _, more, err = proto.ReadPacketWithTimeout(peer, &buf, clientConn, 500*time.Millisecond)
		if err != nil || !more || packet.ID != proto.PacketClientLoginPluginRequest {
			peerDone <- fmt.Errorf("cold ordinary packet: id=0x%02x more=%v err=%v", packet.ID, more, err)
			return
		}
		second, err := proto.DecodeLoginPluginRequest(packet.Data)
		if err != nil {
			peerDone <- err
			return
		}
		if second.MessageID != 2 || !bytes.Equal(second.Data, secondData) {
			peerDone <- fmt.Errorf("cold second request=%+v", second)
			return
		}
		response := proto.NewPacketWriter()
		(proto.LoginPluginResponse{MessageID: second.MessageID, Successful: true, Data: []byte{0x04}}).Encode(response)
		if err := writePacketTo(peer, clientConn, proto.NewRawPacket(proto.PacketServerLoginPluginResponse, response.Bytes())); err != nil {
			peerDone <- err
			return
		}
		if err := clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			peerDone <- err
			return
		}
		var one [1]byte
		n, err := clientConn.Read(one[:])
		_ = clientConn.SetReadDeadline(time.Time{})
		if err == nil || n != 0 {
			peerDone <- fmt.Errorf("unexpected extra cold replay bytes n=%d err=%v", n, err)
			return
		}
		if networkErr, ok := err.(net.Error); !ok || !networkErr.Timeout() {
			peerDone <- fmt.Errorf("cold replay trailing read: %w", err)
			return
		}
		peerDone <- nil
	}()

	compressed, err := replayColdForgeLogin(client, proxyConn, server, new([]byte))
	if err != nil {
		t.Fatal(err)
	}
	if !compressed || !client.IsCompressed() || client.Compressed() != proto.CompressionThreshold {
		t.Fatalf("cold compression state: sent=%v compressed=%v threshold=%d", compressed, client.IsCompressed(), client.Compressed())
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}
