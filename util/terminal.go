package util

import (
	"os"
	"sync"
)

// isTerminal reports whether stderr is attached to a terminal.
var isTerminal = func() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}()

var _ = sync.Once{} // keep sync import if unused elsewhere in this file
