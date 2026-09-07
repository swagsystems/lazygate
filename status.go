package main

// Status/handshake connection serving, mirroring lazymc's status.rs.

import (
	"errors"
	"net"
	"os"
	"path/filepath"

	"lazymc/mc"
	"lazymc/proto"
)

// The ban message prefix.
const banMessagePrefix = "Your IP address is banned from this server.\nReason: "

// The not-whitelisted kick message.
const whitelistMessage = "You are not white-listed on this server!"

// Server icon file path.
const serverIconFile = "server-icon.png"

// Serve handles an inbound connection through the status/join flow.
func Serve(client *proto.Client, inbound net.Conn, config *Config, server *ServerState) error {
	// Note: the connection is NOT closed here; ownership passes to the
	// occupy method (proxy) once login starts. Explicit closes happen on
	// the non-occupy exit paths below.

	// Incoming buffer and packet holding queue
	var buf []byte

	// Remember inbound packets, track client info
	var inboundHistory []byte
	clientInfo := proto.EmptyClientInfo()

	for {
		// Read packet from stream
		packet, raw, more, err := proto.ReadPacket(client, &buf, inbound)
		if err != nil {
			if errors.Is(err, proto.ErrMalformedPacket) {
				ErrorLog(Target, "Closing connection, error occurred")
			}
			break
		}
		if !more {
			break
		}

		// Grab client state
		clientState := client.State()

		// Hijack handshake
		if clientState == proto.ClientStateHandshake && packet.ID == proto.PacketHandshake {
			// Parse handshake
			handshake, err := proto.DecodeHandshake(packet.Data)
			if err != nil {
				DebugLog(Target, "Got malformed handshake from client, disconnecting: %v", err)
				break
			}

			// Parse new state
			newState, ok := clientState.FromID(handshake.NextState)
			if !ok {
				ErrorLog(Target, "Client tried to switch into unknown protcol state (%d), disconnecting", handshake.NextState)
				break
			}

			// Update client info and client state
			p := uint32(handshake.ProtocolVersion)
			clientInfo.Protocol = &p
			clientInfo.Handshake = &handshake
			client.SetState(newState)

			// If logging in with handshake, remember inbound
			if newState == proto.ClientStateLogin {
				inboundHistory = append(inboundHistory, raw...)
			}

			continue
		}

		// Hijack server status packet
		if clientState == proto.ClientStateStatus && packet.ID == proto.PacketServerStatusRequest {
			serverStatus := serverStatusResponse(&clientInfo, config, server)
			jsonStr := serverStatus.StatusResponseJSON()

			// Status response encodes as a length-prefixed JSON string
			data := proto.StringBytes(jsonStr)
			response, ok := proto.NewRawPacket(proto.PacketClientStatusResponse, data).EncodeWithLen(client)
			if !ok {
				break
			}
			if _, err := inbound.Write(response); err != nil {
				break
			}
			continue
		}

		// Hijack ping packet
		if clientState == proto.ClientStateStatus && packet.ID == proto.PacketServerPing {
			if _, err := inbound.Write(raw); err != nil {
				break
			}
			continue
		}

		// Hijack login start
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketServerLoginStart {
			// Try to get login username, update client info
			if loginStart, err := proto.DecodeLoginStart(packet.Data); err == nil {
				clientInfo.Username = &loginStart.Name
			}

			// Kick if lockout is enabled
			if config.Lockout.Enabled {
				if clientInfo.Username != nil {
					InfoLog(Target, "Kicked '%s' because lockout is enabled", *clientInfo.Username)
				} else {
					InfoLog(Target, "Kicked player because lockout is enabled")
				}
				_ = proto.Kick(client, config.Lockout.Message, inbound)
				break
			}

			// Kick if client is banned
			peerIP := peerIP(client.Peer)
			if ban, ok := server.BanEntry(peerIP); ok && ban.IsBanned() {
				var msg string
				if ban.Reason != nil {
					InfoLog(Target, "Login from banned IP %s (%s), disconnecting", peerIP, *ban.Reason)
					msg = *ban.Reason
				} else {
					InfoLog(Target, "Login from banned IP %s, disconnecting", peerIP)
					msg = mc.DefaultBanReason
				}
				_ = proto.Kick(client, banMessagePrefix+msg, inbound)
				break
			}

			// Kick if client is not whitelisted to wake server
			if clientInfo.Username != nil {
				if !server.IsWhitelisted(*clientInfo.Username) {
					InfoLog(Target, "User '%s' tried to wake server but is not whitelisted, disconnecting", *clientInfo.Username)
					_ = proto.Kick(client, whitelistMessage, inbound)
					break
				}
			}

			// In online lobby mode, defer the wake until the login has been
			// cryptographically verified. A bare Login Start username is not an
			// authentication boundary and must not be able to wake Horizon.
			if !config.Auth.OnlineMode {
				server.Start(config, clientInfo.Username)
			}

			// Remember inbound packets
			inboundHistory = append(inboundHistory, raw...)
			inboundHistory = append(inboundHistory, buf...)

			// Build inbound packet queue with everything from login start
			// (including this)
			loginQueue := make([]byte, 0, len(raw)+len(buf))
			loginQueue = append(loginQueue, raw...)
			loginQueue = append(loginQueue, buf...)

			// Buf is fully consumed here
			buf = nil

			// Start occupying client
			return Occupy(client, clientInfo, config, server, inbound, inboundHistory, loginQueue)
		}

		// Show unhandled packet warning
		DebugLog(Target, "Got unhandled packet:")
		DebugLog(Target, "- State: %v", clientState)
		DebugLog(Target, "- Packet ID: %d", packet.ID)
	}

	// Gracefully close connection on non-occupy paths
	_ = CloseTCPStream(inbound)

	return nil
}

