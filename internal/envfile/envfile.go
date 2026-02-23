// Package envfile provides a minimal dotenv-style file parser shared by the
// daemon (internal/daemon) and the CLI (cmd/grove).
package envfile

import (
	"bufio"
	"os"
	"strings"
)

// Load reads a dotenv-style file at path and returns its key-value pairs.
// Lines starting with # and blank lines are silently skipped.
// Returns an empty map (not an error) if the file does not exist.
func Load(path string) map[string]string {
	env := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return env
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return env
}
