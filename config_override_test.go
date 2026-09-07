package main

// Additional parity tests for config key decoding.

import (
	"testing"
)

func TestConfigKeyOverrides(t *testing.T) {
	path := writeTemp(t, "lazymc.toml", `
[public]
address = "0.0.0.0:25570"
version = "1.19.3"
protocol = 761

[server]
directory = "server-dir"
command = "java -jar server.jar"
address = "192.0.2.5:25566"
controller_managed = true
freeze_process = false
wake_on_start = true
wake_on_crash = true
probe_on_start = true
forge = true
start_timeout = 10
stop_timeout = 20
wake_whitelist = false
block_banned_ips = false
drop_banned_ips = true
send_proxy_v2 = true

[time]
sleep_after = 5

[motd]
sleeping = "sleeping motd"
from_server = true

[join]
methods = ["forward", "lobby"]

[join.forward]
address = "127.0.0.1:25577"
send_proxy_v2 = true

[join.lobby]
timeout = 42
message = "lobby msg"
ready_sound = ""

[lockout]
enabled = true
message = "closed"

[rcon]
enabled = true
port = 25575
password = "pw"
randomize_password = false
send_proxy_v2 = true

[advanced]
rewrite_server_properties = false

[config]
version = "0.2.11"
`)
	cfg, err := ConfigLoad(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Public.Address.String() != "0.0.0.0:25570" {
		t.Errorf("public.address = %s", cfg.Public.Address)
	}
	if cfg.Public.Version != "1.19.3" || cfg.Public.Protocol != 761 {
		t.Errorf("public version = %s/%d", cfg.Public.Version, cfg.Public.Protocol)
	}
	if cfg.Server.Directory != "server-dir" || cfg.Server.Command != "java -jar server.jar" {
		t.Errorf("server dir/command = %q/%q", cfg.Server.Directory, cfg.Server.Command)
	}
	if !cfg.Server.ControllerManaged {
		t.Errorf("server controller_managed override was not decoded")
	}
	if cfg.Server.Address.String() != "192.0.2.5:25566" {
		t.Errorf("server.address = %s", cfg.Server.Address)
	}
	if cfg.Server.FreezeProcess || !cfg.Server.WakeOnStart || !cfg.Server.WakeOnCrash || !cfg.Server.ProbeOnStart {
		t.Errorf("server bools wrong: %+v", cfg.Server)
	}
	if !cfg.Server.Forge || cfg.Server.StartTimeout != 10 || cfg.Server.StopTimeout != 20 {
		t.Errorf("server values wrong: %+v", cfg.Server)
	}
	if cfg.Server.WakeWhitelist || cfg.Server.BlockBannedIps || !cfg.Server.DropBannedIps || !cfg.Server.SendProxyV2 {
		t.Errorf("server bools wrong: %+v", cfg.Server)
	}
	if cfg.Time.SleepAfter != 5 || cfg.Time.MinOnlineTime != 60 {
		t.Errorf("time wrong: %+v", cfg.Time)
	}
	if cfg.Motd.Sleeping != "sleeping motd" || !cfg.Motd.FromServer {
		t.Errorf("motd wrong: %+v", cfg.Motd)
	}
	if len(cfg.Join.Methods) != 2 || cfg.Join.Methods[0] != MethodForward || cfg.Join.Methods[1] != MethodLobby {
		t.Errorf("methods wrong: %v", cfg.Join.Methods)
	}
	if cfg.Join.Forward.Address.String() != "127.0.0.1:25577" || !cfg.Join.Forward.SendProxyV2 {
		t.Errorf("forward wrong: %+v", cfg.Join.Forward)
	}
	if cfg.Join.Lobby.Timeout != 42 || cfg.Join.Lobby.Message != "lobby msg" {
		t.Errorf("lobby wrong: %+v", cfg.Join.Lobby)
	}
	if cfg.Join.Lobby.ReadySound == nil || *cfg.Join.Lobby.ReadySound != "" {
		t.Errorf("ready_sound wrong: %v", cfg.Join.Lobby.ReadySound)
	}
	if !cfg.Lockout.Enabled || cfg.Lockout.Message != "closed" {
		t.Errorf("lockout wrong: %+v", cfg.Lockout)
	}
	if !cfg.Rcon.Enabled || cfg.Rcon.Port != 25575 || cfg.Rcon.Password != "pw" || cfg.Rcon.RandomizePassword || !cfg.Rcon.SendProxyV2 {
		t.Errorf("rcon wrong: %+v", cfg.Rcon)
	}
	if cfg.Advanced.RewriteServerProperties {
		t.Errorf("advanced wrong: %+v", cfg.Advanced)
	}
	if cfg.Config.Version == nil || *cfg.Config.Version != "0.2.11" {
		t.Errorf("config.version wrong: %v", cfg.Config.Version)
	}
}
