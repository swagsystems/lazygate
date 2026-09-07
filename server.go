package main

// Server state and lifecycle, mirroring lazymc's server.rs.

import (
	"os/exec"
	"sync"
	"time"

	"lazymc/mc"
	"lazymc/proto"
	"lazymc/proto/packets/play"
)

// Server cooldown after the process quit.
//
// Used to give it some more time to quit forgotten threads, such as for RCON.
const serverQuitCooldown = 2500 * time.Millisecond

// RCON cooldown. Required period between RCON invocations.
//
// The Minecraft RCON implementation is very broken and brittle, this is used
// in the hopes to improve reliability.
const rconCooldown = 15 * time.Second

// Exit codes that are allowed.
var allowedExitCodes = map[int]bool{130: true, 143: true}

// State is the server state.
type State int

// Server states.
const (
	StateStopped State = iota
	StateStarting
	StateStarted
	StateStopping
)

func (s State) String() string {
	switch s {
	case StateStopped:
		return "Stopped"
	case StateStarting:
		return "Starting"
	case StateStarted:
		return "Started"
	case StateStopping:
		return "Stopping"
	}
	return "Unknown"
}

// ServerState holds shared server state.
type ServerState struct {
	// Server state.
	stateMu sync.Mutex
	state   State

	// State watch subscribers, broadcast on state changes.
	subsMu sync.Mutex
	subs   []chan State

	// Server process PID. Set if a server process is running.
	pidMu sync.Mutex
	pid   int
	// pidGeneration identifies a controller helper PID. Zero is used for the
	// ordinary backend-owned process path.
	pidGeneration uint64

	// Controller helper single-flight state. A timed-out observation may be
	// retried, but the existing helper must remain the only child until it
	// exits. Generation checks prevent stale helper cleanup from clearing a
	// newer helper's PID or active slot.
	helperMu         sync.Mutex
	helperGeneration uint64
	helperActive     bool

	// Last known server status. Remains set once known, not cleared if
	// server goes offline.
	statusMu sync.RWMutex
	status   *proto.ServerStatus

	// Last active time. Also set at the moment the server comes online.
	lastActiveMu sync.RWMutex
	lastActive   *time.Time

	// Force server to stay online until.
	keepOnlineMu sync.RWMutex
	keepOnline   *time.Time

	// Time to force kill the server process at. Used as starting/stopping
	// timeout.
	killAtMu sync.RWMutex
	killAt   *time.Time

	// List of banned IPs.
	bannedMu sync.RWMutex
	banned   mc.BannedIps

	// Whitelist if enabled.
	whitelistMu sync.RWMutex
	whitelist   *mc.Whitelist

	// Lock for exclusive RCON operations.
	rconLock chan struct{}

	// Last time server was stopped over RCON.
	rconLastStopMu sync.Mutex
	rconLastStop   *time.Time

	// Probed join game data.
	probedMu sync.RWMutex
	probed   *play.JoinGameData

	// Forge payload, sent to clients when they connect to lobby. Recorded
	// from server by probe.
	forgeMu sync.RWMutex
	forge   [][]byte

	// Forge login exchanges recorded while a real FML3 client is held in
	// LOGIN. They are replayed for later lobby clients and backend logins.
	forgeLoginMu    sync.RWMutex
	forgeLoginCache []ForgeLoginExchange
}

// ForgeLoginExchange records one FML3 login-plugin request and its client
// response. Request is the complete LoginPluginRequest payload, including its
// proxy-local client message id; replay uses the cached request for clients
// and the cached response with the current backend message id.
type ForgeLoginExchange struct {
	Request    []byte
	Success    bool
	Data       []byte
	NoResponse bool
}

// NewServer creates a server in stopped state.
func NewServerState() *ServerState {
	s := &ServerState{
		state:           StateStopped,
		pid:             0,
		banned:          mc.NewBannedIps(),
		rconLock:        make(chan struct{}, 1),
		forge:           [][]byte{},
		forgeLoginCache: []ForgeLoginExchange{},
	}
	s.rconLock <- struct{}{}
	return s
}

