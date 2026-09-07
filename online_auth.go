package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"lazymc/proto"
)

const (
	onlineAuthTimeout      = 15 * time.Second
	maxSessionResponseSize = 1 << 20
)

// Forge clients can spend longer than the generic hostile LOGIN framing
// budget processing the advertised mod set before returning EncryptionResponse.
// Connection admission remains bounded separately, so this phase gets a
// finite but practical client budget without extending the Mojang HTTP call.
var onlineAuthClientResponseTimeout = 60 * time.Second

var lobbyRSAKey struct {
	sync.Once
	private *rsa.PrivateKey
	public  []byte
	err     error
}

func getLobbyRSAKey() (*rsa.PrivateKey, []byte, error) {
	lobbyRSAKey.Do(func() {
		lobbyRSAKey.private, lobbyRSAKey.err = rsa.GenerateKey(rand.Reader, 1024)
		if lobbyRSAKey.err != nil {
			return
		}
		lobbyRSAKey.public, lobbyRSAKey.err = x509.MarshalPKIXPublicKey(&lobbyRSAKey.private.PublicKey)
	})
	return lobbyRSAKey.private, lobbyRSAKey.public, lobbyRSAKey.err
}

// authenticateLobbyClient performs the Java Edition online-mode login
// encryption exchange and validates the username with the Minecraft session
// service. The returned connection transparently decrypts reads and encrypts
// writes for the rest of the lobby and backend relay.
func authenticateLobbyClient(client *proto.Client, conn net.Conn, buf *[]byte, username string, config *Config) (net.Conn, *proto.GameProfile, error) {
	privateKey, publicKey, err := getLobbyRSAKey()
	if err != nil {
		return nil, nil, fmt.Errorf("generate login key: %w", err)
	}

	// Modern Java servers send an empty server ID. The shared secret and
	// public key still make the session hash unique; a random legacy ID causes
	// some Forge 1.20.1 clients to ignore the encryption request entirely.
	serverID := ""
	verifyToken := make([]byte, 4)
	if _, err := rand.Read(verifyToken); err != nil {
		return nil, nil, fmt.Errorf("generate verify token: %w", err)
	}

	w := proto.NewPacketWriter()
	w.WriteString(serverID)
	w.WriteByteArray(publicKey)
	w.WriteByteArray(verifyToken)
	request := proto.NewRawPacket(proto.PacketClientEncryptionRequest, w.Bytes())
	encodedRequest, ok := request.EncodeWithLen(client)
	if !ok {
		return nil, nil, proto.ErrMalformedPacket
	}
	TraceLog(TargetLobby, "Sending encryption request packet_id=0x%02x wire_length=%d server_id_length=%d public_key_length=%d verify_token_length=%d compressed=%t", request.ID, len(encodedRequest), len(serverID), len(publicKey), len(verifyToken), client.IsCompressed())
	written, err := conn.Write(encodedRequest)
	if err != nil {
		return nil, nil, fmt.Errorf("send encryption request: %w", err)
	}
	if written != len(encodedRequest) {
		return nil, nil, fmt.Errorf("send encryption request: %w", io.ErrShortWrite)
	}
	responseStarted := time.Now()

	if err := conn.SetReadDeadline(time.Now().Add(onlineAuthClientResponseTimeout)); err != nil {
		return nil, nil, err
	}
	packet, raw, more, err := proto.ReadPacketWithTimeout(client, buf, conn, onlineAuthClientResponseTimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("read encryption response after %s: %w", time.Since(responseStarted).Round(time.Millisecond), err)
	}
	TraceLog(TargetLobby, "Encryption response read more=%t packet_id=0x%02x wire_length=%d buffered_length=%d elapsed=%s", more, packet.ID, len(raw), len(*buf), time.Since(responseStarted).Round(time.Millisecond))
	if !more || packet.ID != proto.PacketServerEncryptionResponse {
		return nil, nil, errors.New("client did not provide an encryption response")
	}
	// Encryption begins immediately after this frame. ReadPacket may retain
	// bytes read past the frame boundary; those bytes would still be ciphertext
	// and cannot safely be handed to the plaintext packet buffer.
	if len(*buf) != 0 {
		return nil, nil, errors.New("unexpected buffered data at encryption boundary")
	}

	encryptedSecret, rest, err := readLoginByteArray(packet.Data, 512)
	if err != nil {
		return nil, nil, fmt.Errorf("decode shared secret: %w", err)
	}
	encryptedToken, rest, err := readLoginByteArray(rest, 512)
	if err != nil || len(rest) != 0 {
		return nil, nil, errors.New("decode verify token")
	}
	secret, err := rsa.DecryptPKCS1v15(rand.Reader, privateKey, encryptedSecret)
	if err != nil || len(secret) != 16 {
		return nil, nil, errors.New("invalid shared secret")
	}
	token, err := rsa.DecryptPKCS1v15(rand.Reader, privateKey, encryptedToken)
	if err != nil || !bytes.Equal(token, verifyToken) {
		return nil, nil, errors.New("invalid verify token")
	}

	encryptedConn, err := newMinecraftEncryptedConn(conn, secret)
	if err != nil {
		return nil, nil, err
	}
	if err := encryptedConn.SetDeadline(time.Time{}); err != nil {
		return nil, nil, err
	}

	base := config.Auth.SessionServer
	if base == "" {
		base = "https://sessionserver.mojang.com"
	}
	ctx, cancel := context.WithTimeout(context.Background(), onlineAuthTimeout)
	defer cancel()
	profile, err := lookupJoinedProfile(ctx, http.DefaultClient, base, username, minecraftServerHash(serverID, secret, publicKey))
	if err != nil {
		return nil, nil, err
	}
	return encryptedConn, profile, nil
}

