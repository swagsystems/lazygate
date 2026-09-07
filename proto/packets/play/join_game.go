package play

import (
	"net"

	"lazymc/nbt"
	"lazymc/proto"
)

// JoinGameData is data extracted from a JoinGame packet.
type JoinGameData struct {
	Hardcore            *bool
	GameMode            *byte
	PreviousGameMode    *byte
	WorldNames          *[]string
	Dimension           *nbt.Compound
	DimensionCodec      *nbt.Compound
	DimensionType       *string
	WorldName           *string
	HashedSeed          *int64
	MaxPlayers          *int32
	ViewDistance        *int32
	SimulationDistance  *int32
	ReducedDebugInfo    *bool
	EnableRespawnScreen *bool
	IsDebug             *bool
	IsFlat              *bool
}

// FromPacket extracts join game data from a packet.
func JoinGameDataFromPacket(clientInfo *proto.ClientInfo, packet proto.RawPacket) (*JoinGameData, error) {
	protocol := clientInfo.GetProtocol()
	if isV1_20_1(protocol) {
		return decodeJoinGameV1_20_1(packet.Data)
	}
	if protocol != nil && *protocol < ProtocolV1_17 {
		return decodeJoinGameV1_16_3(packet.Data)
	}
	return decodeJoinGameV1_17(packet.Data)
}

// JoinGameIsPacket reports whether the packet ID matches the join game packet
// for the client's protocol version.
func JoinGameIsPacket(clientInfo *proto.ClientInfo, packetID byte) bool {
	protocol := clientInfo.GetProtocol()
	if isV1_20_1(protocol) {
		return packetID == V1_20_1JoinGame
	}
	if protocol != nil && *protocol < ProtocolV1_17 {
		return packetID == V1_16_3JoinGame
	}
	return packetID == V1_17JoinGame
}

type joinGameReader struct {
	data []byte
	pos  int
}

func (r *joinGameReader) varInt() (int32, bool) {
	var result int32
	for i := 0; i < 5; i++ {
		if r.pos >= len(r.data) {
			return 0, false
		}
		b := r.data[r.pos]
		r.pos++
		result |= int32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return result, true
		}
	}
	return 0, false
}

func (r *joinGameReader) bool() (bool, bool) {
	if r.pos >= len(r.data) {
		return false, false
	}
	v := r.data[r.pos]
	r.pos++
	return v != 0, true
}

func (r *joinGameReader) str() (string, bool) {
	n, ok := r.varInt()
	if !ok || n < 0 || int(n) > len(r.data)-r.pos {
		return "", false
	}
	s := string(r.data[r.pos : r.pos+int(n)])
	r.pos += int(n)
	return s, true
}

func (r *joinGameReader) skip(count int) bool {
	if count < 0 || count > len(r.data)-r.pos {
		return false
	}
	r.pos += count
	return true
}

// decodeJoinGameV1_16_3 parses the v1_16_3 join game layout:
// u32 entity_id, bool hardcore, u8 game_mode, u8 previous_game_mode,
// vec<string> world_names, compound dimension_codec, compound dimension,
// string world_name, i64 hashed_seed, varint max_players, varint
// view_distance, bool reduced_debug_info, bool enable_respawn_screen, bool
// is_debug, bool is_flat.
func decodeJoinGameV1_16_3(data []byte) (*JoinGameData, error) {
	r := &joinGameReader{data: data}
	// entity_id u32
	if len(r.data) < 4 {
		return nil, errMalformed("join game")
	}
	r.pos += 4

	d := &JoinGameData{}
	if v, ok := r.bool(); ok {
		d.Hardcore = &v
	} else {
		return nil, errMalformed("join game")
	}
	if len(r.data)-r.pos >= 2 {
		gameMode := r.data[r.pos]
		prev := r.data[r.pos+1]
		r.pos += 2
		d.GameMode = &gameMode
		d.PreviousGameMode = &prev
	} else {
		return nil, errMalformed("join game")
	}

	// world names
	var worldNames []string
	if n, ok := r.varInt(); ok && n >= 0 {
		for i := int32(0); i < n; i++ {
			s, ok := r.str()
			if !ok {
				return nil, errMalformed("join game")
			}
			worldNames = append(worldNames, s)
		}
	} else {
		return nil, errMalformed("join game")
	}
	d.WorldNames = &worldNames

	// dimension codec
	codecReader := nbt.NewReader(r.data[r.pos:])
	codec, ok := nbt.ReadCompoundTag(codecReader)
	if !ok {
		return nil, errMalformed("join game codec")
	}
	r.pos = len(r.data) - codecReader.Remaining()
	d.DimensionCodec = codec

	// dimension
	dimReader := nbt.NewReader(r.data[r.pos:])
	dim, ok := nbt.ReadCompoundTag(dimReader)
	if !ok {
		return nil, errMalformed("join game dimension")
	}
	r.pos = len(r.data) - dimReader.Remaining()
	d.Dimension = dim

	if s, ok := r.str(); ok {
		d.WorldName = &s
	} else {
		return nil, errMalformed("join game")
	}
	if len(r.data)-r.pos >= 8 {
		seed := int64(0)
		for i := 0; i < 8; i++ {
			seed = seed<<8 | int64(r.data[r.pos+i])
		}
		r.pos += 8
		d.HashedSeed = &seed
	} else {
		return nil, errMalformed("join game")
	}
	if v, ok := r.varInt(); ok {
		d.MaxPlayers = &v
	} else {
		return nil, errMalformed("join game")
	}
	if v, ok := r.varInt(); ok {
		d.ViewDistance = &v
	} else {
		return nil, errMalformed("join game")
	}
	if v, ok := r.bool(); ok {
		d.ReducedDebugInfo = &v
	} else {
		return nil, errMalformed("join game")
	}
	if v, ok := r.bool(); ok {
		d.EnableRespawnScreen = &v
	} else {
		return nil, errMalformed("join game")
	}
	if v, ok := r.bool(); ok {
		d.IsDebug = &v
	} else {
		return nil, errMalformed("join game")
	}
	if v, ok := r.bool(); ok {
		d.IsFlat = &v
	} else {
		return nil, errMalformed("join game")
	}

	return d, nil
}

