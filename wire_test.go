package main

// Wire format tests for RCON and HAProxy v2 headers.

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"lazymc/mc"
	"lazymc/proxyv2"
)

// TestRconWireFormat verifies the RCON client's packet framing against the
// rust_rcon spec: LE length, LE id, LE type, body, 2 null terminators.
func TestRconWireFormat(t *testing.T) {
	// A fake RCON server that records what it receives and responds like
	// the Minecraft RCON implementation.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type recv struct {
		id   int32
		typ  int32
		body string
	}
	var received []recv
	done := make(chan struct{})

	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		readPacket := func() recv {
			var hdr [12]byte
			conn.Read(hdr[:])
			length := int32(binary.LittleEndian.Uint32(hdr[0:4]))
			id := int32(binary.LittleEndian.Uint32(hdr[4:8]))
			typ := int32(binary.LittleEndian.Uint32(hdr[8:12]))
			body := make([]byte, length-10)
			conn.Read(body)
			var term [2]byte
			conn.Read(term[:])
			return recv{id, typ, string(body)}
		}

		// Auth packet, respond with auth response
		auth := readPacket()
		received = append(received, auth)
		respond := func(id int32, typ int32, body string) {
			pkt := make([]byte, 0, 12+len(body)+2)
			var tmp [4]byte
			binary.LittleEndian.PutUint32(tmp[:], uint32(10+len(body)))
			pkt = append(pkt, tmp[:]...)
			binary.LittleEndian.PutUint32(tmp[:], uint32(id))
			pkt = append(pkt, tmp[:]...)
			binary.LittleEndian.PutUint32(tmp[:], uint32(typ))
			pkt = append(pkt, tmp[:]...)
			pkt = append(pkt, body...)
			pkt = append(pkt, 0x00, 0x00)
			conn.Write(pkt)
		}
		respond(auth.id, 2, "")

		// Command packet
		cmd := readPacket()
		received = append(received, cmd)
		respond(cmd.id, 0, "Done")

		// End-marker empty command (multi-packet response handling)
		end := readPacket()
		received = append(received, end)
		respond(end.id, 0, "")
	}()

	rcon, err := mc.ConnectRcon(false, ln.Addr().String(), "secret")
	if err != nil {
		t.Fatalf("rcon connect: %v", err)
	}
	defer rcon.Close()

	resp, err := rcon.Cmd("stop")
	if err != nil {
		t.Fatalf("rcon cmd: %v", err)
	}
	if resp != "Done" {
		t.Errorf("rcon response = %q", resp)
	}

	<-done

	if len(received) != 3 {
		t.Fatalf("received %d packets, want 3", len(received))
	}
	// Auth: id 1, type 3, body password
	if received[0].id != 1 || received[0].typ != 3 || received[0].body != "secret" {
		t.Errorf("auth packet wrong: %+v", received[0])
	}
	// Command: id 2, type 2, body stop
	if received[1].id != 2 || received[1].typ != 2 || received[1].body != "stop" {
		t.Errorf("cmd packet wrong: %+v", received[1])
	}
	// End marker: type 2, empty body
	if received[2].typ != 2 || received[2].body != "" {
		t.Errorf("end marker wrong: %+v", received[2])
	}
}

// TestProxyV2Headers verifies HAProxy v2 header bytes.
func TestProxyV2Headers(t *testing.T) {
	// Local header: signature + 0x20 (v2/LOCAL) + 0x00 (UNSPEC/STREAM) +
	// length 0.
	local, err := proxyv2.LocalHeader()
	if err != nil {
		t.Fatal(err)
	}
	wantLocal := []byte{
		0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
		0x20, 0x01, 0x00, 0x00,
	}
	if !bytes.Equal(local, wantLocal) {
		t.Errorf("local header:\n%x\nwant:\n%x", local, wantLocal)
	}

	// Stream header: PROXY v2 IPv4/STREAM with real addresses.
	peer := &net.TCPAddr{IP: net.IPv4(192, 168, 1, 50), Port: 51234}
	localAddr := &net.TCPAddr{IP: net.IPv4(192, 168, 1, 10), Port: 25565}
	stream, err := proxyv2.StreamHeader(peer, localAddr)
	if err != nil {
		t.Fatal(err)
	}
	wantStream := []byte{
		0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
		0x21, 0x11, 0x00, 0x0C,
		192, 168, 1, 50,
		192, 168, 1, 10,
		0xC8, 0x22, // 51234 BE
		0x63, 0xDD, // 25565 BE
	}
	if !bytes.Equal(stream, wantStream) {
		t.Errorf("stream header:\n%x\nwant:\n%x", stream, wantStream)
	}
}
