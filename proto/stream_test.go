package proto

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type deadlineConn struct {
	net.Conn
	deadlines   []time.Time
	readErr     error
	deadlineSet chan struct{}
}

func (c *deadlineConn) Read([]byte) (int, error) {
	if c.deadlineSet != nil {
		<-c.deadlineSet
	}
	return 0, c.readErr
}

func (c *deadlineConn) SetReadDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	if !deadline.IsZero() && c.deadlineSet != nil {
		select {
		case <-c.deadlineSet:
		default:
			close(c.deadlineSet)
		}
	}
	return nil
}

func writePipe(t *testing.T, payload []byte) net.Conn {
	t.Helper()
	reader, writer := net.Pipe()
	go func() {
		_, _ = writer.Write(payload)
		_ = writer.Close()
	}()
	return reader
}

func TestReadPacketAcceptsSmallPacketAndClearsDeadline(t *testing.T) {
	client := DummyClient()
	packet := append(EncodeVarInt(1), byte(0x01))
	conn := writePipe(t, packet)
	defer conn.Close()

	var buf []byte
	got, _, more, err := ReadPacket(client, &buf, conn)
	if err != nil || !more || got.ID != 0x01 {
		t.Fatalf("ReadPacket() = id=%d more=%v err=%v", got.ID, more, err)
	}
	if len(buf) != 0 {
		t.Fatalf("buffer retained %d bytes", len(buf))
	}
}

func TestReadPacketRejectsOversizedOuterLength(t *testing.T) {
	conn := writePipe(t, EncodeVarInt(MaxPacketLength+1))
	defer conn.Close()

	var buf []byte
	_, _, _, err := ReadPacket(DummyClient(), &buf, conn)
	if !errors.Is(err, ErrPacketTooLarge) {
		t.Fatalf("ReadPacket() error = %v, want ErrPacketTooLarge", err)
	}
}

func TestReadPacketRejectsOversizedCompressedLength(t *testing.T) {
	client := DummyClient()
	client.SetCompression(0)
	compressedPayload := append(EncodeVarInt(MaxPacketLength+1), 0)
	packet := append(EncodeVarInt(int32(len(compressedPayload))), compressedPayload...)
	conn := writePipe(t, packet)
	defer conn.Close()

	var buf []byte
	_, _, _, err := ReadPacket(client, &buf, conn)
	if !errors.Is(err, ErrPacketTooLarge) {
		t.Fatalf("ReadPacket() error = %v, want ErrPacketTooLarge", err)
	}
}

func TestReadPacketRejectsQueuedBufferOverflow(t *testing.T) {
	conn := &deadlineConn{readErr: io.ErrUnexpectedEOF}
	buf := make([]byte, MaxBufferedBytes+1)
	_, _, _, err := ReadPacket(DummyClient(), &buf, conn)
	if !errors.Is(err, ErrBufferLimit) {
		t.Fatalf("ReadPacket() error = %v, want ErrBufferLimit", err)
	}
	if len(conn.deadlines) != 0 {
		t.Fatal("read deadline was set before rejecting an oversized queued buffer")
	}
}

func TestReadPacketUsesStateSpecificReadDeadlines(t *testing.T) {
	oldHandshake, oldStatus, oldLogin := HandshakeReadTimeout, StatusReadTimeout, LoginReadTimeout
	HandshakeReadTimeout = 20 * time.Millisecond
	StatusReadTimeout = 30 * time.Millisecond
	LoginReadTimeout = 40 * time.Millisecond
	t.Cleanup(func() {
		HandshakeReadTimeout, StatusReadTimeout, LoginReadTimeout = oldHandshake, oldStatus, oldLogin
	})

	cases := []struct {
		name  string
		state ClientState
		want  time.Duration
	}{
		{"handshake", ClientStateHandshake, HandshakeReadTimeout},
		{"status", ClientStateStatus, StatusReadTimeout},
		{"login", ClientStateLogin, LoginReadTimeout},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := DummyClient()
			client.SetState(tc.state)
			conn := &deadlineConn{
				readErr:     io.ErrUnexpectedEOF,
				deadlineSet: make(chan struct{}),
			}
			var buf []byte
			_, _, _, _ = ReadPacket(client, &buf, conn)
			if len(conn.deadlines) != 2 || conn.deadlines[0].IsZero() || !conn.deadlines[1].IsZero() {
				t.Fatalf("deadlines = %#v, want set then clear", conn.deadlines)
			}
			if got := readTimeout(tc.state); got != tc.want {
				t.Fatalf("readTimeout(%v) = %s, want %s", tc.state, got, tc.want)
			}
		})
	}
}
