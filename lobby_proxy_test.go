package main

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestLobbyRouteProxyRetainsSocketAndAdmissionUntilSessionEnds(t *testing.T) {
	clientConn, clientPeer := net.Pipe()
	serverConn, serverPeer := net.Pipe()
	defer clientPeer.Close()
	defer serverPeer.Close()

	admission := newConnectionAdmission()
	for i := 0; i < maxStatusClients-1; i++ {
		if !admission.acquireStatus() {
			t.Fatalf("failed to reserve status admission %d", i)
		}
	}
	if !admission.acquireStatus() {
		t.Fatal("failed to reserve lobby status admission")
	}

	proxyDone := make(chan struct{})
	go func() {
		lobbyRouteProxy(clientConn, serverConn, nil)
		admission.releaseStatus()
		close(proxyDone)
	}()

	select {
	case <-proxyDone:
		t.Fatal("lobby proxy returned before the proxied session ended")
	case <-time.After(50 * time.Millisecond):
	}
	if admission.acquireStatus() {
		admission.releaseStatus()
		t.Fatal("lobby status admission was released while proxy session was active")
	}

	clientData := []byte("client-to-server")
	clientWrite := make(chan error, 1)
	go func() {
		_, err := clientPeer.Write(clientData)
		clientWrite <- err
	}()
	serverRead := make([]byte, len(clientData))
	if err := readPipeExactly(serverPeer, serverRead); err != nil {
		t.Fatal(err)
	}
	if err := <-clientWrite; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(serverRead, clientData) {
		t.Fatalf("client payload = %q, want %q", serverRead, clientData)
	}

	serverData := []byte("server-to-client")
	serverWrite := make(chan error, 1)
	go func() {
		_, err := serverPeer.Write(serverData)
		serverWrite <- err
	}()
	clientRead := make([]byte, len(serverData))
	if err := readPipeExactly(clientPeer, clientRead); err != nil {
		t.Fatal(err)
	}
	if err := <-serverWrite; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(clientRead, serverData) {
		t.Fatalf("server payload = %q, want %q", clientRead, serverData)
	}

	_ = clientPeer.Close()
	_ = serverPeer.Close()
	select {
	case <-proxyDone:
	case <-time.After(time.Second):
		t.Fatal("lobby proxy did not finish after both session peers closed")
	}
	if !admission.acquireStatus() {
		t.Fatal("lobby status admission was not released after proxy completion")
	}
	admission.releaseStatus()
}

func readPipeExactly(conn net.Conn, buf []byte) error {
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	defer conn.SetReadDeadline(time.Time{})
	_, err := io.ReadFull(conn, buf)
	return err
}
