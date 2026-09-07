package play

import (
	"net"

	"lazymc/nbt"
	"lazymc/proto"
)

// RespawnParams carries the join game data used to build a respawn packet.
type RespawnParams struct {
	Dimension        *nbt.Compound
	WorldName        string
	HashedSeed       int64
	GameMode         byte
	PreviousGameMode byte
	IsDebug          bool
	IsFlat           bool
}

// SendRespawn sends a respawn packet to jump from lobby into the now loaded
// server.
func SendRespawn(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn, params RespawnParams) error {
	protocol := clientInfo.GetProtocol()
	if isV1_20_1(protocol) {
		return sendRespawnV1_20_1(client, conn, params)
	}
	w := proto.NewPacketWriter()
	nbt.WriteCompoundTag(w, params.Dimension)
	w.WriteString(params.WorldName)
	w.WriteInt64(params.HashedSeed)
	w.WriteU8(params.GameMode)
	w.WriteU8(params.PreviousGameMode)
	w.WriteBool(params.IsDebug)
	w.WriteBool(params.IsFlat)
	w.WriteBool(false) // copy_metadata

	packetID := byte(V1_16_3Respawn)
	if protocol != nil && *protocol >= ProtocolV1_17 {
		packetID = V1_17Respawn
	}
	return writePacket(client, conn, proto.NewRawPacket(packetID, w.Bytes()))
}

func sendRespawnV1_20_1(client *proto.Client, conn net.Conn, params RespawnParams) error {
	w := proto.NewPacketWriter()
	w.WriteString("minecraft:overworld") // dimension type
	w.WriteString(params.WorldName)
	w.WriteInt64(params.HashedSeed)
	w.WriteU8(params.GameMode)
	w.WriteU8(params.PreviousGameMode)
	w.WriteBool(params.IsDebug)
	w.WriteBool(params.IsFlat)
	w.WriteBool(false) // death location absent
	w.WriteVarInt(0)   // portal cooldown
	return writePacket(client, conn, proto.NewRawPacket(V1_20_1Respawn, w.Bytes()))
}
