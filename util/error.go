package util

// Error printing and process exit helpers, mirroring lazymc's util/error.rs.

import (
	"fmt"
	"os"
)

// ErrorHints holds hint messages printed alongside errors.
type ErrorHints struct {
	info           []string
	config         bool
	configGenerate bool
	configTest     bool
	verbose        bool
	help           bool
}

// ErrorHintsBuilder builds an ErrorHints.
type ErrorHintsBuilder struct {
	h ErrorHints
}

// NewErrorHintsBuilder creates a builder with default hint flags.
//
// Defaults match lazymc: verbose and help hints enabled.
func NewErrorHintsBuilder() *ErrorHintsBuilder {
	return &ErrorHintsBuilder{h: ErrorHints{verbose: true, help: true}}
}

// AddInfo appends an info message.
func (b *ErrorHintsBuilder) AddInfo(info string) *ErrorHintsBuilder {
	b.h.info = append(b.h.info, info)
	return b
}

// Config enables the --config hint.
func (b *ErrorHintsBuilder) Config() *ErrorHintsBuilder {
	b.h.config = true
	return b
}

// ConfigGenerate enables the config generate hint.
func (b *ErrorHintsBuilder) ConfigGenerate() *ErrorHintsBuilder {
	b.h.configGenerate = true
	return b
}

// ConfigTest enables the config test hint.
func (b *ErrorHintsBuilder) ConfigTest() *ErrorHintsBuilder {
	b.h.configTest = true
	return b
}

// Verbose enables the verbose hint.
func (b *ErrorHintsBuilder) Verbose() *ErrorHintsBuilder {
	b.h.verbose = true
	return b
}

// Help enables the help hint.
func (b *ErrorHintsBuilder) Help() *ErrorHintsBuilder {
	b.h.help = true
	return b
}

// Build returns the hints.
func (b *ErrorHintsBuilder) Build() ErrorHints { return b.h }

// DefaultErrorHints returns hints with default flags.
func DefaultErrorHints() ErrorHints {
	return ErrorHints{verbose: true, help: true}
}

func (h ErrorHints) any() bool {
	return h.config || h.configGenerate || h.configTest || h.verbose || h.help
}

// Print writes the hints to stderr.
func (h ErrorHints) Print(endNewline bool) {
	for _, msg := range h.info {
		fmt.Fprintf(os.Stderr, "%s %s\n", HighlightInfo("info:"), msg)
	}

	if !h.any() {
		return
	}

	fmt.Fprintln(os.Stderr)

	bin := BinName()
	if h.configGenerate {
		fmt.Fprintf(os.Stderr, "Use '%s' to generate a new config file\n", Highlight(fmt.Sprintf("%s config generate", bin)))
	}
	if h.config {
		fmt.Fprintf(os.Stderr, "Use '%s' to select a config file\n", Highlight("--config FILE"))
	}
	if h.configTest {
		fmt.Fprintf(os.Stderr, "Use '%s' to test a config file\n", Highlight(fmt.Sprintf("%s config test -c FILE", bin)))
	}
	if h.verbose {
		fmt.Fprintf(os.Stderr, "For a detailed log add '%s'\n", Highlight("--verbose"))
	}
	if h.help {
		fmt.Fprintf(os.Stderr, "For more information add '%s'\n", Highlight("--help"))
	}

	if endNewline {
		fmt.Fprintln(os.Stderr)
	}
}

// PrintError prints an error with its causes.
func PrintError(err error) {
	// Report each cause
	count := 0
	for e := err; e != nil; e = unwrapOnce(e) {
		msg := e.Error()
		if msg == "" {
			continue
		}
		if count == 0 {
			fmt.Fprintf(os.Stderr, "%s %s\n", HighlightError("error:"), msg)
		} else {
			fmt.Fprintf(os.Stderr, "%s %s\n", HighlightError("caused by:"), msg)
		}
		count++
		if count >= 32 {
			break
		}
	}

	// Fall back to a basic message
	if count == 0 {
		fmt.Fprintf(os.Stderr, "%s an undefined error occurred\n", HighlightError("error:"))
	}
}

// PrintErrorMsg prints an error message.
func PrintErrorMsg(msg interface{ String() string }) {
	PrintError(fmt.Errorf("%v", msg))
}

// PrintWarning prints a warning.
func PrintWarning(msg interface{ String() string }) {
	fmt.Fprintf(os.Stderr, "%s %s\n", HighlightWarning("warning:"), msg)
}

// unwrapOnce unwraps a wrapped error one level, without flattening.
func unwrapOnce(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		if next := u.Unwrap(); next != nil {
			return next
		}
	}
	return nil
}

// Quit exits with code 0.
func Quit() {
	os.Exit(0)
}

// QuitError prints an error plus hints, then exits with code 1.
func QuitError(err error, hints ErrorHints) {
	PrintError(err)
	hints.Print(false)
	os.Exit(1)
}

// QuitErrorMsg prints an error message plus hints, then exits with code 1.
func QuitErrorMsg(msg string, hints ErrorHints) {
	QuitError(fmt.Errorf("%s", msg), hints)
}