// decodeJoinGameV1_17 parses the v1_17 join game layout, identical to
// v1_16_3's.
func decodeJoinGameV1_17(data []byte) (*JoinGameData, error) {
	return decodeJoinGameV1_16_3(data)
}

// decodeJoinGameV1_20_1 parses the Java 1.20.1 (protocol 763) login layout:
// i32 entity_id, bool hardcore, u8 game_mode, i8 previous_game_mode,
// varint-prefixed world names, NBT dimension codec, string dimension type,
// string world name, i64 hashed seed, varint max players, view distance,
// simulation distance, four booleans, optional death location, and portal
// cooldown. The optional death location is not used by the lobby, but must be
// consumed when present so the fixture parser cannot silently shift fields.
func decodeJoinGameV1_20_1(data []byte) (*JoinGameData, error) {
	r := &joinGameReader{data: data}
	if len(r.data)-r.pos < 4 {
		return nil, errMalformed("join game")
	}
	r.pos += 4 // entity_id

	d := &JoinGameData{}
	if v, ok := r.bool(); ok {
		d.Hardcore = &v
	} else {
		return nil, errMalformed("join game")
	}
	if len(r.data)-r.pos >= 2 {
		gameMode := r.data[r.pos]
		previousGameMode := r.data[r.pos+1]
		r.pos += 2
		d.GameMode = &gameMode
		d.PreviousGameMode = &previousGameMode
	} else {
		return nil, errMalformed("join game")
	}

	worldNames := []string{}
	count, ok := r.varInt()
	if !ok || count < 0 {
		return nil, errMalformed("join game")
	}
	for i := int32(0); i < count; i++ {
		name, ok := r.str()
		if !ok {
			return nil, errMalformed("join game")
		}
		worldNames = append(worldNames, name)
	}
	d.WorldNames = &worldNames

	codecReader := nbt.NewReader(r.data[r.pos:])
	codec, ok := nbt.ReadCompoundTag(codecReader)
	if !ok {
		return nil, errMalformed("join game codec")
	}
	r.pos = len(r.data) - codecReader.Remaining()
	d.DimensionCodec = codec

	dimensionType, ok := r.str()
	if !ok {
		return nil, errMalformed("join game dimension type")
	}
	d.DimensionType = &dimensionType
	worldName, ok := r.str()
	if !ok {
		return nil, errMalformed("join game world name")
	}
	d.WorldName = &worldName

	if len(r.data)-r.pos < 8 {
		return nil, errMalformed("join game")
	}
	seed := int64(0)
	for i := 0; i < 8; i++ {
		seed = seed<<8 | int64(r.data[r.pos+i])
	}
	r.pos += 8
	d.HashedSeed = &seed

	if value, ok := r.varInt(); ok {
		d.MaxPlayers = &value
	} else {
		return nil, errMalformed("join game")
	}
	if value, ok := r.varInt(); ok {
		d.ViewDistance = &value
	} else {
		return nil, errMalformed("join game")
	}
	if value, ok := r.varInt(); ok {
		d.SimulationDistance = &value
	} else {
		return nil, errMalformed("join game")
	}
	for _, target := range []**bool{&d.ReducedDebugInfo, &d.EnableRespawnScreen, &d.IsDebug, &d.IsFlat} {
		value, ok := r.bool()
		if !ok {
			return nil, errMalformed("join game")
		}
		*target = &value
	}
	death, ok := r.bool()
	if !ok {
		return nil, errMalformed("join game death location")
	}
	if death {
		if _, ok := r.str(); !ok || !r.skip(8) {
			return nil, errMalformed("join game death location")
		}
	}
	if _, ok := r.varInt(); !ok {
		return nil, errMalformed("join game portal cooldown")
	}
	return d, nil
}

