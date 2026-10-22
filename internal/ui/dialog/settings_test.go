package dialog

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

func newTestSettings(t *testing.T, opts *config.Options) *Settings {
	t.Helper()
	s := styles.CharmtonePantera()
	if opts == nil {
		opts = &config.Options{}
	}
	cfg := &config.Config{Options: opts}
	d := NewSettings(&common.Common{Styles: &s, Workspace: &contextWorkspace{cfg: cfg}})
	d.width = 76
	return d
}

// selectSetting moves the cursor to key.
func selectSetting(t *testing.T, d *Settings, key string) {
	t.Helper()
	i := slices.IndexFunc(settingsList, func(s setting) bool { return s.key == key })
	require.GreaterOrEqual(t, i, 0, key)
	d.cursor = i
}

// clearSettingsEnv unsets every variable that can lock a setting, so the
// golden output does not depend on the shell running the tests.
func clearSettingsEnv(t *testing.T) {
	t.Helper()
	for _, s := range settingsList {
		if s.env != "" {
			t.Setenv(s.env, "")
		}
	}
}

func TestSettingsQuery(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"/settings", " /SETTINGS ", "/preferences"} {
		require.True(t, SettingsQuery(v), v)
	}
	for _, v := range []string{"settings", "/setting", "/settings mouse"} {
		require.False(t, SettingsQuery(v), v)
	}
}

func TestSettingKeysAreUnique(t *testing.T) {
	t.Parallel()
	keys := SettingKeys()
	require.Len(t, keys, len(settingsList))
	seen := map[string]bool{}
	for _, k := range keys {
		require.False(t, seen[k], k)
		seen[k] = true
	}
}

func TestSettingsInCommandPalette(t *testing.T) {
	t.Parallel()

	cmds := newTestCommands(t, nil).defaultCommands()
	i := slices.IndexFunc(cmds, func(cmd *CommandItem) bool { return cmd.ID() == "open_settings" })
	require.GreaterOrEqual(t, i, 0)
	require.Equal(t, CommandGroupApp, cmds[i].Group())
	require.Equal(t, ActionOpenSettings{}, cmds[i].Action())
}

func TestSettingsListShowsValuesAndSources(t *testing.T) {
	clearSettingsEnv(t)

	off := false
	d := newTestSettings(t, &config.Options{
		TUI: &config.TUIOptions{CompactMode: true, DiffMode: "split", Mouse: &off},
	})
	d.height = 0
	view := ansi.Strip(d.content())
	require.Contains(t, view, "Compact mode")
	require.Contains(t, view, "…", "sources are pending until loaded")

	d.SetSources(map[string]config.FieldSource{
		"options.tui.compact_mode": {Kind: config.FieldSourceGlobal, Shell: true},
		"options.tui.diff_mode":    {Kind: config.FieldSourceProject},
		"options.tui.mouse":        {Kind: config.FieldSourceGlobal},
	}, nil)

	rows := map[string]string{}
	for line := range strings.SplitSeq(ansi.Strip(d.content()), "\n") {
		for _, s := range settingsList {
			if strings.Contains(line, s.label+" ") {
				rows[s.key] = line
			}
		}
	}
	require.Regexp(t, `Compact mode\s+On\s+tadarc`, rows["options.tui.compact_mode"])
	require.Regexp(t, `Diff style\s+split\s+project`, rows["options.tui.diff_mode"])
	require.Regexp(t, `Mouse\s+Off\s+tada\.json`, rows["options.tui.mouse"])
	require.Regexp(t, `Restore tabs\s+On\s+default`, rows["options.tui.restore_tabs"])
	require.Regexp(t, `Auto-summarize\s+On\s+default`, rows["options.disable_auto_summarize"])

	for _, g := range []string{"Display", "Behaviour", "Privacy", "Advanced"} {
		require.Contains(t, view, g)
	}
}

func TestSettingsToggleBool(t *testing.T) {
	clearSettingsEnv(t)

	d := newTestSettings(t, nil)
	selectSetting(t, d, "options.tui.compact_mode")
	act := d.HandleMsg(keyMsg(' '))
	require.Equal(t, ActionSetSetting{Key: "options.tui.compact_mode", Value: true}, act)
	require.Contains(t, ansi.Strip(d.content()), "Saving…")

	// Presses while saving are ignored.
	require.Nil(t, d.HandleMsg(keyMsg(' ')))

	d.SetResult("Saved.", nil)
	require.Contains(t, ansi.Strip(d.content()), "Saved.")

	// Default-on settings turn off.
	selectSetting(t, d, "options.tui.mouse")
	require.Equal(t, ActionSetSetting{Key: "options.tui.mouse", Value: false},
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	d.SetResult("", errors.New("disk full"))
	require.Contains(t, ansi.Strip(d.content()), "! disk full")

	// Left and right do nothing on a bool.
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyRight}))
}

