package dialog

import (
	"path/filepath"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/update"
	"github.com/charmbracelet/crush/internal/version"
)

type (
	// ActionCheckForUpdates asks GitHub whether a newer Ta-Da release is
	// out.
	ActionCheckForUpdates struct{}
	// ActionInstallUpdate installs the given release version.
	ActionInstallUpdate struct {
		Version string
	}
)

// updateCommandItem is the command palette entry for updates: "Install
// Update to X" when the last check found a newer release, else "Check for
// Updates". It only reads the cached check result; it never hits the
// network.
func updateCommandItem(t *styles.Styles) *CommandItem {
	state := update.LoadState(filepath.Join(config.GlobalWorkspaceDir(), update.StateFile))
	if v, ok := state.Available(version.Version); ok && !update.IsDevVersion(version.Version) {
		return NewCommandItem(t, "install_update", "Install Update to "+v, "", ActionInstallUpdate{Version: v}).
			WithAliases("update", "upgrade")
	}
	return NewCommandItem(t, "check_for_updates", "Check for Updates", "", ActionCheckForUpdates{}).
		WithAliases("update", "upgrade")
}
