package mc

import (
	_ "embed"
	"fmt"

	"lazymc/nbt"
)

// Embedded SNBT resources for the lobby world.
//
//go:embed res/dimension_codec.snbt
var dimensionCodecSNBT string

//go:embed res/dimension.snbt
var dimensionSNBT string

// LobbyDimension creates a lobby dimension from the given codec.
//
// This creates a dimension suitable for the lobby that should be suitable for
// the current server version, mirroring lazymc's mc/dimension.rs.
func LobbyDimension(codec *nbt.Compound) *nbt.Compound {
	// Retrieve dimension types from codec
	dimensionTypes, err := codec.GetCompound("minecraft:dimension_type")
	if err != nil {
		return lobbyDefaultDimension()
	}

	// Get base dimension
	base := lobbyBaseDimension(dimensionTypes)

	// Change known properties on base to get a more desirable dimension.
	// Re-inserting existing keys updates values in place without reordering,
	// matching the linked-hash-map behavior.
	base.InsertByte("piglin_safe", 1)
	base.InsertFloat("ambient_light", 0.0)
	base.InsertByte("respawn_anchor_works", 0)
	base.InsertByte("has_skylight", 0)
	base.InsertByte("bed_works", 0)
	base.InsertString("effects", "minecraft:the_end")
	base.InsertLong("fixed_time", 0)
	base.InsertByte("has_raids", 0)
	base.InsertInt("min_y", 0)
	base.InsertInt("height", 16)
	base.InsertInt("logical_height", 16)
	base.InsertDouble("coordinate_scale", 1.0)
	base.InsertByte("ultrawarm", 0)
	base.InsertByte("has_ceiling", 0)

	return base
}

// lobbyBaseDimension retrieves the most desirable dimension to use as base
// from the given list of `dimension_types`.
func lobbyBaseDimension(dimensionTypes *nbt.Compound) *nbt.Compound {
	preferred := []string{
		"minecraft:the_end",
		"minecraft:the_nether",
		"minecraft:the_overworld",
	}

	dimensions, err := dimensionTypes.GetCompoundVec("value")
	if err != nil {
		return lobbyDefaultDimension()
	}

	for _, name := range preferred {
		for _, d := range dimensions {
			if n, err := d.GetStr("name"); err == nil && n == name {
				if element, err := d.GetCompound("element"); err == nil {
					return element.Clone()
				}
			}
		}
	}

	// Return first dimension
	if len(dimensions) > 0 {
		if element, err := dimensions[0].GetCompound("element"); err == nil {
			return element.Clone()
		}
	}

	// Fall back to default dimension
	return lobbyDefaultDimension()
}

// DefaultDimensionCodec returns the default lobby dimension codec from the
// embedded resource file.
func DefaultDimensionCodec() *nbt.Compound {
	return snbtToCompoundTag(dimensionCodecSNBT)
}

// lobbyDefaultDimension returns the default lobby dimension from the embedded
// resource file.
func lobbyDefaultDimension() *nbt.Compound {
	return snbtToCompoundTag(dimensionSNBT)
}

// snbtToCompoundTag parses SNBT into a compound tag.
func snbtToCompoundTag(data string) *nbt.Compound {
	compound, err := nbt.ParseSNBT(data)
	if err != nil {
		panic(fmt.Sprintf("failed to parse embedded SNBT: %v", err))
	}
	return compound
}
