package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/x/ansi"
)

// controlInfo renders the sidebar's Control section: who's in control and
// the last few things the agent did. It returns "" when the control hub isn't
// available.
func (m *UI) controlInfo(width int, now time.Time) string {
	s := m.controlSnapshot()
	if s == nil {
		return ""
	}
	t := m.com.Styles
	st := s.State
	var who string
	switch {
	case st.Auto:
		who = t.Control.Alert.Render("Auto") + t.Control.Muted.Render(" · acts without asking")
	case st.Edits:
		who = t.Control.Warn.Render("Edits") + t.Control.Muted.Render(" · project edits run")
	default:
		who = t.Control.On.Render("You") + t.Control.Muted.Render(" · the agent asks first")
	}
	count := t.Resource.AdditionalText.Render(fmt.Sprintf("%d", s.SessionActions))
	lines := []string{
		common.Section(t, t.Resource.Heading.Render("Control"), width, count),
		ansi.Truncate(who, width, "…"),
	}
	if st.Waiting {
		lines = append(lines, t.Control.Warn.Render(ansi.Truncate("A request is waiting", width, "…")))
	}
	if len(s.Recent) == 0 {
		lines = append(lines, t.Resource.AdditionalText.Render(ansi.Truncate("Nothing yet this session", width, "…")))
	}
	for _, a := range s.Recent {
		head := dialog.ActionMark(t, a) + " "
		ago := " " + control.Ago(a.At, now)
		room := max(1, width-2-ansi.StringWidth(ago))
		lines = append(lines, head+
			t.Resource.AdditionalText.Render(ansi.Truncate(a.Summary(), room, "…"))+
			t.Control.Muted.Render(ago))
	}
	return strings.Join(lines, "\n")
}

// missionInfo renders the sidebar's Mission section for the session's
// mission, or "" when there isn't one.
func (m *UI) missionInfo(width int) string {
	s := m.controlSnapshot()
	if s == nil || s.Mission == nil {
		return ""
	}
	t := m.com.Styles
	ms := s.Mission
	done, total := ms.Progress()
	info := ""
	if total > 0 {
		info = t.Resource.AdditionalText.Render(fmt.Sprintf("%d/%d", done, total))
	}
	glyph := control.MissionGlyph(ms.Status, done, total)
	lines := []string{
		common.Section(t, t.Resource.Heading.Render("Mission"), width, info),
		t.Control.Value.Render(ansi.Truncate(glyph+" "+ms.Title, width, "…")),
	}
	if total > 0 {
		pct := done * 100 / total
		lines = append(lines, t.Control.Pip.Render(control.Pips(done, total, min(12, max(4, width-6))))+
			t.Control.Muted.Render(fmt.Sprintf(" %d%%", pct)))
		// The next open item is what the agent should be working on.
		for _, c := range ms.Criteria {
			if !c.Done {
				lines = append(lines, t.Resource.AdditionalText.Render(ansi.Truncate("next: "+c.Text, width, "…")))
				break
			}
		}
	}
	if ms.Status != control.MissionActive {
		lines = append(lines, t.Control.Muted.Render(string(ms.Status)))
	}
	return strings.Join(lines, "\n")
}
