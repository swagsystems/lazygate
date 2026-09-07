package play

import (
	"net"

	"lazymc/proto"
)

// SendTimeUpdate sets world time to 0.
//
// Required once for keep-alive packets.
func SendTimeUpdate(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn) error {
	w := proto.NewPacketWriter()
	w.WriteInt64(0) // world_age
	w.WriteInt64(0) // time_of_day

	packetID := byte(V1_16_3TimeUpdate)
	protocol := clientInfo.GetProtocol()
	if isV1_20_1(protocol) {
		packetID = V1_20_1TimeUpdate
	} else if protocol != nil && *protocol >= ProtocolV1_17 {
		packetID = V1_17TimeUpdate
	}
	return writePacket(client, conn, proto.NewRawPacket(packetID, w.Bytes()))
}
