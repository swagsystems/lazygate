// Package nbt implements a minimal Named Binary Tag (NBT) encoder and an
// SNBT parser, matching the wire behavior of the `named-binary-tag` (nbt)
// crate that lazymc uses for the lobby join game packets.
//
// Compounds preserve insertion order (like the crate's linked-hash-map
// backing), and re-inserting an existing key updates its value in place
// without reordering.
package nbt

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Tag type IDs.
const (
	TagEnd       = 0
	TagByte      = 1
	TagShort     = 2
	TagInt       = 3
	TagLong      = 4
	TagFloat     = 5
	TagDouble    = 6
	TagByteArray = 7
	TagString    = 8
	TagList      = 9
	TagCompound  = 10
	TagIntArray  = 11
	TagLongArray = 12
)

// Tag is a single NBT value.
type Tag struct {
	// Type is the tag type ID.
	Type byte
	// Byte / Short / Int / Long / Float / Double values.
	Num int64
	// Float/Double fractional part when Type is Float or Double.
	F64 float64
	// String value when Type is TagString.
	Str string
	// ByteArray / IntArray / LongArray contents.
	Bytes []byte
	Ints  []int32
	Longs []int64
	// List element type and items (homogeneous).
	ListType byte
	List     []Tag
	// Compound contents.
	Compound *Compound
}

// Compound is an insertion-ordered map of named tags.
type Compound struct {
	names []string
	tags  map[string]*Tag
}

// NewCompound creates an empty compound.
func NewCompound() *Compound {
	return &Compound{tags: map[string]*Tag{}}
}

// Len returns the number of entries.
func (c *Compound) Len() int { return len(c.names) }

// Names returns entry names in insertion order.
func (c *Compound) Names() []string { return c.names }

// Get returns a tag by name.
func (c *Compound) Get(name string) *Tag {
	if c == nil {
		return nil
	}
	return c.tags[name]
}

// GetCompound returns a compound-valued child.
func (c *Compound) GetCompound(name string) (*Compound, error) {
	t := c.Get(name)
	if t == nil {
		return nil, fmt.Errorf("no such key: %s", name)
	}
	if t.Type != TagCompound {
		return nil, fmt.Errorf("key %s is not a compound", name)
	}
	return t.Compound, nil
}

// GetCompoundVec returns a list-of-compounds child.
func (c *Compound) GetCompoundVec(name string) ([]*Compound, error) {
	t := c.Get(name)
	if t == nil {
		return nil, fmt.Errorf("no such key: %s", name)
	}
	if t.Type != TagList {
		return nil, fmt.Errorf("key %s is not a list", name)
	}
	var out []*Compound
	for i := range t.List {
		out = append(out, t.List[i].Compound)
	}
	return out, nil
}

// GetStr returns a string child.
func (c *Compound) GetStr(name string) (string, error) {
	t := c.Get(name)
	if t == nil {
		return "", fmt.Errorf("no such key: %s", name)
	}
	if t.Type != TagString {
		return "", fmt.Errorf("key %s is not a string", name)
	}
	return t.Str, nil
}

// Insert inserts or updates a tag. Existing keys keep their position.
func (c *Compound) Insert(name string, tag *Tag) {
	if _, ok := c.tags[name]; !ok {
		c.names = append(c.names, name)
	}
	c.tags[name] = tag
}

// InsertByte inserts an i8 tag.
func (c *Compound) InsertByte(name string, v int8) {
	c.Insert(name, &Tag{Type: TagByte, Num: int64(v)})
}

// InsertShort inserts an i16 tag.
func (c *Compound) InsertShort(name string, v int16) {
	c.Insert(name, &Tag{Type: TagShort, Num: int64(v)})
}

// InsertInt inserts an i32 tag.
func (c *Compound) InsertInt(name string, v int32) {
	c.Insert(name, &Tag{Type: TagInt, Num: int64(v)})
}

// InsertLong inserts an i64 tag.
func (c *Compound) InsertLong(name string, v int64) {
	c.Insert(name, &Tag{Type: TagLong, Num: v})
}

// InsertFloat inserts an f32 tag.
func (c *Compound) InsertFloat(name string, v float32) {
	c.Insert(name, &Tag{Type: TagFloat, F64: float64(v)})
}

// InsertDouble inserts an f64 tag.
func (c *Compound) InsertDouble(name string, v float64) {
	c.Insert(name, &Tag{Type: TagDouble, F64: v})
}

// InsertString inserts a string tag.
func (c *Compound) InsertString(name, v string) {
	c.Insert(name, &Tag{Type: TagString, Str: v})
}

