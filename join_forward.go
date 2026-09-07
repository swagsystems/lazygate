package main

// Join forward method, mirroring lazymc's join/forward.rs.

import (
	"net"
)

// forwardOccupy forwards the client.
func forwardOccupy(config *Config, inbound net.Conn, inboundHistory *[]byte) (methodResult, error) {
	TraceLog(Target, "Using forward method to occupy joining client")

	DebugLog(Target, "Forwarding client to %v!", config.Join.Forward.Address)

	routeProxyAddressQueue(
		inbound,
		ProxyHeaderProxy.NotNone(config.Join.Forward.SendProxyV2),
		config.Join.Forward.Address.String(),
		append([]byte(nil), *inboundHistory...),
	)

	// TODO: do not consume, continue on proxy connect failure

	return methodResult{consumed: true, stream: inbound}, nil
}