func TestSettingsInvertedBool(t *testing.T) {
	clearSettingsEnv(t)

	d := newTestSettings(t, nil)
	selectSetting(t, d, "options.disable_auto_summarize")
	// Auto-summarize shows On, so turning it off stores disable = true.
	require.Equal(t, ActionSetSetting{Key: "options.disable_auto_summarize", Value: true},
		d.HandleMsg(keyMsg(' ')))

	d = newTestSettings(t, &config.Options{DisableAutoSummarize: true})
	selectSetting(t, d, "options.disable_auto_summarize")
	require.Equal(t, ActionSetSetting{Key: "options.disable_auto_summarize", Value: false},
		d.HandleMsg(keyMsg(' ')))
}

func TestSettingsCycleEnum(t *testing.T) {
	clearSettingsEnv(t)

	d := newTestSettings(t, nil)
	selectSetting(t, d, "options.tui.diff_mode")
	require.Equal(t, ActionSetSetting{Key: "options.tui.diff_mode", Value: "unified"},
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyRight}))

	d = newTestSettings(t, nil)
	selectSetting(t, d, "options.tui.diff_mode")
	require.Equal(t, ActionSetSetting{Key: "options.tui.diff_mode", Value: "split"},
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyLeft}))

	d = newTestSettings(t, &config.Options{TUI: &config.TUIOptions{DiffMode: "split"}})
	selectSetting(t, d, "options.tui.diff_mode")
	require.Equal(t, ActionSetSetting{Key: "options.tui.diff_mode", Value: ""},
		d.HandleMsg(keyMsg(' ')), "cycling wraps around")

	d = newTestSettings(t, nil)
	selectSetting(t, d, "options.notifications")
	require.Equal(t, ActionSetSetting{Key: "options.notifications", Value: "disabled"},
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyLeft}))
}

func TestSettingsEnvLocked(t *testing.T) {
	clearSettingsEnv(t)
	t.Setenv("TADA_NO_AMBIENT", "1")
	t.Setenv("TADA_ENABLE_WEB_TOOLS", "true")

	d := newTestSettings(t, nil)
	d.SetSources(nil, nil)

	selectSetting(t, d, "options.tui.ambient")
	require.Nil(t, d.HandleMsg(keyMsg(' ')))
	view := ansi.Strip(d.content())
	require.Contains(t, view, "Set by TADA_NO_AMBIENT. Unset it to change this here.")
	require.Regexp(t, `Ambient line\s+Off\s+env`, view)

	selectSetting(t, d, "options.enable_web_tools")
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	require.Regexp(t, `Web tools\s+On\s+env`, ansi.Strip(d.content()))

	// "0" leaves the setting unlocked.
	t.Setenv("TADA_NO_AMBIENT", "0")
	selectSetting(t, d, "options.tui.ambient")
	require.Equal(t, ActionSetSetting{Key: "options.tui.ambient", Value: false}, d.HandleMsg(keyMsg(' ')))
}

func TestSettingsProjectOverrideNote(t *testing.T) {
	clearSettingsEnv(t)

	d := newTestSettings(t, nil)
	d.SetSources(map[string]config.FieldSource{
		"options.tui.compact_mode": {Kind: config.FieldSourceWorkspace},
	}, nil)
	selectSetting(t, d, "options.tui.compact_mode")
	require.Contains(t, ansi.Strip(d.content()), "A project config sets this.")
}

func TestSettingsScrollsToSelection(t *testing.T) {
	clearSettingsEnv(t)

	d := newTestSettings(t, nil)
	d.height = 20
	for range len(settingsList) {
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	view := ansi.Strip(d.content())
	require.Contains(t, view, "LSP debug logging")
	require.NotContains(t, view, "Compact mode")
	require.LessOrEqual(t, strings.Count(view, "\n")+1, 20)

	require.Equal(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}

func TestSettingsGolden(t *testing.T) {
	clearSettingsEnv(t)

	off := false
	sources := map[string]config.FieldSource{
		"options.tui.compact_mode": {Kind: config.FieldSourceGlobal, Shell: true},
		"options.tui.mouse":        {Kind: config.FieldSourceGlobal},
		"options.tui.diff_mode":    {Kind: config.FieldSourceProject},
	}
	opts := &config.Options{TUI: &config.TUIOptions{CompactMode: true, DiffMode: "split", Mouse: &off}}

	draw := func(t *testing.T, d *Settings, w, h int) {
		t.Helper()
		scr := uv.NewScreenBuffer(w, h)
		d.Draw(scr, uv.Rect(0, 0, w, h))
		golden.RequireEqual(t, []byte(scr.String()))
	}

	t.Run("Tall", func(t *testing.T) {
		d := newTestSettings(t, opts)
		d.SetSources(sources, nil)
		draw(t, d, 90, 44)
	})
	t.Run("Scrolled", func(t *testing.T) {
		d := newTestSettings(t, opts)
		d.SetSources(sources, nil)
		selectSetting(t, d, "options.enable_web_tools")
		draw(t, d, 90, 24)
	})
	t.Run("Narrow", func(t *testing.T) {
		d := newTestSettings(t, opts)
		d.SetSources(sources, nil)
		draw(t, d, 40, 24)
	})
	t.Run("EnvLocked", func(t *testing.T) {
		t.Setenv("TADA_NO_RICH", "1")
		d := newTestSettings(t, opts)
		d.SetSources(sources, nil)
		selectSetting(t, d, "options.tui.rich_blocks")
		draw(t, d, 90, 24)
	})
}
