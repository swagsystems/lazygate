package main

// NBT encode/decode roundtrip tests.

import (
	"testing"

	"lazymc/mc"

	"lazymc/nbt"
	"lazymc/proto"
)

func TestNbtRoundtrip(t *testing.T) {
	codec := nbt.NewCompound()
	dimTypes := nbt.NewCompound()
	dimTypes.InsertString("type", "minecraft:dimension_type")
	dim := nbt.NewCompound()
	dim.InsertString("name", "minecraft:overworld")
	dim.InsertInt("id", 0)
	element := nbt.NewCompound()
	element.InsertByte("piglin_safe", 0)
	element.InsertByte("natural", 1)
	element.InsertFloat("ambient_light", 0.0)
	element.InsertString("infiniburn", "minecraft:infiniburn_overworld")
	element.InsertDouble("coordinate_scale", 1.0)
	element.InsertLong("fixed_time", 0)
	element.InsertShort("short_val", 42)
	element.Insert("int_array", &nbt.Tag{Type: nbt.TagIntArray, Ints: []int32{1, 2, 3}})
	element.Insert("long_array", &nbt.Tag{Type: nbt.TagLongArray, Longs: []int64{4, 5}})
	element.Insert("byte_array", &nbt.Tag{Type: nbt.TagByteArray, Bytes: []byte{1, 2}})
	dim.Insert("element", &nbt.Tag{Type: nbt.TagCompound, Compound: element})
	dimTypes.Insert("value", &nbt.Tag{Type: nbt.TagList, ListType: nbt.TagCompound, List: []nbt.Tag{{Type: nbt.TagCompound, Compound: dim}}})
	codec.Insert("minecraft:dimension_type", &nbt.Tag{Type: nbt.TagCompound, Compound: dimTypes})

	// Encode via the protocol writer (as used in join game packets)
	w := proto.NewPacketWriter()
	nbt.WriteCompoundTag(w, codec)

	// Decode back
	r := nbt.NewReader(w.Bytes())
	back, ok := nbt.ReadCompoundTag(r)
	if !ok {
		t.Fatalf("roundtrip failed")
	}
	dt, err := back.GetCompound("minecraft:dimension_type")
	if err != nil {
		t.Fatalf("missing dimension_type: %v", err)
	}
	values, err := dt.GetCompoundVec("value")
	if err != nil || len(values) != 1 {
		t.Fatalf("bad value list: %v %d", err, len(values))
	}
	el, err := values[0].GetCompound("element")
	if err != nil {
		t.Fatalf("missing element: %v", err)
	}
	if s, _ := el.GetStr("infiniburn"); s != "minecraft:infiniburn_overworld" {
		t.Errorf("infiniburn wrong: %q", s)
	}
	if r.Remaining() != 0 {
		t.Errorf("trailing bytes after compound: %d", r.Remaining())
	}
}

func TestDimensionCodecSNBT(t *testing.T) {
	codec := mc.DefaultDimensionCodec()
	dt, err := codec.GetCompound("minecraft:dimension_type")
	if err != nil {
		t.Fatalf("codec missing dimension_type: %v", err)
	}
	values, err := dt.GetCompoundVec("value")
	if err != nil {
		t.Fatalf("bad value list: %v", err)
	}
	var names []string
	for _, v := range values {
		n, _ := v.GetStr("name")
		names = append(names, n)
	}
	// The codec must contain the three standard dimensions
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	for _, want := range []string{"minecraft:overworld", "minecraft:the_nether", "minecraft:the_end"} {
		if !found[want] {
			t.Errorf("codec missing %s (have %v)", want, names)
		}
	}
}

func TestLobbyDimension(t *testing.T) {
	codec := mc.DefaultDimensionCodec()
	dim := mc.LobbyDimension(codec)

	// The lobby dimension must have the modified properties
	if h, err := dim.GetStr("effects"); err != nil || h != "minecraft:the_end" {
		t.Errorf("effects wrong: %q %v", h, err)
	}
	// fixed_time is a long
	ft := dim.Get("fixed_time")
	if ft == nil || ft.Type != nbt.TagLong || ft.Num != 0 {
		t.Errorf("fixed_time wrong: %+v", ft)
	}
	// height is 16
	h := dim.Get("height")
	if h == nil || h.Type != nbt.TagInt || h.Num != 16 {
		t.Errorf("height wrong: %+v", h)
	}

	// Encode/decode roundtrip to ensure the lobby dimension NBT is valid
	w := proto.NewPacketWriter()
	nbt.WriteCompoundTag(w, dim)
	r := nbt.NewReader(w.Bytes())
	back, ok := nbt.ReadCompoundTag(r)
	if !ok || r.Remaining() != 0 {
		t.Fatalf("lobby dimension roundtrip failed (remaining=%d)", r.Remaining())
	}
	if h, _ := back.GetStr("effects"); h != "minecraft:the_end" {
		t.Errorf("roundtripped effects wrong: %q", h)
	}
}
