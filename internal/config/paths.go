package config

import (
	"os"
	"path/filepath"
)

// DefaultConfigPath returns the path to use when --config is not specified.
// Resolution order:
//  1. ./config.yaml (if it exists in the current directory — backward compat)
//  2. $XDG_CONFIG_HOME/stash-janitor/config.yaml (Linux/Mac standard)
//  3. ./config.yaml (fallback for first-time users)
func DefaultConfigPath() string {
	// Current directory takes precedence (portable / development use).
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}

	// XDG standard location.
	if dir, err := os.UserConfigDir(); err == nil {
		xdg := filepath.Join(dir, "stash-janitor", "config.yaml")
		if _, err := os.Stat(xdg); err == nil {
			return xdg
		}
	}

	// Nothing found — fall back to current dir (config init will create it
	// in the XDG location).
	return "config.yaml"
}

// DefaultDBPath returns the path to use when --db is not specified.
// Resolution order:
//  1. ./stash-janitor.sqlite (if it exists — backward compat)
//  2. $XDG_DATA_HOME/stash-janitor/stash-janitor.sqlite
//  3. ./stash-janitor.sqlite (fallback)
func DefaultDBPath() string {
	if _, err := os.Stat("stash-janitor.sqlite"); err == nil {
		return "stash-janitor.sqlite"
	}

	if dir := xdgDataHome(); dir != "" {
		xdg := filepath.Join(dir, "stash-janitor", "stash-janitor.sqlite")
		if _, err := os.Stat(xdg); err == nil {
			return xdg
		}
	}

	return "stash-janitor.sqlite"
}

// DefaultConfigInitPath returns the path `config init` should write to.
// Prefers the XDG location; falls back to current directory.
func DefaultConfigInitPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "stash-janitor", "config.yaml")
	}
	return "config.yaml"
}

// DefaultDBInitPath returns the path the store should use when creating
// a new database. Prefers XDG; falls back to current directory.
func DefaultDBInitPath() string {
	if dir := xdgDataHome(); dir != "" {
		return filepath.Join(dir, "stash-janitor", "stash-janitor.sqlite")
	}
	return "stash-janitor.sqlite"
}

// xdgDataHome returns $XDG_DATA_HOME or ~/.local/share per the XDG
// Base Directory Specification.
func xdgDataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// EnsureDir creates dir and all parents if they don't exist.
// Used by config init and store Open to create XDG directories.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0o755)
}
