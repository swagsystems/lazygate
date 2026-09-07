package main

// Service tasks, mirroring lazymc's service module.

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"lazymc/mc"
)

// monitorService is the server monitor task.
func monitorService(config *Config, state *ServerState) {
	MonitorServer(config, state)
}

// probeService probes the server.
func probeService(config *Config, state *ServerState) {
	// Probing at gateway boot is opt-in. Forge+lobby can probe lazily when a
	// client actually joins, so it must not wake the backend here.
	if !probeMustProbe(config) {
		return
	}

	// Probe
	if err := Probe(config, state); err != nil {
		ErrorLog(TargetProbe, "Failed to probe server, this may limit lazymc features")
	} else {
		InfoLog(TargetProbe, "Succesfully probed server")
	}
}

// probeMustProbe checks whether boot probing was explicitly enabled.
func probeMustProbe(config *Config) bool {
	return config.Server.ProbeOnStart
}

// signalService handles SIGINT and SIGTERM, stopping the server or quitting.
func signalService(config *Config, server *ServerState) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	for range sigCh {
		// An external controller owns the backend lifecycle. Stopping this
		// gateway must never be translated into a backend stop request.
		if config.Server.ControllerManaged {
			quit()
		}

		// Quit if stopped
		if server.GetState() == StateStopped {
			quit()
		}

		// Try to stop server
		stopping := server.Stop(config)

		// If not stopping, maybe due to failure, just quit
		if !stopping {
			quit()
		}
	}
}

// quit gracefully quits.
func quit() {
	// TODO: gracefully quit self
	os.Exit(0)
}

// File watcher debounce time.
const watchDebounce = 2 * time.Second

// fileWatcherService watches server file changes to reload the whitelist and
// banned IPs.
func fileWatcherService(config *Config, server *ServerState) {
	// Ensure server directory is set, it must exist
	dir := ServerDirectory(config)
	if dir == nil || !isDir(*dir) {
		WarnLog(Target, "Server directory doesn't exist, can't watch file changes to reload whitelist and banned IPs")
		return
	}

	for {
		// Update all files once
		reloadBans(config, server, filepath.Join(*dir, mc.BanFile))
		reloadWhitelist(config, server, *dir)

		// Watch for changes, update accordingly
		if !watchServer(config, server, *dir) {
			return
		}
	}
}

// watchServer watches the server directory, returning true to watch again.
func watchServer(config *Config, server *ServerState, dir string) bool {
	// Directory must exist
	if !isDir(dir) {
		ErrorLog(Target, "Server directory does not exist at %s anymore, not watching changes", dir)
		return false
	}

	banPath := filepath.Join(dir, mc.BanFile)
	whitelistPath := filepath.Join(dir, mc.WhitelistFile)
	opsPath := filepath.Join(dir, mc.OpsFile)
	propsPath := filepath.Join(dir, mc.ServerPropertiesFile)

	// Record initial states so the first poll does not trigger reloads
	states := map[string]fileState{}
	states[banPath] = currentFileState(banPath)
	states[whitelistPath] = currentFileState(whitelistPath)
	states[opsPath] = currentFileState(opsPath)
	states[propsPath] = currentFileState(propsPath)

	check := func(path string) {
		cur := currentFileState(path)
		if cur != states[path] {
			states[path] = cur
			updateFiles(config, server, dir, path)
		}
	}

	for {
		time.Sleep(500 * time.Millisecond)
		check(banPath)
		check(whitelistPath)
		check(opsPath)
		check(propsPath)
	}
}

// fileState tracks a watched file's mtime/size/existence.
type fileState struct {
	exists bool
	mtime  time.Time
	size   int64
}

// currentFileState snapshots the current state of a file.
func currentFileState(path string) fileState {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return fileState{exists: false}
	}
	return fileState{exists: true, mtime: fi.ModTime(), size: fi.Size()}
}

// updateFiles processes a file change on the given path.
func updateFiles(config *Config, server *ServerState, dir, path string) {
	// Update bans
	if filepath.Base(path) == mc.BanFile {
		reloadBans(config, server, path)
	}

	// Update whitelist
	base := filepath.Base(path)
	if base == mc.WhitelistFile || base == mc.OpsFile || base == mc.ServerPropertiesFile {
		reloadWhitelist(config, server, dir)
	}
}

// reloadBans reloads banned IPs.
func reloadBans(config *Config, server *ServerState, path string) {
	// Bans must be enabled
	if !config.Server.BlockBannedIps && !config.Server.DropBannedIps {
		return
	}

	TraceLog(Target, "Reloading banned IPs...")

	// File must exist, clear file otherwise
	if !isRegularFile(path) {
		DebugLog(Target, "No banned IPs, %s does not exist", mc.BanFile)
		server.SetBannedIPs(mc.NewBannedIps())
		return
	}

	// Load and update banned IPs
	ips, err := mc.LoadBannedIps(path)
	if err != nil {
		DebugLog(Target, "Failed load banned IPs from %s, ignoring: %v", mc.BanFile, err)
	} else {
		server.SetBannedIPs(ips)
	}

	// Show warning if 127.0.0.1 is banned
	if server.IsBannedIP("127.0.0.1") {
		WarnLog(Target, "Local address 127.0.0.1 IP banned, probably not what you want")
		WarnLog(Target, "Use '/pardon-ip 127.0.0.1' on the server to unban")
	}
}

// reloadWhitelist reloads whitelisted users.
func reloadWhitelist(config *Config, server *ServerState, dir string) {
	// Whitelist must be enabled
	if !config.Server.WakeWhitelist {
		return
	}

	// Must be enabled in server.properties
	enabled := false
	if v := mc.ReadServerProperties(filepath.Join(dir, mc.ServerPropertiesFile), "white-list"); v != nil {
		enabled = *v == "true"
	}
	if !enabled {
		server.SetWhitelist(nil)
		DebugLog(Target, "Not using whitelist, not enabled in %s", mc.ServerPropertiesFile)
		return
	}

	TraceLog(Target, "Reloading whitelisted users...")

	// Load and update whitelisted users
	whitelist, err := mc.LoadWhitelistDir(dir)
	if err != nil {
		DebugLog(Target, "Failed load whitelist from %s, ignoring: %v", dir, err)
		return
	}
	server.SetWhitelist(&whitelist)
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
