// lazymc puts your Minecraft server to rest when idle, and wakes it up when
// players connect.
//
// This is a Go rewrite of https://github.com/timvisee/lazymc with 1:1 parity.
package main

import (
	"fmt"
	"os"

	"lazymc/util"
)

// Invoke the intended action based on CLI arguments.
func main() {
	// Initialize logger
	util.InitLog()

	// Parse CLI arguments, invoke intended action
	args := parseArgs(os.Args[1:])

	switch {
	case args.subcommand == "config" && args.subSubcommand == "generate":
		invokeConfigGenerate(args)
	case args.subcommand == "config" && args.subSubcommand == "test":
		invokeConfigTest(args)
	case args.subcommand == "start":
		invokeStart(args)
	default:
		// Should not be reached; parseArgs handles help/errors.
		fmt.Fprintf(os.Stderr, "No action specified\n")
		os.Exit(1)
	}
}
