// Package config handles loregd command-line argument parsing and validation.
package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/peios/loregd/internal/fold"
)

// HiveConfig represents a single hive declaration from the command line.
type HiveConfig struct {
	Name string // Case-preserving hive name as provided.
	Path string // Absolute path to the SQLite database file.
}

// Parse parses loregd command-line arguments.
// Each argument must be in the form HiveName=DatabasePath.
// Returns the parsed hive configurations or an error.
func Parse(args []string) ([]HiveConfig, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("no hive arguments provided")
	}

	seen := make(map[string]bool) // folded name → already seen
	var configs []HiveConfig

	for _, arg := range args {
		idx := strings.IndexByte(arg, '=')
		if idx < 0 {
			return nil, fmt.Errorf("invalid argument %q: expected HiveName=DatabasePath", arg)
		}

		name := arg[:idx]
		path := arg[idx+1:]

		if name == "" {
			return nil, fmt.Errorf("invalid argument %q: empty hive name", arg)
		}
		if path == "" {
			return nil, fmt.Errorf("invalid argument %q: empty database path", arg)
		}

		// Validate hive name: no backslash, no forward slash, no null.
		if strings.ContainsAny(name, "\\/\x00") {
			return nil, fmt.Errorf("invalid hive name %q: contains forbidden characters", name)
		}

		// "CurrentUser" (case-insensitive) is reserved as a kernel alias.
		folded := fold.String(name)
		if folded == "currentuser" {
			return nil, fmt.Errorf("invalid hive name %q: CurrentUser is reserved", name)
		}

		// Duplicate check (case-insensitive).
		if seen[folded] {
			return nil, fmt.Errorf("duplicate hive name %q", name)
		}
		seen[folded] = true

		// Database path must be absolute.
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("database path %q for hive %q is not absolute", path, name)
		}

		configs = append(configs, HiveConfig{Name: name, Path: path})
	}

	return configs, nil
}
