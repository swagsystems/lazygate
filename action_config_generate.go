package main

// Config generate action, mirroring lazymc's action/config_generate.rs.

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"lazymc/util"
)

//go:embed res/lazymc.toml
var configTemplate string

// invokeConfigGenerate generates a config file.
func invokeConfigGenerate(args parsedArgs) {
	// Get config path, attempt to canonicalize
	path := args.config
	if p, err := filepath.Abs(path); err == nil {
		path = p
	}

	// Confirm to overwrite if it exists
	if isRegularFile(path) {
		def := true
		if !util.PromptYes(fmt.Sprintf("Config file already exists, overwrite?\nPath: %s", path), &def) {
			util.Quit()
		}
	}

	// Generate file
	if err := os.WriteFile(path, []byte(configTemplate), 0o644); err != nil {
		hints := util.NewErrorHintsBuilder().Build()
		util.QuitError(fmt.Errorf("Failed to generate config file: %w", err), hints)
	}

	fmt.Fprintf(os.Stderr, "Config saved at: %s\n", path)
}