// beginControllerHelper reserves the single controller-helper slot. The
// returned generation is owned by exactly one child; callers that receive
// launch=false must continue observing the existing child instead of spawning
// another helper.
func (s *ServerState) beginControllerHelper() (uint64, bool) {
	s.helperMu.Lock()
	defer s.helperMu.Unlock()
	if s.helperActive {
		return s.helperGeneration, false
	}
	s.helperGeneration++
	s.helperActive = true
	return s.helperGeneration, true
}

// finishControllerHelper releases only the matching helper generation. A
// stale child exit cannot clear a newer helper's active slot or PID.
func (s *ServerState) finishControllerHelper(generation uint64) {
	s.helperMu.Lock()
	if generation == 0 || generation != s.helperGeneration {
		s.helperMu.Unlock()
		return
	}
	s.helperActive = false
	s.helperMu.Unlock()

	s.pidMu.Lock()
	if s.pidGeneration == generation {
		s.pid = 0
		s.pidGeneration = 0
	}
	s.pidMu.Unlock()
}

// State returns the current state.
func (s *ServerState) GetState() State {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.state
}

// Subscribe registers a state-change subscriber, returning a channel that
// receives the current state and future changes.
func (s *ServerState) Subscribe() chan State {
	ch := make(chan State, 1)
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	s.subs = append(s.subs, ch)
	ch <- s.GetState()
	return ch
}

// broadcastState notifies all subscribers of a state change.
func (s *ServerState) broadcastState(state State) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- state:
		default:
			// Replace stale value with the latest state
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- state:
			default:
			}
		}
	}
}

// updateState sets a new state unconditionally.
func (s *ServerState) updateState(state State, config *Config) bool {
	return s.updateStateFrom(nil, state, config)
}

// updateStateFrom sets a new state, from a current state.
//
// Returns false if the current state didn't match `from` or if nothing
// changed.
func (s *ServerState) updateStateFrom(from *State, new State, config *Config) bool {
	s.stateMu.Lock()
	old := s.state
	if from != nil && old != *from {
		s.stateMu.Unlock()
		return false
	}
	if old == new {
		s.stateMu.Unlock()
		return false
	}
	s.state = new
	s.stateMu.Unlock()

	TraceLog(Target, "Change server state from %v to %v", old, new)

	// Broadcast change
	s.broadcastState(new)

	// Update kill at time for starting/stopping state
	var killAt *time.Time
	switch new {
	case StateStarting:
		if config.Server.StartTimeout > 0 {
			t := time.Now().Add(time.Duration(config.Server.StartTimeout) * time.Second)
			killAt = &t
		}
	case StateStopping:
		if config.Server.StopTimeout > 0 {
			t := time.Now().Add(time.Duration(config.Server.StopTimeout) * time.Second)
			killAt = &t
		}
	}
	s.killAtMu.Lock()
	s.killAt = killAt
	s.killAtMu.Unlock()

	// Online/offline messages
	switch new {
	case StateStarted:
		InfoLog(TargetMonitor, "Server is now online")
	case StateStopped:
		InfoLog(TargetMonitor, "Server is now sleeping")
	}

	// Entering Started, whether from Starting (lazymc started it) or from
	// Stopped (passively detected as already running), update active time
	// and keep it online for the configured time.
	if new == StateStarted {
		s.updateLastActive()
		s.keepOnlineFor(config.Time.MinOnlineTime)
	}

	return true
}

// UpdateStatus updates the status as obtained from the server.
func (s *ServerState) UpdateStatus(config *Config, status *proto.ServerStatus) {
	// Update state based on current
	switch {
	case (s.GetState() == StateStopped || s.GetState() == StateStarting) && status != nil:
		s.updateState(StateStarted, config)
	case s.GetState() == StateStarted && status == nil:
		s.updateState(StateStopped, config)
	}

	// Update last status if known
	if status != nil {
		// Update last active time if there are online players
		if status.Players.Online > 0 {
			s.updateLastActive()
		}

		s.statusMu.Lock()
		s.status = status
		s.statusMu.Unlock()
	}
}

