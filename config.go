package main

// Configuration, mirroring lazymc's config.rs 1:1.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"lazymc/proto"
	"lazymc/util"
)

// Default configuration file location.
const ConfigFile = "lazymc.toml"

// Configuration version user should be using, or a warning will be shown.
const configVersion = "0.2.11"

// Config is the full lazymc configuration.
type Config struct {
	// Configuration path if known.
	//
	// Should be used as base directory for filesystem operations.
	Path string

	// Public configuration.
	Public Public

	// Server configuration.
	Server Server

	// Authentication and trusted backend forwarding configuration.
	Auth Auth

	// Time configuration.
	Time Time

	// MOTD configuration.
	Motd Motd

	// Join configuration.
	Join Join

	// Lockout feature.
	Lockout Lockout

	// RCON configuration.
	Rcon Rcon

	// Advanced configuration.
	Advanced Advanced

	// Config configuration.
	Config ConfigConfig

	// admission is initialized by Service and is intentionally not loaded
	// from TOML. It bounds unauthenticated status and proxy admission.
	admission *connectionAdmission
}

// Auth configures online client authentication and authenticated profile
// forwarding to a loopback-only backend.
type Auth struct {
	// Authenticate joining lobby clients with the Minecraft session service.
	OnlineMode bool `toml:"online_mode"`

	// Session server base URL. The production default is Mojang's session
	// service; the override exists for deterministic tests.
	SessionServer string `toml:"session_server"`

	// File containing the shared HMAC secret used for Velocity modern
	// forwarding. If empty, the forwarding-secret systemd credential is used.
	ForwardingSecretFile string `toml:"forwarding_secret_file"`
}

// SocketAddr is a resolved IP:port address, supporting hostname resolution
// like Rust's ToSocketAddrs.
type SocketAddr struct {
	IP   net.IP
	Port int
}

// UnmarshalTOML resolves an address string.
func (s *SocketAddr) UnmarshalTOML(v interface{}) error {
	raw, ok := v.(string)
	if !ok {
		return fmt.Errorf("expected a string address")
	}
	resolved, err := resolveSocketAddr(raw)
	if err != nil {
		return err
	}
	s.IP = resolved.IP
	s.Port = resolved.Port
	return nil
}

// String renders the address as ip:port.
func (s SocketAddr) String() string {
	return net.JoinHostPort(s.IP.String(), strconv.Itoa(s.Port))
}

// Public configuration.
type Public struct {
	// Public address. IP and port users connect to.
	Address SocketAddr `toml:"address"`

	// Minecraft protocol version name hint.
	Version string `toml:"version"`

	// Minecraft protocol version hint.
	Protocol uint32 `toml:"protocol"`
}

// Server configuration.
type Server struct {
	// Server directory. Defaults to current directory.
	Directory string `toml:"directory"`

	// Start command.
	Command string `toml:"command"`

	// Delegate the backend lifecycle to an external controller. In this mode
	// Command is a bounded controller helper, not the backend process itself;
	// socket observation is authoritative and lazymc never stops or signals the
	// backend.
	ControllerManaged bool `toml:"controller_managed"`

	// Server address. Internal address of the server to proxy to.
	Address SocketAddr `toml:"address"`

	// Freeze the server process instead of restarting it when no players
	// online, making it start up faster. Only works on Unix.
	FreezeProcess bool `toml:"freeze_process"`

	// Immediately wake server when starting lazymc.
	WakeOnStart bool `toml:"wake_on_start"`

	// Immediately wake server after crash.
	WakeOnCrash bool `toml:"wake_on_crash"`

	// Probe required server details when starting lazymc, wakes server on
	// start.
	ProbeOnStart bool `toml:"probe_on_start"`

	// Whether this server runs forge.
	Forge bool `toml:"forge"`

	// Optional bounded Forge server-list metadata snapshot. This lets a cold
	// gateway advertise the real modded endpoint before Java has been woken.
	ForgeStatusFile string `toml:"forge_status_file"`

	// Server starting timeout. Force kill server process if it takes longer.
	StartTimeout uint32 `toml:"start_timeout"`

	// Server stopping timeout. Force kill server process if it takes longer.
	StopTimeout uint32 `toml:"stop_timeout"`

	// To wake server, user must be in server whitelist if enabled on server.
	WakeWhitelist bool `toml:"wake_whitelist"`

	// Block banned IPs as listed in banned-ips.json in server directory.
	BlockBannedIps bool `toml:"block_banned_ips"`

	// Drop connections from banned IPs.
	DropBannedIps bool `toml:"drop_banned_ips"`

	// Add HAProxy v2 header to proxied connections.
	SendProxyV2 bool `toml:"send_proxy_v2"`
}

