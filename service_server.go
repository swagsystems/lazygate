package main

// Main service entrypoint, mirroring lazymc's service/server.rs.

import (
	"errors"
	"fmt"
	"net"
	"sync/atomic"

	"lazymc/proto"
	"lazymc/util"
)

const (
	maxStatusClients = 128
	maxProxyClients  = 256
)

// connectionAdmission bounds unauthenticated public-edge work. Status
// admission is held while Serve owns a connection; proxy admission is held
// for the lifetime of the proxy task.
type connectionAdmission struct {
	status chan struct{}
	proxy  chan struct{}
}

var activeAdmission atomic.Pointer[connectionAdmission]

func newConnectionAdmission() *connectionAdmission {
	return &connectionAdmission{
		status: make(chan struct{}, maxStatusClients),
		proxy:  make(chan struct{}, maxProxyClients),
	}
}

func (a *connectionAdmission) acquireStatus() bool {
	select {
	case a.status <- struct{}{}:
		return true
	default:
		return false
	}
}

func (a *connectionAdmission) releaseStatus() {
	select {
	case <-a.status:
	default:
	}
}

func (a *connectionAdmission) acquireProxy() bool {
	select {
	case a.proxy <- struct{}{}:
		return true
	default:
		return false
	}
}

func (a *connectionAdmission) releaseProxy() {
	select {
	case <-a.proxy:
	default:
	}
}

func admissionFor(config *Config) *connectionAdmission {
	if config != nil && config.admission != nil {
		return config.admission
	}
	return activeAdmission.Load()
}

// Service starts lazymc.
//
// Main entrypoint to start all server/status/proxy logic.
func Service(config *Config) error {
	if err := validateControllerManagedConfig(config); err != nil {
		return err
	}
	if err := validateOnlineModeConfig(config); err != nil {
		return err
	}

	// Load server state
	server := NewServerState()
	if config.Server.ForgeStatusFile != "" {
		snapshot, err := loadForgeStatusSnapshot(config)
		if err != nil {
			return fmt.Errorf("load Forge status snapshot: %w", err)
		}
		if snapshot != nil {
			server.SeedStatus(snapshot)
		}
	}
	config.admission = newConnectionAdmission()
	activeAdmission.Store(config.admission)

	// Listen for new connections
	listener, err := net.Listen("tcp", config.Public.Address.String())
	if err != nil {
		util.QuitError(fmt.Errorf("Failed to start proxy server: %w", err), util.DefaultErrorHints())
	}

	InfoLog(Target, "Proxying public %s to server %s", config.Public.Address, config.Server.Address)

	if config.Lockout.Enabled {
		WarnLog(Target, "Lockout mode is enabled, nobody will be able to connect through the proxy")
	}

	// Spawn services: monitor, signal handler
	go monitorService(config, server)
	go signalService(config, server)

	// Initiate server start
	if config.Server.WakeOnStart {
		server.Start(config, nil)
	}

	// Spawn additional services: probe and ban manager
	go probeService(config, server)
	go fileWatcherService(config, server)

	// Route all incoming connections
	for {
		inbound, err := listener.Accept()
		if err != nil {
			WarnLog(Target, "Failed to accept connection: %v", err)
			continue
		}
		route(inbound, config, server)
	}
}

func validateControllerManagedConfig(config *Config) error {
	if !config.Server.ControllerManaged {
		return nil
	}
	if config.Server.FreezeProcess {
		return errors.New("controller-managed lifecycle is incompatible with process freezing")
	}
	if config.Server.WakeOnCrash {
		return errors.New("controller-managed lifecycle is incompatible with gateway crash restarts")
	}
	if config.Rcon.Enabled {
		return errors.New("controller-managed lifecycle is incompatible with gateway RCON ownership")
	}
	if config.Advanced.RewriteServerProperties {
		return errors.New("controller-managed lifecycle is incompatible with gateway server.properties rewrites")
	}
	return nil
}

