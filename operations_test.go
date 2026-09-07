package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"lazymc/proto"
	"lazymc/proto/packets/play"
)

type forgeTimeoutError struct{}

func (forgeTimeoutError) Error() string   { return "forge read timeout" }
func (forgeTimeoutError) Timeout() bool   { return true }
func (forgeTimeoutError) Temporary() bool { return true }

type forgeDrainConn struct {
	release     chan struct{}
	releaseOnce sync.Once
	started     chan struct{}
	startedOnce sync.Once
	active      atomic.Int32
	deadlines   atomic.Int32
}

func newForgeDrainConn() *forgeDrainConn {
	return &forgeDrainConn{
		release: make(chan struct{}),
		started: make(chan struct{}),
	}
}

func (c *forgeDrainConn) Read([]byte) (int, error) {
	c.active.Add(1)
	c.startedOnce.Do(func() { close(c.started) })
	<-c.release
	c.active.Add(-1)
	return 0, forgeTimeoutError{}
}

func (c *forgeDrainConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *forgeDrainConn) Close() error {
	c.releaseOnce.Do(func() { close(c.release) })
	return nil
}
func (c *forgeDrainConn) LocalAddr() net.Addr              { return forgeAddr("local") }
func (c *forgeDrainConn) RemoteAddr() net.Addr             { return forgeAddr("remote") }
func (c *forgeDrainConn) SetDeadline(time.Time) error      { return nil }
func (c *forgeDrainConn) SetWriteDeadline(time.Time) error { return nil }
func (c *forgeDrainConn) SetReadDeadline(time.Time) error {
	c.deadlines.Add(1)
	c.releaseOnce.Do(func() { close(c.release) })
	return nil
}

type forgeAddr string

func (a forgeAddr) Network() string { return "forge-test" }
func (a forgeAddr) String() string  { return string(a) }

func TestDrainForgeResponsesTimeoutDoesNotLeaveReader(t *testing.T) {
	oldTimeout := clientDrainForgeTimeout
	clientDrainForgeTimeout = 10 * time.Millisecond
	defer func() { clientDrainForgeTimeout = oldTimeout }()

	conn := newForgeDrainConn()
	err := drainForgeResponses(proto.DummyClient(), conn, new([]byte), 1)
	if err != nil {
		t.Fatalf("drainForgeResponses returned error: %v", err)
	}
	if conn.active.Load() != 0 {
		t.Fatalf("Forge response reader remains active after timeout: %d", conn.active.Load())
	}
	if got := conn.deadlines.Load(); got < 2 {
		t.Fatalf("read deadline calls = %d, want at least set and clear", got)
	}
}

func TestDrainForgeResponsesReadsExpectedResponse(t *testing.T) {
	client := proto.DummyClient()
	client.SetState(proto.ClientStateLogin)
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	response, ok := proto.NewRawPacket(proto.PacketServerLoginPluginResponse, nil).EncodeWithLen(client)
	if !ok {
		t.Fatal("failed to encode Forge response")
	}
	done := make(chan struct{})
	go func() {
		_, _ = serverConn.Write(response)
		close(done)
	}()

	var buf []byte
	if err := drainForgeResponses(client, clientConn, &buf, 1); err != nil {
		t.Fatalf("drainForgeResponses returned error: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Forge response writer did not complete")
	}
}

func TestProbeMustProbeRequiresExplicitOptIn(t *testing.T) {
	cfg := Config{
		Server: Server{Forge: true, ProbeOnStart: false},
		Join:   Join{Methods: []Method{MethodLobby}},
	}
	if probeMustProbe(&cfg) {
		t.Fatal("Forge+lobby must not probe or wake the backend when probe_on_start is false")
	}
	cfg.Server.ProbeOnStart = true
	if !probeMustProbe(&cfg) {
		t.Fatal("probe_on_start=true must retain boot probing")
	}
}

