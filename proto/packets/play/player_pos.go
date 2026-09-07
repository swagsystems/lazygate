package play

import (
	"net"

	"lazymc/proto"
)

// SendPlayerPos moves the player to the world origin.
func SendPlayerPos(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn) error {
	w := proto.NewPacketWriter()
	w.WriteFloat64(0.0)  // x
	w.WriteFloat64(0.0)  // y
	w.WriteFloat64(0.0)  // z
	w.WriteFloat32(0.0)  // yaw
	w.WriteFloat32(90.0) // pitch
	w.WriteU8(0)         // flags
	w.WriteVarInt(0)     // teleport_id
	// v1_17 only: dismount_vehicle

	packetID := byte(V1_16_3PlayerPositionAndLook)
	protocol := clientInfo.GetProtocol()
	if isV1_20_1(protocol) {
		packetID = V1_20_1PlayerPositionAndLook
	} else if protocol != nil && *protocol >= ProtocolV1_17 {
		packetID = V1_17PlayerPositionAndLook
		w.WriteBool(true) // dismount_vehicle
	}
	return writePacket(client, conn, proto.NewRawPacket(packetID, w.Bytes()))
}
