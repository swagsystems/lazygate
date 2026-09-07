package play

import (
	"net"
	"sync/atomic"

	"lazymc/proto"
)

// Auto incrementing ID source for keep alive packets.
var keepAliveID atomic.Uint64

// SendKeepAlive sends a keep alive packet to the client.
//
// Required periodically in play mode to prevent client timeout.
func SendKeepAlive(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn) error {
	// Keep sending new IDs
	id := keepAliveID.Add(1) - 1

	w := proto.NewPacketWriter()
	w.WriteUint64(id)

	idByte := byte(V1_16_3ClientBoundKeepAlive)
	protocol := clientInfo.GetProtocol()
	if isV1_20_1(protocol) {
		idByte = V1_20_1ClientBoundKeepAlive
	} else if protocol != nil && *protocol >= ProtocolV1_17 {
		idByte = V1_17ClientBoundKeepAlive
	}
	return writePacket(client, conn, proto.NewRawPacket(idByte, w.Bytes()))
}
