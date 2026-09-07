package main

// Lobby emulation, mirroring lazymc's lobby.rs.

import (
	"errors"
	"net"
	"time"

	"lazymc/mc"
	"lazymc/proto"
	"lazymc/proto/packets/play"
)

// Interval to send keep-alive packets at.
const keepAliveInterval = 10 * time.Second

// Timeout for creating new server connection for lobby client.
const serverConnectTimeout = 2 * time.Minute

// Timeout for server sending join game packet.
const serverJoinGameTimeout = 20 * time.Second

// Time to wait before responding to newly connected server.
//
// Notchian servers are slow, we must wait a little before sending play
// packets, because the server needs time to transition the client into this
// state.
const serverWarmup = 1 * time.Second

// LobbyServe serves the lobby service for the given client connection.
//
// The client must be in the login state, or this will error.
func LobbyServe(client *proto.Client, clientInfo proto.ClientInfo, inbound net.Conn, config *Config, server *ServerState, queue []byte) error {
	defer inbound.Close()

	// Client must be in login state
	if client.State() != proto.ClientStateLogin {
		ErrorLog(TargetLobby, "Client reached lobby service with invalid state: %v", client.State())
		return errors.New("invalid client state")
	}

	// We must have useful client info
	if clientInfo.Username == nil {
		ErrorLog(TargetLobby, "Client username is unknown, closing connection")
		return errors.New("unknown username")
	}

	// Incoming buffer
	inboundBuf := append([]byte(nil), queue...)

	for {
		// Read packet from stream
		packet, _, more, err := proto.ReadPacket(client, &inboundBuf, inbound)
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

		// Hijack login start
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketServerLoginStart {
			// Parse login start packet
			loginStart, err := proto.DecodeLoginStart(packet.Data)
			if err != nil {
				break
			}

			DebugLog(TargetLobby, "Login on lobby server (user: %s)", loginStart.Name)
			protocol := uint32(0)
			if value := clientInfo.GetProtocol(); value != nil {
				protocol = *value
			}
			TraceLog(TargetForge, "Lobby client protocol=%d forge_marker=%s", protocol, forgeHandshakeMarker(&clientInfo))

			// A lobby consumes the original login instead of transparently
			// proxying it, so it must authenticate the client itself before
			// entering play state.
			if config.Auth.OnlineMode {
				authenticatedConn, profile, err := authenticateLobbyClient(client, inbound, &inboundBuf, loginStart.Name, config)
				if err != nil {
					WarnLog(TargetLobby, "Online authentication failed for %s: %v", loginStart.Name, err)
					break
				}
				inbound = authenticatedConn
				clientInfo.Profile = profile
				loginStart.Name = profile.Name
				clientInfo.Username = &loginStart.Name
			}

			// Authentication now owns the wake boundary for lobby clients.
			// This is a no-op if Horizon already reports the backend started.
			server.Start(config, clientInfo.Username)

			// FML3 cannot be synthesized from the old probe payload. For the
			// first modern Forge client, hold the authenticated client in LOGIN,
			// connect to the already-started backend, and relay its real exchange.
			if config.Server.Forge && isForgeFML3(&clientInfo) && len(server.ForgeLoginCache()) == 0 {
				if err := waitForServer(server, config); err != nil {
					break
				}
				// Modern proxies negotiate compression with the authenticated client
				// before delaying LoginSuccess and starting the backend FML relay.
				// PCF's offline backend begins Forge negotiation before it sends its
				// own SetCompression packet, so waiting for the backend leaves the
				// client in the wrong framing state for the first large FML request.
				if _, err := ensureClientCompression(client, inbound); err != nil {
					ErrorLog(TargetForge, "Failed to enable compression before FML3 hot bootstrap: %v", err)
					break
				}
				relay := newForgeLoginRelay(client, inbound, &inboundBuf, server)
				_, outbound, serverBuf, err := connectToServerWithForge(&clientInfo, inbound, config, server, relay)
				if err != nil {
					ErrorLog(TargetForge, "FML3 hot bootstrap failed: %v", err)
					break
				}
				lobbyRouteProxy(inbound, outbound, serverBuf)
				return nil
			}

			compressionSent := false
			// Replay modern cached requests, or preserve the legacy FML2 probe
			// payload behavior for older Forge protocol versions. FML3 clients
			// must receive and apply compression before cached requests are sent.
			if config.Server.Forge && isForgeFML3(&clientInfo) {
				var err error
				compressionSent, err = replayColdForgeLogin(client, inbound, server, &inboundBuf)
				if err != nil {
					break
				}
			} else if config.Server.Forge {
				if err := replayLoginPayload(client, inbound, server, &inboundBuf); err != nil {
					break
				}
			}

			// Respond with set compression if compression is enabled based
			// on threshold
			if proto.CompressionThreshold >= 0 && !compressionSent {
				TraceLog(TargetLobby, "Enabling compression for lobby client because server has it enabled (threshold: %d)", proto.CompressionThreshold)
				if err := respondSetCompression(client, inbound, proto.CompressionThreshold); err != nil {
					break
				}
				client.SetCompression(proto.CompressionThreshold)
			}

			// Respond with login success, switch to play state
			if err := respondLoginSuccess(client, &clientInfo, inbound, &loginStart); err != nil {
				break
			}
			client.SetState(proto.ClientStatePlay)

			TraceLog(TargetLobby, "Client login success, sending required play packets for lobby world")

			// Send packets to client required to get into workable play
			// state for lobby world
			if err := sendLobbyPlayPackets(client, &clientInfo, inbound, server, config); err != nil {
				break
			}

			// Wait for server to come online
			if err := stageWait(client, &clientInfo, server, config, inbound); err != nil {
				break
			}

			// Start new connection to server
			serverClient, outbound, serverBuf, err := connectToServerWithForge(&clientInfo, inbound, config, server, nil)
			if err != nil {
				break
			}

			// Grab join game packet from server
			joinGameData, err := waitForServerJoinGame(serverClient, &clientInfo, outbound, &serverBuf, TargetLobby)
			if err != nil {
				outbound.Close()
				break
			}

			// Reset lobby title
			if err := play.SendTitle(client, &clientInfo, inbound, ""); err != nil {
				outbound.Close()
				break
			}

			// Play ready sound if configured
			if err := playLobbyReadySound(client, &clientInfo, inbound, config); err != nil {
				outbound.Close()
				break
			}

			// Wait a second because Notchian servers are slow
			TraceLog(TargetLobby, "Waiting a second before relaying client connection...")
			time.Sleep(serverWarmup)

			// Send respawn packet, initiates teleport to real server world
			if err := sendLobbyRespawn(client, &clientInfo, inbound, joinGameData); err != nil {
				outbound.Close()
				break
			}

			// Drain inbound connection so we don't confuse the server
			TraceLog(TargetLobby, "Voiding remaining incoming lobby client data before relay to real server")
			drainStream(inbound)

			// Client and server connection ready now, move client to proxy
			DebugLog(TargetLobby, "Server connection ready, relaying lobby client to proxy")
			lobbyRouteProxy(inbound, outbound, serverBuf)

			return nil
		}

		// Show unhandled packet warning
		DebugLog(Target, "Got unhandled packet:")
		DebugLog(Target, "- State: %v", clientState)
		DebugLog(Target, "- Packet ID: 0x%02X (%d)", packet.ID, packet.ID)
	}

	// Gracefully close connection
	_ = CloseTCPStream(inbound)

	return nil
}