func readLoginByteArray(data []byte, max int) ([]byte, []byte, error) {
	n, length, ok := proto.ReadVarInt(data)
	if !ok || length < 0 || int(length) > max || n+int(length) > len(data) {
		return nil, nil, proto.ErrMalformedPacket
	}
	value := append([]byte(nil), data[n:n+int(length)]...)
	return value, data[n+int(length):], nil
}

func minecraftServerHash(serverID string, secret, publicKey []byte) string {
	h := sha1.New()
	h.Write([]byte(serverID))
	h.Write(secret)
	h.Write(publicKey)
	digest := h.Sum(nil)
	n := new(big.Int).SetBytes(digest)
	if digest[0]&0x80 != 0 {
		twoTo160 := new(big.Int).Lsh(big.NewInt(1), uint(len(digest)*8))
		n.Sub(n, twoTo160)
	}
	return n.Text(16)
}

type joinedProfileResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Properties []struct {
		Name      string  `json:"name"`
		Value     string  `json:"value"`
		Signature *string `json:"signature"`
	} `json:"properties"`
}

func lookupJoinedProfile(ctx context.Context, client *http.Client, base, username, serverHash string) (*proto.GameProfile, error) {
	endpoint, err := url.Parse(strings.TrimRight(base, "/") + "/session/minecraft/hasJoined")
	if err != nil {
		return nil, err
	}
	if endpoint.Scheme != "https" {
		host := endpoint.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("Minecraft session service must use HTTPS")
		}
	}
	q := endpoint.Query()
	q.Set("username", username)
	q.Set("serverId", serverHash)
	endpoint.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("verify Minecraft session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, errors.New("Minecraft session was not verified")
	}
	var wire joinedProfileResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxSessionResponseSize))
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode Minecraft session profile: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("Minecraft session profile contains trailing data")
	}
	if !strings.EqualFold(wire.Name, username) {
		return nil, errors.New("Minecraft session profile name mismatch")
	}
	id, err := parseProfileUUID(wire.ID)
	if err != nil {
		return nil, err
	}
	if len(wire.Name) == 0 || len(wire.Name) > 16 || len(wire.Properties) > 64 {
		return nil, errors.New("invalid Minecraft session profile bounds")
	}
	profile := &proto.GameProfile{UUID: id, Name: wire.Name, Properties: make([]proto.ProfileProperty, 0, len(wire.Properties))}
	for _, property := range wire.Properties {
		if property.Name == "" || len(property.Name) > 64 || property.Value == "" || len(property.Value) > 32767 || (property.Signature != nil && len(*property.Signature) > 8192) {
			return nil, errors.New("invalid Minecraft session profile property")
		}
		profile.Properties = append(profile.Properties, proto.ProfileProperty{Name: property.Name, Value: property.Value, Signature: property.Signature})
	}
	return profile, nil
}

func parseProfileUUID(raw string) ([16]byte, error) {
	var id [16]byte
	raw = strings.ReplaceAll(raw, "-", "")
	if len(raw) != 32 {
		return id, errors.New("invalid Minecraft session UUID")
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		return id, errors.New("invalid Minecraft session UUID")
	}
	copy(id[:], b)
	return id, nil
}

type minecraftEncryptedConn struct {
	net.Conn
	reader  cipher.Stream
	writer  cipher.Stream
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func newMinecraftEncryptedConn(conn net.Conn, secret []byte) (*minecraftEncryptedConn, error) {
	readBlock, err := aes.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	writeBlock, err := aes.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	return &minecraftEncryptedConn{
		Conn:   conn,
		reader: newCFB8(readBlock, secret, true),
		writer: newCFB8(writeBlock, secret, false),
	}, nil
}

func (c *minecraftEncryptedConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.reader.XORKeyStream(p[:n], p[:n])
	}
	return n, err
}

func (c *minecraftEncryptedConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	encrypted := make([]byte, len(p))
	c.writer.XORKeyStream(encrypted, p)
	written := 0
	for written < len(encrypted) {
		n, err := c.Conn.Write(encrypted[written:])
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return len(p), nil
}

type cfb8 struct {
	block   cipher.Block
	iv      []byte
	scratch []byte
	decrypt bool
}

func newCFB8(block cipher.Block, iv []byte, decrypt bool) cipher.Stream {
	return &cfb8{block: block, iv: append([]byte(nil), iv...), scratch: make([]byte, block.BlockSize()), decrypt: decrypt}
}

func (c *cfb8) XORKeyStream(dst, src []byte) {
	if len(dst) < len(src) {
		panic("cfb8: output smaller than input")
	}
	for i, b := range src {
		copy(c.scratch, c.iv)
		c.block.Encrypt(c.iv, c.iv)
		out := b ^ c.iv[0]
		copy(c.iv, c.scratch[1:])
		if c.decrypt {
			c.iv[len(c.iv)-1] = b
		} else {
			c.iv[len(c.iv)-1] = out
		}
		dst[i] = out
	}
}
