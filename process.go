package main

// Process management helpers.

import (
	"net"
	"os"
	"os/exec"
)

// osProcessState aliases os.ProcessState for the exit code helper.
type osProcessState = os.ProcessState

// startCommand starts the given command with inherited stdio, mirroring
// tokio's default process behavior.
func startCommand(cmd *exec.Cmd) (*exec.Cmd, error) {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	return cmd, nil
}

// parseIP parses an IP address string.
func parseIP(s string) net.IP {
	return net.ParseIP(s)
}

// shlexSplit splits a shell command string into arguments, mirroring the
// shlex crate's behavior: supports single and double quotes with escapes,
// and backslash escaping outside quotes.
func shlexSplit(s string) []string {
	var words []string
	var cur []byte
	quoted := false
	var quote byte
	inWord := false

	i := 0
	for i < len(s) {
		c := s[i]

		switch {
		case !quoted && (c == '\'' || c == '"'):
			quote = c
			quoted = true
			inWord = true
			i++
		case quoted && c == quote:
			quoted = false
			inWord = true
			i++
		case !quoted && (c == ' ' || c == '\t' || c == '\n'):
			if inWord {
				words = append(words, string(cur))
				cur = nil
				inWord = false
			}
			i++
		case !quoted && c == '\\':
			if i+1 < len(s) {
				cur = append(cur, s[i+1])
				inWord = true
				i += 2
			} else {
				cur = append(cur, '\\')
				i++
			}
		case quoted && quote == '"' && c == '\\':
			// In double quotes, backslash only escapes a few chars
			if i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\' || s[i+1] == '$' || s[i+1] == '`') {
				cur = append(cur, s[i+1])
				i += 2
			} else {
				cur = append(cur, '\\')
				i++
			}
		default:
			cur = append(cur, c)
			inWord = true
			i++
		}
	}

	if inWord {
		words = append(words, string(cur))
	}

	return words
}
