package proto

// Client-facing actions, mirroring lazymc's proto/action.rs.

import (
	"io"
	"net"
)

// Kick kicks a client with a message.
//
// Should close the connection afterwards.
func Kick(client *Client, msg string, conn net.Conn) error {
	// Login disconnect id 0x00, game disconnect id 0x1A
	id := byte(PacketClientLoginDisconnect)
	if client.State() == ClientStatePlay {
		id = PacketGameDisconnect
	}

	// Messages encode as a length-prefixed JSON string
	data := StringBytes(ChatMessageJSON(msg))

	packet := NewRawPacket(id, data)
	encoded, ok := packet.EncodeWithLen(client)
	if !ok {
		return io.ErrClosedPipe
	}
	_, err := conn.Write(encoded)
	return err
}
