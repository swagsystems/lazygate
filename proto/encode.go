package proto

// Wire field encoders, mirroring the minecraft-protocol crate's
// EncoderWriteExt behavior byte-for-byte.

import (
	"encoding/binary"
	"math"
)

// PacketWriter accumulates protocol bytes.
type PacketWriter struct {
	buf []byte
}

// NewPacketWriter creates an empty writer.
func NewPacketWriter() *PacketWriter { return &PacketWriter{} }

// Bytes returns the accumulated output.
func (w *PacketWriter) Bytes() []byte { return w.buf }

// Write appends raw bytes.
func (w *PacketWriter) Write(b []byte) { w.buf = append(w.buf, b...) }

// WriteU8 appends a single byte.
func (w *PacketWriter) WriteU8(b byte) { w.buf = append(w.buf, b) }

// WriteBool appends a boolean as a byte.
func (w *PacketWriter) WriteBool(b bool) {
	if b {
		w.buf = append(w.buf, 1)
	} else {
		w.buf = append(w.buf, 0)
	}
}

// WriteInt16 appends big-endian i16.
func (w *PacketWriter) WriteInt16(v int16) { w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(v)) }

// WriteUint16 appends big-endian u16.
func (w *PacketWriter) WriteUint16(v uint16) { w.buf = binary.BigEndian.AppendUint16(w.buf, v) }

// WriteInt32 appends big-endian i32.
func (w *PacketWriter) WriteInt32(v int32) { w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(v)) }

// WriteUint32 appends big-endian u32.
func (w *PacketWriter) WriteUint32(v uint32) { w.buf = binary.BigEndian.AppendUint32(w.buf, v) }

// WriteInt64 appends big-endian i64.
func (w *PacketWriter) WriteInt64(v int64) { w.buf = binary.BigEndian.AppendUint64(w.buf, uint64(v)) }

// WriteUint64 appends big-endian u64.
func (w *PacketWriter) WriteUint64(v uint64) { w.buf = binary.BigEndian.AppendUint64(w.buf, v) }

// WriteFloat32 appends big-endian f32.
func (w *PacketWriter) WriteFloat32(v float32) { w.WriteUint32(math.Float32bits(v)) }

// WriteFloat64 appends big-endian f64.
func (w *PacketWriter) WriteFloat64(v float64) { w.WriteUint64(math.Float64bits(v)) }

// WriteVarInt appends a var-int.
func (w *PacketWriter) WriteVarInt(v int32) { w.Write(EncodeVarInt(v)) }

// WriteString appends a length-prefixed UTF-8 string.
func (w *PacketWriter) WriteString(s string) {
	w.WriteVarInt(int32(len(s)))
	w.Write([]byte(s))
}

// WriteByteArray appends a length-prefixed byte array.
func (w *PacketWriter) WriteByteArray(b []byte) {
	w.WriteVarInt(int32(len(b)))
	w.Write(b)
}
