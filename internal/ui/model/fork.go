package model

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// forkDoneMsg delivers the outcome of a fork.
type forkDoneMsg struct {
	sessionID string
	fork      session.Session
	err       error
}

// forkSession copies sessionID into a new session, as far as atMessageID
// (empty copies everything), and opens the copy in a new tab.
func (m *UI) forkSession(sessionID, atMessageID string) tea.Cmd {
	if sessionID == "" {
		return util.ReportWarn("Nothing to fork yet: start a chat first.")
	}
	ws := m.com.Workspace
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fork, err := ws.ForkSession(ctx, sessionID, atMessageID)
		return forkDoneMsg{sessionID: sessionID, fork: fork, err: err}
	}
}

// forkCurrentSession forks the session being viewed at atMessageID.
func (m *UI) forkCurrentSession(atMessageID string) tea.Cmd {
	if !m.hasSession() {
		return util.ReportWarn("Nothing to fork yet: start a chat first.")
	}
	return m.forkSession(m.session.ID, atMessageID)
}

// handleForkDone closes the timeline, if the fork came from it, and opens
// the fork.
func (m *UI) handleForkDone(msg forkDoneMsg) tea.Cmd {
	if msg.err != nil {
		if d := m.rewindDialog(msg.sessionID); d != nil {
			d.SetForkResult(msg.err)
			return nil
		}
		return util.ReportError(fmt.Errorf("fork failed: %w", msg.err))
	}
	if m.rewindDialog(msg.sessionID) != nil {
		m.dialog.CloseDialog(dialog.RewindID)
	}
	return tea.Batch(
		m.loadSession(msg.fork.ID),
		util.ReportInfo(fmt.Sprintf("Forked into %q (%s).", msg.fork.Title, countOf(int(msg.fork.MessageCount), "message"))),
	)
}
