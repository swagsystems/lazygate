package main

// Join lobby method, mirroring lazymc's join/lobby.rs.

import (
	"net"

	"lazymc/proto"
)

// lobbyOccupy keeps the client in the lobby.
func lobbyOccupy(client *proto.Client, clientInfo proto.ClientInfo, config *Config, server *ServerState, inbound net.Conn, inboundQueue []byte) (methodResult, error) {
	TraceLog(Target, "Using lobby method to occupy joining client")

	// Must be ready to lobby
	if mustStillProbe(config, server, &clientInfo) {
		WarnLog(Target, "Client connected but lobby is not ready, using next join method, probing not completed")
		return methodResult{consumed: false, stream: inbound}, nil
	}

	// Start lobby
	_ = LobbyServe(client, clientInfo, inbound, config, server, inboundQueue)

	// TODO: do not consume client here, allow other join method on fail

	return methodResult{consumed: true, stream: inbound}, nil
}

// mustStillProbe checks whether we still have to probe before we can use the
// lobby.

func mustStillProbe(config *Config, server *ServerState, clientInfo *proto.ClientInfo) bool {
	// Modern Forge is bootstrapped from the first real authenticated client and
	// then replayed from the captured FML3 exchange cache. Requiring the legacy
	// synthetic probe here prevents that first client from ever reaching the
	// bootstrap path.
	if config.Server.Forge && isForgeFML3(clientInfo) {
		return false
	}
	return mustProbe(config) && server.ProbedJoinGame() == nil
}

// mustProbe checks whether we must have probed data.
func mustProbe(config *Config) bool {
	return config.Server.Forge
}
