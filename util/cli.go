package util

// Prompt helpers, mirroring lazymc's util/cli.rs.

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

var stdinReader = bufio.NewReader(os.Stdin)

// Prompt asks the user to enter a value, showing the given message.
func Prompt(msg string) string {
	fmt.Fprintf(os.Stderr, "%s: ", msg)

	input, err := stdinReader.ReadString('\n')
	if err != nil && input == "" {
		QuitError(fmt.Errorf("failed to read input from prompt: %v", err), DefaultErrorHints())
	}

	return strings.TrimSpace(input)
}

// PromptYes asks a yes/no question, returning true for yes.
//
// A default may be given, chosen when enter is pressed without input.
func PromptYes(msg string, def *bool) bool {
	options := fmt.Sprintf("[%s/%s]", yesLabel(def, true), yesLabel(def, false))
	answer := Prompt(fmt.Sprintf("%s %s", msg, options))

	// Assume the default if the answer is empty
	if answer == "" && def != nil {
		return *def
	}

	// Derive a boolean and return
	if b, ok := deriveBool(answer); ok {
		return b
	}
	return PromptYes(msg, def)
}

func yesLabel(def *bool, yes bool) string {
	if def != nil && *def == yes {
		return map[bool]string{true: "Y", false: "N"}[yes]
	}
	return map[bool]string{true: "y", false: "n"}[yes]
}

// deriveBool tries to parse yes/no from input.
func deriveBool(input string) (bool, bool) {
	input = strings.ToLower(strings.TrimSpace(input))

	switch input {
	case "y", "ye", "t", "1":
		return true, true
	case "n", "f", "0":
		return false, true
	}

	if strings.HasPrefix(input, "yes") || strings.HasPrefix(input, "true") {
		return true, true
	}
	if strings.HasPrefix(input, "no") || strings.HasPrefix(input, "false") {
		return false, true
	}

	return false, false
}
