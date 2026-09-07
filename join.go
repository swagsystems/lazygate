package main

// Join occupation methods, mirroring lazymc's join module.

import (
	"net"

	"lazymc/proto"
)

// MethodResult is a result returned by a join occupy method.
type methodResult struct {
	// Client is consumed.
	consumed bool

	// Method is done, continue with the next (stream returned).
	stream net.Conn
}

// Occupy starts occupying the client.
//
// This assumes the login start packet has just been received.
func Occupy(client *proto.Client, clientInfo proto.ClientInfo, config *Config, server *ServerState, inbound net.Conn, inboundHistory, loginQueue []byte) error {
	// Go through all configured join methods
	for _, method := range config.Join.Methods {
		// Invoke method, take result
		var result methodResult
		var err error

		switch method {
		// Kick method, immediately kick client
		case MethodKick:
			result, err = kickOccupy(client, config, server, inbound)

		// Hold method, hold client connection while server starts
		case MethodHold:
			result, err = holdOccupy(config, server, inbound, &inboundHistory)

		// Forward method, forward client connection while server starts
		case MethodForward:
			result, err = forwardOccupy(config, inbound, &inboundHistory)

		// Lobby method, keep client in lobby while server starts
		case MethodLobby:
			result, err = lobbyOccupy(client, clientInfo, config, server, inbound, loginQueue)
		}

		if err != nil {
			return err
		}

		// Handle method result
		if result.consumed {
			return nil
		}
		inbound = result.stream
	}

	DebugLog(Target, "No method left to occupy joining client, disconnecting")

	// Gracefully close connection
	return CloseTCPStream(inbound)
}
