package main

// Start action, mirroring lazymc's action/start.rs.

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"lazymc/mc"
	"lazymc/proto"
	"lazymc/util"
)

// RCON randomized password length.
const rconPasswordLength = 32

// invokeStart starts lazymc.
func invokeStart(args parsedArgs) {
	// Load config
	config := LoadConfig(args.config)

	// Prepare RCON if enabled
	prepareRcon(&config)

	// Rewrite server server.properties file
	rewriteServerProperties(&config)

	// Start server service
	Service(&config)
}

// prepareRcon validates and randomizes the RCON configuration.
func prepareRcon(config *Config) {
	// Skip if not enabled
	if !config.Rcon.Enabled {
		return
	}

	// Must configure RCON port different from server port
	if config.Server.Address.Port == int(config.Rcon.Port) {
		hints := util.NewErrorHintsBuilder().AddInfo("change 'rcon.port' in the config file").Build()
		util.QuitErrorMsg("RCON port cannot be the same as the server", hints)
	}

	// Must configure RCON password with no randomization
	if len(config.Rcon.Password) == 0 && !config.Rcon.RandomizePassword {
		hints := util.NewErrorHintsBuilder().
			AddInfo("change 'rcon.randomize_password' to 'true' in the config file").
			AddInfo("or change 'rcon.password' in the config file").
			Build()
		util.QuitErrorMsg("RCON password can't be empty, or enable randomization", hints)
	}

	// RCON password randomization
	if config.Rcon.RandomizePassword {
		// Must enable server.properties rewrite
		if !config.Advanced.RewriteServerProperties {
			hints := util.NewErrorHintsBuilder().
				AddInfo("change 'advanced.rewrite_server_properties' to 'true' in the config file").
				Build()
			util.QuitErrorMsg(fmt.Sprintf("You must enable %s rewrite to use RCON password randomization", mc.ServerPropertiesFile), hints)
		}

		// Randomize password
		config.Rcon.Password = generateRandomPassword()
	}
}

// generateRandomPassword generates a secure random alphanumeric password.
func generateRandomPassword() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, rconPasswordLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(fmt.Sprintf("failed to generate random password: %v", err))
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out)
}

// rewriteServerProperties rewrites the server server.properties file with the
// correct internal IP and port.
func rewriteServerProperties(config *Config) {
	// Rewrite must be enabled
	if !config.Advanced.RewriteServerProperties {
		return
	}

	// Ensure server directory is set, it must exist
	dir := ServerDirectory(config)
	if dir == nil || *dir == "" {
		WarnLog(Target, "Not rewriting %s file, server directory not configured (server.directory)", mc.ServerPropertiesFile)
		return
	}

	// Build list of changes
	changes := map[string]string{
		"server-ip":     config.Server.Address.IP.String(),
		"server-port":   fmt.Sprintf("%d", config.Server.Address.Port),
		"enable-status": "true",
		"query.port":    fmt.Sprintf("%d", config.Server.Address.Port),
	}

	// If connecting to server over non-loopback address, disable proxy
	// blocking
	if !config.Server.Address.IP.IsLoopback() {
		changes["prevent-proxy-connections"] = "false"
	}

	// Update network compression threshold for lobby mode
	for _, m := range config.Join.Methods {
		if m == MethodLobby {
			changes["network-compression-threshold"] = fmt.Sprintf("%d", proto.CompressionThreshold)
		}
	}

	// Add RCON configuration
	if config.Rcon.Enabled {
		changes["rcon.port"] = fmt.Sprintf("%d", config.Rcon.Port)
		changes["rcon.password"] = config.Rcon.Password
		changes["enable-rcon"] = "true"
	}

	// Rewrite file
	mc.RewriteServerPropertiesDir(*dir, changes)
}
