package mc

import (
	"os"
	"path/filepath"
	"strings"

	"lazymc/util"
)

// Server properties file name.
const ServerPropertiesFile = "server.properties"

// EOL in server.properties file.
const serverPropertiesEOL = "\r\n"

// RewriteServerPropertiesDir rewrites changes in server.properties in dir.
func RewriteServerPropertiesDir(dir string, changes map[string]string) {
	if len(changes) == 0 {
		return
	}

	// Ensure directory exists
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		util.Warn("lazymc", "Not rewriting %s file, configured server directory doesn't exist: %s", ServerPropertiesFile, dir)
		return
	}

	RewriteServerPropertiesFile(filepath.Join(dir, ServerPropertiesFile), changes)
}

// RewriteServerPropertiesFile rewrites changes in a server.properties file.
func RewriteServerPropertiesFile(file string, changes map[string]string) {
	if len(changes) == 0 {
		return
	}

	// File must exist
	if !isFile(file) {
		util.Warn("lazymc", "Not writing %s file, not found at: %s", ServerPropertiesFile, file)
		return
	}

	// Read contents
	contents, err := os.ReadFile(file)
	if err != nil {
		util.Error("lazymc", "Failed to rewrite %s file, could not load: %v", ServerPropertiesFile, err)
		return
	}

	// Rewrite file contents, return if nothing changed
	newContents, changed := rewriteContents(string(contents), changes)
	if !changed {
		util.Debug("lazymc", "Not rewriting %s file, no changes to apply", ServerPropertiesFile)
		return
	}

	// Write changes
	if err := os.WriteFile(file, []byte(newContents), 0o644); err != nil {
		util.Error("lazymc", "Failed to rewrite %s file, could not save changes: %v", ServerPropertiesFile, err)
		return
	}
	util.Info("lazymc", "Rewritten %s file with updated values", ServerPropertiesFile)
}

// rewriteContents rewrites file contents with new properties, returning the
// new contents and whether anything changed.
func rewriteContents(contents string, changes map[string]string) (string, bool) {
	if len(changes) == 0 {
		return contents, false
	}

	changed := false

	// Remaining changes to append later
	remaining := map[string]string{}
	for k, v := range changes {
		remaining[k] = v
	}

	// Match Rust's str::lines() semantics: split on \n, strip trailing \r,
	// and drop empty lines entirely.
	rawLines := strings.Split(contents, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		l = strings.TrimSuffix(l, "\r")
		if l == "" {
			continue
		}
		lines = append(lines, l)
	}
	newLines := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)

		// Skip comments or empty lines
		if strings.HasPrefix(trim, "#") || trim == "" {
			newLines = append(newLines, line)
			continue
		}

		// Try to split property
		idx := strings.Index(line, "=")
		if idx < 0 {
			newLines = append(newLines, line)
			continue
		}

		key := strings.TrimSpace(line[:idx])
		value := line[idx+1:]

		// Take any new value, and update it
		if new, ok := remaining[strings.ToLower(key)]; ok {
			if value != new {
				line = key + "=" + new
				changed = true
			}
			delete(remaining, strings.ToLower(key))
		}

		newLines = append(newLines, line)
	}

	newContents := strings.Join(newLines, serverPropertiesEOL)

	// Append any missed changes
	for key, value := range remaining {
		newContents += serverPropertiesEOL + key + "=" + value
		changed = true
	}

	return newContents, changed
}

// ReadServerProperties reads the given property from the given file.
func ReadServerProperties(file, property string) *string {
	if !isFile(file) {
		util.Warn("lazymc", "Failed to read property from %s file, it does not exist", ServerPropertiesFile)
		return nil
	}

	contents, err := os.ReadFile(file)
	if err != nil {
		util.Error("lazymc", "Failed to read property from %s file, could not load: %v", ServerPropertiesFile, err)
		return nil
	}

	for _, line := range strings.Split(string(contents), "\n") {
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if strings.ToLower(key) == strings.ToLower(property) {
			val := strings.TrimSpace(line[idx+1:])
			return &val
		}
	}

	return nil
}
