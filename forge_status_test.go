package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadForgeStatusSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forge-status.json")
	raw := `{"channels":[],"mods":[],"fmlNetworkVersion":3,"d":"abc"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	config := &Config{
		Path:   filepath.Join(dir, "lazymc.toml"),
		Public: Public{Version: "1.20.1", Protocol: 763},
		Server: Server{ForgeStatusFile: "forge-status.json"},
	}
	status, err := loadForgeStatusSnapshot(config)
	if err != nil {
		t.Fatal(err)
	}
	if status.Version.Protocol != 763 || string(status.ForgeData) != raw {
		t.Fatalf("status = %#v", status)
	}
}

func TestLoadForgeStatusSnapshotFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forge-status.json")
	if err := os.WriteFile(path, []byte(`{"mods":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadForgeStatusSnapshot(&Config{Path: filepath.Join(dir, "lazymc.toml"), Server: Server{ForgeStatusFile: path}})
	if err == nil {
		t.Fatal("incomplete Forge status snapshot was accepted")
	}
}

func TestForgeStatusSnapshotPersistsAndLoads(t *testing.T) {
	dir := t.TempDir()
	config := &Config{
		Path:   filepath.Join(dir, "lazymc.toml"),
		Public: Public{Version: "1.20.1", Protocol: 763},
		Server: Server{ForgeStatusFile: "forge-status.json"},
	}
	if status, err := loadForgeStatusSnapshot(config); err != nil || status != nil {
		t.Fatalf("missing snapshot: status=%#v err=%v", status, err)
	}
	raw := []byte(`{"channels":[],"mods":[],"fmlNetworkVersion":3,"d":"persisted"}`)
	if err := persistForgeStatusSnapshot(config, raw); err != nil {
		t.Fatal(err)
	}
	status, err := loadForgeStatusSnapshot(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(status.ForgeData) != string(raw) {
		t.Fatalf("forgeData = %s", status.ForgeData)
	}
	info, err := os.Stat(filepath.Join(dir, "forge-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode=%v", info.Mode().Perm())
	}
}
