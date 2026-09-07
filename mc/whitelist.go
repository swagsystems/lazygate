package mc

import (
	"encoding/json"
	"os"
	"path/filepath"

	"lazymc/util"
)

// Whitelist file name.
const WhitelistFile = "whitelist.json"

// OPs file name.
const OpsFile = "ops.json"

// Whitelisted users, including OPs which are automatically whitelisted.
type Whitelist struct {
	whitelist []string
	ops       []string
}

// IsWhitelisted checks whether a user is whitelisted.
func (w Whitelist) IsWhitelisted(username string) bool {
	for _, u := range w.whitelist {
		if u == username {
			return true
		}
	}
	for _, u := range w.ops {
		if u == username {
			return true
		}
	}
	return false
}

// A whitelist user entry.
type whitelistUser struct {
	Username string  `json:"name"`
	UUID     *string `json:"uuid"`
}

// An OP user entry.
type opUser struct {
	Username            string  `json:"name"`
	UUID                *string `json:"uuid"`
	Level               *uint32 `json:"level"`
	BypassesPlayerLimit *bool   `json:"bypassesPlayerLimit"`
}

// LoadWhitelistDir loads the whitelist from a server directory.
func LoadWhitelistDir(dir string) (Whitelist, error) {
	whitelistFile := filepath.Join(dir, WhitelistFile)
	opsFile := filepath.Join(dir, OpsFile)

	var whitelist []string
	if isFile(whitelistFile) {
		var err error
		whitelist, err = loadWhitelist(whitelistFile)
		if err != nil {
			return Whitelist{}, err
		}
	}

	var ops []string
	if isFile(opsFile) {
		var err error
		ops, err = loadOps(opsFile)
		if err != nil {
			return Whitelist{}, err
		}
	}

	util.Debug("lazymc", "Loaded %d whitelist and %d OP users", len(whitelist), len(ops))

	return Whitelist{whitelist: whitelist, ops: ops}, nil
}

func loadWhitelist(path string) ([]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var users []whitelistUser
	if err := json.Unmarshal(contents, &users); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(users))
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out, nil
}

func loadOps(path string) ([]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var users []opUser
	if err := json.Unmarshal(contents, &users); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(users))
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out, nil
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