// respondSetCompression responds to the client with a set compression packet.
func respondSetCompression(client *proto.Client, conn net.Conn, threshold int32) error {
	w := proto.NewPacketWriter()
	w.WriteVarInt(threshold)
	return writePacketTo(client, conn, proto.NewRawPacket(proto.PacketClientSetCompression, w.Bytes()))
}

// replayColdForgeLogin sends compression before replaying cached FML3 login
// requests. The caller skips the generic compression block when this returns
// true, preventing a second SetCompression packet on the wire.
func replayColdForgeLogin(client *proto.Client, conn net.Conn, server *ServerState, inboundBuf *[]byte) (bool, error) {
	compressionSent, err := ensureClientCompression(client, conn)
	if err != nil {
		return false, err
	}
	if err := replayForgeLoginPayload(client, conn, server, inboundBuf); err != nil {
		return compressionSent, err
	}
	return compressionSent, nil
}

// ensureClientCompression establishes the proxy-facing compression state once.
// It returns true whenever compression is active so callers can suppress a
// duplicate SetCompression packet later in the login flow.
func ensureClientCompression(client *proto.Client, conn net.Conn) (bool, error) {
	if proto.CompressionThreshold < 0 {
		return false, nil
	}
	if client.IsCompressed() {
		if client.Compressed() != proto.CompressionThreshold {
			return false, errors.New("client compression threshold mismatch")
		}
		return true, nil
	}
	TraceLog(TargetLobby, "Enabling compression for authenticated Forge client (threshold: %d)", proto.CompressionThreshold)
	if err := respondSetCompression(client, conn, proto.CompressionThreshold); err != nil {
		return false, err
	}
	client.SetCompression(proto.CompressionThreshold)
	return true, nil
}

