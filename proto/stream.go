package proto

// Stream packet reading, mirroring lazymc's proto/packet.rs read_packet.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrMalformedPacket indicates a malformed packet on the stream.
var ErrMalformedPacket = errors.New("malformed packet")

// ErrPacketTooLarge indicates that an untrusted packet length exceeded the
// bounded public edge budget.
var ErrPacketTooLarge = errors.New("packet too large")

// ErrBufferLimit indicates that the stream's queued bytes exceeded the
// bounded public edge budget.
var ErrBufferLimit = errors.New("packet buffer limit exceeded")

// Public edge framing limits. The outer length and, when compression is
// enabled, the advertised decompressed length are both checked before any
// decoder or decompressor is allowed to consume them.
const (
	MaxPacketLength  = 1 << 20
	MaxBufferedBytes = MaxPacketLength + 5

	defaultHandshakeReadTimeout = 10 * time.Second
	defaultStatusReadTimeout    = 5 * time.Second
	defaultLoginReadTimeout     = 15 * time.Second
)

// These variables are overridable by focused tests while retaining strict
// production defaults.
var (
	HandshakeReadTimeout = defaultHandshakeReadTimeout
	StatusReadTimeout    = defaultStatusReadTimeout
	LoginReadTimeout     = defaultLoginReadTimeout
)

// ReadPacket reads a raw packet from the stream, buffering partial reads.
//
// Returns the decoded packet plus the raw consumed bytes. more=false with
// nil error indicates a clean EOF, and ErrMalformedPacket indicates a
// malformed packet.
func ReadPacket(client *Client, buf *[]byte, conn net.Conn) (RawPacket, []byte, bool, error) {
	return ReadPacketWithTimeout(client, buf, conn, readTimeout(client.State()))
}

// ReadPacketWithTimeout reads one packet using a caller-selected bounded
// timeout. Protocol handshakes use the strict state defaults above, while a
// known authenticated Forge login exchange may need longer to build its
// mod-list response.
func ReadPacketWithTimeout(client *Client, buf *[]byte, conn net.Conn, timeout time.Duration) (RawPacket, []byte, bool, error) {
	if len(*buf) > MaxBufferedBytes {
		return RawPacket{}, nil, false, ErrBufferLimit
	}

	// A packet read may span multiple socket reads. Enforce the protocol-state
	// timeout without overwriting a caller-owned shorter deadline. The timer
	// only installs and later clears a deadline when it actually fires, so the
	// long-lived proxy path and existing deadline-aware callers are preserved.
	stopDeadline := armReadTimeout(conn, timeout)
	defer stopDeadline()

	// Keep reading until we have at least 2 bytes
	for len(*buf) < 2 {
		if err := readMore(buf, conn); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
				return RawPacket{}, nil, false, nil
			}
			return RawPacket{}, nil, false, err
		}
	}

	// Attempt to read packet length
	consumed, pktLen, ok := ReadVarInt(*buf)
	if !ok {
		ErrorLog("lazymc", "Malformed packet, could not read packet length")
		return RawPacket{}, nil, false, ErrMalformedPacket
	}
	if pktLen < 0 {
		return RawPacket{}, nil, false, ErrMalformedPacket
	}
	if pktLen > MaxPacketLength {
		return RawPacket{}, nil, false, ErrPacketTooLarge
	}
	total := consumed + int(pktLen)
	if total > MaxBufferedBytes {
		return RawPacket{}, nil, false, ErrPacketTooLarge
	}

	// Keep reading until we have all packet bytes
	for len(*buf) < total {
		if err := readMore(buf, conn); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
				return RawPacket{}, nil, false, nil
			}
			return RawPacket{}, nil, false, err
		}
	}

	if client.IsCompressed() {
		_, dataLen, ok := ReadVarInt((*buf)[consumed:total])
		if !ok || dataLen < 0 || dataLen > MaxPacketLength {
			return RawPacket{}, nil, false, ErrPacketTooLarge
		}
	}

	// Parse packet from full buffer
	raw := append([]byte(nil), (*buf)[:consumed+int(pktLen)]...)
	packet, ok := DecodeWithLen(client, raw)
	if !ok {
		return RawPacket{}, nil, false, fmt.Errorf("%w: could not decode", ErrMalformedPacket)
	}

	// Remove consumed bytes from buffer
	*buf = (*buf)[consumed+int(pktLen):]

	return packet, raw, true, nil
}

func readTimeout(state ClientState) time.Duration {
	switch state {
	case ClientStateHandshake:
		return HandshakeReadTimeout
	case ClientStateStatus:
		return StatusReadTimeout
	case ClientStateLogin:
		return LoginReadTimeout
	default:
		return LoginReadTimeout
	}
}

func armReadTimeout(conn net.Conn, timeout time.Duration) func() {
	expired := atomic.Bool{}
	done := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		_ = conn.SetReadDeadline(time.Now())
		expired.Store(true)
		close(done)
	})
	return func() {
		if timer.Stop() {
			return
		}
		<-done
		if expired.Load() {
			_ = conn.SetReadDeadline(time.Time{})
		}
	}
}

func readMore(buf *[]byte, conn net.Conn) error {
	remaining := MaxBufferedBytes - len(*buf)
	if remaining <= 0 {
		return ErrBufferLimit
	}
	readSize := BufSize
	if remaining < readSize {
		readSize = remaining
	}
	tmp := make([]byte, readSize)
	n, err := conn.Read(tmp)
	if n > 0 {
		*buf = append(*buf, tmp[:n]...)
	}
	if n == 0 && err == nil {
		return io.ErrNoProgress
	}
	return err
}
