package model

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

func TestRecentCommandsRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data", recentCommandsFileName)

	// Nothing saved yet: loading reports nothing.
	require.Nil(t, loadRecentCommands(path)())

	saveRecentCommands(path, []string{"quit", "switch_model"})()
	msg, ok := loadRecentCommands(path)().(recentCommandsLoadedMsg)
	require.True(t, ok)
	require.Equal(t, []string{"quit", "switch_model"}, msg.ids)
}

func TestRecentCommandsLoadNormalizes(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), recentCommandsFileName)
	data := `{"version":1,"ids":["a","","b","a","c","d","e","f","g"]}`
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	msg, ok := loadRecentCommands(path)().(recentCommandsLoadedMsg)
	require.True(t, ok)
	require.Equal(t, []string{"a", "b", "c", "d", "e"}, msg.ids)

	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))
	require.Nil(t, loadRecentCommands(path)())
}

func TestRecentCommandsNoPathStaysInMemory(t *testing.T) {
	t.Parallel()

	require.Nil(t, loadRecentCommands(""))
	require.Nil(t, saveRecentCommands("", []string{"quit"}))
}

func TestRecentCommandsLoadedKeepsEarlierPicks(t *testing.T) {
	t.Parallel()

	m := &UI{recentCommands: []string{"quit"}}
	m.handleRecentCommandsLoaded(recentCommandsLoadedMsg{ids: []string{"switch_model", "quit"}})
	require.Equal(t, []string{"quit", "switch_model"}, m.recentCommands)
}

func TestRecordRecentCommandFromPalette(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
	com := common.DefaultCommon(&testWorkspace{cfg: cfg})
	path := filepath.Join(t.TempDir(), recentCommandsFileName)
	m := &UI{
		com:                com,
		dialog:             dialog.NewOverlay(),
		recentCommands:     []string{"switch_model"},
		recentCommandsPath: path,
	}

	c, err := dialog.NewCommands(com, "", false, false, false, nil, nil)
	require.NoError(t, err)
	c.SetRecent(m.recentCommands)
	m.dialog.OpenDialog(c)

	// Nothing picked yet.
	require.Nil(t, m.recordRecentCommand())

	// The selection starts on the recent "switch_model"; one step down skips
	// the Session header and lands on its first command.
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	cmd := m.recordRecentCommand()
	require.NotNil(t, cmd)
	cmd()
	require.Equal(t, "new_session", m.recentCommands[0])
	require.Equal(t, []string{"new_session", "switch_model"}, m.recentCommands)

	msg, ok := loadRecentCommands(path)().(recentCommandsLoadedMsg)
	require.True(t, ok)
	require.Equal(t, m.recentCommands, msg.ids)
}