// Clone deep-copies the compound.
func (c *Compound) Clone() *Compound {
	if c == nil {
		return nil
	}
	out := NewCompound()
	for _, name := range c.names {
		out.Insert(name, cloneTag(c.tags[name]))
	}
	return out
}

func cloneTag(t *Tag) *Tag {
	if t == nil {
		return nil
	}
	cp := *t
	if t.Compound != nil {
		cp.Compound = t.Compound.Clone()
	}
	if t.List != nil {
		cp.List = make([]Tag, len(t.List))
		for i := range t.List {
			cp.List[i] = *cloneTag(&t.List[i])
		}
	}
	if t.Bytes != nil {
		cp.Bytes = append([]byte(nil), t.Bytes...)
	}
	if t.Ints != nil {
		cp.Ints = append([]int32(nil), t.Ints...)
	}
	if t.Longs != nil {
		cp.Longs = append([]int64(nil), t.Longs...)
	}
	return &cp
}

// ByteWriter is the minimal interface needed for NBT encoding. Both the
// nbt.Writer and proto.PacketWriter implement it.
type ByteWriter interface {
	Write([]byte)
	WriteU8(byte)
	WriteInt16(int16)
	WriteInt32(int32)
	WriteInt64(int64)
	WriteFloat32(float32)
	WriteFloat64(float64)
	WriteVarInt(int32)
}

// WriteCompoundTag writes a compound tag to the writer, including the root
// type byte and (empty) root name, matching named-binary-tag's
// `write_compound_tag`. This is the format used inside protocol packets.
func WriteCompoundTag(w ByteWriter, c *Compound) {
	w.WriteU8(TagCompound)
	writeString(w, "")
	writeInnerCompound(w, c)
}

// writeInnerCompound writes the payload of a compound (no type/name header).
func writeInnerCompound(w ByteWriter, c *Compound) {
	for _, name := range c.names {
		w.WriteU8(c.tags[name].Type)
		writeString(w, name)
		writeTag(w, c.tags[name])
	}
	w.WriteU8(TagEnd)
}

func writeTag(w ByteWriter, t *Tag) {
	switch t.Type {
	case TagByte:
		w.WriteU8(byte(t.Num))
	case TagShort:
		w.WriteInt16(int16(t.Num))
	case TagInt:
		w.WriteInt32(int32(t.Num))
	case TagLong:
		w.WriteInt64(t.Num)
	case TagFloat:
		w.WriteFloat32(float32(t.F64))
	case TagDouble:
		w.WriteFloat64(t.F64)
	case TagByteArray:
		w.WriteInt32(int32(len(t.Bytes)))
		w.Write(t.Bytes)
	case TagString:
		writeString(w, t.Str)
	case TagList:
		w.WriteU8(t.ListType)
		w.WriteInt32(int32(len(t.List)))
		for i := range t.List {
			writeTag(w, &t.List[i])
		}
	case TagCompound:
		writeInnerCompound(w, t.Compound)
	case TagIntArray:
		w.WriteInt32(int32(len(t.Ints)))
		for _, v := range t.Ints {
			w.WriteInt32(v)
		}
	case TagLongArray:
		w.WriteInt32(int32(len(t.Longs)))
		for _, v := range t.Longs {
			w.WriteInt64(v)
		}
	}
}

func writeString(w ByteWriter, s string) {
	// NBT strings are u16 big-endian length-prefixed (per the NBT spec),
	// unlike Minecraft protocol strings which use varints.
	w.WriteInt16(int16(len(s)))
	w.Write([]byte(s))
}

// Writer accumulates NBT bytes.
type Writer struct {
	buf []byte
}

// NewWriter creates an empty writer.
func NewWriter() *Writer { return &Writer{} }

// Bytes returns the accumulated output.
func (w *Writer) Bytes() []byte { return w.buf }

// Write appends raw bytes.
func (w *Writer) Write(b []byte) { w.buf = append(w.buf, b...) }

// WriteU8 appends a single byte.
func (w *Writer) WriteU8(b byte) { w.buf = append(w.buf, b) }

// WriteInt16 appends big-endian i16.
func (w *Writer) WriteInt16(v int16) {
	w.buf = append(w.buf, byte(v>>8), byte(v))
}

