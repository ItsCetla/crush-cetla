package model

import (
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

type configWrite struct {
	scope config.Scope
	key   string
	value any
}

// settingsWorkspace records config writes. Unless overridden is set it
// applies them to the in-memory config, as a reload would.
type settingsWorkspace struct {
	testWorkspace
	writes     []configWrite
	overridden bool
	err        error
}

func (w *settingsWorkspace) SetConfigField(scope config.Scope, key string, value any) error {
	if w.err != nil {
		return w.err
	}
	w.writes = append(w.writes, configWrite{scope, key, value})
	if w.overridden {
		return nil
	}
	tui := w.cfg.Options.TUI
	switch key {
	case "options.tui.mouse":
		v := value.(bool)
		tui.Mouse = &v
	case "options.tui.compact_mode":
		tui.CompactMode = value.(bool)
	case "options.progress":
		v := value.(bool)
		w.cfg.Options.Progress = &v
	}
	return nil
}

func newSettingsTestUI(t *testing.T) (*UI, *settingsWorkspace) {
	t.Helper()
	cfg := &config.Config{Options: &config.Options{
		TUI:           &config.TUIOptions{},
		DataDirectory: t.TempDir(),
	}}
	ws := &settingsWorkspace{testWorkspace: testWorkspace{cfg: cfg}}
	m := &UI{
		com:          common.DefaultCommon(ws),
		dialog:       dialog.NewOverlay(),
		mouseEnabled: true,
	}
	return m, ws
}

func TestSetSettingCmdWritesGlobalConfig(t *testing.T) {
	t.Parallel()

	m, ws := newSettingsTestUI(t)
	msg := m.setSettingCmd("options.tui.mouse", false)()
	require.Equal(t, settingResultMsg{key: "options.tui.mouse", notice: "Saved."}, msg)
	require.Equal(t, []configWrite{{config.ScopeGlobal, "options.tui.mouse", false}}, ws.writes)
}

func TestSetSettingCmdReportsProjectOverride(t *testing.T) {
	t.Parallel()

	m, ws := newSettingsTestUI(t)
	ws.overridden = true
	msg := m.setSettingCmd("options.tui.compact_mode", true)().(settingResultMsg)
	require.NoError(t, msg.err)
	require.Equal(t, "Saved, but a project config overrides it here.", msg.notice)
}

func TestSetSettingCmdReportsError(t *testing.T) {
	t.Parallel()

	m, ws := newSettingsTestUI(t)
	ws.err = errors.New("read-only file system")
	msg := m.setSettingCmd("options.progress", false)().(settingResultMsg)
	require.ErrorContains(t, msg.err, "read-only file system")
}

func TestHandleSettingResultAppliesLive(t *testing.T) {
	t.Parallel()

	m, _ := newSettingsTestUI(t)
	m.progressBarEnabled = true
	cmd := m.openSettingsDialog()
	require.NotNil(t, cmd, "opening loads the sources")
	require.True(t, m.dialog.ContainsDialog(dialog.SettingsID))

	msg := m.setSettingCmd("options.tui.mouse", false)().(settingResultMsg)
	require.NotNil(t, m.handleSettingResult(msg), "a save reloads the sources")
	require.False(t, m.mouseEnabled)

	msg = m.setSettingCmd("options.progress", false)().(settingResultMsg)
	m.handleSettingResult(msg)
	require.False(t, m.progressBarEnabled)

	// A failed save leaves the running UI alone.
	m.handleSettingResult(settingResultMsg{key: "options.progress", err: errors.New("nope")})
	require.False(t, m.progressBarEnabled)
}

func TestHandleSettingSourcesWithoutDialog(t *testing.T) {
	t.Parallel()

	m, _ := newSettingsTestUI(t)
	// Sources arriving after the dialog closed are dropped.
	m.handleSettingSources(settingSourcesMsg{})
	require.False(t, m.dialog.ContainsDialog(dialog.SettingsID))
}
