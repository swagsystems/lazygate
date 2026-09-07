package main

// End-to-end test: runs lazymc in-process against a mock Minecraft server
// and verifies the full lifecycle: sleeping status, wake-on-join via the
// hold method, proxy relay to the real server, and idle sleep.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lazymc/internal/testserver"
	"lazymc/proto"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

var mockServerBinary string

func TestMain(m *testing.M) {
	// Build the mock server helper binary used as the managed "server
	// process" in e2e tests.
	dir, err := os.MkdirTemp("", "lazymc-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mockServerBinary = filepath.Join(dir, "lazymc-mock-server")
	cmd := exec.Command("go", "build", "-o", mockServerBinary, "./cmd/mockserver")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build mock server: %v\n%s", err, out)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// mockState is the on-disk state of the mock server process.
type mockState struct {
	Statuses int                `json:"status_queries"`
	Logins   []testserver.Login `json:"logins"`
	Shutdown bool               `json:"shutdown"`
}

func readMockState(t *testing.T, file string) mockState {
	// Retry briefly on transient parse errors (concurrent writer).
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		data, err := os.ReadFile(file)
		if err == nil {
			var st mockState
			if err := json.Unmarshal(data, &st); err == nil {
				return st
			} else {
				lastErr = err
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if t != nil {
		t.Fatalf("bad mock state (%s): %v", file, lastErr)
	}
	return mockState{}
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

type e2eHarness struct {
	dir        string
	publicAddr string
	mockPort   int
	marker     string
	stateFile  string
	configPath string
}

func newHarness(t *testing.T, configExtra string) *e2eHarness {
	t.Helper()
	dir := t.TempDir()

	publicPort := freePort(t)
	mockPort := freePort(t)

	marker := filepath.Join(dir, "started.marker")
	stateFile := filepath.Join(dir, "mock-state.json")

	// The server start script: brings the mock server up (like a real
	// Minecraft server), then stays alive. Exec replaces the shell so the
	// script PID becomes the mock server process, which lazymc then manages
	// with signals.
	script := filepath.Join(dir, "server.sh")
	scriptBody := fmt.Sprintf(`#!/bin/bash
touch "%s"
exec %s -addr 127.0.0.1:%d -state "%s"
`, marker, mockServerBinary, mockPort, stateFile)
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "lazymc.toml")
	configBody := fmt.Sprintf(`
[public]
address = "127.0.0.1:%d"

[server]
directory = "%s"
command = "bash %s"
address = "127.0.0.1:%d"
freeze_process = false
start_timeout = 10
stop_timeout = 10

[time]
sleep_after = 2
min_online_time = 1

[join]
methods = ["hold", "kick"]
%s
`, publicPort, dir, script, mockPort, configExtra)

	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	return &e2eHarness{
		dir:        dir,
		publicAddr: fmt.Sprintf("127.0.0.1:%d", publicPort),
		mockPort:   mockPort,
		marker:     marker,
		stateFile:  stateFile,
		configPath: configPath,
	}
}

// queryStatus performs a status request against lazymc and returns the raw
// JSON, or "" on failure.
func queryStatus(addr string) string {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()

	client := proto.DummyClient()
	w := proto.NewPacketWriter()
	w.WriteVarInt(765)
	w.WriteString("127.0.0.1")
	w.WriteUint16(uint16(mustPort(addr)))
	w.WriteVarInt(1) // status
	if !writeRaw(conn, client, proto.PacketHandshake, w.Bytes()) {
		return ""
	}
	if !writeRaw(conn, client, proto.PacketServerStatusRequest, nil) {
		return ""
	}

	var buf []byte
	for {
		packet, _, more, err := proto.ReadPacket(client, &buf, conn)
		if err != nil || !more {
			return ""
		}
		if packet.ID == proto.PacketClientStatusResponse {
			_, strLen, ok := proto.ReadVarInt(packet.Data)
			if !ok {
				return ""
			}
			return string(packet.Data[varIntLen(packet.Data) : varIntLen(packet.Data)+int(strLen)])
		}
	}
}

func varIntLen(data []byte) int {
	for i := 0; i < len(data) && i < 5; i++ {
		if data[i]&0x80 == 0 {
			return i + 1
		}
	}
	return len(data)
}

func mustPort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return port
}

func writeRaw(conn net.Conn, client *proto.Client, id byte, data []byte) bool {
	packet := proto.NewRawPacket(id, data)
	encoded, ok := packet.EncodeWithLen(client)
	if !ok {
		return false
	}
	_, err := conn.Write(encoded)
	return err == nil
}

// waitFor polls fn until it returns a non-empty value or times out.
func waitFor(t *testing.T, timeout time.Duration, what string, fn func() string) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := fn()
		if out != "" {
			return out
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return ""
}

func TestEndToEndLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e in short mode")
	}

	h := newHarness(t, "")

	// Load config and start lazymc in-process
	cfg := LoadConfig(h.configPath)
	serviceDone := make(chan struct{})
	go func() {
		_ = Service(&cfg)
		close(serviceDone)
	}()

	// 1. Sleeping status: MOTD, version hint, favicon
	statusJSON := waitFor(t, 10*time.Second, "lazymc to serve status", func() string {
		return queryStatus(h.publicAddr)
	})
	var status struct {
		Version struct {
			Name     string `json:"name"`
			Protocol int    `json:"protocol"`
		} `json:"version"`
		Players struct {
			Max    int `json:"max"`
			Online int `json:"online"`
		} `json:"players"`
		Description string  `json:"description"`
		Favicon     *string `json:"favicon"`
	}
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("status JSON unparseable: %v (raw: %s)", err, statusJSON)
	}
	if status.Description != "☠ Server is sleeping\n§2☻ Join to start it up" {
		t.Errorf("sleeping MOTD wrong: %q", status.Description)
	}
	if status.Version.Name != "1.20.3" || status.Version.Protocol != 765 {
		t.Errorf("version hint wrong: %+v", status.Version)
	}
	if status.Favicon == nil || !strings.HasPrefix(*status.Favicon, "data:image/png;base64,") {
		t.Errorf("favicon missing")
	}
	if status.Players.Max != 0 || status.Players.Online != 0 {
		t.Errorf("players wrong: %+v", status.Players)
	}

	// 2. Login: wakes the server, held client is proxied to the mock server
	conn, err := net.DialTimeout("tcp", h.publicAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := proto.DummyClient()
	w := proto.NewPacketWriter()
	w.WriteVarInt(765)
	w.WriteString("127.0.0.1")
	w.WriteUint16(uint16(mustPort(h.publicAddr)))
	w.WriteVarInt(2) // login
	if !writeRaw(conn, client, proto.PacketHandshake, w.Bytes()) {
		t.Fatal("failed to write handshake")
	}
	lw := proto.NewPacketWriter()
	lw.WriteString("TestUser")
	if !writeRaw(conn, client, proto.PacketServerLoginStart, lw.Bytes()) {
		t.Fatal("failed to write login start")
	}

	// Expect the proxied server to respond: set compression then login
	// success.
	var buf []byte
	gotCompression := false
	gotLoginSuccess := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !(gotCompression && gotLoginSuccess) {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		packet, _, more, err := proto.ReadPacket(client, &buf, conn)
		if err != nil || !more {
			continue
		}
		switch packet.ID {
		case proto.PacketClientSetCompression:
			gotCompression = true
			if sc, err := proto.DecodeSetCompression(packet.Data); err == nil {
				client.SetCompression(sc.Threshold)
			}
		case proto.PacketClientLoginSuccess:
			gotLoginSuccess = true
		}
	}
	if !gotCompression || !gotLoginSuccess {
		t.Fatalf("did not receive proxied login flow (compression=%v success=%v)", gotCompression, gotLoginSuccess)
	}

	// 3. Server process was started
	waitFor(t, 10*time.Second, "server marker", func() string {
		if isRegularFile(h.marker) {
			return "started"
		}
		return ""
	})

	// 4. Mock server saw the proxied login
	var st mockState
	waitFor(t, 10*time.Second, "mock server login", func() string {
		st = readMockState(t, h.stateFile)
		if len(st.Logins) > 0 {
			return "login"
		}
		return ""
	})
	last := st.Logins[len(st.Logins)-1]
	if last.Username != "TestUser" || last.Protocol != 765 {
		t.Errorf("proxied login wrong: %+v", last)
	}

	// 5. Status now relays the real server status
	realStatus := waitFor(t, 10*time.Second, "real server status", func() string {
		s := queryStatus(h.publicAddr)
		if strings.Contains(s, "Mock MOTD") {
			return s
		}
		return ""
	})
	if !strings.Contains(realStatus, `"description":"Mock MOTD"`) {
		t.Errorf("real status not relayed: %s", realStatus)
	}

	// 6. Idle sleep: the server process is stopped
	waitFor(t, 25*time.Second, "server to go back to sleep", func() string {
		s := queryStatus(h.publicAddr)
		if strings.Contains(s, "Server is sleeping") {
			return s
		}
		return ""
	})

	// The mock server (managed by the script) must have shut down
	waitFor(t, 15*time.Second, "mock server shutdown", func() string {
		if readMockState(t, h.stateFile).Shutdown {
			return "shutdown"
		}
		return ""
	})

	// 7. Wake again after sleep
	conn2, err := net.DialTimeout("tcp", h.publicAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	client2 := proto.DummyClient()
	w2 := proto.NewPacketWriter()
	w2.WriteVarInt(765)
	w2.WriteString("127.0.0.1")
	w2.WriteUint16(uint16(mustPort(h.publicAddr)))
	w2.WriteVarInt(2)
	if !writeRaw(conn2, client2, proto.PacketHandshake, w2.Bytes()) {
		t.Fatal("failed to write handshake")
	}
	lw2 := proto.NewPacketWriter()
	lw2.WriteString("SecondUser")
	if !writeRaw(conn2, client2, proto.PacketServerLoginStart, lw2.Bytes()) {
		t.Fatal("failed to write login start")
	}
	var buf2 []byte
	secondLogin := false
	deadline2 := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline2) && !secondLogin {
		conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
		packet, _, more, err := proto.ReadPacket(client2, &buf2, conn2)
		if err != nil || !more {
			continue
		}
		switch packet.ID {
		case proto.PacketClientSetCompression:
			if sc, err := proto.DecodeSetCompression(packet.Data); err == nil {
				client2.SetCompression(sc.Threshold)
			}
		case proto.PacketClientLoginSuccess:
			secondLogin = true
		}
	}
	if !secondLogin {
		t.Fatalf("second wake login did not complete")
	}

	// Cleanup: stop the server process so the mock shuts down and the temp
	// directory can be removed. lazymc's signal service stops the server on
	// SIGINT.
	stopService(serviceDone, h)
}

// stopService stops the managed mock server process so the test binary can
// exit cleanly. lazymc's own goroutines are left running (they go quiet once
// the mock is gone); no signals are sent, since in-process lazymc instances
// share the process signal channel and would os.Exit.
func stopService(serviceDone chan struct{}, h *e2eHarness) {
	_ = exec.Command("pkill", "-9", "-f", h.stateFile).Run()
	select {
	case <-serviceDone:
	default:
		close(serviceDone)
	}
}
