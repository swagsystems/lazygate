package main

import (
	"bytes"
	"net"
	"testing"
	"time"

	"lazymc/nbt"
	"lazymc/proto"
	"lazymc/proto/packets/play"
)

func TestWaitForServerJoinGamePreservesEarlierPlayPackets(t *testing.T) {
	proxyConn, backendConn := net.Pipe()
	defer proxyConn.Close()
	defer backendConn.Close()

	protocol := uint32(play.ProtocolV1_20_1)
	clientInfo := &proto.ClientInfo{Protocol: &protocol}
	backend := proto.DummyClient()
	backend.SetState(proto.ClientStatePlay)
	backend.SetCompression(proto.CompressionThreshold)

	preJoin := proto.NewRawPacket(0x17, []byte("backend-registration-payload"))
	preJoinWire, ok := preJoin.EncodeWithLen(backend)
	if !ok {
		t.Fatal("encode pre-JoinGame packet")
	}
	joinData := play.LobbyJoinGameData(clientInfo, play.JoinGameParams{
		DimensionCodec:      nbt.NewCompound(),
		MaxPlayers:          10,
		ViewDistance:        10,
		SimulationDistance:  8,
		EnableRespawnScreen: true,
	})
	joinWire, ok := proto.NewRawPacket(play.JoinGamePacketID(clientInfo.GetProtocol()), joinData).EncodeWithLen(backend)
	if !ok {
		t.Fatal("encode JoinGame packet")
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := backendConn.Write(append(preJoinWire, joinWire...))
		writeDone <- err
	}()

	var serverBuf []byte
	join, err := waitForServerJoinGameNoTimeout(backend, clientInfo, proxyConn, &serverBuf, TargetLobby)
	if err != nil {
		t.Fatal(err)
	}
	if join == nil || join.WorldName == nil || *join.WorldName != "lazymc:lobby" {
		t.Fatalf("unexpected JoinGame data: %#v", join)
	}
	if !bytes.Equal(serverBuf, preJoinWire) {
		t.Fatalf("queued bytes = %x, want %x", serverBuf, preJoinWire)
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("backend write did not complete")
	}
}
