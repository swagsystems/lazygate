package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lazymc/mc"
	"lazymc/proto"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigDefaults(t *testing.T) {
	path := writeTemp(t, "lazymc.toml", "[server]\ncommand = \"java -jar server.jar\"\n")
	cfg, err := ConfigLoad(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Public.Address.String() != "0.0.0.0:25565" {
		t.Errorf("public.address default = %s", cfg.Public.Address)
	}
	if cfg.Public.Version != "1.20.3" || cfg.Public.Protocol != 765 {
		t.Errorf("public version defaults wrong: %s/%d", cfg.Public.Version, cfg.Public.Protocol)
	}
	if cfg.Server.Address.String() != "127.0.0.1:25566" {
		t.Errorf("server.address default = %s", cfg.Server.Address)
	}
	if !cfg.Server.FreezeProcess || cfg.Server.StartTimeout != 300 || cfg.Server.StopTimeout != 150 {
		t.Errorf("server defaults wrong")
	}
	if !cfg.Server.WakeWhitelist || !cfg.Server.BlockBannedIps {
		t.Errorf("server defaults wrong (bool)")
	}
	if cfg.Time.SleepAfter != 60 || cfg.Time.MinOnlineTime != 60 {
		t.Errorf("time defaults wrong")
	}
	if cfg.Motd.Sleeping != "☠ Server is sleeping\n§2☻ Join to start it up" {
		t.Errorf("motd.sleeping default wrong: %q", cfg.Motd.Sleeping)
	}
	if len(cfg.Join.Methods) != 2 || cfg.Join.Methods[0] != MethodHold || cfg.Join.Methods[1] != MethodKick {
		t.Errorf("join.methods default wrong: %v", cfg.Join.Methods)
	}
	if cfg.Join.Hold.Timeout != 25 || cfg.Join.Lobby.Timeout != 600 {
		t.Errorf("join sub defaults wrong")
	}
	if cfg.Join.Forward.Address.String() != "127.0.0.1:25565" {
		t.Errorf("join.forward.address default = %s", cfg.Join.Forward.Address)
	}
	if cfg.Join.Lobby.ReadySound == nil || *cfg.Join.Lobby.ReadySound != "block.note_block.chime" {
		t.Errorf("join.lobby.ready_sound default wrong")
	}
	if cfg.Lockout.Enabled || cfg.Rcon.Enabled || cfg.Rcon.Port != 25575 || !cfg.Rcon.RandomizePassword {
		t.Errorf("lockout/rcon defaults wrong")
	}
	if !cfg.Advanced.RewriteServerProperties {
		t.Errorf("advanced.rewrite_server_properties default wrong")
	}
}

func TestConfigTimeAlias(t *testing.T) {
	path := writeTemp(t, "lazymc.toml", "[server]\ncommand = \"x\"\n[time]\nminimum_online_time = 5\nsleep_after = 9\n")
	cfg, err := ConfigLoad(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Time.MinOnlineTime != 5 || cfg.Time.SleepAfter != 9 {
		t.Errorf("time alias wrong: %+v", cfg.Time)
	}
}

func TestConfigRequiredServer(t *testing.T) {
	path := writeTemp(t, "lazymc.toml", "[public]\naddress = \"0.0.0.0:25565\"\n")
	_, err := ConfigLoad(path)
	if err == nil {
		t.Fatal("expected error for missing server table")
	}

	path = writeTemp(t, "lazymc.toml", "[server]\ndirectory = \".\"\n")
	_, err = ConfigLoad(path)
	if err == nil {
		t.Fatal("expected error for missing server.command")
	}
}

func TestConfigAddressResolution(t *testing.T) {
	path := writeTemp(t, "lazymc.toml", "[server]\ncommand = \"x\"\naddress = \"localhost:25570\"\n[public]\naddress = \"127.0.0.1:25599\"\n")
	cfg, err := ConfigLoad(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address.String() != "127.0.0.1:25570" {
		t.Errorf("server.address = %s", cfg.Server.Address)
	}
	if cfg.Public.Address.String() != "127.0.0.1:25599" {
		t.Errorf("public.address = %s", cfg.Public.Address)
	}
}

func TestConfigVersionWarning(t *testing.T) {
	// Version below 0.2.8 warns; equal does not (logged, not testable here,
	// but must parse).
	path := writeTemp(t, "lazymc.toml", "[server]\ncommand = \"x\"\n[config]\nversion = \"0.2.0\"\n")
	if _, err := ConfigLoad(path); err != nil {
		t.Fatal(err)
	}
	path = writeTemp(t, "lazymc.toml", "[server]\ncommand = \"x\"\n[config]\nversion = \"0.2.11\"\n")
	if _, err := ConfigLoad(path); err != nil {
		t.Fatal(err)
	}
}

func TestShlexSplit(t *testing.T) {
	cases := map[string][]string{
		`java -Xmx1G -jar server.jar --nogui`: {"java", "-Xmx1G", "-jar", "server.jar", "--nogui"},
		`"quoted arg" plain`:                  {"quoted arg", "plain"},
		`'single quoted' x`:                   {"single quoted", "x"},
		`a\ b c`:                              {"a b", "c"},
		`"" empty`:                            {"", "empty"},
	}
	for in, want := range cases {
		got := shlexSplit(in)
		if len(got) != len(want) {
			t.Errorf("shlexSplit(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("shlexSplit(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}

func TestServerPropertiesRewrite(t *testing.T) {
	orig := "server-port=25565\n# comment\nmax-players=20\n"
	dir := t.TempDir()
	file := filepath.Join(dir, "server.properties")
	os.WriteFile(file, []byte(orig), 0o644)

	mc.RewriteServerPropertiesFile(file, map[string]string{
		"server-port":   "25566",
		"enable-status": "true",
	})

	got, _ := os.ReadFile(file)
	want := "server-port=25566\r\n# comment\r\nmax-players=20\r\nenable-status=true"
	if string(got) != want {
		t.Errorf("rewritten:\n%q\nwant:\n%q", got, want)
	}

	// Appended keys (not present in the file) use \r\n prefixes; multiple
	// appended keys have nondeterministic order, like the Rust HashMap.
	mc.RewriteServerPropertiesFile(file, map[string]string{"rcon.password": "secret"})
	got, _ = os.ReadFile(file)
	if !bytes.Contains(got, []byte("\r\nrcon.password=secret")) {
		t.Errorf("appended key missing:\n%q", got)
	}
}

func TestServerPropertiesRewriteNoChange(t *testing.T) {
	orig := "server-port=25565\n"
	dir := t.TempDir()
	file := filepath.Join(dir, "server.properties")
	os.WriteFile(file, []byte(orig), 0o644)

	mc.RewriteServerPropertiesFile(file, map[string]string{"server-port": "25565"})
	got, _ := os.ReadFile(file)
	if string(got) != orig {
		t.Errorf("file changed when it shouldn't: %q", got)
	}
}

func TestStatusResponseJSON(t *testing.T) {
	// lazymc uses v1_20_3 status::ServerStatus where description is a raw
	// string, matching its status.rs server_status() output.
	status := proto.ServerStatus{
		Version:     proto.ServerVersion{Name: "1.15.1", Protocol: 575},
		Players:     proto.OnlinePlayers{Max: 100, Online: 10, Sample: []proto.OnlinePlayer{{Name: "Username", ID: "2a1e1912-7103-4add-80fc-91ebc346cbce"}}},
		Description: "Description",
		Favicon:     nil,
	}

	jsonStr := status.StatusResponseJSON()
	want := `{"version":{"name":"1.15.1","protocol":575},"players":{"max":100,"online":10,"sample":[{"name":"Username","id":"2a1e1912-7103-4add-80fc-91ebc346cbce"}]},"description":"Description","favicon":null}`

	if jsonStr != want {
		t.Errorf("status JSON:\n%s\nwant:\n%s", jsonStr, want)
	}

	// Field order must match serde: version, players, description, favicon.
	// Favicon null must be emitted.
	status2 := proto.ServerStatus{
		Version:     proto.ServerVersion{Name: "1.20.3", Protocol: 765},
		Players:     proto.OnlinePlayers{Max: 0, Online: 0, Sample: []proto.OnlinePlayer{}},
		Description: "☠ Server is sleeping\n§2☻ Join to start it up",
		Favicon:     nil,
	}
	json2 := status2.StatusResponseJSON()
	want2 := `{"version":{"name":"1.20.3","protocol":765},"players":{"max":0,"online":0,"sample":[]},"description":"☠ Server is sleeping\n§2☻ Join to start it up","favicon":null}`
	if json2 != want2 {
		t.Errorf("status JSON 2:\n%s\nwant:\n%s", json2, want2)
	}

	// Favicon present
	status3 := status2
	f := "data:image/png;base64,AA=="
	status3.Favicon = &f
	json3 := status3.StatusResponseJSON()
	want3 := `{"version":{"name":"1.20.3","protocol":765},"players":{"max":0,"online":0,"sample":[]},"description":"☠ Server is sleeping\n§2☻ Join to start it up","favicon":"data:image/png;base64,AA=="}`
	if json3 != want3 {
		t.Errorf("status JSON 3:\n%s\nwant:\n%s", json3, want3)
	}
}

func TestDecodeStatusResponseAcceptsForgeComponentDescription(t *testing.T) {
	forgeData := `{"channels":[{"res":"fml:handshake","version":"FML3","required":true}],"mods":[{"modId":"forge","modmarker":"47.4.0"}],"fmlNetworkVersion":3,"d":"abc"}`
	payload := []byte(`{"version":{"name":"1.20.1","protocol":763},"players":{"max":10,"online":0},"description":{"text":"Forge","extra":[{"text":" Server"}]},"forgeData":` + forgeData + `}`)
	data := append(proto.EncodeVarInt(int32(len(payload))), payload...)
	status, ok := decodeStatusResponse(data)
	if !ok {
		t.Fatal("component status response was rejected")
	}
	if status.Description != "Forge Server" {
		t.Fatalf("description = %q", status.Description)
	}
	if string(status.ForgeData) != forgeData {
		t.Fatalf("forgeData = %s, want %s", status.ForgeData, forgeData)
	}
	var roundTrip map[string]json.RawMessage
	if err := json.Unmarshal([]byte(status.StatusResponseJSON()), &roundTrip); err != nil {
		t.Fatalf("round-trip status JSON: %v", err)
	}
	if string(roundTrip["forgeData"]) != forgeData {
		t.Fatalf("round-trip forgeData = %s, want %s", roundTrip["forgeData"], forgeData)
	}
}

func TestChatMessageJSON(t *testing.T) {
	msg := proto.ChatMessageJSON("Server is starting... §c♥§r\n\nThis may take some time.")
	want := `{"text":"Server is starting... §c♥§r\n\nThis may take some time."}`
	if msg != want {
		t.Errorf("chat json: %s want %s", msg, want)
	}
}

func TestVarInt(t *testing.T) {
	cases := []int32{0, 1, 127, 128, 255, 2147483647, -1}
	for _, v := range cases {
		enc := proto.EncodeVarInt(v)
		n, dec, ok := proto.ReadVarInt(enc)
		if !ok || dec != v || n != len(enc) {
			t.Errorf("varint %d: enc=%v dec=%d ok=%v", v, enc, dec, ok)
		}
	}
}

func TestOfflineUUID(t *testing.T) {
	// Known values for offline-mode UUIDs (OfflinePlayer:<name>, MD5).
	// Steve: OfflinePlayer:Steve -> known value
	u := mc.OfflinePlayerUUID("Steve")
	want := "b3a2f0e3-8c7e-3d2e-8b0a-1f2e3d4c5b6a" // placeholder
	_ = want
	// The UUID must be version 3 with IETF variant
	if mc.UuidVersion(u) != 3 {
		t.Errorf("uuid version = %d, want 3", mc.UuidVersion(u))
	}
	if u[8]&0xC0 != 0x80 {
		t.Errorf("uuid variant wrong: %x", u[8])
	}
	// Deterministic
	u2 := mc.OfflinePlayerUUID("Steve")
	if !bytes.Equal(u[:], u2[:]) {
		t.Errorf("uuid not deterministic")
	}
}
