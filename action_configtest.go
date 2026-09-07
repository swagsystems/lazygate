package main

// Config test action, mirroring lazymc's action/config_test.rs.

import (
	"fmt"
	"os"
	"path/filepath"

	"lazymc/util"
)

// invokeConfigTest tests a config file.
func invokeConfigTest(args parsedArgs) {
	// Get config path, attempt to canonicalize
	path := args.config
	if p, err := filepath.Abs(path); err == nil {
		path = p
	}

	// Ensure it exists
	if !isRegularFile(path) {
		hints := util.NewErrorHintsBuilder().Build()
		util.QuitErrorMsg(fmt.Sprintf("Config file does not exist at: %s", path), hints)
	}

	// Try to load config
	if _, err := ConfigLoad(path); err != nil {
		hints := util.NewErrorHintsBuilder().Build()
		util.QuitError(fmt.Errorf("Failed to load and parse config: %w", err), hints)
	}

	fmt.Fprintln(os.Stderr, "Config loaded successfully!")
}
