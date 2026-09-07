// Package proxyv2 builds HAProxy PROXY protocol v2 headers.
package proxyv2

import (
	"encoding/binary"
	"fmt"
	"net"
)

// haproxy v2 signature.
var signature = []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}

// LocalHeader builds the header for a locally initiated connection
// (command LOCAL, UNSPEC addresses).
//
// Note: the proxy-protocol crate emits the STREAM transport protocol bit
// (0x01) even for UNSPEC addresses; this is matched here for byte parity.
func LocalHeader() ([]byte, error) {
	header := append([]byte(nil), signature...)
	header = append(header, 0x20, 0x01, 0x00, 0x00)
	return header, nil
}

// StreamHeader builds the PROXY header for a proxied TCP connection.
func StreamHeader(peer, local net.Addr) ([]byte, error) {
	peerTCP, peerOK := peer.(*net.TCPAddr)
	localTCP, localOK := local.(*net.TCPAddr)
	if !peerOK || !localOK {
		return nil, fmt.Errorf("addresses not TCP")
	}

	src := peerTCP.IP
	dst := localTCP.IP

	header := append([]byte(nil), signature...)

	if src4 := src.To4(); src4 != nil && dst.To4() != nil {
		// ver_cmd: version 2, command PROXY; fam_proto: IPv4/STREAM
		header = append(header, 0x21, 0x11, 0x00, 0x0C)
		header = append(header, src4...)
		header = append(header, dst.To4()...)
		var p [2]byte
		binary.BigEndian.PutUint16(p[:], uint16(peerTCP.Port))
		header = append(header, p[:]...)
		binary.BigEndian.PutUint16(p[:], uint16(localTCP.Port))
		header = append(header, p[:]...)
		return header, nil
	}

	// IPv6/STREAM
	header = append(header, 0x21, 0x21, 0x00, 0x24)
	header = append(header, src.To16()...)
	header = append(header, dst.To16()...)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(peerTCP.Port))
	header = append(header, p[:]...)
	binary.BigEndian.PutUint16(p[:], uint16(localTCP.Port))
	header = append(header, p[:]...)
	return header, nil
}