// respondLoginSuccess responds to the client with a login success packet.
//
// TODO: support online mode here
func respondLoginSuccess(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn, loginStart *proto.LoginStart) error {
	uuid := mc.OfflinePlayerUUID(loginStart.Name)
	if clientInfo.Profile != nil {
		uuid = clientInfo.Profile.UUID
	}

	w := proto.NewPacketWriter()
	protocol := clientInfo.GetProtocol()
	// Minecraft 1.16+ uses a raw UUID. Protocol 763 also requires a
	// property array after the username.
	if protocol != nil && *protocol >= 735 {
		w.Write(uuid[:])
	} else {
		w.WriteString(hyphenatedUUID(uuid))
	}
	w.WriteString(loginStart.Name)
	if protocol != nil && *protocol >= 759 {
		properties := []proto.ProfileProperty(nil)
		if clientInfo.Profile != nil {
			properties = clientInfo.Profile.Properties
		}
		w.WriteVarInt(int32(len(properties)))
		for _, property := range properties {
			w.WriteString(property.Name)
			w.WriteString(property.Value)
			w.WriteBool(property.Signature != nil)
			if property.Signature != nil {
				w.WriteString(*property.Signature)
			}
		}
	}

	return writePacketTo(client, conn, proto.NewRawPacket(proto.PacketClientLoginSuccess, w.Bytes()))
}

// hyphenatedUUID renders a UUID in hyphenated form.
func hyphenatedUUID(u [16]byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, b := range u {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0F])
	}
	return string(out)
}

// playLobbyReadySound plays the lobby ready sound effect if configured.
func playLobbyReadySound(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn, config *Config) error {
	if config.Join.Lobby.ReadySound != nil {
		soundName := *config.Join.Lobby.ReadySound
		// Must not be empty string
		if len(soundName) == 0 || allSpaces(soundName) {
			WarnLog(TargetLobby, "Lobby ready sound effect is an empty string, you should remove the configuration item instead")
			return nil
		}

		// Play sound effect
		if err := play.SendPlayerPos(client, clientInfo, conn); err != nil {
			return err
		}
		if err := play.SendSound(client, clientInfo, conn, soundName); err != nil {
			return err
		}
	}

	return nil
}

