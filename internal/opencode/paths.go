// Package opencode reads a user's local OpenCode installation: its JSON(C)
// configuration files, the models.dev catalog OpenCode caches on disk, and
// (only when asked) the API keys OpenCode stores.
//
// The package never touches the network and never writes: config and cache
// files are only read, and OpenCode's SQLite database is opened read-only.
// Missing files are not errors. Everything is driven by the [Paths] and
// getenv function the caller passes in, so the package holds no global state
// and is safe to use concurrently.
//
// Project configuration can run {file:...} and {env:...} substitutions, so
// a hostile repository could use it to read local secrets into values that
// are later sent over the network. Callers must only set
// [LoadOptions.IncludeProject] for trusted projects.
package opencode

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Paths locates an OpenCode installation. OpenCode uses XDG-style
// directories on every platform, macOS included.
type Paths struct {
	// ConfigDir is the global config directory: $OPENCODE_CONFIG_DIR, else
	// $XDG_CONFIG_HOME/opencode, else ~/.config/opencode.
	ConfigDir string
	// DataDir holds opencode.db and the legacy auth.json:
	// $XDG_DATA_HOME/opencode, else ~/.local/share/opencode.
	DataDir string
	// CacheDir holds the legacy models.json catalog cache:
	// $XDG_CACHE_HOME/opencode, else ~/.cache/opencode.
	CacheDir string
	// ManagedDir is the system-wide (administrator managed) config
	// directory, whose files override every other source. Empty disables
	// it.
	ManagedDir string
	// Home is the user's home directory, used for "~/" in {file:...}.
	Home string
}

// ResolvePaths computes the OpenCode directories the same way OpenCode
// does. getenv may be nil, which behaves like an empty environment. Empty
// variables count as unset.
func ResolvePaths(getenv func(string) string, home string) Paths {
	getenv = envOrEmpty(getenv)
	base := func(xdg string, fallback ...string) string {
		if v := getenv(xdg); v != "" {
			return filepath.Join(v, "opencode")
		}
		if home == "" {
			return ""
		}
		return filepath.Join(append(append([]string{home}, fallback...), "opencode")...)
	}
	p := Paths{
		ConfigDir:  base("XDG_CONFIG_HOME", ".config"),
		DataDir:    base("XDG_DATA_HOME", ".local", "share"),
		CacheDir:   base("XDG_CACHE_HOME", ".cache"),
		ManagedDir: managedDir(runtime.GOOS, getenv),
		Home:       home,
	}
	if v := getenv("OPENCODE_CONFIG_DIR"); v != "" {
		p.ConfigDir = v
	}
	return p
}

func managedDir(goos string, getenv func(string) string) string {
	switch goos {
	case "darwin":
		return "/Library/Application Support/opencode"
	case "windows":
		pd := getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "opencode")
	default:
		return "/etc/opencode"
	}
}

func envOrEmpty(getenv func(string) string) func(string) string {
	if getenv == nil {
		return func(string) string { return "" }
	}
	return getenv
}

// truthy mirrors OpenCode's flag parsing: "1" or "true" in any case.
func truthy(v string) bool {
	return v == "1" || strings.EqualFold(v, "true")
}
