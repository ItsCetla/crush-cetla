package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// This app was called Crush before it became Ta-Da. The helpers below keep
// settings from that name working: old CRUSH_* variables, and the old global
// config and data folders.

const legacyAppName = "crush"

// ImportLegacyEnv copies every CRUSH_<NAME> variable to TADA_<NAME> unless
// TADA_<NAME> is already set. Call it once, before anything reads the env.
func ImportLegacyEnv() {
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "CRUSH_") {
			continue
		}
		newName := "TADA_" + strings.TrimPrefix(name, "CRUSH_")
		if _, set := os.LookupEnv(newName); !set {
			_ = os.Setenv(newName, value)
		}
	}
}

// MigrateLegacyDirs copies the global config and data folders from the old
// crush location (e.g. ~/.config/crush) to the new tada one, the first time
// the new folder does not exist yet. The old folders are left untouched.
// Databases, logs, locks and backups are not copied.
func MigrateLegacyDirs() {
	for _, dir := range []string{filepath.Dir(GlobalConfig()), filepath.Dir(GlobalConfigData())} {
		migrateLegacyDir(dir)
	}
}

func migrateLegacyDir(newDir string) {
	if filepath.Base(newDir) != appName {
		return // custom location from an env var; nothing to migrate
	}
	oldDir := filepath.Join(filepath.Dir(newDir), legacyAppName)
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		return
	}
	if info, err := os.Stat(oldDir); err != nil || !info.IsDir() {
		return
	}
	if err := copyLegacyTree(oldDir, newDir, true); err != nil {
		slog.Warn("Could not copy settings from the old crush folder", "from", oldDir, "to", newDir, "error", err)
		return
	}
	slog.Info("Copied settings from the old crush folder", "from", oldDir, "to", newDir)
}

// legacyFileName maps an old file name to its new one.
func legacyFileName(name string) string {
	switch name {
	case legacyAppName + ".json":
		return appName + ".json"
	case legacyAppName + "rc":
		return appName + "rc"
	case "CRUSH.md":
		return "TADA.md"
	}
	return name
}

func skipLegacyEntry(name string, isDir bool) bool {
	if isDir {
		return name == "logs" || name == "cache"
	}
	return strings.HasSuffix(name, ".lock") ||
		strings.Contains(name, ".bak") ||
		strings.Contains(name, ".db") ||
		strings.HasSuffix(name, ".log") ||
		strings.HasSuffix(name, ".sock")
}

func copyLegacyTree(src, dst string, top bool) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.Type()&os.ModeSymlink != 0 || skipLegacyEntry(name, e.IsDir()) {
			continue
		}
		newName := name
		if top {
			newName = legacyFileName(name)
		}
		from, to := filepath.Join(src, name), filepath.Join(dst, newName)
		if e.IsDir() {
			if err := copyLegacyTree(from, to, false); err != nil {
				return err
			}
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		if err := copyLegacyFile(from, to); err != nil {
			return err
		}
	}
	return nil
}

func copyLegacyFile(from, to string) error {
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
