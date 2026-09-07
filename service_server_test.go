package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateOnlineModeConfigFailsClosed(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "forwarding.secret")
	if err := os.WriteFile(secretPath, []byte("a-long-enough-forwarding-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := defaultConfig()
	config.Auth.OnlineMode = true
	config.Auth.ForwardingSecretFile = secretPath
	config.Join.Methods = []Method{MethodLobby, MethodKick}
	if err := validateOnlineModeConfig(&config); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	config.Server.Address.IP = net.ParseIP("192.0.2.10")
	if err := validateOnlineModeConfig(&config); err == nil {
		t.Fatal("non-loopback backend accepted")
	}
	config.Server.Address.IP = net.ParseIP("127.0.0.1")
	config.Join.Methods = []Method{MethodKick, MethodLobby}
	if err := validateOnlineModeConfig(&config); err == nil {
		t.Fatal("non-lobby first join method accepted")
	}
}

func TestValidateControllerManagedConfigFailsClosed(t *testing.T) {
	config := defaultConfig()
	config.Server.ControllerManaged = true
	config.Server.FreezeProcess = false
	config.Server.WakeOnCrash = false
	config.Rcon.Enabled = false
	config.Advanced.RewriteServerProperties = false
	if err := validateControllerManagedConfig(&config); err != nil {
		t.Fatalf("valid controller-managed config rejected: %v", err)
	}

	checks := []struct {
		name string
		set  func(*Config)
	}{
		{"freeze", func(c *Config) { c.Server.FreezeProcess = true }},
		{"wake-on-crash", func(c *Config) { c.Server.WakeOnCrash = true }},
		{"rcon", func(c *Config) { c.Rcon.Enabled = true }},
		{"rewrite", func(c *Config) { c.Advanced.RewriteServerProperties = true }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			candidate := config
			check.set(&candidate)
			if err := validateControllerManagedConfig(&candidate); err == nil {
				t.Fatal("incompatible controller-managed config accepted")
			}
		})
	}
}

func TestConnectionAdmissionBoundsStatusAndProxyWork(t *testing.T) {
	admission := newConnectionAdmission()

	for i := 0; i < maxStatusClients; i++ {
		if !admission.acquireStatus() {
			t.Fatalf("status acquire %d failed before limit", i)
		}
	}
	if admission.acquireStatus() {
		t.Fatal("status admission exceeded configured limit")
	}
	admission.releaseStatus()
	if !admission.acquireStatus() {
		t.Fatal("status admission did not recover after release")
	}

	for i := 0; i < maxProxyClients; i++ {
		if !admission.acquireProxy() {
			t.Fatalf("proxy acquire %d failed before limit", i)
		}
	}
	if admission.acquireProxy() {
		t.Fatal("proxy admission exceeded configured limit")
	}
	admission.releaseProxy()
	if !admission.acquireProxy() {
		t.Fatal("proxy admission did not recover after release")
	}
}

func TestOnlineModeNeverBypassesAuthenticationWhenBackendStarted(t *testing.T) {
	server := NewServerState()
	stateConfig := defaultConfig()
	server.updateState(StateStarted, &stateConfig)

	config := &Config{Auth: Auth{OnlineMode: true}}
	if shouldRouteDirectProxy(config, server, false) {
		t.Fatal("online-mode connection bypassed lobby authentication")
	}

	config.Auth.OnlineMode = false
	if !shouldRouteDirectProxy(config, server, false) {
		t.Fatal("legacy direct proxy was not selected for a started backend")
	}
}

func TestRouteProxyRejectsFullAdmissionAndClosesInbound(t *testing.T) {
	admission := newConnectionAdmission()
	for i := 0; i < maxProxyClients; i++ {
		if !admission.acquireProxy() {
			t.Fatal("failed to fill proxy admission")
		}
	}

	peer, inbound := net.Pipe()
	defer peer.Close()
	config := &Config{admission: admission}
	routeProxy(inbound, config)

	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := peer.Read(one[:]); err == nil {
		t.Fatal("rejected inbound connection remained open")
	}
}

func TestRouteProxyReleasesAdmissionAndClosesDialFailure(t *testing.T) {
	admission := newConnectionAdmission()
	config := &Config{
		admission: admission,
		Server:    Server{Address: SocketAddr{IP: net.ParseIP("127.0.0.1"), Port: unusedPort(t)}},
	}
	peer, inbound := net.Pipe()
	defer peer.Close()

	routeProxy(inbound, config)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if admission.acquireProxy() {
			admission.releaseProxy()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("proxy admission was not released after dial failure")
}

func unusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
