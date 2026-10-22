package model

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/rewind"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// rewindTimelineMsg delivers a session's turns to the rewind dialog.
type rewindTimelineMsg struct {
	sessionID string
	turns     []rewind.Turn
	err       error
}

// rewindPlanMsg delivers a dry run to the rewind dialog.
type rewindPlanMsg struct {
	sessionID string
	req       rewind.Request
	res       rewind.Result
	err       error
}

// rewindDoneMsg delivers the outcome of a rewind.
type rewindDoneMsg struct {
	sessionID string
	req       rewind.Request
	res       rewind.Result
	err       error
}

// openRewindDialog opens the timeline for the current session. messageID,
// when set, preselects the turn it starts.
func (m *UI) openRewindDialog(messageID string) tea.Cmd {
	if !m.hasSession() {
		return util.ReportWarn("Nothing to rewind yet: start a chat first.")
	}
	if m.dialog.ContainsDialog(dialog.RewindID) {
		m.dialog.CloseDialog(dialog.RewindID)
	}
	sessionID := m.session.ID
	m.dialog.OpenDialog(dialog.NewRewind(m.com, sessionID, m.tabWorkingDir(), messageID))
	return m.rewindTimelineCmd(sessionID)
}

func (m *UI) rewindTimelineCmd(sessionID string) tea.Cmd {
	ws := m.com.Workspace
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		turns, err := ws.SessionTimeline(ctx, sessionID)
		return rewindTimelineMsg{sessionID: sessionID, turns: turns, err: err}
	}
}

func (m *UI) rewindPlanCmd(sessionID string, req rewind.Request) tea.Cmd {
	ws := m.com.Workspace
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := ws.RewindSession(ctx, sessionID, req)
		return rewindPlanMsg{sessionID: sessionID, req: req, res: res, err: err}
	}
}

func (m *UI) rewindCmd(sessionID string, req rewind.Request) tea.Cmd {
	ws := m.com.Workspace
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := ws.RewindSession(ctx, sessionID, req)
		return rewindDoneMsg{sessionID: sessionID, req: req, res: res, err: err}
	}
}

// rewindDialog returns the open rewind dialog for sessionID, or nil.
func (m *UI) rewindDialog(sessionID string) *dialog.Rewind {
	if !m.dialog.ContainsDialog(dialog.RewindID) {
		return nil
	}
	d, ok := m.dialog.Dialog(dialog.RewindID).(*dialog.Rewind)
	if !ok || d.SessionID() != sessionID {
		return nil
	}
	return d
}

// handleRewindMsg routes rewind replies. It reports whether msg was one.
func (m *UI) handleRewindMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case rewindTimelineMsg:
		if d := m.rewindDialog(msg.sessionID); d != nil {
			d.SetTimeline(msg.turns, msg.err)
		}
		return nil, true
	case rewindPlanMsg:
		if d := m.rewindDialog(msg.sessionID); d != nil {
			d.SetPlan(msg.req, msg.res, msg.err)
		}
		return nil, true
	case rewindDoneMsg:
		return m.handleRewindDone(msg), true
	}
	return nil, false
}

// handleRewindDone closes the dialog, reloads the chat and sidebar, and
// puts the removed prompt back in the editor.
func (m *UI) handleRewindDone(msg rewindDoneMsg) tea.Cmd {
	if msg.err != nil {
		if d := m.rewindDialog(msg.sessionID); d != nil {
			d.SetResult(msg.err)
			return nil
		}
		return util.ReportError(msg.err)
	}
	if m.rewindDialog(msg.sessionID) != nil {
		m.dialog.CloseDialog(dialog.RewindID)
	}
	var cmds []tea.Cmd
	if m.hasSession() && m.session.ID == msg.sessionID {
		// Reload rather than patch: the chat, the sidebar's file list and
		// the read-file set all changed underneath us.
		cmds = append(cmds, m.loadSession(msg.sessionID))
		if msg.res.Prompt != "" && msg.req.Mode != rewind.ModeFiles && strings.TrimSpace(m.textarea.Value()) == "" {
			prevHeight := m.textarea.Height()
			m.textarea.SetValue(msg.res.Prompt)
			m.textarea.MoveToEnd()
			m.syncBangModeFromTextarea()
			cmds = append(cmds, m.handleTextareaHeightChange(prevHeight))
		}
	}
	text, warn := rewindSummary(msg.req, msg.res)
	if warn {
		cmds = append(cmds, util.ReportWarn(text))
	} else {
		cmds = append(cmds, util.ReportInfo(text))
	}
	return tea.Batch(cmds...)
}

// rewindSummary describes a finished rewind for the status bar. warn is
// set when some files were left alone.
func rewindSummary(req rewind.Request, res rewind.Result) (text string, warn bool) {
	var restored, deleted, skipped int
	for _, f := range res.Files {
		switch {
		case f.Skipped:
			skipped++
		case f.Op == rewind.OpDelete:
			deleted++
		default:
			restored++
		}
	}
	var parts []string
	if req.Mode != rewind.ModeChat {
		parts = append(parts, fmt.Sprintf("%s restored", countOf(restored, "file")))
		if deleted > 0 {
			parts = append(parts, fmt.Sprintf("%d deleted", deleted))
		}
		if skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped (edited outside Ta-Da)", skipped))
		}
	}
	if req.Mode != rewind.ModeFiles {
		parts = append(parts, fmt.Sprintf("%s removed", countOf(res.MessagesDeleted, "message")))
	}
	return "Rewound: " + strings.Join(parts, ", ") + ".", skipped > 0
}

func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
