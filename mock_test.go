package main

// TestMockServer directly exercises the test mock server's login flow with a
// compression-aware client.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lazymc/internal/testserver"
	"lazymc/proto"
)

func TestMockServerLoginFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	srv, err := testserver.NewServer("127.0.0.1:0", stateFile)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	conn, err := net.Dial("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := proto.DummyClient()
	w := proto.NewPacketWriter()
	w.WriteVarInt(765)
	w.WriteString("127.0.0.1")
	w.WriteUint16(25566)
	w.WriteVarInt(2)
	writeRaw(conn, client, proto.PacketHandshake, w.Bytes())

	lw := proto.NewPacketWriter()
	lw.WriteString("UserA")
	writeRaw(conn, client, proto.PacketServerLoginStart, lw.Bytes())

	var buf []byte
	got := map[byte]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		packet, _, more, err := proto.ReadPacket(client, &buf, conn)
		if err != nil {
			t.Fatalf("read error: %v", err)
		}
		if !more {
			break
		}
		got[packet.ID] = true
		fmt.Printf("packet id=0x%02x len=%d\n", packet.ID, len(packet.Data))
		if packet.ID == proto.PacketClientSetCompression {
			sc, _ := proto.DecodeSetCompression(packet.Data)
			client.SetCompression(sc.Threshold)
		}
	}
	// Keep reading briefly for the join game after login success
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !got[0x26] {
		conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		packet, _, more, err := proto.ReadPacket(client, &buf, conn)
		if err != nil || !more {
			break
		}
		got[packet.ID] = true
	}
	if !got[proto.PacketClientSetCompression] {
		t.Errorf("no set compression")
	}
	if !got[proto.PacketClientLoginSuccess] {
		t.Errorf("no login success")
	}
	if !got[0x26] {
		t.Errorf("no join game")
	}
	_ = os.Remove(stateFile)
}
