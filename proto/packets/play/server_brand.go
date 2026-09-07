package play

import (
	"net"

	"lazymc/proto"
)

// Minecraft channel to set brand.
const brandChannel = "minecraft:brand"

// Server brand to send to client in lobby world.
//
// Shown in F3 menu. Updated once client is relayed to real server.
var serverBrand = []byte("lazymc")

// SendServerBrand sends the lobby brand to the client.
func SendServerBrand(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn) error {
	w := proto.NewPacketWriter()
	w.WriteString(brandChannel)
	protocol := clientInfo.GetProtocol()
	if isV1_20_1(protocol) {
		// 1.20.1 custom payload data is the packet's remaining bytes; it is
		// not a second length-prefixed byte array.
		w.Write(serverBrand)
	} else {
		// Older protocol definitions used the byte-array helper here.
		w.WriteByteArray(serverBrand)
	}

	packetID := byte(V1_16_3ClientBoundPluginMessage)
	if isV1_20_1(protocol) {
		packetID = V1_20_1ClientBoundPluginMessage
	} else if protocol != nil && *protocol >= ProtocolV1_17 {
		packetID = V1_17ClientBoundPluginMessage
	}
	return writePacket(client, conn, proto.NewRawPacket(packetID, w.Bytes()))
}
