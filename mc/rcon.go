package mc

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"lazymc/proxyv2"
	"lazymc/util"
)

// Minecraft RCON quirk.
//
// Wait this time between RCON operations. The Minecraft RCON implementation
// is very broken and brittle, this is used in the hopes to improve
// reliability.
const quirkRconGraceTime = 200 * time.Millisecond

// rust_rcon crate constants.
const rconInitialPacketID = 1
const rconDelayTimeMillis = 3 * time.Millisecond
const rconMaxPayloadSize = 1413

// RCON packet types.
const (
	rconTypeAuth          = 3
	rconTypeAuthResponse  = 2
	rconTypeExecCommand   = 2
	rconTypeResponseValue = 0
)

// Rcon is an RCON client, mirroring lazymc's mc/rcon.rs over rust_rcon.
type Rcon struct {
	conn         net.Conn
	nextPacketID int32
}

// ConnectRcon connects to an RCON host with the given password.
func ConnectRcon(sendProxyV2 bool, addr, pass string) (*Rcon, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	// Add proxy header
	if sendProxyV2 {
		util.Trace("lazymc::rcon", "Sending local proxy header for RCON connection")
		header, err := proxyv2.LocalHeader()
		if err != nil {
			conn.Close()
			return nil, err
		}
		if _, err := conn.Write(header); err != nil {
			conn.Close()
			return nil, err
		}
	}

	r := &Rcon{conn: conn, nextPacketID: rconInitialPacketID}
	if err := r.auth(pass); err != nil {
		conn.Close()
		return nil, err
	}

	return r, nil
}

// ConnectRconConfig connects to RCON from the given configuration.
func ConnectRconConfig(sendProxyV2 bool, serverAddr net.IP, serverPort uint16, rconPort uint16, password string) (*Rcon, error) {
	addr := fmt.Sprintf("%s:%d", serverAddr.String(), rconPort)
	return ConnectRcon(sendProxyV2, addr, password)
}

// auth performs the RCON authentication handshake.
func (r *Rcon) auth(password string) error {
	_, err := r.send(rconTypeAuth, password)
	if err != nil {
		return err
	}

	for {
		pkt, err := r.receivePacket()
		if err != nil {
			return err
		}
		if pkt.ptype == rconTypeAuthResponse {
			if pkt.id < 0 {
				return fmt.Errorf("authentication failed")
			}
			return nil
		}
	}
}

// Cmd sends a command over RCON.
func (r *Rcon) Cmd(cmd string) (string, error) {
	if len(cmd) > rconMaxPayloadSize {
		return "", fmt.Errorf("command exceeds the maximum length")
	}

	if _, err := r.send(rconTypeExecCommand, cmd); err != nil {
		return "", err
	}

	// Minecraft quirk: small delay between send and response
	time.Sleep(rconDelayTimeMillis)

	return r.receiveMultiPacketResponse()
}

// receiveMultiPacketResponse reads the (possibly multi-packet) response,
// using an empty command as end marker like rust_rcon does.
func (r *Rcon) receiveMultiPacketResponse() (string, error) {
	endID, err := r.send(rconTypeExecCommand, "")
	if err != nil {
		return "", err
	}

	var result string
	for {
		pkt, err := r.receivePacket()
		if err != nil {
			return "", err
		}
		if pkt.id == endID {
			return result, nil
		}
		result += pkt.body
	}
}

// send writes an RCON packet and returns its id.
func (r *Rcon) send(ptype int32, body string) (int32, error) {
	id := r.nextPacketID
	r.nextPacketID++
	if r.nextPacketID < rconInitialPacketID {
		r.nextPacketID = rconInitialPacketID
	}

	buf := make([]byte, 0, 10+len(body))
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], uint32(10+len(body)))
	buf = append(buf, tmp[:]...)
	binary.LittleEndian.PutUint32(tmp[:], uint32(id))
	buf = append(buf, tmp[:]...)
	binary.LittleEndian.PutUint32(tmp[:], uint32(ptype))
	buf = append(buf, tmp[:]...)
	buf = append(buf, body...)
	buf = append(buf, 0x00, 0x00)

	if _, err := r.conn.Write(buf); err != nil {
		return 0, err
	}
	return id, nil
}

type rconPacket struct {
	id    int32
	ptype int32
	body  string
}

func (r *Rcon) receivePacket() (rconPacket, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r.conn, hdr[:]); err != nil {
		return rconPacket{}, err
	}
	length := int32(binary.LittleEndian.Uint32(hdr[0:4]))
	id := int32(binary.LittleEndian.Uint32(hdr[4:8]))
	ptype := int32(binary.LittleEndian.Uint32(hdr[8:12]))

	bodyLen := length - 10
	if bodyLen < 0 {
		return rconPacket{}, fmt.Errorf("invalid RCON packet length %d", length)
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r.conn, body); err != nil {
		return rconPacket{}, err
	}

	var term [2]byte
	if _, err := io.ReadFull(r.conn, term[:]); err != nil {
		return rconPacket{}, err
	}

	return rconPacket{id: id, ptype: ptype, body: string(body)}, nil
}

// Close gracefully closes the connection, after the Minecraft quirk sleep.
func (r *Rcon) Close() {
	time.Sleep(quirkRconGraceTime)
	_ = r.conn.Close()
}
