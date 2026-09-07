package main

// Log adapters for the root package.
//
// The Rust code logs with target strings like "lazymc", "lazymc::monitor".
// These constants keep those targets stable.

import "lazymc/util"

// Log targets.
const (
	Target        = "lazymc"
	TargetConfig  = "lazymc::config"
	TargetMonitor = "lazymc::monitor"
	TargetProbe   = "lazymc::probe"
	TargetForge   = "lazymc::forge"
	TargetRcon    = "lazymc::rcon"
	TargetLobby   = "lazymc::lobby"
	TargetStatus  = "lazymc::status"
)

// TraceLog logs at trace level.
func TraceLog(target, format string, args ...interface{}) {
	util.Trace(target, format, args...)
}

// DebugLog logs at debug level.
func DebugLog(target, format string, args ...interface{}) {
	util.Debug(target, format, args...)
}

// InfoLog logs at info level.
func InfoLog(target, format string, args ...interface{}) {
	util.Info(target, format, args...)
}

// WarnLog logs at warn level.
func WarnLog(target, format string, args ...interface{}) {
	util.Warn(target, format, args...)
}

// ErrorLog logs at error level.
func ErrorLog(target, format string, args ...interface{}) {
	util.Error(target, format, args...)
}
