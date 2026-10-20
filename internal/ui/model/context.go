package model

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/dialog"
)

// contextResultMsg delivers the outcome of a Magic Context change.
type contextResultMsg struct {
	notice string
	err    error
}

// openContextDialog opens the context settings screen.
func (m *UI) openContextDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ContextID) {
		m.dialog.CloseDialog(dialog.ContextID)
	}
	m.dialog.OpenDialog(dialog.NewContext(m.com))
	return nil
}

// setMagicContextCmd saves the Magic Context switch to the global config
// and rebuilds the agents so it applies without a restart.
func (m *UI) setMagicContextCmd(enabled bool) tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	ws := m.com.Workspace
	return m.updateAgentModelCmd(func() tea.Msg {
		if err := ws.SetConfigField(config.ScopeGlobal, "options.magic_context.enabled", enabled); err != nil {
			return contextResultMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := ws.UpdateAgentModel(ctx); err != nil {
			return contextResultMsg{err: err}
		}
		if cfg := ws.Config(); cfg != nil && cfg.Options.MagicContextEnabled() != enabled {
			return contextResultMsg{notice: "Saved, but a project config overrides it here."}
		}
		if enabled {
			return contextResultMsg{notice: "Magic Context is on."}
		}
		return contextResultMsg{notice: "Magic Context is off."}
	})
}

// handleContextResult shows a finished change.
func (m *UI) handleContextResult(msg contextResultMsg) {
	if !m.dialog.ContainsDialog(dialog.ContextID) {
		return
	}
	if d, ok := m.dialog.Dialog(dialog.ContextID).(*dialog.Context); ok {
		d.SetResult(msg.notice, msg.err)
	}
}