// WriteInt32 appends big-endian i32.
func (w *Writer) WriteInt32(v int32) {
	w.buf = append(w.buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// WriteInt64 appends big-endian i64.
func (w *Writer) WriteInt64(v int64) {
	w.buf = append(w.buf, byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32), byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// WriteFloat32 appends big-endian f32.
func (w *Writer) WriteFloat32(v float32) {
	w.WriteInt32(int32Bits(v))
}

// WriteFloat64 appends big-endian f64.
func (w *Writer) WriteFloat64(v float64) {
	w.WriteInt64(int64Bits(v))
}

// WriteVarInt appends a Minecraft varint.
func (w *Writer) WriteVarInt(v int32) {
	u := uint32(v)
	for {
		b := byte(u & 0x7F)
		u >>= 7
		if u != 0 {
			b |= 0x80
		}
		w.buf = append(w.buf, b)
		if u == 0 {
			break
		}
	}
}

func int32Bits(f float32) int32 {
	return int32(math.Float32bits(f))
}

func int64Bits(f float64) int64 {
	return int64(math.Float64bits(f))
}

// ---- SNBT parsing ----

type snbtParser struct {
	s   string
	pos int
}

// ParseSNBT parses an SNBT document into a compound tag.
func ParseSNBT(data string) (*Compound, error) {
	p := &snbtParser{s: data}
	p.skipWs()
	tag, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if tag.Type != TagCompound || tag.Compound == nil {
		return nil, errors.New("SNBT root must be a compound")
	}
	p.skipWs()
	if p.pos < len(p.s) {
		return nil, fmt.Errorf("trailing data at offset %d", p.pos)
	}
	return tag.Compound, nil
}

func (p *snbtParser) skipWs() {
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *snbtParser) peek() byte {
	if p.pos < len(p.s) {
		return p.s[p.pos]
	}
	return 0
}

func (p *snbtParser) parseValue() (*Tag, error) {
	p.skipWs()
	if p.pos >= len(p.s) {
		return nil, errors.New("unexpected end of SNBT")
	}
	switch p.peek() {
	case '{':
		return p.parseCompound()
	case '[':
		return p.parseListOrArray()
	case '"', '\'':
		s, err := p.parseQuotedString()
		if err != nil {
			return nil, err
		}
		return &Tag{Type: TagString, Str: s}, nil
	default:
		return p.parseScalar()
	}
}

func (p *snbtParser) parseCompound() (*Tag, error) {
	p.pos++ // consume '{'
	c := NewCompound()
	p.skipWs()
	if p.peek() == '}' {
		p.pos++
		return &Tag{Type: TagCompound, Compound: c}, nil
	}
	for {
		p.skipWs()
		// Parse key
		var key string
		if p.peek() == '"' || p.peek() == '\'' {
			k, err := p.parseQuotedString()
			if err != nil {
				return nil, err
			}
			key = k
		} else {
			start := p.pos
			for p.pos < len(p.s) && p.s[p.pos] != ':' && p.s[p.pos] != ',' && p.s[p.pos] != '}' {
				p.pos++
			}
			if start == p.pos {
				return nil, fmt.Errorf("expected key at offset %d", p.pos)
			}
			key = strings.TrimSpace(p.s[start:p.pos])
		}
		p.skipWs()
		if p.peek() != ':' {
			return nil, fmt.Errorf("expected ':' after key %q at offset %d", key, p.pos)
		}
		p.pos++
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		c.Insert(key, v)
		p.skipWs()
		switch p.peek() {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return &Tag{Type: TagCompound, Compound: c}, nil
		default:
			return nil, fmt.Errorf("expected ',' or '}' at offset %d", p.pos)
		}
	}
}

func (p *snbtParser) parseListOrArray() (*Tag, error) {
	p.pos++ // consume '['
	p.skipWs()

	// Typed array: [B; ...], [I; ...], [L; ...]
	if p.pos+1 < len(p.s) && (p.s[p.pos] == 'B' || p.s[p.pos] == 'I' || p.s[p.pos] == 'L') && p.s[p.pos+1] == ';' {
		arrType := p.s[p.pos]
		p.pos += 2
		p.skipWs()
		t := &Tag{Type: TagIntArray} // placeholder
		switch arrType {
		case 'B':
			t.Type = TagByteArray
		case 'I':
			t.Type = TagIntArray
		case 'L':
			t.Type = TagLongArray
		}
		if p.peek() == ']' {
			p.pos++
			return t, nil
		}
		for {
			p.skipWs()
			start := p.pos
			for p.pos < len(p.s) && p.s[p.pos] != ',' && p.s[p.pos] != ']' {
				p.pos++
			}
			raw := strings.TrimSpace(p.s[start:p.pos])
			switch arrType {
			case 'B':
				v, err := strconv.ParseInt(strings.TrimSuffix(raw, "b"), 10, 8)
				if err != nil {
					return nil, fmt.Errorf("invalid byte array element %q: %v", raw, err)
				}
				t.Bytes = append(t.Bytes, byte(int8(v)))
			case 'I':
				v, err := strconv.ParseInt(raw, 10, 32)
				if err != nil {
					return nil, fmt.Errorf("invalid int array element %q: %v", raw, err)
				}
				t.Ints = append(t.Ints, int32(v))
			case 'L':
				v, err := strconv.ParseInt(strings.TrimSuffix(raw, "L"), 10, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid long array element %q: %v", raw, err)
				}
				t.Longs = append(t.Longs, v)
			}
			p.skipWs()
			switch p.peek() {
			case ',':
				p.pos++
			case ']':
				p.pos++
				return t, nil
			default:
				return nil, fmt.Errorf("expected ',' or ']' in array at offset %d", p.pos)
			}
		}
	}

	// Regular list, homogeneous
	t := &Tag{Type: TagList}
	if p.peek() == ']' {
		p.pos++
		return t, nil
	}
	first := true
	for {
		p.skipWs()
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if first {
			t.ListType = v.Type
			first = false
		} else if v.Type != t.ListType {
			return nil, fmt.Errorf("heterogeneous list at offset %d", p.pos)
		}
		t.List = append(t.List, *v)
		p.skipWs()
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return t, nil
		default:
			return nil, fmt.Errorf("expected ',' or ']' in list at offset %d", p.pos)
		}
	}
}

func (p *snbtParser) parseQuotedString() (string, error) {
	quote := p.peek()
	p.pos++
	var sb strings.Builder
	for p.pos < len(p.s) {
		ch := p.s[p.pos]
		p.pos++
		if ch == '\\' {
			if p.pos >= len(p.s) {
				return "", errors.New("unterminated escape in string")
			}
			esc := p.s[p.pos]
			p.pos++
			switch esc {
			case '\\', '"', '\'':
				sb.WriteByte(esc)
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			default:
				sb.WriteByte(esc)
			}
			continue
		}
		if ch == quote {
			return sb.String(), nil
		}
		sb.WriteByte(ch)
	}
	return "", errors.New("unterminated string in SNBT")
}

func (p *snbtParser) parseScalar() (*Tag, error) {
	start := p.pos
	for p.pos < len(p.s) {
		ch := p.s[p.pos]
		if ch == ',' || ch == '}' || ch == ']' || ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			break
		}
		p.pos++
	}
	if start == p.pos {
		return nil, fmt.Errorf("expected value at offset %d", p.pos)
	}
	raw := p.s[start:p.pos]

	// Booleans are bytes in NBT
	if raw == "true" {
		return &Tag{Type: TagByte, Num: 1}, nil
	}
	if raw == "false" {
		return &Tag{Type: TagByte, Num: 0}, nil
	}

	// Try to parse as number with optional suffix
	suffix := byte(0)
	body := raw
	if len(raw) > 0 {
		last := raw[len(raw)-1]
		switch last {
		case 'b', 'B', 's', 'S', 'l', 'L', 'f', 'F', 'd', 'D':
			suffix = last
			body = raw[:len(raw)-1]
		}
	}

	// Float / double
	if isFloatBody(body) {
		f, err := strconv.ParseFloat(body, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q at offset %d", raw, start)
		}
		switch suffix {
		case 'f', 'F':
			return &Tag{Type: TagFloat, F64: f}, nil
		case 'd', 'D', 0:
			return &Tag{Type: TagDouble, F64: f}, nil
		default:
			return nil, fmt.Errorf("invalid number suffix on %q", raw)
		}
	}

	// Integer
	iv, err := strconv.ParseInt(body, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid number %q at offset %d", raw, start)
	}
	switch suffix {
	case 'b', 'B':
		return &Tag{Type: TagByte, Num: iv}, nil
	case 's', 'S':
		return &Tag{Type: TagShort, Num: iv}, nil
	case 'l', 'L':
		return &Tag{Type: TagLong, Num: iv}, nil
	case 0:
		if iv >= -2147483648 && iv <= 2147483647 {
			return &Tag{Type: TagInt, Num: iv}, nil
		}
		return &Tag{Type: TagLong, Num: iv}, nil
	default:
		return nil, fmt.Errorf("invalid number suffix on %q", raw)
	}
}

func isFloatBody(body string) bool {
	if body == "" {
		return false
	}
	if strings.ContainsAny(body, ".eE") {
		return true
	}
	return false
}
