package model

import (
	"encoding/json"
	"log/slog"
	"os"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/dialog"
)

// Recent commands: the command palette lists the last few system commands
// run from it in a Recent section. They are saved to
// <global data directory>/recent_commands.json so they carry across runs
// and projects.

// recentCommandsFileName is the saved recent command list inside the
// global data directory.
const recentCommandsFileName = "recent_commands.json"

// savedRecentCommands is the recent_commands.json format.
type savedRecentCommands struct {
	Version int      `json:"version"`
	IDs     []string `json:"ids"`
}

// recentCommandsLoadedMsg carries the recent command IDs read from disk.
type recentCommandsLoadedMsg struct {
	ids []string
}

// loadRecentCommands reads the saved recent commands in the background.
// It does nothing when there is no path (as in tests).
func loadRecentCommands(path string) tea.Cmd {
	if path == "" {
		return nil
	}
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("Could not read recent commands", "path", path, "error", err)
			}
			return nil
		}
		var saved savedRecentCommands
		if err := json.Unmarshal(data, &saved); err != nil {
			slog.Warn("Could not parse recent commands", "path", path, "error", err)
			return nil
		}
		// Normalize: drop empties and duplicates, cap the length.
		var ids []string
		for _, id := range slices.Backward(saved.IDs) {
			ids = dialog.PushRecentCommand(ids, id)
		}
		return recentCommandsLoadedMsg{ids: ids}
	}
}

// saveRecentCommands writes ids to path in the background. It does
// nothing when there is no path.
func saveRecentCommands(path string, ids []string) tea.Cmd {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(savedRecentCommands{Version: 1, IDs: slices.Clone(ids)})
	if err != nil {
		return nil
	}
	return func() tea.Msg {
		if err := writeFileAtomic(path, data); err != nil {
			slog.Warn("Could not save recent commands", "path", path, "error", err)
		}
		return nil
	}
}

// handleRecentCommandsLoaded merges the saved recent commands behind any
// picked before the file finished loading. A palette already open keeps
// its list (rebuilding it would clear what is typed); the next one shows
// them.
func (m *UI) handleRecentCommandsLoaded(msg recentCommandsLoadedMsg) {
	merged := slices.Clone(msg.ids)
	for _, id := range slices.Backward(m.recentCommands) {
		merged = dialog.PushRecentCommand(merged, id)
	}
	m.recentCommands = merged
}

// recordRecentCommand notes the system command just picked in the palette,
// if any, and saves the updated list.
func (m *UI) recordRecentCommand() tea.Cmd {
	c, ok := m.dialog.Dialog(dialog.CommandsID).(*dialog.Commands)
	if !ok {
		return nil
	}
	id := c.TakeChosen()
	if id == "" {
		return nil
	}
	m.recentCommands = dialog.PushRecentCommand(m.recentCommands, id)
	return saveRecentCommands(m.recentCommandsPath, m.recentCommands)
}