func allSpaces(s string) bool {
	for _, c := range s {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}

// sendLobbyPlayPackets sends packets to the client to get a workable play
// state for the lobby world.
func sendLobbyPlayPackets(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn, server *ServerState, config *Config) error {
	// See: https://wiki.vg/Protocol_FAQ#What.27s_the_normal_login_sequence_for_a_client.3F

	// Send initial game join
	params := lobbyJoinGameParams(config, server)
	if err := play.SendJoinGame(client, clientInfo, conn, params); err != nil {
		return err
	}

	// Send server brand
	if err := play.SendServerBrand(client, clientInfo, conn); err != nil {
		return err
	}

	// Send spawn and player position, disables 'download terrain' screen
	if err := play.SendPlayerPos(client, clientInfo, conn); err != nil {
		return err
	}

	// Notify client of world time, required once before keep-alive packets
	if err := play.SendTimeUpdate(client, clientInfo, conn); err != nil {
		return err
	}

	return nil
}

// lobbyJoinGameParams builds the join game parameters for the lobby.
func lobbyJoinGameParams(config *Config, server *ServerState) play.JoinGameParams {
	status := server.Status()
	probed := server.ProbedJoinGame()

	// Get dimension codec and build lobby dimension
	var dimensionCodec = mc.DefaultDimensionCodec()
	if probed != nil && probed.DimensionCodec != nil {
		dimensionCodec = probed.DimensionCodec
	}

	// Get other values from status and probed join game data
	dimension := mc.LobbyDimension(dimensionCodec)
	hardcore := false
	if probed != nil && probed.Hardcore != nil {
		hardcore = *probed.Hardcore
	}
	maxPlayers := int32(20)
	if status != nil {
		maxPlayers = int32(status.Players.Max)
	} else if probed != nil && probed.MaxPlayers != nil {
		maxPlayers = *probed.MaxPlayers
	}
	viewDistance := int32(10)
	if probed != nil && probed.ViewDistance != nil {
		viewDistance = *probed.ViewDistance
	}
	reducedDebugInfo := false
	if probed != nil && probed.ReducedDebugInfo != nil {
		reducedDebugInfo = *probed.ReducedDebugInfo
	}
	enableRespawnScreen := true
	if probed != nil && probed.EnableRespawnScreen != nil {
		enableRespawnScreen = *probed.EnableRespawnScreen
	}
	isDebug := false
	if probed != nil && probed.IsDebug != nil {
		isDebug = *probed.IsDebug
	}
	isFlat := false
	if probed != nil && probed.IsFlat != nil {
		isFlat = *probed.IsFlat
	}

	return play.JoinGameParams{
		DimensionCodec:      dimensionCodec,
		Dimension:           dimension,
		MaxPlayers:          maxPlayers,
		ViewDistance:        viewDistance,
		Hardcore:            hardcore,
		ReducedDebugInfo:    reducedDebugInfo,
		EnableRespawnScreen: enableRespawnScreen,
		IsDebug:             isDebug,
		IsFlat:              isFlat,
	}
}

// sendLobbyRespawn sends a respawn packet to jump from lobby into the now
// loaded server.
func sendLobbyRespawn(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn, data *play.JoinGameData) error {
	dimension := data.Dimension
	if dimension == nil {
		var codec = mc.DefaultDimensionCodec()
		if data.DimensionCodec != nil {
			codec = data.DimensionCodec
		}
		dimension = mc.LobbyDimension(codec)
	}

	worldName := "minecraft:overworld"
	if data.WorldName != nil {
		worldName = *data.WorldName
	}
	hashedSeed := int64(0)
	if data.HashedSeed != nil {
		hashedSeed = *data.HashedSeed
	}
	gameMode := byte(0)
	if data.GameMode != nil {
		gameMode = *data.GameMode
	}
	previousGameMode := byte(0xFF)
	if data.PreviousGameMode != nil {
		previousGameMode = *data.PreviousGameMode
	}
	isDebug := false
	if data.IsDebug != nil {
		isDebug = *data.IsDebug
	}
	isFlat := false
	if data.IsFlat != nil {
		isFlat = *data.IsFlat
	}

	return play.SendRespawn(client, clientInfo, conn, play.RespawnParams{
		Dimension:        dimension,
		WorldName:        worldName,
		HashedSeed:       hashedSeed,
		GameMode:         gameMode,
		PreviousGameMode: previousGameMode,
		IsDebug:          isDebug,
		IsFlat:           isFlat,
	})
}

// keepAliveLoop sends keep-alive and title packets until stopped.
func keepAliveLoop(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn, config *Config, stop <-chan struct{}) error {
	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return nil
		case <-ticker.C:
			TraceLog(TargetLobby, "Sending keep-alive sequence to lobby client")

			// Send keep alive and title packets
			if err := play.SendKeepAlive(client, clientInfo, conn); err != nil {
				return err
			}
			if err := play.SendTitle(client, clientInfo, conn, config.Join.Lobby.Message); err != nil {
				return err
			}

			// TODO: verify we receive correct keep alive response
		}
	}
}

// stageWait waits for the server to come online while sending keep-alive
// packets.
func stageWait(client *proto.Client, clientInfo *proto.ClientInfo, server *ServerState, config *Config, conn net.Conn) error {
	// Start keep-alive loop
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- keepAliveLoop(client, clientInfo, conn, config, stop)
	}()

	// Wait for server to come online
	err := waitForServer(server, config)

	// Stop the keep-alive loop before the caller takes over the connection
	close(stop)

	return err
}

