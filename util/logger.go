package util

// Logging, mirroring lazymc's pretty_env_logger setup.
//
// - Loads `.env` from the working directory (dotenv).
// - Defaults RUST_LOG to "info" when unset.
// - Format: `[2022-04-14T19:27:23Z INFO  lazymc] message`

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is a log level.
type Level int

// Log levels.
const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelOff
)

func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "TRACE"
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "OFF"
	}
}

func (l Level) color() string {
	if !isTerminal {
		return ""
	}
	switch l {
	case LevelTrace:
		return "\x1b[35m" // magenta
	case LevelDebug:
		return "\x1b[34m" // blue
	case LevelInfo:
		return "\x1b[32m" // green
	case LevelWarn:
		return "\x1b[33m" // yellow
	case LevelError:
		return "\x1b[31m" // red
	default:
		return ""
	}
}

var loggerOnce sync.Once

// A parsed RUST_LOG directive.
type logDirective struct {
	target string // empty = global default
	level  Level
}

var logFilter = struct {
	sync.RWMutex
	global     Level
	directives []logDirective
}{
	global: LevelInfo,
}

// InitLog initializes the logger, loading .env and RUST_LOG.
func InitLog() {
	loggerOnce.Do(func() {
		// Load .env variables
		_ = loadDotenv(".env")

		// Set default log level if none is set
		level := os.Getenv("RUST_LOG")
		if level == "" {
			level = "info"
		}

		parseLogFilter(level)
	})
}

// parseLogFilter parses RUST_LOG style directives.
func parseLogFilter(spec string) {
	logFilter.Lock()
	defer logFilter.Unlock()

	logFilter.directives = nil
	logFilter.global = LevelInfo

	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		var target string
		var levelStr string
		if idx := strings.Index(part, "="); idx >= 0 {
			target = part[:idx]
			levelStr = part[idx+1:]
		} else {
			levelStr = part
		}

		level, ok := parseLevel(levelStr)
		if !ok {
			continue
		}

		if target == "" {
			logFilter.global = level
		} else {
			logFilter.directives = append(logFilter.directives, logDirective{target: target, level: level})
		}
	}
}

func parseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, true
	case "debug":
		return LevelDebug, true
	case "info":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error":
		return LevelError, true
	case "off":
		return LevelOff, true
	}
	return LevelInfo, false
}

// enabled reports whether a log statement with the given target and level
// should be printed. Targets match by prefix (env_logger semantics: a
// directive for "lazymc" matches "lazymc::monitor" too).
func enabled(level Level, target string) bool {
	logFilter.RLock()
	defer logFilter.RUnlock()

	// Per-target directives take precedence over the global default
	for _, d := range logFilter.directives {
		if d.target == target || strings.HasPrefix(target, d.target+"::") {
			return level >= d.level
		}
	}
	return level >= logFilter.global
}

// Log prints a log line when the level/target pass the filter.
func Log(level Level, target, format string, args ...interface{}) {
	if !enabled(level, target) {
		return
	}

	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}

	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	levelStr := level.String()
	padded := levelStr
	if len(padded) < 5 {
		padded = strings.Repeat(" ", 5-len(padded)) + padded
	}

	levelColored := levelStr
	if level.color() != "" {
		levelColored = level.color() + levelStr + ansiReset
	}

	fmt.Fprintf(os.Stderr, "[%s %s %s] %s\n", ts, levelColored, target, msg)
}

// Trace logs at trace level.
func Trace(target, format string, args ...interface{}) { Log(LevelTrace, target, format, args...) }

// Debug logs at debug level.
func Debug(target, format string, args ...interface{}) { Log(LevelDebug, target, format, args...) }

// Info logs at info level.
func Info(target, format string, args ...interface{}) { Log(LevelInfo, target, format, args...) }

// Warn logs at warn level.
func Warn(target, format string, args ...interface{}) { Log(LevelWarn, target, format, args...) }

// Error logs at error level.
func Error(target, format string, args ...interface{}) { Log(LevelError, target, format, args...) }

// loadDotenv loads KEY=VALUE pairs from the given file into the environment.
//
// Supports `export` prefixes, quoted values, and comments, matching the
// dotenv crate behavior closely enough for lazymc's usage.
func loadDotenv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Strip export prefix
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(line[len("export "):])
		}

		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		// Strip surrounding quotes
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}

		// Skip if already set in the environment
		if _, exists := os.LookupEnv(key); exists {
			continue
		}

		_ = os.Setenv(key, val)
	}

	return nil
}