// Start tries to start the server.
//
// Does nothing if currently not in stopped state.
func (s *ServerState) Start(config *Config, username *string) bool {
	if config.Server.ControllerManaged && config.Server.FreezeProcess {
		DebugLog(Target, "Not starting controller-managed server with process freezing enabled")
		return false
	}

	// Must set state from stopped to starting
	stopped := StateStopped
	if !s.updateStateFrom(&stopped, StateStarting, config) {
		return false
	}

	// Log starting message
	if username != nil {
		InfoLog(Target, "Starting server for '%s'...", *username)
	} else {
		InfoLog(Target, "Starting server...")
	}

	// Unfreeze server if it is frozen
	if config.Server.FreezeProcess && s.unfreezeServerSignal(config) {
		return true
	}

	var helperGeneration uint64
	if config.Server.ControllerManaged {
		var launch bool
		helperGeneration, launch = s.beginControllerHelper()
		if !launch {
			DebugLog(Target, "Controller lifecycle helper is already active; observing the existing helper")
			return true
		}
	}

	// Spawn server in new task
	go s.invokeServerCmd(config, helperGeneration)

	return true
}

// invokeServerCmd runs the server command, storing the PID and waiting for it
// to quit.
func (s *ServerState) invokeServerCmd(config *Config, helperGeneration uint64) {
	// Configure command
	parts := shlexSplit(config.Server.Command)
	if len(parts) == 0 {
		ErrorLog(Target, "Failed to start server process through command")
		if config.Server.ControllerManaged {
			s.finishControllerHelper(helperGeneration)
		}
		return
	}

	cmd := exec.Command(parts[0], parts[1:]...)

	// Set working directory
	if dir := ServerDirectory(config); dir != nil {
		cmd.Dir = *dir
	}

	// Spawn process
	child, err := startCommand(cmd)
	if err != nil {
		ErrorLog(Target, "Failed to start server process through command")
		if config.Server.ControllerManaged {
			s.finishControllerHelper(helperGeneration)
		}
		return
	}

	// Remember PID
	s.pidMu.Lock()
	s.pid = child.Process.Pid
	s.pidGeneration = helperGeneration
	s.pidMu.Unlock()

	// Wait for process to exit, handle status
	crashed := false
	err = child.Wait()
	status := child.ProcessState
	if config.Server.ControllerManaged {
		switch {
		case err == nil:
			DebugLog(Target, "Controller lifecycle helper stopped successfully (%v)", status)
		case status != nil:
			WarnLog(Target, "Controller lifecycle helper stopped with error code (%v); retaining socket-observed backend state", status)
		default:
			ErrorLog(Target, "Failed to wait for controller lifecycle helper: %v", err)
		}

		// The helper PID is never the backend PID. Horizon remains the sole
		// lifecycle owner, and the monitor will observe the backend becoming
		// ready, stopped, or timing out.
		s.finishControllerHelper(helperGeneration)
		return
	}
	switch {
	case err == nil:
		DebugLog(Target, "Server process stopped successfully (%v)", status)
	case exitCodeAllowed(status):
		DebugLog(Target, "Server process stopped successfully by SIGTERM (%v)", status)
	case status != nil:
		WarnLog(Target, "Server process stopped with error code (%v)", status)
		crashed = s.GetState() == StateStarted
	default:
		ErrorLog(Target, "Failed to wait for server process to quit: %v", err)
		ErrorLog(Target, "Assuming server quit, cleaning up...")
	}

	// Forget server PID
	s.pidMu.Lock()
	s.pid = 0
	s.pidMu.Unlock()

	// Give server a little more time to quit forgotten threads
	time.Sleep(serverQuitCooldown)

	// Set server state to stopped
	s.updateState(StateStopped, config)

	// Restart on crash
	if crashed && config.Server.WakeOnCrash {
		WarnLog(Target, "Server crashed, restarting...")
		s.Start(config, nil)
	}
}

