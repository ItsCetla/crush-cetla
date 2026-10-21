package model

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/limits"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/x/ansi"
)

// limitsPollInterval is how often the usage-limits file is checked. The
// file is only re-read when it changed.
const limitsPollInterval = 30 * time.Second

// limitsState holds the last reading of options.tui.limit_rings.
type limitsState struct {
	reader    *limits.Reader
	providers []limits.Provider
	err       error
}

// limitsMsg carries one reading of the file at path.
type limitsMsg struct {
	path      string
	providers []limits.Provider
	err       error
}

// limitsPath returns the configured file, or "" when the option is off.
func (m *UI) limitsPath() string {
	cfg := m.com.Config()
	if cfg == nil || cfg.Options == nil {
		return ""
	}
	return cfg.Options.TUI.LimitRingsPath()
}

// limitsPoll reads the file after delay, off the UI thread. It keeps
// ticking while the option is off so turning it on in settings takes
// effect without a restart, but then it touches no files.
func (m *UI) limitsPoll(delay time.Duration) tea.Cmd {
	path := m.limitsPath()
	if path == "" {
		m.lim.reader = nil
	} else if m.lim.reader == nil || m.lim.reader.Path != path {
		m.lim.reader = &limits.Reader{Path: path}
	}
	r := m.lim.reader
	return tea.Tick(delay, func(now time.Time) tea.Msg {
		if r == nil {
			return limitsMsg{}
		}
		ps, err := r.Read(now)
		return limitsMsg{path: r.Path, providers: ps, err: err}
	})
}

// handleLimits stores a reading when it is for the file still configured,
// and schedules the next one.
func (m *UI) handleLimits(msg limitsMsg) tea.Cmd {
	if msg.path == m.limitsPath() {
		m.lim.providers, m.lim.err = msg.providers, msg.err
	} else {
		m.lim.providers, m.lim.err = nil, nil
	}
	return m.limitsPoll(limitsPollInterval)
}

// limitsInfo renders the sidebar's Limits section, or "" when the option
// is off or the file has no providers yet.
func (m *UI) limitsInfo(width int, now time.Time) string {
	if m.limitsPath() == "" || (len(m.lim.providers) == 0 && m.lim.err == nil) {
		return ""
	}
	t := m.com.Styles
	lines := []string{common.Section(t, t.Resource.Heading.Render("Limits"), width, "")}
	if m.lim.err != nil {
		lines = append(lines, t.Control.Muted.Render(ansi.Truncate("Can't read the limits file", width, "…")))
		return strings.Join(lines, "\n")
	}
	for _, p := range m.lim.providers {
		name := t.Control.Value.Render(p.Name)
		if p.Stale {
			tag := t.Control.Muted.Render("stale")
			gap := max(1, width-lipgloss.Width(name)-lipgloss.Width(tag))
			lines = append(lines, ansi.Truncate(name+strings.Repeat(" ", gap)+tag, width, "…"))
			continue
		}
		lines = append(lines, ansi.Truncate(name, width, "…"))
		for _, row := range []struct {
			label string
			w     limits.Window
		}{{"5h", p.Five}, {"week", p.Week}} {
			lines = append(lines, ansi.Truncate(m.limitRow(row.label, row.w, now), width, "…"))
		}
	}
	return strings.Join(lines, "\n")
}

// limitRow renders " ◕ 5h    84%  3:12 PM".
func (m *UI) limitRow(label string, w limits.Window, now time.Time) string {
	t := m.com.Styles
	val := t.Control.Value
	switch {
	case w.Full():
		val = t.Control.Alert.Bold(true)
	case w.Warn():
		val = t.Control.Warn.Bold(true)
	case !w.Known:
		val = t.Control.Muted
	}
	pct := w.Percent()
	row := " " + val.Render(w.Ring()) + " " +
		t.Control.Label.Render(padRight(label, 5)) +
		val.Render(padLeft(pct, 4))
	if reset := w.ResetLabel(now); reset != "" {
		row += t.Control.Muted.Render("  " + reset)
	}
	return row
}

func padRight(s string, n int) string {
	return s + strings.Repeat(" ", max(0, n-ansi.StringWidth(s)))
}

func padLeft(s string, n int) string {
	return strings.Repeat(" ", max(0, n-ansi.StringWidth(s))) + s
}
