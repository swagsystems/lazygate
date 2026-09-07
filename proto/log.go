package proto

// Minimal log adapters so proto and play packages can log without importing
// the root package (avoiding import cycles).

import "lazymc/util"

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
