package model

import (
	"cmp"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/ui/notification"
)

// Notifications follow edges, not events: a session announces once when
// it starts waiting for you (however many permission requests pile up),
// once when its work is done and once when it fails. A cancel never
// announces, and neither does a tab set aside.

// announce sends n for sessionID unless its tab is set aside, and rings
// the bell when options.tui.notify_bell is on and the session is not in
// view.
func (m *UI) announce(sessionID string, n notification.Notification) tea.Cmd {
	if m.tabAside(sessionID) {
		return nil
	}
	cmds := []tea.Cmd{m.sendNotification(n)}
	if m.bellWanted(sessionID) {
		cmds = append(cmds, tea.Raw("\x07"))
	}
	return tea.Batch(cmds...)
}

// bellWanted reports whether an announcement for sessionID also rings the
// terminal bell.
func (m *UI) bellWanted(sessionID string) bool {
	if m.com == nil || m.com.Workspace == nil {
		return false
	}
	cfg := m.com.Config()
	if cfg == nil || cfg.Options == nil || cfg.Options.TUI == nil || !cfg.Options.TUI.NotifyBell {
		return false
	}
	if cfg.Options.Notifications == "disabled" || m.viewingSession(sessionID) {
		return false
	}
	// The bell backend already rang for this one.
	_, isBell := m.notifyBackend.(*notification.BellBackend)
	return !isBell || !m.shouldSendNotification()
}

// announceTabEdges announces each session that started waiting for the
// user since the last message, and re-arms sessions that stopped.
func (m *UI) announceTabEdges() tea.Cmd {
	var cmds []tea.Cmd
	for id, s := range m.tabStatuses {
		if !s.needsYou() {
			s.announced = false
			continue
		}
		if s.announced {
			continue
		}
		s.announced = true
		cmds = append(cmds, m.announce(id, notification.Notification{
			Title:   "Ta-Da is waiting...",
			Message: cmp.Or(s.needReason, "A session needs your input"),
		}))
	}
	return tea.Batch(cmds...)
}

// tabAside reports whether the tab for sessionID is set aside.
func (m *UI) tabAside(sessionID string) bool {
	if i := m.tabIndex(sessionID); i >= 0 {
		return m.tabs[i].aside
	}
	return false
}

// tabStatusTrace logs every change of a tab's status when
// TADA_STATUS_TRACE=1, to debug marks that look wrong.
var tabStatusTrace = os.Getenv("TADA_STATUS_TRACE") == "1"

// tabStatusSnapshot describes every session's status in one string per
// session, for the trace.
func (m *UI) tabStatusSnapshot() map[string]string {
	if !tabStatusTrace {
		return nil
	}
	out := make(map[string]string, len(m.tabStatuses))
	for id, s := range m.tabStatuses {
		out[id] = s.describe()
	}
	return out
}

// traceTabStatus logs the sessions whose status differs from before.
func (m *UI) traceTabStatus(before map[string]string, msg tea.Msg) {
	if !tabStatusTrace {
		return
	}
	after := m.tabStatusSnapshot()
	ids := make([]string, 0, len(after))
	for id := range after {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if before[id] != after[id] {
			slog.Info("Tab status", "session", id, "from", before[id], "to", after[id], "event", eventName(msg))
		}
	}
}

// describe renders a status as "working needs=perm:2 unread".
func (s *tabStatus) describe() string {
	if s == nil {
		return ""
	}
	parts := []string{[...]string{"idle", "working", "done", "failed"}[s.phase]}
	if len(s.perms) > 0 {
		parts = append(parts, "perms="+strings.Repeat("|", len(s.perms)))
	}
	if s.asking {
		parts = append(parts, "asking")
	}
	if s.unread {
		parts = append(parts, "unread")
	}
	return strings.Join(parts, " ")
}

// eventName names a message's type for the trace.
func eventName(msg tea.Msg) string {
	return strings.NewReplacer("github.com/charmbracelet/crush/internal/", "", "charm.land/", "").Replace(fmt.Sprintf("%T", msg))
}