// exitCodeAllowed checks whether the process exited with an allowed code.
func exitCodeAllowed(state *osProcessState) bool {
	if state == nil {
		return false
	}
	return allowedExitCodes[state.ExitCode()]
}

// Stop stops the running server.
//
// This will attempt to stop the server with all available methods.
func (s *ServerState) Stop(config *Config) bool {
	if config.Server.ControllerManaged {
		DebugLog(Target, "Not stopping server because lifecycle is controller-managed")
		return false
	}

	// Try to freeze through signal
	if config.Server.FreezeProcess && s.freezeServerSignal(config) {
		return true
	}

	// Try to stop through RCON if started
	if s.GetState() == StateStarted && s.stopServerRcon(config) {
		return true
	}

	// Try to stop through signal
	if s.stopServerSignal(config) {
		return true
	}

	WarnLog(Target, "Failed to stop server, no more suitable stopping method to use")
	return false
}

// ForceKill force kills the running server.
func (s *ServerState) ForceKill(config *Config) bool {
	if config == nil {
		DebugLog(Target, "Not force killing server without lifecycle configuration")
		return false
	}
	if config.Server.ControllerManaged {
		DebugLog(Target, "Not force killing server because lifecycle is controller-managed")
		return false
	}
	s.pidMu.Lock()
	pid := s.pid
	s.pidMu.Unlock()
	if pid != 0 {
		return ForceKill(pid)
	}
	return false
}

// ShouldSleep decides whether the server should sleep.
//
// Always returns false if it is currently not online.
func (s *ServerState) ShouldSleep(config *Config) bool {
	if config.Server.ControllerManaged {
		return false
	}

	// Server must be online
	if s.GetState() != StateStarted {
		return false
	}

	// Never sleep if players are online
	s.statusMu.RLock()
	playersOnline := false
	if s.status != nil && s.status.Players.Online > 0 {
		playersOnline = true
	}
	s.statusMu.RUnlock()
	if playersOnline {
		TraceLog(Target, "Not sleeping because players are online")
		return false
	}

	// Don't sleep when keep online until isn't expired
	s.keepOnlineMu.RLock()
	keepOnline := s.keepOnline != nil && !s.keepOnline.Before(time.Now())
	s.keepOnlineMu.RUnlock()
	if keepOnline {
		TraceLog(Target, "Not sleeping because of keep online")
		return false
	}

	// Last active time must have passed sleep threshold
	s.lastActiveMu.RLock()
	lastActive := s.lastActive
	s.lastActiveMu.RUnlock()
	if lastActive != nil {
		return time.Since(*lastActive) >= time.Duration(config.Time.SleepAfter)*time.Second
	}

	return false
}

// ShouldKill decides whether to force kill the server process.
func (s *ServerState) ShouldKill() bool {
	s.killAtMu.RLock()
	defer s.killAtMu.RUnlock()
	return s.killAt != nil && !s.killAt.After(time.Now())
}

// ExpireControllerManagedTransition fails a timed-out observed transition
// closed without ever signalling a helper or backend process.
func (s *ServerState) ExpireControllerManagedTransition(config *Config) bool {
	if !config.Server.ControllerManaged || !s.ShouldKill() {
		return false
	}
	starting := StateStarting
	return s.updateStateFrom(&starting, StateStopped, config)
}

// Status returns the last known server status.
func (s *ServerState) Status() *proto.ServerStatus {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status
}

// SeedStatus installs status metadata without changing lifecycle state. It is
// used at process start so a sleeping Forge server remains discoverable as
// modded while Horizon is still the sole lifecycle owner.
func (s *ServerState) SeedStatus(status *proto.ServerStatus) {
	if status == nil {
		return
	}
	s.statusMu.Lock()
	if s.status == nil {
		s.status = status
	}
	s.statusMu.Unlock()
}