// ServerDirectory returns the server directory, relative to the config
// directory if known.
//
// Matches Rust's Path::join semantics: an absolute directory replaces the
// config directory base.
func ServerDirectory(config *Config) *string {
	dir := config.Server.Directory
	if config.Path != "" && !filepath.IsAbs(dir) {
		configDir := filepath.Dir(config.Path)
		full := filepath.Join(configDir, dir)
		return &full
	}
	return &dir
}

// Time configuration.
type Time struct {
	// Sleep after number of seconds.
	SleepAfter uint32

	// Minimum time in seconds to stay online when server is started.
	MinOnlineTime uint32
}

// UnmarshalTOML handles the minimum_online_time alias.
func (t *Time) UnmarshalTOML(v interface{}) error {
	m, ok := v.(map[string]interface{})
	if !ok {
		return fmt.Errorf("time must be a table")
	}
	if v, ok := m["sleep_after"].(int64); ok {
		t.SleepAfter = uint32(v)
	}
	if v, ok := m["min_online_time"].(int64); ok {
		t.MinOnlineTime = uint32(v)
	}
	if v, ok := m["minimum_online_time"].(int64); ok {
		t.MinOnlineTime = uint32(v)
	}
	return nil
}

// MOTD configuration.
type Motd struct {
	// MOTD when server is sleeping.
	Sleeping string `toml:"sleeping"`

	// MOTD when server is starting.
	Starting string `toml:"starting"`

	// MOTD when server is stopping.
	Stopping string `toml:"stopping"`

	// Use MOTD from Minecraft server once known.
	FromServer bool `toml:"from_server"`
}

// Method is a join method type.
type Method string

// Join methods.
const (
	MethodKick    Method = "kick"
	MethodHold    Method = "hold"
	MethodForward Method = "forward"
	MethodLobby   Method = "lobby"
)

// UnmarshalTOML parses a join method name.
func (m *Method) UnmarshalTOML(v interface{}) error {
	raw, ok := v.(string)
	if !ok {
		return fmt.Errorf("invalid join method")
	}
	switch strings.ToLower(raw) {
	case "kick":
		*m = MethodKick
	case "hold":
		*m = MethodHold
	case "forward":
		*m = MethodForward
	case "lobby":
		*m = MethodLobby
	default:
		return fmt.Errorf("unknown join method %q", raw)
	}
	return nil
}

// Join configuration.
type Join struct {
	// Join methods.
	Methods []Method `toml:"methods"`

	// Join kick configuration.
	Kick JoinKick `toml:"kick"`

	// Join hold configuration.
	Hold JoinHold `toml:"hold"`

	// Join forward configuration.
	Forward JoinForward `toml:"forward"`

	// Join lobby configuration.
	Lobby JoinLobby `toml:"lobby"`
}

// JoinKick configuration.
type JoinKick struct {
	// Kick message when server is starting.
	Starting string `toml:"starting"`

	// Kick message when server is stopping.
	Stopping string `toml:"stopping"`
}

// JoinHold configuration.
type JoinHold struct {
	// Hold client for number of seconds on connect while server starts.
	Timeout uint32 `toml:"timeout"`
}

// JoinForward configuration.
type JoinForward struct {
	// IP and port to forward to.
	Address SocketAddr `toml:"address"`

	// Add HAProxy v2 header to proxied connections.
	SendProxyV2 bool `toml:"send_proxy_v2"`
}

// JoinLobby configuration.
type JoinLobby struct {
	// Hold client in lobby for number of seconds on connect while server
	// starts.
	Timeout uint32 `toml:"timeout"`

	// Message banner in lobby shown to client.
	Message string `toml:"message"`

	// Sound effect to play when server is ready.
	ReadySound *string `toml:"ready_sound"`
}

// Lockout configuration.
type Lockout struct {
	// Enable to prevent everybody from connecting through lazymc.
	Enabled bool `toml:"enabled"`

	// Kick players with the following message.
	Message string `toml:"message"`
}

// Rcon configuration.
type Rcon struct {
	// Enable sleeping server through RCON.
	Enabled bool `toml:"enabled"`

	// Server RCON port.
	Port uint16 `toml:"port"`

	// Server RCON password.
	Password string `toml:"password"`

	// Randomize server RCON password on each start.
	RandomizePassword bool `toml:"randomize_password"`

	// Add HAProxy v2 header to RCON connections.
	SendProxyV2 bool `toml:"send_proxy_v2"`
}

// Advanced configuration.
type Advanced struct {
	// Rewrite server.properties.
	RewriteServerProperties bool `toml:"rewrite_server_properties"`
}

// ConfigConfig configuration.
type ConfigConfig struct {
	// Configuration for lazymc version.
	Version *string `toml:"version"`
}

