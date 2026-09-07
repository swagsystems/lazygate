// Command mockserver runs the test-only mock Minecraft server as a
// standalone process, used by the e2e tests as a stand-in for a real
// Minecraft server process that lazymc starts and stops.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"lazymc/internal/testserver"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:25566", "listen address")
	stateFile := flag.String("state", "", "state JSON file")
	captureDir := flag.String("capture", "", "directory to record first 16 bytes of each connection")
	flag.Parse()

	srv, err := testserver.NewServer(*addr, *stateFile)
	if *captureDir != "" {
		_ = os.MkdirAll(*captureDir, 0o755)
	}
	srv.CaptureDir = *captureDir
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock server: %v\n", err)
		os.Exit(1)
	}

	// Record shutdown and exit gracefully on SIGTERM/SIGINT, like a real
	// Minecraft server reacting to lazymc's stop signal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	srv.Shutdown()
}