// peerIP extracts the IP string from a peer address.
func peerIP(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// serverStatusResponse builds the server status object to respond with.
func serverStatusResponse(clientInfo *proto.ClientInfo, config *Config, server *ServerState) proto.ServerStatus {
	status := server.Status()
	serverState := server.GetState()

	// Respond with real server status if started
	if serverState == StateStarted && status != nil {
		return *status
	}

	// Select version and player max from last known server status
	var version proto.ServerVersion
	max := uint32(0)
	if status != nil {
		version = status.Version
		max = status.Players.Max
	} else {
		version = proto.ServerVersion{
			Name:     config.Public.Version,
			Protocol: config.Public.Protocol,
		}
	}

	// Select description, use server MOTD if enabled, or use configured
	description := ""
	if config.Motd.FromServer && status != nil {
		description = status.Description
	} else {
		switch serverState {
		case StateStopped, StateStarted:
			description = config.Motd.Sleeping
		case StateStarting:
			description = config.Motd.Starting
		case StateStopping:
			description = config.Motd.Stopping
		}
	}

	// Extract favicon from real server status, load from disk, or use default
	var favicon *string
	if mc.SupportsFavicon(clientInfo.GetProtocol()) {
		if config.Motd.FromServer && status != nil && status.Favicon != nil {
			f := *status.Favicon
			favicon = &f
		}
		if favicon == nil {
			f := serverFavicon(config)
			favicon = &f
		}
	}

	// Build status response
	return proto.ServerStatus{
		Version:     version,
		Description: description,
		ForgeData:   append([]byte(nil), statusForgeData(status)...),
		Players: proto.OnlinePlayers{
			Online: 0,
			Max:    max,
			Sample: []proto.OnlinePlayer{},
		},
		Favicon: favicon,
	}
}

func statusForgeData(status *proto.ServerStatus) []byte {
	if status == nil {
		return nil
	}
	return status.ForgeData
}

// serverFavicon returns the server status favicon, defaulting when unset.
func serverFavicon(config *Config) string {
	// Get server dir
	dir := ServerDirectory(config)
	if dir == nil {
		return mc.DefaultFavicon()
	}

	// Server icon file, ensure it exists
	path := filepath.Join(*dir, serverIconFile)
	if !isRegularFile(path) {
		return mc.DefaultFavicon()
	}

	// Read icon data
	data, err := os.ReadFile(path)
	if err != nil {
		ErrorLog(TargetStatus, "Failed to read favicon from %s, using default: %v", serverIconFile, err)
		return mc.DefaultFavicon()
	}

	return mc.EncodeFavicon(data)
}
