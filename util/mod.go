package util

// Misc helpers, mirroring lazymc's util/mod.rs.

import (
	"os"
	"path/filepath"
)

// BinName returns the name of the invoked executable.
//
// Uses the first program argument, falling back to the current executable
// name, then the crate name "lazymc".
func BinName() string {
	if len(os.Args) > 0 && os.Args[0] != "" {
		if name := filepath.Base(os.Args[0]); name != "" && name != "." && name != string(filepath.Separator) {
			return name
		}
	}
	if exe, err := os.Executable(); err == nil {
		if name := filepath.Base(exe); name != "" {
			return name
		}
	}
	return "lazymc"
}