func validateOnlineModeConfig(config *Config) error {
	if !config.Auth.OnlineMode {
		return nil
	}
	if !config.Server.Address.IP.IsLoopback() {
		return errors.New("online lobby mode requires a loopback-only backend address")
	}
	if len(config.Join.Methods) == 0 || config.Join.Methods[0] != MethodLobby {
		return errors.New("online lobby mode requires lobby as the first join method")
	}
	if _, err := forwardingSecret(config); err != nil {
		return fmt.Errorf("online lobby forwarding secret: %w", err)
	}
	return nil
}

// route routes an inbound TCP stream to the correct service, spawning a new
// task.
func route(inbound net.Conn, config *Config, server *ServerState) {
	// Get user peer address
	peer := inbound.RemoteAddr()
	if peer == nil {
		WarnLog(Target, "Connection from unknown peer address, disconnecting")
		inbound.Close()
		return
	}

	// Check ban state, just drop connection if enabled
	peerIP := peerIP(peer)
	banned := server.IsBannedIP(peerIP)
	if banned && config.Server.DropBannedIps {
		InfoLog(Target, "Connection from banned IP %s, dropping", peerIP)
		inbound.Close()
		return
	}

	// Route connection through proper channel
	shouldProxy := shouldRouteDirectProxy(config, server, banned)
	if shouldProxy {
		routeProxy(inbound, config)
	} else {
		routeStatus(inbound, config, server, peer)
	}
}

// shouldRouteDirectProxy returns whether a connection may bypass the lobby.
// Online lobby mode terminates client authentication here and forwards the
// verified profile to an offline, loopback-only backend. Such clients must
// always pass through Serve/LobbyServe, even while the backend is already up.
func shouldRouteDirectProxy(config *Config, server *ServerState, banned bool) bool {
	return !banned && server.GetState() == StateStarted && !config.Lockout.Enabled && !config.Auth.OnlineMode
}

// routeStatus routes an inbound TCP stream to the status server.
func routeStatus(inbound net.Conn, config *Config, server *ServerState, peer net.Addr) {
	admission := admissionFor(config)
	if admission != nil && !admission.acquireStatus() {
		WarnLog(Target, "Status admission limit reached, closing connection")
		_ = CloseTCPStream(inbound)
		return
	}

	// When server is not online, spawn a status server
	client := proto.NewClient(peer)
	go func() {
		if admission != nil {
			defer admission.releaseStatus()
		}
		if err := Serve(client, inbound, config, server); err != nil {
			WarnLog(Target, "Failed to serve status: %v", err)
		}
	}()
}

// routeProxy routes an inbound TCP stream to the proxy.
func routeProxy(inbound net.Conn, config *Config) {
	admission := admissionFor(config)
	if admission != nil && !admission.acquireProxy() {
		WarnLog(Target, "Proxy admission limit reached, closing connection")
		_ = CloseTCPStream(inbound)
		return
	}

	// When server is online, proxy all
	go func() {
		if admission != nil {
			defer admission.releaseProxy()
		}
		defer CloseTCPStream(inbound)
		if err := Proxy(inbound, ProxyHeaderProxy.NotNone(config.Server.SendProxyV2), config.Server.Address.String()); err != nil {
			WarnLog(Target, "Failed to proxy: %v", err)
		}
	}()
}

// routeProxyQueue routes an inbound TCP stream to the proxy with queued
// data.
func routeProxyQueue(inbound net.Conn, config *Config, queue []byte) {
	routeProxyAddressQueue(
		inbound,
		ProxyHeaderProxy.NotNone(config.Server.SendProxyV2),
		config.Server.Address.String(),
		queue,
	)
}

// routeProxyAddressQueue routes an inbound TCP stream to the proxy with the
// given address and queued data.
func routeProxyAddressQueue(inbound net.Conn, proxyHeader ProxyHeader, addr string, queue []byte) {
	admission := admissionFor(nil)
	if admission != nil && !admission.acquireProxy() {
		WarnLog(Target, "Proxy admission limit reached, closing queued connection")
		_ = CloseTCPStream(inbound)
		return
	}

	// When server is online, proxy all
	go func() {
		if admission != nil {
			defer admission.releaseProxy()
		}
		defer CloseTCPStream(inbound)
		if err := ProxyWithQueue(inbound, proxyHeader, addr, queue); err != nil {
			WarnLog(Target, "Failed to proxy: %v", err)
		}
	}()
}
