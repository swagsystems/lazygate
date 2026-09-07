package proto

// zlib helpers for packet compression.

import (
	"bytes"
	"compress/zlib"
	"io"
)

// zlibCompress compresses data with zlib at default level.
func zlibCompress(data []byte) ([]byte, bool) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, false
	}
	if err := w.Close(); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// zlibDecompress decompresses data with zlib, verifying the output length.
func zlibDecompress(data []byte, expectedLen int) ([]byte, bool) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	defer r.Close()

	decompressed, err := io.ReadAll(io.LimitReader(r, int64(expectedLen)+1))
	if err != nil {
		ErrorLog("lazymc", "Packet decompression error: %v", err)
		return nil, false
	}

	// Decompressed data must match length
	if len(decompressed) != expectedLen {
		ErrorLog("lazymc", "Decompressed packet has different length than expected (%db != %db)", len(decompressed), expectedLen)
		return nil, false
	}

	return decompressed, true
}
