package play

import (
	"net"

	"lazymc/proto"
)

// SendSound plays a sound effect at world origin.
func SendSound(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn, soundName string) error {
	protocol := clientInfo.GetProtocol()
	if isV1_20_1(protocol) {
		return sendSoundV1_20_1(client, conn, soundName)
	}
	w := proto.NewPacketWriter()
	w.WriteString(soundName)
	w.WriteVarInt(0)    // sound_category master
	w.WriteInt32(0)     // effect_pos_x (multiplied by 8)
	w.WriteInt32(0)     // effect_pos_y
	w.WriteInt32(0)     // effect_pos_z
	w.WriteFloat32(1.0) // volume
	w.WriteFloat32(1.0) // pitch

	packetID := byte(V1_16_3NamedSoundEffect)
	if protocol != nil && *protocol >= ProtocolV1_17 {
		packetID = V1_17NamedSoundEffect
	}
	return writePacket(client, conn, proto.NewRawPacket(packetID, w.Bytes()))
}

func sendSoundV1_20_1(client *proto.Client, conn net.Conn, soundName string) error {
	w := proto.NewPacketWriter()
	w.WriteVarInt(0) // inline sound event follows sound ID 0
	w.WriteString(soundName)
	w.WriteBool(false)  // optional range absent
	w.WriteVarInt(0)    // master sound source
	w.WriteInt32(0)     // x, fixed-point block coordinate (x * 8)
	w.WriteInt32(0)     // y
	w.WriteInt32(0)     // z
	w.WriteFloat32(1.0) // volume
	w.WriteFloat32(1.0) // pitch
	w.WriteInt64(0)     // seed
	return writePacket(client, conn, proto.NewRawPacket(V1_20_1NamedSoundEffect, w.Bytes()))
}
