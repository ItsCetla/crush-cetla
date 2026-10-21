package model

import (
	"cmp"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/crush/internal/claudecode"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/notification"
)

// Claude Code sessions: with options.tui.claude_sessions on, the sidebar
// lists the interactive Claude Code sessions running on this machine and
// Ta-Da announces one when it starts waiting for you. It only reads the
// CLI's own session files (~/.claude/sessions/*.json); it never talks to
// the sessions or sends anything anywhere.

// claudeSessionsInterval is how often the session files are read.
const claudeSessionsInterval = 5 * time.Second

// claudeSessions is the last read of the Claude Code sessions.
type claudeSessions struct {
	list []claudecode.LiveSession
	// seeded is set after the first read, which never announces.
	seeded bool
	// announced holds sessionId+statusUpdated of each wait announced.
	announced map[string]bool
}

// claudeSessionsMsg carries one read of the session files.
type claudeSessionsMsg struct {
	list []claudecode.LiveSession
}

// claudeSessionsEnabled reports whether the opt-in list is on.
func (m *UI) claudeSessionsEnabled() bool {
	if m.com == nil || m.com.Workspace == nil {
		return false
	}
	cfg := m.com.Config()
	return cfg != nil && cfg.Options != nil && cfg.Options.TUI != nil && cfg.Options.TUI.ClaudeSessions
}

// claudeSessionsPoll reads the session files after delay, off the UI
// goroutine.
func (m *UI) claudeSessionsPoll(delay time.Duration) tea.Cmd {
	if !m.claudeSessionsEnabled() {
		return nil
	}
	read := func() tea.Msg { return claudeSessionsMsg{list: claudecode.LiveSessions()} }
	if delay <= 0 {
		return read
	}
	return tea.Tick(delay, func(time.Time) tea.Msg { return read() })
}

// handleClaudeSessions stores a read, announces sessions that started
// waiting and schedules the next read.
func (m *UI) handleClaudeSessions(msg claudeSessionsMsg) tea.Cmd {
	c := &m.tabx.claude
	if c.announced == nil {
		c.announced = make(map[string]bool)
	}
	var cmds []tea.Cmd
	live := make(map[string]bool, len(msg.list))
	for _, s := range msg.list {
		if s.Status != "waiting" {
			continue
		}
		key := s.SessionID + "@" + strconv.FormatInt(s.StatusUpdated.UnixMilli(), 10)
		live[key] = true
		if c.announced[key] {
			continue
		}
		c.announced[key] = true
		if c.seeded {
			cmds = append(cmds, m.announce("", notification.Notification{
				Title:   "Claude Code is waiting...",
				Message: claudeSessionLabel(s) + " needs your input",
			}))
		}
	}
	// Forget waits that ended, so the map stays small.
	for k := range c.announced {
		if !live[k] {
			delete(c.announced, k)
		}
	}
	c.list = msg.list
	c.seeded = true
	cmds = append(cmds, m.claudeSessionsPoll(claudeSessionsInterval))
	return tea.Batch(cmds...)
}

// claudeSessionLabel names a session: its name, else its folder.
func claudeSessionLabel(s claudecode.LiveSession) string {
	if name := strings.TrimSpace(s.Name); name != "" {
		return name
	}
	return cmp.Or(filepath.Base(s.Cwd), "session "+strconv.Itoa(s.PID))
}

// claudeSessionsInfo renders the sidebar list of Claude Code sessions.
func (m *UI) claudeSessionsInfo(width int, now time.Time) string {
	if !m.claudeSessionsEnabled() || len(m.tabx.claude.list) == 0 {
		return ""
	}
	t := m.com.Styles
	waiting := 0
	for _, s := range m.tabx.claude.list {
		if s.Status == "waiting" {
			waiting++
		}
	}
	info := ""
	if waiting > 0 {
		info = lipgloss.NewStyle().Foreground(t.Tabs.Needs).Render(strconv.Itoa(waiting) + " " + needsLabel(waiting))
	}
	lines := []string{common.Section(t, t.Resource.Heading.Render("Claude Code"), width, info)}
	for _, s := range m.tabx.claude.list {
		mark, style := "○", t.Control.Muted
		switch s.Status {
		case "waiting":
			mark, style = tabMarkNeeds, lipgloss.NewStyle().Foreground(t.Tabs.Needs).Bold(true)
		case "busy":
			// A steady dot: the list redraws only when it is read again.
			mark, style = "●", lipgloss.NewStyle().Foreground(t.Tabs.MarkWorking)
		}
		age := ""
		if !s.StatusUpdated.IsZero() {
			age = shortAge(now.Sub(s.StatusUpdated))
		}
		left := style.Render(mark) + " "
		room := width - lipgloss.Width(left) - lipgloss.Width(age) - 1
		name := ansi.Truncate(claudeSessionLabel(s), max(room, 1), "…")
		gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(name)-lipgloss.Width(age))
		lines = append(lines, left+t.Control.Value.Render(name)+strings.Repeat(" ", gap)+t.Control.Muted.Render(age))
	}
	return strings.Join(lines, "\n")
}
