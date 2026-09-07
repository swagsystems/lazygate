package nbt

// Binary NBT reading, used to parse join game packets from the server.

import (
	"encoding/binary"
	"math"
)

// Reader reads NBT from a byte slice.
type Reader struct {
	data []byte
	pos  int
}

// NewReader creates a reader over the given bytes.
func NewReader(data []byte) *Reader { return &Reader{data: data} }

// Remaining returns the number of unread bytes.
func (r *Reader) Remaining() int { return len(r.data) - r.pos }

func (r *Reader) u8() (byte, bool) {
	if r.pos >= len(r.data) {
		return 0, false
	}
	b := r.data[r.pos]
	r.pos++
	return b, true
}

func (r *Reader) i16() (int16, bool) {
	if r.Remaining() < 2 {
		return 0, false
	}
	v := int16(binary.BigEndian.Uint16(r.data[r.pos:]))
	r.pos += 2
	return v, true
}

func (r *Reader) i32() (int32, bool) {
	if r.Remaining() < 4 {
		return 0, false
	}
	v := int32(binary.BigEndian.Uint32(r.data[r.pos:]))
	r.pos += 4
	return v, true
}

func (r *Reader) i64() (int64, bool) {
	if r.Remaining() < 8 {
		return 0, false
	}
	v := int64(binary.BigEndian.Uint64(r.data[r.pos:]))
	r.pos += 8
	return v, true
}

func (r *Reader) f32() (float32, bool) {
	if r.Remaining() < 4 {
		return 0, false
	}
	v := binary.BigEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return float32FromBits(v), true
}

func (r *Reader) f64() (float64, bool) {
	if r.Remaining() < 8 {
		return 0, false
	}
	v := binary.BigEndian.Uint64(r.data[r.pos:])
	r.pos += 8
	return float64FromBits(v), true
}

// ReadString reads a length-prefixed string.
func (r *Reader) ReadString() (string, bool) {
	n, ok := r.i16()
	if !ok || int(n) > r.Remaining() {
		return "", false
	}
	s := string(r.data[r.pos : r.pos+int(n)])
	r.pos += int(n)
	return s, true
}

// ReadCompoundTag reads a full compound tag including type byte and root
// name, matching named-binary-tag's read_compound_tag.
func ReadCompoundTag(r *Reader) (*Compound, bool) {
	tagID, ok := r.u8()
	if !ok {
		return nil, false
	}
	if _, ok := r.ReadString(); !ok {
		return nil, false
	}
	tag, ok := readTag(r, tagID)
	if !ok {
		return nil, false
	}
	if tag.Type != TagCompound || tag.Compound == nil {
		return nil, false
	}
	return tag.Compound, true
}

func readTag(r *Reader, tagID byte) (*Tag, bool) {
	switch tagID {
	case TagEnd:
		return &Tag{Type: TagEnd}, true
	case TagByte:
		v, ok := r.u8()
		if !ok {
			return nil, false
		}
		return &Tag{Type: TagByte, Num: int64(int8(v))}, true
	case TagShort:
		v, ok := r.i16()
		if !ok {
			return nil, false
		}
		return &Tag{Type: TagShort, Num: int64(v)}, true
	case TagInt:
		v, ok := r.i32()
		if !ok {
			return nil, false
		}
		return &Tag{Type: TagInt, Num: int64(v)}, true
	case TagLong:
		v, ok := r.i64()
		if !ok {
			return nil, false
		}
		return &Tag{Type: TagLong, Num: v}, true
	case TagFloat:
		v, ok := r.f32()
		if !ok {
			return nil, false
		}
		return &Tag{Type: TagFloat, F64: float64(v)}, true
	case TagDouble:
		v, ok := r.f64()
		if !ok {
			return nil, false
		}
		return &Tag{Type: TagDouble, F64: v}, true
	case TagByteArray:
		n, ok := r.i32()
		if !ok || int(n) > r.Remaining() {
			return nil, false
		}
		out := append([]byte(nil), r.data[r.pos:r.pos+int(n)]...)
		r.pos += int(n)
		return &Tag{Type: TagByteArray, Bytes: out}, true
	case TagString:
		s, ok := r.ReadString()
		if !ok {
			return nil, false
		}
		return &Tag{Type: TagString, Str: s}, true
	case TagList:
		elemType, ok := r.u8()
		if !ok {
			return nil, false
		}
		n, ok := r.i32()
		if !ok {
			return nil, false
		}
		t := &Tag{Type: TagList, ListType: elemType}
		for i := int32(0); i < n; i++ {
			elem, ok := readTag(r, elemType)
			if !ok {
				return nil, false
			}
			t.List = append(t.List, *elem)
		}
		return t, true
	case TagCompound:
		c := NewCompound()
		for {
			childType, ok := r.u8()
			if !ok {
				return nil, false
			}
			if childType == TagEnd {
				return &Tag{Type: TagCompound, Compound: c}, true
			}
			name, ok := r.ReadString()
			if !ok {
				return nil, false
			}
			child, ok := readTag(r, childType)
			if !ok {
				return nil, false
			}
			c.Insert(name, child)
		}
	case TagIntArray:
		n, ok := r.i32()
		if !ok || int(n)*4 > r.Remaining() {
			return nil, false
		}
		t := &Tag{Type: TagIntArray}
		for i := int32(0); i < n; i++ {
			v, ok := r.i32()
			if !ok {
				return nil, false
			}
			t.Ints = append(t.Ints, v)
		}
		return t, true
	case TagLongArray:
		n, ok := r.i32()
		if !ok || int(n)*8 > r.Remaining() {
			return nil, false
		}
		t := &Tag{Type: TagLongArray}
		for i := int32(0); i < n; i++ {
			v, ok := r.i64()
			if !ok {
				return nil, false
			}
			t.Longs = append(t.Longs, v)
		}
		return t, true
	default:
		return nil, false
	}
}

func float32FromBits(bits uint32) float32 {
	return math.Float32frombits(bits)
}

func float64FromBits(bits uint64) float64 {
	return math.Float64frombits(bits)
}
