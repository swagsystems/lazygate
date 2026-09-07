package main

// CLI argument parsing and help output, matching clap's output byte-for-byte.

import (
	"fmt"
	"os"
	"strings"
)

// App metadata, matching the Cargo package info.
const (
	appName        = "lazymc"
	appVersion     = "0.2.11"
	appAuthor      = "Tim Visee <3a4fb3964f@sinenomine.email>"
	appDescription = "Put your Minecraft server to rest when idle."
)

// parsedArgs holds the parsed CLI invocation.
type parsedArgs struct {
	// Subcommand: "start" or "config".
	subcommand string

	// Config sub-subcommand: "generate" or "test".
	subSubcommand string

	// Config file path.
	config string
}

// cliError exits with clap's error style and code 2.
func cliError(msg, usage string) {
	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	if usage != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", usage)
	}
	fmt.Fprintf(os.Stderr, "\nFor more information, try '--help'.\n")
	os.Exit(2)
}

func usageLine(cmd string) string {
	switch cmd {
	case "start":
		return "Usage: " + appName + " start [OPTIONS]"
	case "config":
		return "Usage: " + appName + " config [OPTIONS] <COMMAND>"
	case "config generate":
		return "Usage: " + appName + " config generate [OPTIONS]"
	case "config test":
		return "Usage: " + appName + " config test [OPTIONS]"
	default:
		return "Usage: " + appName + " [OPTIONS] [COMMAND]"
	}
}

func printHelp(cmd string) {
	switch cmd {
	case "start":
		fmt.Printf("Start lazymc and server (default)\n\n%s\n\nOptions:\n  -c, --config <FILE>  Use config file [default: lazymc.toml]\n  -h, --help           Print help\n", usageLine(cmd))
	case "config":
		fmt.Printf(`Config actions

%s

Commands:
  generate  Generate config
  test      Test config
  help      Print this message or the help of the given subcommand(s)

Options:
  -c, --config <FILE>  Use config file [default: lazymc.toml]
  -h, --help           Print help
`, usageLine(cmd))
	case "config generate":
		fmt.Printf("Generate config\n\n%s\n\nOptions:\n  -c, --config <FILE>  Use config file [default: lazymc.toml]\n  -h, --help           Print help\n", usageLine(cmd))
	case "config test":
		fmt.Printf("Test config\n\n%s\n\nOptions:\n  -c, --config <FILE>  Use config file [default: lazymc.toml]\n  -h, --help           Print help\n", usageLine(cmd))
	default:
		fmt.Printf(`%s

%s

Commands:
  start   Start lazymc and server (default)
  config  Config actions
  help    Print this message or the help of the given subcommand(s)

Options:
  -c, --config <FILE>  Use config file [default: lazymc.toml]
  -h, --help           Print help
  -V, --version        Print version
`, appDescription, usageLine(cmd))
	}
}

// parseArgs parses program arguments, mirroring the clap app.
//
// Exits with an error on unknown input, or prints help/version.
func parseArgs(args []string) parsedArgs {
	out := parsedArgs{config: ConfigFile}

	// Parse global flags and positional args (flags may appear anywhere)
	var positional []string

	i := 0
	for i < len(args) {
		arg := args[i]

		switch {
		case arg == "-h" || arg == "--help":
			// Help for the current context (subcommand if known so far)
			cmd := "root"
			if len(positional) > 0 {
				cmd = positional[0]
				if cmd == "config" && len(positional) > 1 {
					cmd = "config " + positional[1]
				}
			}
			printHelp(cmd)
			os.Exit(0)
		case arg == "-V" || arg == "--version":
			fmt.Printf("%s %s\n", appName, appVersion)
			os.Exit(0)
		case arg == "-c" || arg == "--config" || arg == "--cfg":
			if i+1 >= len(args) {
				cliError("a value is required for '--config <FILE>' but none was supplied", "")
			}
			i++
			out.config = args[i]
		case strings.HasPrefix(arg, "--config="):
			out.config = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c="):
			out.config = strings.TrimPrefix(arg, "-c=")
		case strings.HasPrefix(arg, "-") && arg != "-":
			cliError(fmt.Sprintf("unexpected argument '%s' found", arg), usageLine("root"))
		default:
			positional = append(positional, arg)
		}
		i++
	}

	// No subcommand: default to start
	if len(positional) == 0 {
		out.subcommand = "start"
		return out
	}

	cmd := positional[0]
	switch cmd {
	case "start", "run":
		out.subcommand = "start"
		if len(positional) > 1 {
			cliError(fmt.Sprintf("unexpected argument '%s' found", positional[1]), usageLine("start"))
		}
		return out
	case "config", "cfg":
		out.subcommand = "config"
	default:
		cliError(fmt.Sprintf("unrecognized subcommand '%s'", cmd), usageLine("root"))
	}

	// Config sub-subcommand
	if len(positional) < 2 {
		printHelp("config")
		os.Exit(2)
	}

	switch positional[1] {
	case "generate", "gen":
		out.subSubcommand = "generate"
	case "test":
		out.subSubcommand = "test"
	default:
		cliError(fmt.Sprintf("unrecognized subcommand '%s'", positional[1]), usageLine("config"))
	}

	// Extra positional arguments are an error
	if len(positional) > 2 {
		cliError(fmt.Sprintf("unexpected argument '%s' found", positional[2]), usageLine("config "+positional[1]))
	}

	return out
}
