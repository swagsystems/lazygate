package main

// Join kick method, mirroring lazymc's join/kick.rs.

import (
	"net"

	"lazymc/proto"
)

// kickOccupy kicks the client.
func kickOccupy(client *proto.Client, config *Config, server *ServerState, inbound net.Conn) (methodResult, error) {
	TraceLog(Target, "Using kick method to occupy joining client")

	// Select message and kick
	var msg string
	switch server.GetState() {
	case StateStarting, StateStopped, StateStarted:
		msg = config.Join.Kick.Starting
	case StateStopping:
		msg = config.Join.Kick.Stopping
	}
	_ = proto.Kick(client, msg, inbound)

	// Gracefully close connection
	_ = CloseTCPStream(inbound)

	return methodResult{consumed: true, stream: inbound}, nil
}