// waitForServer waits for the server to come online.
//
// Returns nil once the server is online, error if waiting failed.
func waitForServer(server *ServerState, config *Config) error {
	DebugLog(TargetLobby, "Waiting on server to come online...")

	// A task to wait for suitable server state
	done := make(chan bool, 1)
	go func() {
		stateCh := server.Subscribe()
		for state := range stateCh {
			switch state {
			// Still waiting on server start
			case StateStarting:
				TraceLog(TargetLobby, "Server not ready, holding client for longer")
				continue

			// Server started, start relaying and proxy
			case StateStarted:
				done <- true
				return

			// Server stopping, this shouldn't happen, kick
			case StateStopping, StateStopped:
				done <- false
				return
			}
		}
		done <- false
	}()

	// Wait for server state with timeout
	timeout := time.Duration(config.Join.Lobby.Timeout) * time.Second
	select {
	case ok := <-done:
		if ok {
			DebugLog(TargetLobby, "Server ready for lobby client")
			return nil
		}
	case <-time.After(timeout):
		WarnLog(TargetLobby, "Lobby client waiting for server to come online reached timeout of %ds", int(timeout.Seconds()))
	}

	return errors.New("failed to wait for server")
}

// connectToServer creates a connection to the server, with timeout.
//
// This initializes the connection to the play state. Client details are
// used.
func connectToServer(clientInfo *proto.ClientInfo, inbound net.Conn, config *Config) (*proto.Client, net.Conn, []byte, error) {
	return connectToServerWithForge(clientInfo, inbound, config, nil, nil)
}

// connectToServerWithForge initializes a backend login. A non-nil relay keeps
// the actual lobby client in LOGIN and performs the FML3 exchange live; nil
// uses cached FML3 responses (or the legacy FML2 synthesizer).
func connectToServerWithForge(clientInfo *proto.ClientInfo, inbound net.Conn, config *Config, server *ServerState, relay *forgeLoginRelay) (*proto.Client, net.Conn, []byte, error) {
	type result struct {
		client *proto.Client
		conn   net.Conn
		buf    []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		client, conn, buf, err := connectToServerNoTimeout(clientInfo, inbound, config, server, relay)
		done <- result{client, conn, buf, err}
	}()

	select {
	case r := <-done:
		return r.client, r.conn, r.buf, r.err
	case <-time.After(serverConnectTimeout):
		ErrorLog(TargetLobby, "Creating new server connection for lobby client timed out after %ds", int(serverConnectTimeout.Seconds()))
		return nil, nil, nil, errors.New("server connect timeout")
	}
}