// updateLastActive updates the last active time.
func (s *ServerState) updateLastActive() {
	t := time.Now()
	s.lastActiveMu.Lock()
	s.lastActive = &t
	s.lastActiveMu.Unlock()
}

// keepOnlineFor forces the server to be online for the given number of
// seconds.
func (s *ServerState) keepOnlineFor(duration uint32) {
	if duration > 0 {
		t := time.Now().Add(time.Duration(duration) * time.Second)
		s.keepOnlineMu.Lock()
		s.keepOnline = &t
		s.keepOnlineMu.Unlock()
	} else {
		s.keepOnlineMu.Lock()
		s.keepOnline = nil
		s.keepOnlineMu.Unlock()
	}
}

// IsBannedIP checks whether the given IP is banned.
func (s *ServerState) IsBannedIP(ip string) bool {
	s.bannedMu.RLock()
	defer s.bannedMu.RUnlock()
	return s.banned.IsBanned(parseIP(ip))
}

// BanEntry returns the ban entry for the given IP.
func (s *ServerState) BanEntry(ip string) (mc.BannedIp, bool) {
	s.bannedMu.RLock()
	defer s.bannedMu.RUnlock()
	return s.banned.Get(parseIP(ip))
}

// IsWhitelisted checks whether the given username is whitelisted.
//
// Returns true if no whitelist is currently used.
func (s *ServerState) IsWhitelisted(username string) bool {
	s.whitelistMu.RLock()
	defer s.whitelistMu.RUnlock()
	if s.whitelist == nil {
		return true
	}
	return s.whitelist.IsWhitelisted(username)
}

// SetBannedIPs updates the list of banned IPs.
func (s *ServerState) SetBannedIPs(ips mc.BannedIps) {
	s.bannedMu.Lock()
	s.banned = ips
	s.bannedMu.Unlock()
}

// SetWhitelist updates the whitelist.
func (s *ServerState) SetWhitelist(whitelist *mc.Whitelist) {
	s.whitelistMu.Lock()
	s.whitelist = whitelist
	s.whitelistMu.Unlock()
}

// ProbedJoinGame returns the probed join game data.
func (s *ServerState) ProbedJoinGame() *play.JoinGameData {
	s.probedMu.RLock()
	defer s.probedMu.RUnlock()
	return s.probed
}

// SetProbedJoinGame stores probed join game data.
func (s *ServerState) SetProbedJoinGame(data *play.JoinGameData) {
	s.probedMu.Lock()
	s.probed = data
	s.probedMu.Unlock()
}

// ForgePayload returns the recorded Forge login payload.
func (s *ServerState) ForgePayload() [][]byte {
	s.forgeMu.RLock()
	defer s.forgeMu.RUnlock()
	return s.forge
}

// SetForgePayload stores the recorded Forge login payload.
func (s *ServerState) SetForgePayload(payload [][]byte) {
	s.forgeMu.Lock()
	s.forge = payload
	s.forgeMu.Unlock()
}

// ForgeLoginCache returns a deep copy of the recorded FML3 exchanges.
func (s *ServerState) ForgeLoginCache() []ForgeLoginExchange {
	s.forgeLoginMu.RLock()
	defer s.forgeLoginMu.RUnlock()
	cache := make([]ForgeLoginExchange, len(s.forgeLoginCache))
	for i, exchange := range s.forgeLoginCache {
		cache[i] = ForgeLoginExchange{
			Request:    append([]byte(nil), exchange.Request...),
			Success:    exchange.Success,
			Data:       append([]byte(nil), exchange.Data...),
			NoResponse: exchange.NoResponse,
		}
	}
	return cache
}

// SetForgeLoginCache replaces the FML3 exchange cache with a deep copy.
func (s *ServerState) SetForgeLoginCache(cache []ForgeLoginExchange) {
	s.forgeLoginMu.Lock()
	s.forgeLoginCache = make([]ForgeLoginExchange, len(cache))
	for i, exchange := range cache {
		s.forgeLoginCache[i] = ForgeLoginExchange{
			Request:    append([]byte(nil), exchange.Request...),
			Success:    exchange.Success,
			Data:       append([]byte(nil), exchange.Data...),
			NoResponse: exchange.NoResponse,
		}
	}
	s.forgeLoginMu.Unlock()
}

