package main

// Join hold method, mirroring lazymc's join/hold.rs.

import (
	"net"
	"time"
)

// holdOccupy holds the client.
func holdOccupy(config *Config, server *ServerState, inbound net.Conn, inboundHistory *[]byte) (methodResult, error) {
	TraceLog(Target, "Using hold method to occupy joining client")

	// Server must be starting
	if server.GetState() != StateStarting {
		return methodResult{consumed: false, stream: inbound}, nil
	}

	// Start holding, consume client
	if holdClient(config, server) {
		routeProxyQueue(inbound, config, append([]byte(nil), *inboundHistory...))
		return methodResult{consumed: true, stream: inbound}, nil
	}

	return methodResult{consumed: false, stream: inbound}, nil
}

// holdClient holds a client while the server starts.
//
// Returns holding status. true if the client is held and it should be
// proxied, false if it was held but it timed out.
func holdClient(config *Config, server *ServerState) bool {
	TraceLog(Target, "Started holding client")

	// A task to wait for suitable server state
	done := make(chan bool, 1)
	go func() {
		stateCh := server.Subscribe()
		for state := range stateCh {
			switch state {
			// Still waiting on server start
			case StateStarting:
				TraceLog(Target, "Server not ready, holding client for longer")
				continue

			// Server started, start relaying and proxy
			case StateStarted:
				done <- true
				return

			// Server stopping, this shouldn't happen, kick
			case StateStopping:
				WarnLog(Target, "Server stopping for held client, disconnecting")
				done <- false
				return

			// Server stopped, this shouldn't happen, disconnect
			case StateStopped:
				ErrorLog(Target, "Server stopped for held client, disconnecting")
				done <- false
				return
			}
		}
		done <- false
	}()

	// Wait for server state with timeout
	timeout := time.Duration(config.Join.Hold.Timeout) * time.Second
	select {
	case ok := <-done:
		if ok {
			InfoLog(Target, "Server ready for held client, relaying to server")
			return true
		}
		WarnLog(Target, "Server stopping for held client")
		return false
	case <-time.After(timeout):
		WarnLog(Target, "Held client reached timeout of %ds", config.Join.Hold.Timeout)
		return false
	}
}
