package main

// Process signal helpers, mirroring lazymc's os module (Unix).

import (
	"syscall"

	"lazymc/util"
)

// ForceKill force kills a process.
//
// Results in undefined behavior if the PID is invalid.
func ForceKill(pid int) bool {
	return unixSignal(pid, syscall.SIGKILL)
}

// KillGracefully gracefully kills a process.
func KillGracefully(pid int) bool {
	return unixSignal(pid, syscall.SIGTERM)
}

// Freeze freezes a process with SIGSTOP.
func Freeze(pid int) bool {
	return unixSignal(pid, syscall.SIGSTOP)
}

// Unfreeze unfreezes a process with SIGCONT.
func Unfreeze(pid int) bool {
	return unixSignal(pid, syscall.SIGCONT)
}

// unixSignal sends a signal to a process.
func unixSignal(pid int, signal syscall.Signal) bool {
	err := syscall.Kill(pid, signal)
	if err != nil {
		util.Warn("lazymc", "Sending %v signal to server failed: %v", signal, err)
		return false
	}
	return true
}
