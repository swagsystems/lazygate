package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lazymc/proto"
)

func TestMinecraftServerHashSignedHex(t *testing.T) {
	tests := map[string]string{
		"Notch": "4ed1f46bbe04bc756bcb17c0c7ce3e4632f06a48",
		"jeb_":  "-7c9d5b0044c130109a5d7b5fb5c317c02b4e28c1",
		"simon": "88e16a1019277b15d58faf0541e11910eb756f6",
	}
	for input, want := range tests {
		if got := minecraftServerHash(input, nil, nil); got != want {
			t.Errorf("minecraftServerHash(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLookupJoinedProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/minecraft/hasJoined" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("username") != "PlayerOne" || r.URL.Query().Get("serverId") != "abc" {
			t.Errorf("query = %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"00112233445566778899aabbccddeeff","name":"PlayerOne","properties":[{"name":"textures","value":"value","signature":"sig"}]}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	profile, err := lookupJoinedProfile(ctx, server.Client(), server.URL, "PlayerOne", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "PlayerOne" || len(profile.Properties) != 1 || profile.Properties[0].Signature == nil {
		t.Fatalf("profile = %+v", profile)
	}
	if got := profile.UUID; got != [16]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff} {
		t.Fatalf("uuid = %x", got)
	}
}

func TestMinecraftEncryptedConnRoundTrip(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	secret := []byte("0123456789abcdef")
	server, err := newMinecraftEncryptedConn(left, secret)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newMinecraftEncryptedConn(right, secret)
	if err != nil {
		t.Fatal(err)
	}

	want := []byte("encrypted minecraft stream")
	errCh := make(chan error, 1)
	go func() {
		_, err := server.Write(want)
		errCh <- err
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	block, err := aes.NewCipher(secret)
	if err != nil {
		t.Fatal(err)
	}
	if block.BlockSize() != len(secret) {
		t.Fatal("unexpected AES block size")
	}
}

func TestCFB8MatchesNISTVector(t *testing.T) {
	decode := func(value string) []byte {
		out, err := hex.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	key := decode("2b7e151628aed2a6abf7158809cf4f3c")
	iv := decode("000102030405060708090a0b0c0d0e0f")
	plain := decode("6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e51")
	want := decode("3b79424c9c0dd436bace9e0ed4586a4f32b9ded50ae3ba69d472e88267fb5052")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(plain))
	newCFB8(block, iv, false).XORKeyStream(got, plain)
	if !bytes.Equal(got, want) {
		t.Fatalf("CFB8 ciphertext = %x, want %x", got, want)
	}
	decoded := make([]byte, len(got))
	decryptBlock, _ := aes.NewCipher(key)
	newCFB8(decryptBlock, iv, true).XORKeyStream(decoded, got)
	if !bytes.Equal(decoded, plain) {
		t.Fatalf("CFB8 plaintext = %x, want %x", decoded, plain)
	}
}

func TestAuthenticateLobbyClient(t *testing.T) {
	oldLoginTimeout := proto.LoginReadTimeout
	oldAuthClientTimeout := onlineAuthClientResponseTimeout
	proto.LoginReadTimeout = 5 * time.Millisecond
	onlineAuthClientResponseTimeout = 250 * time.Millisecond
	defer func() {
		proto.LoginReadTimeout = oldLoginTimeout
		onlineAuthClientResponseTimeout = oldAuthClientTimeout
	}()
	session := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") != "PlayerOne" || r.URL.Query().Get("serverId") == "" {
			t.Errorf("unexpected session query: %v", r.URL.Query())
		}
		_, _ = io.WriteString(w, `{"id":"00112233445566778899aabbccddeeff","name":"PlayerOne","properties":[]}`)
	}))
	defer session.Close()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	_ = clientConn.SetDeadline(time.Now().Add(3 * time.Second))
	config := &Config{Auth: Auth{OnlineMode: true, SessionServer: session.URL}}
	serverClient := proto.DummyClient()
	serverClient.SetState(proto.ClientStateLogin)
	type authResult struct {
		conn    net.Conn
		profile *proto.GameProfile
		err     error
	}
	resultCh := make(chan authResult, 1)
	go func() {
		var buf []byte
		conn, profile, err := authenticateLobbyClient(serverClient, serverConn, &buf, "PlayerOne", config)
		resultCh <- authResult{conn: conn, profile: profile, err: err}
	}()

	clientState := proto.DummyClient()
	clientState.SetState(proto.ClientStateLogin)
	var clientBuf []byte
	request, _, more, err := proto.ReadPacketWithTimeout(clientState, &clientBuf, clientConn, 250*time.Millisecond)
	if err != nil || !more || request.ID != proto.PacketClientEncryptionRequest {
		t.Fatalf("encryption request: id=%d more=%v err=%v", request.ID, more, err)
	}
	serverID, rest, err := readTestString(request.Data)
	if err != nil {
		t.Fatal(err)
	}
	if serverID != "" {
		t.Fatalf("server ID = %q, want empty for modern Java login", serverID)
	}
	publicDER, rest, err := readLoginByteArray(rest, 4096)
	if err != nil {
		t.Fatal(err)
	}
	verifyToken, rest, err := readLoginByteArray(rest, 64)
	if err != nil || len(rest) != 0 {
		t.Fatalf("verify token: rest=%d err=%v", len(rest), err)
	}
	parsed, err := x509.ParsePKIXPublicKey(publicDER)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatal("login public key was not RSA")
	}
	secret := []byte("0123456789abcdef")
	encryptedSecret, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey, secret)
	if err != nil {
		t.Fatal(err)
	}
	encryptedToken, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey, verifyToken)
	if err != nil {
		t.Fatal(err)
	}
	response := proto.NewPacketWriter()
	response.WriteByteArray(encryptedSecret)
	response.WriteByteArray(encryptedToken)
	// The authentication phase has its own bounded budget and must not inherit
	// the shorter generic LOGIN edge timeout.
	time.Sleep(25 * time.Millisecond)
	if err := writePacketTo(clientState, clientConn, proto.NewRawPacket(proto.PacketServerEncryptionResponse, response.Bytes())); err != nil {
		t.Fatal(err)
	}

	result := <-resultCh
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.profile == nil || result.profile.Name != "PlayerOne" {
		t.Fatalf("profile = %+v", result.profile)
	}

	encryptedClient, err := newMinecraftEncryptedConn(clientConn, secret)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("post-auth")
	writeErr := make(chan error, 1)
	go func() {
		_, err := result.conn.Write(want)
		writeErr <- err
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(encryptedClient, got); err != nil {
		t.Fatal(err)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func readTestString(data []byte) (string, []byte, error) {
	b, rest, err := readLoginByteArray(data, 32767)
	return string(b), rest, err
}

func TestRespondVelocityForwarding(t *testing.T) {
	secret := []byte("a-production-length-forwarding-secret")
	secretPath := filepath.Join(t.TempDir(), "forwarding.secret")
	if err := os.WriteFile(secretPath, append(secret, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	signature := "signed"
	profile := &proto.GameProfile{
		UUID:       [16]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff},
		Name:       "PlayerOne",
		Properties: []proto.ProfileProperty{{Name: "textures", Value: "value", Signature: &signature}},
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	serverClient := proto.DummyClient()
	serverClient.SetState(proto.ClientStateLogin)
	request := proto.LoginPluginRequest{MessageID: 42, Channel: velocityPlayerInfoChannel, Data: []byte{4}}
	errCh := make(chan error, 1)
	go func() {
		errCh <- respondVelocityForwarding(serverClient, left, request, profile, &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 25565}, &Config{Auth: Auth{ForwardingSecretFile: secretPath}})
	}()

	reader := proto.DummyClient()
	reader.SetState(proto.ClientStateLogin)
	var buf []byte
	packet, _, more, err := proto.ReadPacket(reader, &buf, right)
	if err != nil || !more || packet.ID != proto.PacketServerLoginPluginResponse {
		t.Fatalf("response: id=%d more=%v err=%v", packet.ID, more, err)
	}
	response, err := proto.DecodeLoginPluginResponse(packet.Data)
	if err != nil {
		t.Fatal(err)
	}
	if response.MessageID != 42 || !response.Successful || len(response.Data) <= sha256.Size {
		t.Fatalf("response = %+v", response)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(response.Data[sha256.Size:])
	if !hmac.Equal(response.Data[:sha256.Size], mac.Sum(nil)) {
		t.Fatal("forwarding HMAC mismatch")
	}
	data := response.Data[sha256.Size:]
	n, version, ok := proto.ReadVarInt(data)
	if !ok || version != velocityModernLazySession {
		t.Fatalf("version = %d", version)
	}
	address, rest, err := readTestString(data[n:])
	if err != nil || address != "203.0.113.9" || len(rest) < 16 {
		t.Fatalf("address=%q rest=%d err=%v", address, len(rest), err)
	}
	if got := rest[:16]; string(got) != string(profile.UUID[:]) {
		t.Fatalf("uuid = %x", got)
	}
	name, _, err := readTestString(rest[16:])
	if err != nil || name != profile.Name {
		t.Fatalf("name=%q err=%v", name, err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}
