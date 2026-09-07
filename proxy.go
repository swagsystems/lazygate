// Package main holds lazymc's top-level modules: proxy, net, os helpers,
// server state, monitor, status, probe, forge, lobby and the join methods.

package main

import (
	"io"
	"net"

	"lazymc/proxyv2"
)

// ProxyHeader describes whether/how to add a HAProxy v2 header.
type ProxyHeader int

// Proxy header kinds.
const (
	// ProxyHeaderNone does not add a proxy header.
	ProxyHeaderNone ProxyHeader = iota

	// ProxyHeaderLocal is a header for a locally initiated connection.
	ProxyHeaderLocal

	// ProxyHeaderProxy is a header for a proxied connection.
	ProxyHeaderProxy
)

// NotNone changes the header to None if `notNone` is false. None stays None.
func (h ProxyHeader) NotNone(notNone bool) ProxyHeader {
	if notNone {
		return h
	}
	return ProxyHeaderNone
}

// LocalProxyHeader builds the proxy header for a locally initiated
// connection (command LOCAL, UNSPEC addresses).
func LocalProxyHeader() ([]byte, error) {
	return proxyv2.LocalHeader()
}

// StreamProxyHeader builds the proxy header for the given inbound connection.
func StreamProxyHeader(peer, local net.Addr) ([]byte, error) {
	return proxyv2.StreamHeader(peer, local)
}

// Proxy proxies the inbound stream to a target address.
func Proxy(inbound net.Conn, proxyHeader ProxyHeader, addrTarget string) error {
	return ProxyWithQueue(inbound, proxyHeader, addrTarget, nil)
}

// ProxyWithQueue proxies the inbound stream to a target address, sending the
// queue to the target server before proxying.
func ProxyWithQueue(inbound net.Conn, proxyHeader ProxyHeader, addrTarget string, queue []byte) error {
	// Set up connection to server
	outbound, err := net.Dial("tcp", addrTarget)
	if err != nil {
		return err
	}

	// Add proxy header
	switch proxyHeader {
	case ProxyHeaderNone:
	case ProxyHeaderLocal:
		header, err := LocalProxyHeader()
		if err != nil {
			outbound.Close()
			return err
		}
		if _, err := outbound.Write(header); err != nil {
			outbound.Close()
			return err
		}
	case ProxyHeaderProxy:
		header, err := StreamProxyHeader(inbound.RemoteAddr(), inbound.LocalAddr())
		if err != nil {
			outbound.Close()
			return err
		}
		if _, err := outbound.Write(header); err != nil {
			outbound.Close()
			return err
		}
	}

	return ProxyInboundOutboundWithQueue(inbound, outbound, nil, queue)
}

// ProxyInboundOutboundWithQueue proxies between two streams, forwarding
// queued bytes first.
func ProxyInboundOutboundWithQueue(inbound, outbound net.Conn, inboundQueue, outboundQueue []byte) error {
	// Forward queued bytes to client once writable
	if len(inboundQueue) > 0 {
		TraceLog("lazymc", "Relaying %d queued bytes to client", len(inboundQueue))
		if _, err := inbound.Write(inboundQueue); err != nil {
			inbound.Close()
			outbound.Close()
			return err
		}
	}

	// Forward queued bytes to server once writable
	if len(outboundQueue) > 0 {
		TraceLog("lazymc", "Relaying %d queued bytes to server", len(outboundQueue))
		if _, err := outbound.Write(outboundQueue); err != nil {
			inbound.Close()
			outbound.Close()
			return err
		}
	}

	done := make(chan struct{}, 2)
	var errs [2]error

	// client -> server
	go func() {
		_, errs[0] = io.Copy(outbound, inbound)
		closeWrite(outbound)
		done <- struct{}{}
	}()

	// server -> client
	go func() {
		_, errs[1] = io.Copy(inbound, outbound)
		closeWrite(inbound)
		done <- struct{}{}
	}()

	<-done
	<-done

	// Gracefully close connection
	CloseTCPStream(inbound)
	CloseTCPStream(outbound)

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// closeWrite shuts down the write side of a TCP connection.
func closeWrite(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

// CloseTCPStream gracefully closes the given TCP stream, succeeding if
// already closed.
func CloseTCPStream(conn net.Conn) error {
	err := conn.Close()
	if err != nil && isNotConnected(err) {
		return nil
	}
	return err
}

func isNotConnected(err error) bool {
	return err != nil && (err.Error() == "use of closed network connection" || err.Error() == "transport endpoint is not connected")
}