// defaultConfig returns a config with all Rust defaults applied.
func defaultConfig() Config {
	address := SocketAddr{IP: net.IPv4zero, Port: 25565}
	serverAddress := SocketAddr{IP: net.IPv4(127, 0, 0, 1), Port: 25566}
	forwardAddress := SocketAddr{IP: net.IPv4(127, 0, 0, 1), Port: 25565}
	readySound := "block.note_block.chime"

	return Config{
		Public: Public{
			Address:  address,
			Version:  proto.ProtoDefaultVersion,
			Protocol: proto.ProtoDefaultProtocol,
		},
		Server: Server{
			Directory:      ".",
			Address:        serverAddress,
			FreezeProcess:  true,
			StartTimeout:   300,
			StopTimeout:    150,
			WakeWhitelist:  true,
			BlockBannedIps: true,
		},
		Auth: Auth{
			SessionServer: "https://sessionserver.mojang.com",
		},
		Time: Time{
			SleepAfter:    60,
			MinOnlineTime: 60,
		},
		Motd: Motd{
			Sleeping: "☠ Server is sleeping\n§2☻ Join to start it up",
			Starting: "§2☻ Server is starting...\n§7⌛ Please wait...",
			Stopping: "☠ Server going to sleep...\n⌛ Please wait...",
		},
		Join: Join{
			Methods: []Method{MethodHold, MethodKick},
			Kick: JoinKick{
				Starting: "Server is starting... §c♥§r\n\nThis may take some time.\n\nPlease try to reconnect in a minute.",
				Stopping: "Server is going to sleep... §7☠§r\n\nPlease try to reconnect in a minute to wake it again.",
			},
			Hold:    JoinHold{Timeout: 25},
			Forward: JoinForward{Address: forwardAddress},
			Lobby: JoinLobby{
				Timeout:    10 * 60,
				Message:    "§2Server is starting\n§7⌛ Please wait...",
				ReadySound: &readySound,
			},
		},
		Lockout: Lockout{
			Message: "Server is closed §7☠§r\n\nPlease come back another time.",
		},
		Rcon: Rcon{
			Enabled:           false, // true on Windows
			Port:              25575,
			RandomizePassword: true,
		},
		Advanced: Advanced{
			RewriteServerProperties: true,
		},
	}
}

// resolveSocketAddr resolves an "ip:port" or "host:port" address.
func resolveSocketAddr(addr string) (SocketAddr, error) {
	// Match Rust's ToSocketAddrs: try to resolve, use first result.
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return SocketAddr{}, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return SocketAddr{}, err
	}

	// Resolve host if not an IP literal
	ip := net.ParseIP(host)
	if ip == nil {
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return SocketAddr{}, fmt.Errorf("invalid IP or resolvable host and port")
		}
		ip = ips[0]
	}

	return SocketAddr{IP: ip, Port: port}, nil
}

// LoadConfig loads config from file, based on CLI arguments.
//
// Quits with an error message on failure.
func LoadConfig(path string) Config {
	// Canonicalize path
	if p, err := filepath.Abs(path); err == nil {
		path = p
	}

	// Ensure configuration file exists
	if !isRegularFile(path) {
		hints := util.NewErrorHintsBuilder().Config().ConfigGenerate().Build()
		util.QuitErrorMsg(fmt.Sprintf("Config file does not exist: %s", path), hints)
	}

	// Load config
	config, err := ConfigLoad(path)
	if err != nil {
		hints := util.NewErrorHintsBuilder().Config().ConfigTest().Build()
		util.QuitError(fmt.Errorf("Failed to load config: %w", err), hints)
	}

	return config
}

// ConfigLoad loads configuration from file.
func ConfigLoad(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	config := defaultConfig()
	md, err := toml.Decode(string(data), &config)
	if err != nil {
		return Config{}, err
	}

	// The server table is required, with a command
	if !md.IsDefined("server") {
		return Config{}, fmt.Errorf("missing field `server`")
	}
	if !md.IsDefined("server", "command") {
		return Config{}, fmt.Errorf("missing field `command`")
	}

	// Show warning if config version is problematic
	if config.Config.Version == nil {
		WarnLog(TargetConfig, "Config version unknown, it may be outdated")
	} else {
		cmp, err := compareVersions(*config.Config.Version, configVersion)
		if err != nil {
			WarnLog(TargetConfig, "Config version is invalid, you may need to update it")
		} else if cmp < 0 {
			WarnLog(TargetConfig, "Config is for older lazymc version, you may need to update it")
		}
	}

	config.Path = path

	return config, nil
}

// compareVersions compares two dotted version strings, returning <0, 0, >0.
func compareVersions(a, b string) (int, error) {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	if len(pa) == 0 || len(pb) == 0 {
		return 0, fmt.Errorf("invalid version")
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var va, vb int64
		if i < len(pa) {
			va, _ = strconv.ParseInt(pa[i], 10, 64)
		}
		if i < len(pb) {
			vb, _ = strconv.ParseInt(pb[i], 10, 64)
		}
		if va < vb {
			return -1, nil
		}
		if va > vb {
			return 1, nil
		}
	}
	return 0, nil
}

func isRegularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
