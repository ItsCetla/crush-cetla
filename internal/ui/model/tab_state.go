package model

import (
	"strconv"
	"time"

	"charm.land/lipgloss/v2"
)

// tabInactiveAfter is how long an idle session waits before its state
// line says "Inactive"; a fresh session shows nothing.
const tabInactiveAfter = 5 * time.Minute

// sessionStateText is the one-line state of the session in view, for the
// compact header and under the sidebar title: "◎ Needs you · 2m ago",
// "⠋ Working · 5m", "✔ Done · just now", "✗ Failed · 2m ago" or
// "Inactive · 3h". It is empty for a fresh idle session.
func (m *UI) sessionStateText(muted lipgloss.Style, now time.Time) string {
	if m.session == nil || m.session.ID == "" {
		return ""
	}
	s := m.tabStatusOf(m.tabOwner(m.session.ID))
	mark, kind := m.tabMark(s, now)
	var label, age string
	switch kind {
	case tabMarkKindNeeds:
		label, age = "Needs you", agoText(s.since, now)
	case tabMarkKindWorking:
		label = "Working"
		if d := now.Sub(s.since); !s.since.IsZero() && d >= time.Minute {
			age = runningAge(d)
		}
	case tabMarkKindFailed:
		label, age = "Failed", agoText(s.since, now)
	case tabMarkKindUnread, tabMarkKindRead:
		label, age = "Done", agoText(s.since, now)
	default:
		if m.session.UpdatedAt <= 0 {
			return ""
		}
		d := now.Sub(time.Unix(m.session.UpdatedAt, 0))
		if d < tabInactiveAfter {
			return ""
		}
		return muted.Render("Inactive · " + shortAge(d))
	}
	st := lipgloss.NewStyle().Foreground(s.markColor(m.markKindColor(kind), now))
	if kind == tabMarkKindNeeds || kind == tabMarkKindUnread {
		st = st.Bold(true)
	}
	out := st.Render(mark + " " + label)
	if age != "" {
		out += muted.Render(" · " + age)
	}
	return out
}

// otherTabsNeedingText is a "◎ 2" badge for the other tabs that wait for
// the user, or empty.
func (m *UI) otherTabsNeedingText() string {
	n := 0
	cur := m.activeTabIndex()
	for _, i := range m.tabsNeedingYou() {
		if i != cur {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(m.com.Styles.Tabs.Needs).Bold(true).
		Render(tabMarkNeeds + " " + strconv.Itoa(n))
}

// agoText formats when something happened: "just now", "2m ago".
func agoText(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	if d < time.Minute {
		return "just now"
	}
	return shortAge(d) + " ago"
}

// runningAge formats how long something has run: "5m", "1h 20m".
func runningAge(d time.Duration) string {
	if d < time.Hour {
		return shortAge(d)
	}
	h := int(d / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	if mins == 0 || h >= 24 {
		return shortAge(d)
	}
	return strconv.Itoa(h) + "h " + strconv.Itoa(mins) + "m"
}