func TestModernForgeLobbyCanBootstrapWithoutLegacyProbe(t *testing.T) {
	cfg := Config{Server: Server{Forge: true}}
	server := NewServerState()
	clientInfo := proto.EmptyClientInfo()
	protocol := uint32(play.ProtocolV1_20_1)
	clientInfo.Protocol = &protocol

	if mustStillProbe(&cfg, server, &clientInfo) {
		t.Fatal("protocol 763 must reach the real-client FML3 bootstrap without legacy probe data")
	}

	legacyProtocol := uint32(play.ProtocolV1_17)
	clientInfo.Protocol = &legacyProtocol
	if !mustStillProbe(&cfg, server, &clientInfo) {
		t.Fatal("legacy Forge lobby must still require captured probe data")
	}
}

func TestForgeStatusMagicUsesFML3ForProtocol763(t *testing.T) {
	if got := forgeStatusMagic(play.ProtocolV1_20_1); got != "\x00FML3\x00" {
		t.Fatalf("protocol 763 magic = %q", got)
	}
	if got := forgeStatusMagic(play.ProtocolV1_17); got != "\x00FML2\x00" {
		t.Fatalf("legacy magic = %q", got)
	}
}

func TestForgeHandshakeMarkerClassification(t *testing.T) {
	for marker, want := range map[string]string{
		"example.test\x00FML3\x00": "FML3",
		"example.test\x00FML2\x00": "FML2",
		"example.test":             "absent",
	} {
		info := &proto.ClientInfo{Handshake: &proto.Handshake{ServerAddr: marker}}
		if got := forgeHandshakeMarker(info); got != want {
			t.Fatalf("marker %q classified as %q, want %q", marker, got, want)
		}
	}
}

func TestSignalServiceHandlesSIGTERM(t *testing.T) {
	if os.Getenv("LAZYMC_SIGTERM_HELPER") == "1" {
		if err := os.WriteFile(os.Getenv("LAZYMC_SIGTERM_READY"), nil, 0o600); err != nil {
			os.Exit(2)
		}
		signalService(&Config{}, NewServerState())
		return
	}

	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalServiceHandlesSIGTERM$")
	cmd.Env = append(os.Environ(),
		"LAZYMC_SIGTERM_HELPER=1",
		"LAZYMC_SIGTERM_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("signal helper did not become ready: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("SIGTERM helper did not exit through graceful path: %v", err)
	}
}

