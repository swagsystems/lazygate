package util

// Colorized text helpers, mirroring lazymc's util/style.rs.
//
// Colors are emitted only when the output is a terminal, matching the
// behavior of the `colored` crate (auto-detection).

const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiBold   = "\x1b[1m"
)

// Highlight the given text with a color (yellow).
func Highlight(msg string) string {
	return colorize(msg, ansiYellow)
}

// HighlightError colors text as a bold red error.
func HighlightError(msg string) string {
	return colorize(msg, ansiRed+ansiBold)
}

// HighlightWarning colors text as a bold yellow warning.
func HighlightWarning(msg string) string {
	return colorize(msg, ansiYellow+ansiBold)
}

// HighlightInfo colors text cyan.
func HighlightInfo(msg string) string {
	return colorize(msg, ansiCyan)
}

// colorize wraps msg in color codes when stderr is a terminal.
func colorize(msg, code string) string {
	if !isTerminal {
		return msg
	}
	return code + msg + ansiReset
}
