// Package play implements the play-state packets lazymc sends to lobby
// clients, mirroring lazymc's proto/packets/play module.
package play

// Protocol versions (from the minecraft-protocol crate).
const (
	ProtocolV1_16_3 = 753
	ProtocolV1_17   = 755
	// ProtocolV1_20_1 is the Java 1.20.1 wire protocol. It must not be
	// treated as the older 1.17 layout: several play packet IDs and fields
	// changed before this release.
	ProtocolV1_20_1 = 763
)

func isV1_20_1(protocol *uint32) bool {
	return protocol != nil && *protocol == ProtocolV1_20_1
}

// Packet IDs for v1_16_3 play client-bound packets.
const (
	V1_16_3ClientBoundPluginMessage = 0x17
	V1_16_3NamedSoundEffect         = 0x18
	V1_16_3JoinGame                 = 0x24
	V1_16_3PlayerPositionAndLook    = 0x34
	V1_16_3Respawn                  = 0x39
	V1_16_3TimeUpdate               = 0x4E
	V1_16_3Title                    = 0x4F
	V1_16_3ClientBoundKeepAlive     = 0x1F
)

// Packet IDs for v1_17 play client-bound packets.
const (
	V1_17ClientBoundPluginMessage = 0x18
	V1_17NamedSoundEffect         = 0x19
	V1_17JoinGame                 = 0x26
	V1_17PlayerPositionAndLook    = 0x38
	V1_17Respawn                  = 0x3D
	V1_17SetTitleSubtitle         = 0x57
	V1_17TimeUpdate               = 0x58
	V1_17SetTitleText             = 0x59
	V1_17SetTitleTimes            = 0x5A
	V1_17ClientBoundKeepAlive     = 0x21
)

// Packet IDs for Java 1.20.1 (protocol 763) play client-bound packets.
const (
	V1_20_1ClientBoundPluginMessage = 0x17
	V1_20_1NamedSoundEffect         = 0x62 // Clientbound Sound Effect
	V1_20_1JoinGame                 = 0x28 // Clientbound Login
	V1_20_1PlayerPositionAndLook    = 0x3C
	V1_20_1Respawn                  = 0x41
	V1_20_1SetTitleSubtitle         = 0x5D
	V1_20_1TimeUpdate               = 0x5E
	V1_20_1SetTitleText             = 0x5F
	V1_20_1SetTitleTimes            = 0x60
	V1_20_1ClientBoundKeepAlive     = 0x23
)

// Forge packet IDs (v1_13 login wrapper channel).
const (
	ForgeModList         = 1
	ForgeModListReply    = 2
	ForgeAcknowledgement = 99
)
