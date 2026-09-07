package proto

import (
	"net"
	"sync"
)

// ClientState is the protocol state a client may be in.
//
// Note: this does not include the `play` state in the state map since lazymc
// never proxies a client through play state at the handshake level.
type ClientState int32

// Client states.
const (
	ClientStateHandshake ClientState = iota
	ClientStateStatus
	ClientStateLogin
	ClientStatePlay
)

// FromID converts a protocol state id.
func (s ClientState) FromID(id int32) (ClientState, bool) {
	switch id {
	case 0:
		return ClientStateHandshake, true
	case 1:
		return ClientStateStatus, true
	case 2:
		return ClientStateLogin, true
	default:
		return s, false
	}
}

// ToID returns the protocol state id. Play is -1.
func (s ClientState) ToID() int32 {
	switch s {
	case ClientStateHandshake:
		return 0
	case ClientStateStatus:
		return 1
	case ClientStateLogin:
		return 2
	default:
		return -1
	}
}

// Client tracks per-connection protocol state.
type Client struct {
	// Client peer address.
	Peer net.Addr

	// Current client state.
	stateMu sync.Mutex
	state   ClientState

	// Compression threshold. -1 when disabled.
	compression int32
}

// NewClient constructs a new client.
func NewClient(peer net.Addr) *Client {
	return &Client{Peer: peer, state: ClientStateHandshake, compression: -1}
}

// DummyClient constructs a client with a dummy peer address.
func DummyClient() *Client {
	return NewClient(&net.TCPAddr{IP: net.IPv4zero, Port: 0})
}

// State returns the client state.
func (c *Client) State() ClientState {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state
}

// SetState sets the client state.
func (c *Client) SetState(state ClientState) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.state = state
}

// Compressed returns the compression threshold, negative if disabled.
func (c *Client) Compressed() int32 {
	return c.compression
}

// IsCompressed reports whether compression is used.
func (c *Client) IsCompressed() bool {
	return c.compression >= 0
}

// SetCompression sets the compression threshold.
func (c *Client) SetCompression(threshold int32) {
	TraceLog("lazymc", "Client now uses compression threshold of %d", threshold)
	c.compression = threshold
}

// ClientInfo is client information useful during connection handling.
type ClientInfo struct {
	// Used protocol version.
	Protocol *uint32

	// Handshake as received from client.
	Handshake *Handshake

	// Client username.
	Username *string

	// Authenticated online profile. Nil for an unauthenticated/offline client.
	Profile *GameProfile
}

// GameProfile is the identity returned by the Minecraft session service.
type GameProfile struct {
	UUID       [16]byte
	Name       string
	Properties []ProfileProperty
}

// ProfileProperty is a signed or unsigned game-profile property.
type ProfileProperty struct {
	Name      string
	Value     string
	Signature *string
}

// EmptyClientInfo returns an empty client info.
func EmptyClientInfo() ClientInfo { return ClientInfo{} }

// GetProtocol returns the protocol version.
func (i *ClientInfo) GetProtocol() *uint32 {
	if i.Protocol != nil {
		return i.Protocol
	}
	if i.Handshake != nil {
		p := uint32(i.Handshake.ProtocolVersion)
		return &p
	}
	return nil
}