func TestControllerManagedHelperExitRetainsStartingState(t *testing.T) {
	if os.Getenv("LAZYMC_CONTROLLER_HELPER") == "1" {
		if err := os.WriteFile(os.Getenv("LAZYMC_CONTROLLER_MARKER"), nil, 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(75)
	}

	config := defaultConfig()
	config.Server.ControllerManaged = true
	config.Server.FreezeProcess = false
	config.Server.Command = os.Args[0] + " -test.run=^TestControllerManagedHelperExitRetainsStartingState$"
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	config.Server.Directory = workingDirectory
	config.Server.StartTimeout = 30
	marker := filepath.Join(t.TempDir(), "controller-helper-ran")

	old := os.Getenv("LAZYMC_CONTROLLER_HELPER")
	oldMarker := os.Getenv("LAZYMC_CONTROLLER_MARKER")
	if err := os.Setenv("LAZYMC_CONTROLLER_HELPER", "1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("LAZYMC_CONTROLLER_MARKER", marker); err != nil {
		t.Fatal(err)
	}
	defer os.Setenv("LAZYMC_CONTROLLER_HELPER", old)
	defer os.Setenv("LAZYMC_CONTROLLER_MARKER", oldMarker)

	server := NewServerState()
	if !server.Start(&config, nil) {
		t.Fatal("controller-managed start was rejected")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		server.pidMu.Lock()
		pid := server.pid
		server.pidMu.Unlock()
		if pid == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("controller helper did not run: %v", err)
	}
	if got := server.GetState(); got != StateStarting {
		t.Fatalf("helper exit changed controller-managed state to %v", got)
	}
	if server.Stop(&config) {
		t.Fatal("controller-managed stop claimed backend ownership")
	}
}

func TestControllerManagedSignalNeverStopsBackend(t *testing.T) {
	if os.Getenv("LAZYMC_CONTROLLER_SIGNAL_HELPER") == "1" {
		config := defaultConfig()
		config.Server.ControllerManaged = true
		server := NewServerState()
		server.updateState(StateStarted, &config)
		if err := os.WriteFile(os.Getenv("LAZYMC_CONTROLLER_SIGNAL_READY"), nil, 0o600); err != nil {
			os.Exit(2)
		}
		signalService(&config, server)
		return
	}

	ready := filepath.Join(t.TempDir(), "controller-signal-ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestControllerManagedSignalNeverStopsBackend$")
	cmd.Env = append(os.Environ(),
		"LAZYMC_CONTROLLER_SIGNAL_HELPER=1",
		"LAZYMC_CONTROLLER_SIGNAL_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("controller signal helper did not become ready: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("controller-managed SIGTERM did not exit cleanly: %v", err)
	}
}

func TestControllerManagedStartTimeoutDoesNotKill(t *testing.T) {
	config := defaultConfig()
	config.Server.ControllerManaged = true
	config.Server.FreezeProcess = false
	config.Server.StartTimeout = 1
	server := NewServerState()
	server.updateState(StateStarting, &config)

	server.killAtMu.Lock()
	past := time.Now().Add(-time.Second)
	server.killAt = &past
	server.killAtMu.Unlock()

	if !server.ExpireControllerManagedTransition(&config) {
		t.Fatal("timed-out controller-managed transition was not expired")
	}
	if got := server.GetState(); got != StateStopped {
		t.Fatalf("timed-out state = %v, want Stopped", got)
	}
}

func TestControllerManagedRetryDoesNotLaunchSecondHelper(t *testing.T) {
	if os.Getenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_HELPER") == "1" {
		marker := os.Getenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_MARKER")
		file, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = file.WriteString("started\n")
		_ = file.Close()
		for {
			if _, err := os.Stat(os.Getenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_RELEASE")); err == nil {
				os.Exit(0)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	config := defaultConfig()
	config.Server.ControllerManaged = true
	config.Server.FreezeProcess = false
	config.Server.StartTimeout = 1
	config.Server.Command = os.Args[0] + " -test.run=^TestControllerManagedRetryDoesNotLaunchSecondHelper$"
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	config.Server.Directory = workingDirectory
	root := t.TempDir()
	marker := filepath.Join(root, "starts")
	release := filepath.Join(root, "release")
	oldHelper := os.Getenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_HELPER")
	oldMarker := os.Getenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_MARKER")
	oldRelease := os.Getenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_RELEASE")
	if err := os.Setenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_HELPER", "1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_MARKER", marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_RELEASE", release); err != nil {
		t.Fatal(err)
	}
	defer os.Setenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_HELPER", oldHelper)
	defer os.Setenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_MARKER", oldMarker)
	defer os.Setenv("LAZYMC_CONTROLLER_SINGLEFLIGHT_RELEASE", oldRelease)
	defer os.WriteFile(release, nil, 0o600)

	server := NewServerState()
	if !server.Start(&config, nil) {
		t.Fatal("initial controller-managed start was rejected")
	}
	waitForControllerTest(t, 5*time.Second, func() bool {
		data, _ := os.ReadFile(marker)
		server.pidMu.Lock()
		pid := server.pid
		server.pidMu.Unlock()
		return strings.Count(string(data), "started\n") == 1 && pid != 0
	})

	server.killAtMu.Lock()
	past := time.Now().Add(-time.Second)
	server.killAt = &past
	server.killAtMu.Unlock()
	if !server.ExpireControllerManagedTransition(&config) {
		t.Fatal("controller-managed timeout was not expired")
	}
	if got := server.GetState(); got != StateStopped {
		t.Fatalf("expired state = %v, want Stopped", got)
	}
	if !server.Start(&config, nil) {
		t.Fatal("controller-managed retry was rejected")
	}
	time.Sleep(50 * time.Millisecond)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "started\n"); got != 1 {
		t.Fatalf("helper starts after timeout+retry = %d, want 1", got)
	}
	if got := server.GetState(); got != StateStarting {
		t.Fatalf("retry state = %v, want Starting", got)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForControllerTest(t, 5*time.Second, func() bool {
		server.helperMu.Lock()
		active := server.helperActive
		server.helperMu.Unlock()
		return !active
	})
	if got := server.GetState(); got != StateStarting {
		t.Fatalf("helper exit changed observed state to %v", got)
	}
}

func TestControllerManagedStaleHelperExitCannotClearNewGeneration(t *testing.T) {
	config := defaultConfig()
	config.Server.ControllerManaged = true
	config.Server.FreezeProcess = false
	server := NewServerState()

	first, launch := server.beginControllerHelper()
	if !launch {
		t.Fatal("first helper generation was not reserved")
	}
	server.pidMu.Lock()
	server.pid = 101
	server.pidGeneration = first
	server.pidMu.Unlock()
	server.finishControllerHelper(first)

	server.updateState(StateStarting, &config)
	second, launch := server.beginControllerHelper()
	if !launch || second == first {
		t.Fatalf("second helper generation = %d, launch=%v; first=%d", second, launch, first)
	}
	server.pidMu.Lock()
	server.pid = 202
	server.pidGeneration = second
	server.pidMu.Unlock()
	server.updateState(StateStarted, &config)

	server.finishControllerHelper(first)
	server.helperMu.Lock()
	active := server.helperActive
	server.helperMu.Unlock()
	server.pidMu.Lock()
	pid := server.pid
	server.pidMu.Unlock()
	if !active || pid != 202 || server.GetState() != StateStarted {
		t.Fatalf("stale helper exit mutated newer state: active=%t pid=%d state=%v", active, pid, server.GetState())
	}

	server.finishControllerHelper(second)
}

func TestControllerManagedForceKillAndUnfreezeAreNoOps(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	config := defaultConfig()
	config.Server.ControllerManaged = true
	config.Server.FreezeProcess = false
	server := NewServerState()
	server.pidMu.Lock()
	server.pid = child.Process.Pid
	server.pidMu.Unlock()
	if server.ForceKill(&config) {
		t.Fatal("controller-managed ForceKill claimed ownership")
	}
	if server.ForceKill(nil) {
		t.Fatal("ForceKill with nil config claimed ownership")
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("ForceKill terminated the helper/backend sentinel: %v", err)
	}

	config.Server.FreezeProcess = true
	if server.Start(&config, nil) {
		t.Fatal("controller-managed Start accepted process freezing")
	}
	if server.unfreezeServerSignal(&config) {
		t.Fatal("controller-managed unfreeze claimed ownership")
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unfreeze affected the helper/backend sentinel: %v", err)
	}
}

func TestControllerManagedHelperExitCodesRetainObservedState(t *testing.T) {
	if raw := os.Getenv("LAZYMC_CONTROLLER_EXIT_CODE"); raw != "" {
		code, err := strconv.Atoi(raw)
		if err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("LAZYMC_CONTROLLER_EXIT_MARKER"), nil, 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(code)
	}

	for _, code := range []int{0, 75, 78} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			config := defaultConfig()
			config.Server.ControllerManaged = true
			config.Server.FreezeProcess = false
			config.Server.Command = os.Args[0] + " -test.run=^TestControllerManagedHelperExitCodesRetainObservedState$"
			workingDirectory, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			config.Server.Directory = workingDirectory
			marker := filepath.Join(t.TempDir(), "helper-exited")
			oldCode := os.Getenv("LAZYMC_CONTROLLER_EXIT_CODE")
			oldMarker := os.Getenv("LAZYMC_CONTROLLER_EXIT_MARKER")
			if err := os.Setenv("LAZYMC_CONTROLLER_EXIT_CODE", strconv.Itoa(code)); err != nil {
				t.Fatal(err)
			}
			if err := os.Setenv("LAZYMC_CONTROLLER_EXIT_MARKER", marker); err != nil {
				t.Fatal(err)
			}
			defer os.Setenv("LAZYMC_CONTROLLER_EXIT_CODE", oldCode)
			defer os.Setenv("LAZYMC_CONTROLLER_EXIT_MARKER", oldMarker)

			server := NewServerState()
			if !server.Start(&config, nil) {
				t.Fatal("controller-managed start was rejected")
			}
			waitForControllerTest(t, 5*time.Second, func() bool {
				_, err := os.Stat(marker)
				server.helperMu.Lock()
				active := server.helperActive
				server.helperMu.Unlock()
				return err == nil && !active
			})
			if got := server.GetState(); got != StateStarting {
				t.Fatalf("helper exit %d changed observed state to %v", code, got)
			}
		})
	}
}

func waitForControllerTest(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("controller test condition did not become true before timeout")
}
