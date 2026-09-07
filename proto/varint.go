package proto

// VarInt encode/decode helpers, mirroring lazymc's types.rs.

// ReadVarInt tries to read a var-int from the buffer, returning the number of
// bytes consumed and the value.
func ReadVarInt(buf []byte) (int, int32, bool) {
	max := 5
	if len(buf) < max {
		max = len(buf)
	}

	for length := 1; length <= max; length++ {
		// Find var-int byte size
		extraByte := (buf[length-1] & (1 << 7)) > 0
		if extraByte {
			continue
		}

		// Parse var-int from bytes
		val, ok := decodeVarInt(buf[:length])
		if !ok {
			return 0, 0, false
		}
		return length, val, true
	}

	// The buffer wasn't complete or the var-int is invalid
	return 0, 0, false
}

// decodeVarInt decodes a var-int from a byte slice.
func decodeVarInt(buf []byte) (int32, bool) {
	var result int32
	for i, b := range buf {
		result |= int32(b&0x7F) << (7 * i)
	}
	return result, true
}

// EncodeVarInt encodes an integer into a var-int.
func EncodeVarInt(i int32) []byte {
	u := uint32(i)
	out := make([]byte, 0, 5)
	for {
		b := byte(u & 0x7F)
		u >>= 7
		if u != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if u == 0 {
			break
		}
	}
	return out
}