type malformedError string

func (e malformedError) Error() string { return string(e) }

func errMalformed(what string) error { return malformedError("malformed " + what) }

// JoinGameParams carries the values needed to build a lobby join game packet.
type JoinGameParams struct {
	// Dimension codec, from probed join game data or default.
	DimensionCodec *nbt.Compound
	// Dimension, computed via mc/dimension LobbyDimension.
	Dimension *nbt.Compound
	// Max players from status or probed data.
	MaxPlayers          int32
	ViewDistance        int32
	SimulationDistance  int32
	Hardcore            bool
	ReducedDebugInfo    bool
	EnableRespawnScreen bool
	IsDebug             bool
	IsFlat              bool
}

// DefaultWorldNames is the fallback world list.
var DefaultWorldNames = []string{"minecraft:overworld", "minecraft:the_nether", "minecraft:the_end"}

// LobbyJoinGameData builds the data bytes for a lobby join game packet for
// the client's protocol version.
func LobbyJoinGameData(clientInfo *proto.ClientInfo, params JoinGameParams) []byte {
	if isV1_20_1(clientInfo.GetProtocol()) {
		return lobbyJoinGameDataV1_20_1(params)
	}
	w := proto.NewPacketWriter()
	w.WriteUint32(0) // entity_id, must be unique
	w.WriteBool(params.Hardcore)
	w.WriteU8(3)    // game_mode spectator
	w.WriteU8(0xFF) // previous_game_mode -1
	worldNames := DefaultWorldNames
	w.WriteVarInt(int32(len(worldNames)))
	for _, n := range worldNames {
		w.WriteString(n)
	}
	nbt.WriteCompoundTag(w, params.DimensionCodec)
	nbt.WriteCompoundTag(w, params.Dimension)
	w.WriteString("lazymc:lobby")
	w.WriteInt64(0) // hashed_seed
	w.WriteVarInt(params.MaxPlayers)
	w.WriteVarInt(params.ViewDistance)
	w.WriteBool(params.ReducedDebugInfo)
	w.WriteBool(params.EnableRespawnScreen)
	w.WriteBool(params.IsDebug)
	w.WriteBool(params.IsFlat)
	return w.Bytes()
}

func lobbyJoinGameDataV1_20_1(params JoinGameParams) []byte {
	w := proto.NewPacketWriter()
	w.WriteInt32(0) // entity_id
	w.WriteBool(params.Hardcore)
	w.WriteU8(3)    // game_mode spectator
	w.WriteU8(0xFF) // previous_game_mode -1
	w.WriteVarInt(int32(len(DefaultWorldNames)))
	for _, name := range DefaultWorldNames {
		w.WriteString(name)
	}
	nbt.WriteCompoundTag(w, params.DimensionCodec)
	w.WriteString("minecraft:overworld") // dimension type
	w.WriteString("lazymc:lobby")        // world name
	w.WriteInt64(0)                      // hashed_seed
	w.WriteVarInt(params.MaxPlayers)
	w.WriteVarInt(params.ViewDistance)
	simulationDistance := params.SimulationDistance
	if simulationDistance <= 0 {
		simulationDistance = params.ViewDistance
	}
	w.WriteVarInt(simulationDistance)
	w.WriteBool(params.ReducedDebugInfo)
	w.WriteBool(params.EnableRespawnScreen)
	w.WriteBool(params.IsDebug)
	w.WriteBool(params.IsFlat)
	w.WriteBool(false) // death location absent
	w.WriteVarInt(0)   // portal cooldown
	return w.Bytes()
}

// JoinGamePacketID returns the join game packet id for the protocol version.
func JoinGamePacketID(protocol *uint32) byte {
	if isV1_20_1(protocol) {
		return V1_20_1JoinGame
	}
	if protocol != nil && *protocol < ProtocolV1_17 {
		return V1_16_3JoinGame
	}
	return V1_17JoinGame
}

// SendJoinGame writes a lobby join game packet to the client.
func SendJoinGame(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn, params JoinGameParams) error {
	data := LobbyJoinGameData(clientInfo, params)
	packet := proto.NewRawPacket(JoinGamePacketID(clientInfo.GetProtocol()), data)
	return writePacket(client, conn, packet)
}

// writePacket encodes and writes a packet to the connection.
func writePacket(client *proto.Client, conn net.Conn, packet proto.RawPacket) error {
	encoded, ok := packet.EncodeWithLen(client)
	if !ok {
		return errMalformed("encode")
	}
	_, err := conn.Write(encoded)
	return err
}