// stopServerRcon stops the server through RCON.
func (s *ServerState) stopServerRcon(config *Config) bool {
	// RCON must be enabled
	if !config.Rcon.Enabled {
		TraceLog(Target, "Not using RCON to stop server, disabled in config")
		return false
	}

	// Grab RCON lock
	<-s.rconLock
	defer func() { s.rconLock <- struct{}{} }()

	// Ensure RCON has cooled down
	s.rconLastStopMu.Lock()
	cooledDown := s.rconLastStop == nil || time.Since(*s.rconLastStop) >= rconCooldown
	s.rconLastStopMu.Unlock()
	if !cooledDown {
		DebugLog(Target, "Not using RCON to stop server, in cooldown, used too recently")
		return false
	}

	// Create RCON client
	rcon, err := mc.ConnectRconConfig(config.Rcon.SendProxyV2, config.Server.Address.IP, uint16(config.Server.Address.Port), config.Rcon.Port, config.Rcon.Password)
	if err != nil {
		ErrorLog(Target, "Failed to RCON server to sleep: %v", err)
		return false
	}

	// Invoke stop
	if _, err := rcon.Cmd("stop"); err != nil {
		ErrorLog(Target, "Failed to invoke stop through RCON: %v", err)
		rcon.Close()
		return false
	}

	// Set server to stopping state, update last RCON time
	t := time.Now()
	s.rconLastStopMu.Lock()
	s.rconLastStop = &t
	s.rconLastStopMu.Unlock()
	s.updateState(StateStopping, config)

	// Gracefully close connection
	rcon.Close()

	return true
}

// stopServerSignal stops the server by sending SIGTERM.
func (s *ServerState) stopServerSignal(config *Config) bool {
	// Grab PID
	s.pidMu.Lock()
	pid := s.pid
	s.pidMu.Unlock()
	if pid == 0 {
		DebugLog(Target, "Could not send stop signal to server process, PID unknown")
		return false
	}

	if !KillGracefully(pid) {
		ErrorLog(Target, "Failed to send stop signal to server process")
		return false
	}

	starting := StateStarting
	s.updateStateFrom(&starting, StateStopping, config)
	started := StateStarted
	s.updateStateFrom(&started, StateStopping, config)

	return true
}

// freezeServerSignal freezes the server by sending SIGSTOP.
func (s *ServerState) freezeServerSignal(config *Config) bool {
	// Grab PID
	s.pidMu.Lock()
	pid := s.pid
	s.pidMu.Unlock()
	if pid == 0 {
		DebugLog(Target, "Could not send freeze signal to server process, PID unknown")
		return false
	}

	if !Freeze(pid) {
		ErrorLog(Target, "Failed to send freeze signal to server process.")
	}

	starting := StateStarting
	s.updateStateFrom(&starting, StateStopped, config)
	started := StateStarted
	s.updateStateFrom(&started, StateStopped, config)

	return true
}

// unfreezeServerSignal unfreezes the server by sending SIGCONT.
func (s *ServerState) unfreezeServerSignal(config *Config) bool {
	if config.Server.ControllerManaged {
		DebugLog(Target, "Not unfreezing server because lifecycle is controller-managed")
		return false
	}

	// Grab PID
	s.pidMu.Lock()
	pid := s.pid
	s.pidMu.Unlock()
	if pid == 0 {
		DebugLog(Target, "Could not send unfreeze signal to server process, PID unknown")
		return false
	}

	if !Unfreeze(pid) {
		ErrorLog(Target, "Failed to send unfreeze signal to server process.")
	}

	stopping := StateStopping
	s.updateStateFrom(&stopping, StateStarting, config)
	stopped := StateStopped
	s.updateStateFrom(&stopped, StateStarting, config)

	return true
}
