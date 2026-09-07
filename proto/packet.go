package proto

// RawPacket is a raw Minecraft packet: a packet ID and raw data bytes.
type RawPacket struct {
	// Packet ID.
	ID byte

	// Packet data.
	Data []byte
}

// NewRawPacket constructs a new raw packet.
func NewRawPacket(id byte, data []byte) RawPacket {
	return RawPacket{ID: id, Data: data}
}

// readPacketIDData reads a packet ID from the buffer, using the remaining
// buffer as data.
func readPacketIDData(buf []byte) (RawPacket, bool) {
	read, packetID, ok := ReadVarInt(buf)
	if !ok {
		return RawPacket{}, false
	}
	buf = buf[read:]
	return NewRawPacket(byte(packetID), append([]byte(nil), buf...)), true
}

// DecodeWithLen decodes a packet from a raw buffer, including the length
// header. Handles both compressed and uncompressed packets based on the
// client threshold preference.
func DecodeWithLen(client *Client, buf []byte) (RawPacket, bool) {
	read, pktLen, ok := ReadVarInt(buf)
	if !ok {
		return RawPacket{}, false
	}
	buf = buf[read:]
	if pktLen < 0 || int(pktLen) > len(buf) {
		return RawPacket{}, false
	}
	buf = buf[:pktLen]

	return DecodeWithoutLen(client, buf)
}

// DecodeWithoutLen decodes a packet from a raw buffer without the length
// header.
func DecodeWithoutLen(client *Client, buf []byte) (RawPacket, bool) {
	// If no compression is used, read remaining packet ID and data
	if !client.IsCompressed() {
		return readPacketIDData(buf)
	}

	// Read data length
	read, dataLen, ok := ReadVarInt(buf)
	if !ok {
		return RawPacket{}, false
	}
	buf = buf[read:]

	// If data length is zero, the rest is not compressed
	if dataLen == 0 {
		return readPacketIDData(buf)
	}

	// Decompress packet ID and data section
	decompressed, ok := zlibDecompress(buf, int(dataLen))
	if !ok {
		return RawPacket{}, false
	}

	return readPacketIDData(decompressed)
}

// EncodeWithLen encodes the packet to a raw buffer, including the length
// header and compression based on the client threshold preference.
func (p RawPacket) EncodeWithLen(client *Client) ([]byte, bool) {
	payload, ok := p.EncodeWithoutLen(client)
	if !ok {
		return nil, false
	}

	packet := EncodeVarInt(int32(len(payload)))
	packet = append(packet, payload...)
	return packet, true
}

// EncodeWithoutLen encodes the packet without the length header.
func (p RawPacket) EncodeWithoutLen(client *Client) ([]byte, bool) {
	threshold := client.Compressed()
	if threshold >= 0 {
		return p.encodeCompressed(threshold)
	}
	return p.encodeUncompressed()
}

// encodeCompressed encodes a compressed packet.
func (p RawPacket) encodeCompressed(threshold int32) ([]byte, bool) {
	// Packet payload: packet ID and data buffer
	payload := EncodeVarInt(int32(p.ID))
	payload = append(payload, p.Data...)

	// Determine whether to compress, encode data length bytes
	dataLen := int32(len(payload))
	compress := dataLen > threshold
	dataLenHeader := int32(0)
	if compress {
		dataLenHeader = dataLen
	}

	// Compress payload
	if compress {
		compressed, ok := zlibCompress(payload)
		if !ok {
			return nil, false
		}
		payload = compressed
	}

	// Add data length header
	packet := EncodeVarInt(dataLenHeader)
	packet = append(packet, payload...)

	return packet, true
}

// encodeUncompressed encodes an uncompressed packet.
func (p RawPacket) encodeUncompressed() ([]byte, bool) {
	packet := EncodeVarInt(int32(p.ID))
	packet = append(packet, p.Data...)
	return packet, true
}
