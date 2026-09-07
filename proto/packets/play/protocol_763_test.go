package play

import (
	"encoding/hex"
	"net"
	"testing"

	"lazymc/nbt"
	"lazymc/proto"
)

func protocol763Info() (*proto.Client, *proto.ClientInfo) {
	protocol := uint32(ProtocolV1_20_1)
	return proto.DummyClient(), &proto.ClientInfo{Protocol: &protocol}
}

func capturePackets763(t *testing.T, count int, send func(*proto.Client, *proto.ClientInfo, net.Conn) error) []proto.RawPacket {
	t.Helper()
	server, client := net.Pipe()
	sender, info := protocol763Info()
	done := make(chan error, 1)
	go func() {
		done <- send(sender, info, client)
	}()

	reader := proto.DummyClient()
	var buf []byte
	packets := make([]proto.RawPacket, 0, count)
	for i := 0; i < count; i++ {
		packet, _, more, err := proto.ReadPacket(reader, &buf, server)
		if err != nil {
			t.Fatalf("read packet %d: %v", i, err)
		}
		if !more {
			t.Fatalf("packet %d was not available", i)
		}
		packets = append(packets, packet)
	}
	if err := <-done; err != nil {
		t.Fatalf("send packets: %v", err)
	}
	_ = server.Close()
	_ = client.Close()
	return packets
}

func mustHex763(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode golden bytes: %v", err)
	}
	return decoded
}

func TestProtocol763JoinGameGoldenAndFixture(t *testing.T) {
	client, info := protocol763Info()
	params := JoinGameParams{
		DimensionCodec:      nbt.NewCompound(),
		MaxPlayers:          20,
		ViewDistance:        10,
		SimulationDistance:  8,
		EnableRespawnScreen: true,
	}
	want := mustHex763(t, "000000000003ff03136d696e6563726166743a6f766572776f726c64146d696e6563726166743a7468655f6e6574686572116d696e6563726166743a7468655f656e640a000000136d696e6563726166743a6f766572776f726c640c6c617a796d633a6c6f6262790000000000000000140a08000100000000")
	if got := LobbyJoinGameData(info, params); string(got) != string(want) {
		t.Fatalf("join game payload mismatch:\n got %x\nwant %x", got, want)
	}
	if JoinGamePacketID(info.GetProtocol()) != 0x28 {
		t.Fatalf("join game packet id = 0x%02x, want 0x28", JoinGamePacketID(info.GetProtocol()))
	}

	fixture := append([]byte(nil), want...)
	parsed, err := JoinGameDataFromPacket(info, proto.NewRawPacket(0x28, fixture))
	if err != nil {
		t.Fatalf("parse 763 join fixture: %v", err)
	}
	if parsed.DimensionType == nil || *parsed.DimensionType != "minecraft:overworld" {
		t.Fatalf("dimension type = %v", parsed.DimensionType)
	}
	if parsed.WorldName == nil || *parsed.WorldName != "lazymc:lobby" {
		t.Fatalf("world name = %v", parsed.WorldName)
	}
	if parsed.SimulationDistance == nil || *parsed.SimulationDistance != 8 {
		t.Fatalf("simulation distance = %v", parsed.SimulationDistance)
	}
	if parsed.HashedSeed == nil || *parsed.HashedSeed != 0 {
		t.Fatalf("hashed seed = %v", parsed.HashedSeed)
	}
	if !JoinGameIsPacket(info, 0x28) || JoinGameIsPacket(info, 0x26) {
		t.Fatal("protocol 763 join-game ID recognition is incorrect")
	}
	_ = client
}