// connectToServerNoTimeout initializes a connection to the play state.
func connectToServerNoTimeout(clientInfo *proto.ClientInfo, inbound net.Conn, config *Config, server *ServerState, relay *forgeLoginRelay) (*proto.Client, net.Conn, []byte, error) {
	// Open connection
	outbound, err := net.DialTimeout("tcp", config.Server.Address.String(), 30*time.Second)
	if err != nil {
		return nil, nil, nil, err
	}

	// Add proxy header
	if config.Server.SendProxyV2 {
		TraceLog(TargetLobby, "Sending client proxy header for server connection")
		header, err := StreamProxyHeader(inbound.RemoteAddr(), inbound.LocalAddr())
		if err != nil {
			outbound.Close()
			return nil, nil, nil, err
		}
		if _, err := outbound.Write(header); err != nil {
			outbound.Close()
			return nil, nil, nil, err
		}
	}

	// Construct temporary server client
	tmpClient := proto.DummyClient()
	tmpClient.SetState(proto.ClientStateLogin)

	// Replay client handshake packet
	if clientInfo.Handshake == nil {
		outbound.Close()
		return nil, nil, nil, errors.New("client handshake unknown")
	}
	w := proto.NewPacketWriter()
	clientInfo.Handshake.Encode(w)
	if err := writePacketTo(tmpClient, outbound, proto.NewRawPacket(proto.PacketHandshake, w.Bytes())); err != nil {
		outbound.Close()
		return nil, nil, nil, err
	}

	// Request login start
	if clientInfo.Username == nil {
		outbound.Close()
		return nil, nil, nil, errors.New("client username unknown")
	}
	lw := proto.NewPacketWriter()
	lw.WriteString(*clientInfo.Username)
	if protocol := clientInfo.GetProtocol(); protocol != nil && *protocol >= 759 {
		lw.WriteBool(clientInfo.Profile != nil)
		if clientInfo.Profile != nil {
			lw.Write(clientInfo.Profile.UUID[:])
		}
	}
	if err := writePacketTo(tmpClient, outbound, proto.NewRawPacket(proto.PacketServerLoginStart, lw.Bytes())); err != nil {
		outbound.Close()
		return nil, nil, nil, err
	}

	// Incoming buffer
	var buf []byte
	forgeReplayIndex := 0

	profileForwarded := false
	for {
		// Read packet from stream
		packet, _, more, err := proto.ReadPacket(tmpClient, &buf, outbound)
		if err != nil {
			if errors.Is(err, proto.ErrMalformedPacket) {
				ErrorLog(TargetLobby, "Closing connection, error occurred")
			}
			break
		}
		if !more {
			break
		}

		// Grab client state
		clientState := tmpClient.State()

		// Catch set compression
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketClientSetCompression {
			// Decode compression packet
			setCompression, err := proto.DecodeSetCompression(packet.Data)
			if err != nil {
				break
			}

			// Client and server compression threshold should match
			if setCompression.Threshold != proto.CompressionThreshold {
				ErrorLog(TargetLobby, "Compression threshold sent to lobby client does not match threshold from server, this may cause errors (client: %d, server: %d)", proto.CompressionThreshold, setCompression.Threshold)
			}

			// During a hot FML3 bootstrap the compression negotiation must reach
			// the real client before any relayed login-plugin request.
			if relay != nil {
				if relay.client.IsCompressed() {
					if relay.client.Compressed() != setCompression.Threshold {
						ErrorLog(TargetForge, "Backend compression threshold changed during hot FML3 bootstrap (client: %d, backend: %d)", relay.client.Compressed(), setCompression.Threshold)
						break
					}
				} else {
					if err := writePacketTo(relay.client, relay.inbound, packet); err != nil {
						break
					}
					relay.client.SetCompression(setCompression.Threshold)
				}
			}

			// Set client compression
			tmpClient.SetCompression(setCompression.Threshold)
			continue
		}

		// Catch encryption requests
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketClientEncryptionRequest {
			ErrorLog(TargetLobby, "Got encryption request from server, this is unsupported. Server must be in offline mode to use lobby.")
			break
		}

		// Hijack login plugin request
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketClientLoginPluginRequest {
			// Decode login plugin request
			pluginRequest, err := proto.DecodeLoginPluginRequest(packet.Data)
			if err != nil {
				ErrorLog(TargetLobby, "Failed to decode backend login plugin request: %v", err)
				break
			}
			TraceLog(TargetLobby, "Backend login plugin request id=%d channel=%q bytes=%d", pluginRequest.MessageID, pluginRequest.Channel, len(pluginRequest.Data))

			if pluginRequest.Channel == velocityPlayerInfoChannel {
				if err := respondVelocityForwarding(tmpClient, outbound, pluginRequest, clientInfo.Profile, inbound.RemoteAddr(), config); err != nil {
					ErrorLog(TargetLobby, "Authenticated profile forwarding failed: %v", err)
					break
				}
				profileForwarded = true
				continue
			}

			// A real modded login may interleave mod-specific queries with the
			// fml:loginwrapper exchange. The authenticated hot bootstrap must
			// preserve that complete ordered sequence; rejecting one query leaves
			// the Forge client and backend in different handshake states.
			if relay != nil {
				TraceLog(TargetForge, "Handling authenticated bootstrap login plugin channel %q", pluginRequest.Channel)
				if err := relayForgeLoginRequest(relay, tmpClient, outbound, pluginRequest); err != nil {
					ErrorLog(TargetForge, "Login plugin client relay failed on channel %q: %v", pluginRequest.Channel, err)
					break
				}
				continue
			}

			if server != nil && pluginRequest.Channel == forgeChannelLoginWrapper {
				cache := server.ForgeLoginCache()
				if len(cache) != 0 {
					if forgeReplayIndex >= len(cache) {
						// A backend asking for more exchanges than the captured
						// sequence is answered negatively rather than synthesized.
						if err := writeForgeLoginResponse(tmpClient, outbound, pluginRequest.MessageID, false, nil); err != nil {
							break
						}
						continue
					}
					exchange := cache[forgeReplayIndex]
					if !cachedForgeRequestMatches(exchange, pluginRequest) {
						ErrorLog(TargetForge, "Cached login plugin sequence mismatch at %d: backend=%q", forgeReplayIndex, pluginRequest.Channel)
						break
					}
					forgeReplayIndex++
					if exchange.NoResponse {
						continue
					}
					if err := replayForgeLoginResponse(tmpClient, outbound, &exchange, pluginRequest.MessageID); err != nil {
						break
					}
					continue
				}
			}

			// Match modern proxy behavior for unrelated login queries: reject the
			// individual query and keep negotiating until Forge sends its
			// fml:loginwrapper exchange. Feeding a protocol-763 query into the
			// legacy FML2 synthesizer tears down the backend connection.
			if config.Server.Forge && isForgeFML3(clientInfo) {
				TraceLog(TargetForge, "Rejecting unsupported protocol-763 login plugin channel %q", pluginRequest.Channel)
				if err := writeForgeLoginResponse(tmpClient, outbound, pluginRequest.MessageID, false, nil); err != nil {
					ErrorLog(TargetForge, "Failed to reject unsupported login plugin channel %q: %v", pluginRequest.Channel, err)
					break
				}
				continue
			}

			// Respond with Forge messages
			if config.Server.Forge {
				TraceLog(TargetLobby, "Got login plugin request from server, responding with Forge reply")

				// Respond to Forge login plugin request
				if err := respondLoginPluginRequest(tmpClient, outbound, pluginRequest); err != nil {
					ErrorLog(TargetForge, "Failed legacy Forge reply on channel %q: %v", pluginRequest.Channel, err)
					break
				}
				continue
			}

			WarnLog(TargetLobby, "Got unexpected login plugin request from server, you may need to enable Forge support")

			// Write unsuccessful login plugin response
			rw := proto.NewPacketWriter()
			rw.WriteVarInt(pluginRequest.MessageID)
			rw.WriteBool(false)
			response := proto.NewRawPacket(proto.PacketServerLoginPluginResponse, rw.Bytes())
			if err := writePacketTo(tmpClient, outbound, response); err != nil {
				break
			}

			continue
		}

		// Hijack login success
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketClientLoginSuccess {
			if config.Auth.OnlineMode && !profileForwarded {
				ErrorLog(TargetLobby, "Backend accepted login without requesting authenticated profile forwarding")
				break
			}
			TraceLog(TargetLobby, "Got login success from server connection, change to play mode")

			if relay != nil {
				if err := writePacketTo(relay.client, relay.inbound, packet); err != nil {
					break
				}
				relay.commit()
				relay.client.SetState(proto.ClientStatePlay)
			}

			// Switch to play state
			tmpClient.SetState(proto.ClientStatePlay)

			// Server must enable compression if enabled for client
			if tmpClient.IsCompressed() != (proto.CompressionThreshold >= 0) {
				ErrorLog(TargetLobby, "Compression enabled for lobby client while the server did not, this will cause errors")
			}

			return tmpClient, outbound, buf, nil
		}

		// Hijack disconnect
		if clientState == proto.ClientStateLogin && packet.ID == proto.PacketClientLoginDisconnect {
			ErrorLog(TargetLobby, "Got disconnect from server connection")
			// TODO: report/forward error to client
			break
		}

		// Show unhandled packet warning
		DebugLog(TargetLobby, "Got unhandled packet from server in connect_to_server:")
		DebugLog(TargetLobby, "- State: %v", clientState)
		DebugLog(TargetLobby, "- Packet ID: 0x%02X (%d)", packet.ID, packet.ID)
	}

	// Gracefully close connection
	_ = CloseTCPStream(outbound)

	return nil, nil, nil, errors.New("server connection ended unexpectedly")
}

// lobbyRouteProxy routes the lobby client through the proxy to the real
// server. This handoff is synchronous: LobbyServe owns the connection and its
// status admission until the proxied session has ended.
//
// `inboundQueue` is used for data already received from the server, that
// needs to be pushed to the client.
func lobbyRouteProxy(inbound, outbound net.Conn, inboundQueue []byte) {
	if err := ProxyInboundOutboundWithQueue(inbound, outbound, inboundQueue, nil); err != nil {
		WarnLog(Target, "Failed to proxy: %v", err)
	}
}

// drainStream drains the given connection until nothing is left, voiding all
// data.
func drainStream(conn net.Conn) {
	drainBuf := make([]byte, 8*1024)
	// Set a read deadline in the past to make reads non-blocking
	_ = conn.SetReadDeadline(time.Now())
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()
	for {
		n, err := conn.Read(drainBuf)
		if err != nil {
			// WouldBlock / timeout means nothing left to drain
			return
		}
		if n == 0 {
			return
		}
	}
}
