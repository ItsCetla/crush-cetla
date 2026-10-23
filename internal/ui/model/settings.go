package model

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/dialog"
)

// settingSourcesMsg carries which config file sets each settings key.
type settingSourcesMsg struct {
	sources map[string]config.FieldSource
	err     error
}

// settingResultMsg delivers the outcome of a settings change.
type settingResultMsg struct {
	key    string
	notice string
	err    error
}

// openSettingsDialog opens the settings screen and looks up, in the
// background, where each value comes from.
func (m *UI) openSettingsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.SettingsID) {
		m.dialog.CloseDialog(dialog.SettingsID)
	}
	m.dialog.OpenDialog(dialog.NewSettings(m.com))
	return m.loadSettingSourcesCmd()
}

// loadSettingSourcesCmd reads the config files to find which one sets each
// key. It may run the global tadarc, so it never runs on the update path.
func (m *UI) loadSettingSourcesCmd() tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	cfg := m.com.Config()
	if cfg == nil || cfg.Options == nil {
		return nil
	}
	workingDir := m.com.Workspace.WorkingDir()
	dataDir := cfg.Options.DataDirectory
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sources, err := config.FieldSources(ctx, workingDir, dataDir, dialog.SettingKeys())
		return settingSourcesMsg{sources: sources, err: err}
	}
}

// handleSettingSources hands the sources to the open settings screen.
func (m *UI) handleSettingSources(msg settingSourcesMsg) {
	if d, ok := m.dialog.Dialog(dialog.SettingsID).(*dialog.Settings); ok {
		d.SetSources(msg.sources, msg.err)
	}
}

// setSettingCmd saves one settings value to the global config. Web tools
// also rebuild the agents so the tool list changes without a restart.
func (m *UI) setSettingCmd(key string, value any) tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	ws := m.com.Workspace
	save := func() tea.Msg {
		if err := ws.SetConfigField(config.ScopeGlobal, key, value); err != nil {
			return settingResultMsg{key: key, err: fmt.Errorf("could not save: %w", err)}
		}
		if key == "options.enable_web_tools" {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := ws.UpdateAgentModel(ctx); err != nil {
				return settingResultMsg{key: key, err: err}
			}
		}
		if got, ok := dialog.SettingStored(ws.Config(), key); ok && got != value {
			return settingResultMsg{key: key, notice: "Saved, but a project config overrides it here."}
		}
		return settingResultMsg{key: key, notice: "Saved."}
	}
	if key == "options.enable_web_tools" {
		return m.updateAgentModelCmd(save)
	}
	return save
}

// handleSettingResult applies a saved change to the running UI where it
// can take effect at once, shows the outcome, and refreshes the sources.
func (m *UI) handleSettingResult(msg settingResultMsg) tea.Cmd {
	var cmds []tea.Cmd
	if msg.err == nil {
		cmds = append(cmds, m.applyLiveSetting(msg.key))
	}
	d, ok := m.dialog.Dialog(dialog.SettingsID).(*dialog.Settings)
	if !ok {
		return tea.Batch(cmds...)
	}
	d.SetResult(msg.notice, msg.err)
	if msg.err == nil {
		cmds = append(cmds, m.loadSettingSourcesCmd())
	}
	return tea.Batch(cmds...)
}

// applyLiveSetting brings the running UI in line with the reloaded config
// for the settings that apply without a restart.
func (m *UI) applyLiveSetting(key string) tea.Cmd {
	cfg := m.com.Config()
	if cfg == nil || cfg.Options == nil {
		return nil
	}
	opts := cfg.Options
	switch key {
	case "options.tui.compact_mode":
		if opts.TUI != nil && opts.TUI.CompactMode != m.forceCompactMode {
			return m.applyCompactMode(opts.TUI.CompactMode)
		}
	case "options.tui.transparent":
		m.isTransparent = opts.TUI.IsTransparent()
	case "options.tui.rich_blocks":
		chat.SetRichBlocks(opts.TUI.RichBlocksEnabled())
		if m.chat != nil {
			m.chat.InvalidateRenderCaches()
		}
	case "options.tui.text_width":
		chat.SetMaxTextWidth(opts.TUI.MaxTextWidth())
		if m.chat != nil {
			m.chat.InvalidateRenderCaches()
		}
	case "options.tui.ambient":
		if m.ambient != nil {
			m.ambient.enabled = opts.TUI.AmbientEnabled()
		}
	case "options.tui.vertical_tabs":
		m.updateLayoutAndSize()
	case "options.tui.mouse":
		m.mouseEnabled = opts.TUI == nil || opts.TUI.Mouse == nil || *opts.TUI.Mouse
	case "options.notifications":
		m.notifyBackend = selectNotificationBackend(m.caps, cfg)
	case "options.progress":
		m.progressBarEnabled = opts.Progress == nil || *opts.Progress
	}
	return nil
}