func TestProtocol763RespawnGolden(t *testing.T) {
	params := RespawnParams{
		WorldName:        "world",
		HashedSeed:       0x0102030405060708,
		GameMode:         1,
		PreviousGameMode: 0xFF,
		IsFlat:           true,
	}
	packets := capturePackets763(t, 1, func(client *proto.Client, info *proto.ClientInfo, conn net.Conn) error {
		return SendRespawn(client, info, conn, params)
	})
	if packets[0].ID != 0x41 {
		t.Fatalf("respawn packet id = 0x%02x, want 0x41", packets[0].ID)
	}
	want := mustHex763(t, "136d696e6563726166743a6f766572776f726c6405776f726c64010203040506070801ff00010000")
	if string(packets[0].Data) != string(want) {
		t.Fatalf("respawn payload mismatch: got %x want %x", packets[0].Data, want)
	}
}

func TestProtocol763LobbyPacketsGolden(t *testing.T) {
	t.Run("brand", func(t *testing.T) {
		packets := capturePackets763(t, 1, SendServerBrand)
		if packets[0].ID != 0x17 {
			t.Fatalf("brand packet id = 0x%02x, want 0x17", packets[0].ID)
		}
		want := mustHex763(t, "0f6d696e6563726166743a6272616e646c617a796d63")
		if string(packets[0].Data) != string(want) {
			t.Fatalf("brand payload mismatch: got %x want %x", packets[0].Data, want)
		}
	})

	t.Run("position", func(t *testing.T) {
		packets := capturePackets763(t, 1, SendPlayerPos)
		if packets[0].ID != 0x3C {
			t.Fatalf("position packet id = 0x%02x, want 0x3c", packets[0].ID)
		}
		want := mustHex763(t, "0000000000000000000000000000000000000000000000000000000042b400000000")
		if string(packets[0].Data) != string(want) {
			t.Fatalf("position payload mismatch: got %x want %x", packets[0].Data, want)
		}
	})

	t.Run("time", func(t *testing.T) {
		packets := capturePackets763(t, 1, SendTimeUpdate)
		if packets[0].ID != 0x5E || len(packets[0].Data) != 16 {
			t.Fatalf("time packet = id 0x%02x len %d", packets[0].ID, len(packets[0].Data))
		}
		if string(packets[0].Data) != string(make([]byte, 16)) {
			t.Fatalf("time payload = %x", packets[0].Data)
		}
	})

	t.Run("keepalive", func(t *testing.T) {
		keepAliveID.Store(0)
		packets := capturePackets763(t, 1, SendKeepAlive)
		if packets[0].ID != 0x23 || string(packets[0].Data) != string(make([]byte, 8)) {
			t.Fatalf("keepalive packet = id 0x%02x data %x", packets[0].ID, packets[0].Data)
		}
	})

	t.Run("sound", func(t *testing.T) {
		packets := capturePackets763(t, 1, func(client *proto.Client, info *proto.ClientInfo, conn net.Conn) error {
			return SendSound(client, info, conn, "minecraft:block.note_block.harp")
		})
		if packets[0].ID != 0x62 {
			t.Fatalf("sound packet id = 0x%02x, want 0x62", packets[0].ID)
		}
		want := mustHex763(t,
			"001f6d696e6563726166743a626c6f636b2e6e6f74655f626c6f636b2e68617270"+
				"0000000000000000000000000000"+
				"3f8000003f800000"+
				"0000000000000000",
		)
		if string(packets[0].Data) != string(want) {
			t.Fatalf("sound payload mismatch: got %x want %x", packets[0].Data, want)
		}
	})

	t.Run("title", func(t *testing.T) {
		packets := capturePackets763(t, 3, func(client *proto.Client, info *proto.ClientInfo, conn net.Conn) error {
			return SendTitle(client, info, conn, "ready\nwaiting")
		})
		wantIDs := []byte{0x5F, 0x5D, 0x60}
		wantData := [][]byte{
			mustHex763(t, "107b2274657874223a227265616479227d"),
			mustHex763(t, "127b2274657874223a2277616974696e67227d"),
			mustHex763(t, "000000000000019000000000"),
		}
		for i, packet := range packets {
			if packet.ID != wantIDs[i] || string(packet.Data) != string(wantData[i]) {
				t.Fatalf("title packet %d = id 0x%02x data %x", i, packet.ID, packet.Data)
			}
		}
	})
}
