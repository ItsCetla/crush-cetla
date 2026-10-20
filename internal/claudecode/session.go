package claudecode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// sessionNamespace makes non-UUID Ta-Da session IDs map to stable UUIDs.
var sessionNamespace = uuid.MustParse("6f0b8f3e-7a51-4c2e-9d0a-7461646121cc")

// SessionUUID returns the Claude Code session ID for a Ta-Da session ID.
// UUIDs pass through; anything else maps to a stable UUID v5.
func SessionUUID(id string) string {
	if u, err := uuid.Parse(id); err == nil {
		return u.String()
	}
	return uuid.NewSHA1(sessionNamespace, []byte(id)).String()
}

// configDirs lists the directories the CLI may keep transcripts in.
func configDirs() []string {
	var dirs []string
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude"))
	}
	return dirs
}

// SessionExists reports whether the CLI has a saved transcript for id.
func SessionExists(id string) bool {
	if id == "" || strings.ContainsAny(id, `/\*?[`) {
		return false
	}
	for _, dir := range configDirs() {
		matches, _ := filepath.Glob(filepath.Join(dir, "projects", "*", id+".jsonl"))
		if len(matches) > 0 {
			return true
		}
	}
	return false
}

// FindBinary locates the claude CLI: PATH first, then the installer's
// usual locations. It returns "" when none is found.
func FindBinary() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, p := range []string{
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(home, ".claude", "local", "claude"),
		filepath.Join(home, ".local", "bin", "claude.exe"),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// ProviderType is the Ta-Da provider type that runs turns through the
// Claude Code CLI. ProviderID is the id of the auto-added provider.
const (
	ProviderType = "claude-code"
	ProviderID   = "claude-code"
)
