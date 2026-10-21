package model

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/crush/internal/ui/common"
)

// Vertical tabs: with options.tui.vertical_tabs on, the open sessions are
// listed in the sidebar, grouped, instead of in the bar above the chat.

// sidebarTabRowKind says what a click on a row of the list does.
type sidebarTabRowKind int

const (
	sideRowNone sidebarTabRowKind = iota
	sideRowTab
	sideRowGroup
	sideRowNew
)

// sidebarTabRow is one line of the sessions list.
type sidebarTabRow struct {
	kind  sidebarTabRowKind
	index int
	group string
}

// sidebarTabRows maps the sessions list's lines back to tabs. start is
// the sidebar content line the list starts on, or -1 when it is not
// shown; top is the screen row the sidebar's scrolling content starts at.
type sidebarTabRows struct {
	start int
	top   int
	rows  []sidebarTabRow
}

// sidebarTabsVisible reports whether the sessions list is in the sidebar.
func (m *UI) sidebarTabsVisible() bool {
	if m.com == nil || m.com.Workspace == nil || m.state != uiChat || m.isCompact || m.session == nil {
		return false
	}
	cfg := m.com.Config()
	return cfg != nil && cfg.Options != nil && cfg.Options.TUI != nil && cfg.Options.TUI.VerticalTabs
}

// sessionsInfo renders the sidebar's sessions list and records its rows.
func (m *UI) sessionsInfo(width int, now time.Time) (string, []sidebarTabRow) {
	if !m.sidebarTabsVisible() || len(m.tabs) == 0 {
		return "", nil
	}
	t := m.com.Styles
	info := ""
	if n := len(m.tabsNeedingYou()); n > 0 {
		info = lipgloss.NewStyle().Foreground(t.Tabs.Needs).Render(strconv.Itoa(n) + " " + needsLabel(n))
	}
	lines := []string{common.Section(t, t.Resource.Heading.Render("Sessions"), width, info)}
	rows := []sidebarTabRow{{kind: sideRowNone}}
	add := func(line string, row sidebarTabRow) {
		lines = append(lines, line)
		rows = append(rows, row)
	}

	active := m.activeTabIndex()
	var groups []string
	seen := map[string]bool{}
	ungrouped := 0
	for _, tab := range m.tabs {
		switch {
		case tab.group == "":
			ungrouped++
		case !seen[tab.group]:
			seen[tab.group] = true
			groups = append(groups, tab.group)
		}
	}
	for _, g := range groups {
		add(m.sideGroupRow(g, width), sidebarTabRow{kind: sideRowGroup, group: g})
		for _, i := range m.tabGroupMembers(g) {
			if m.tabHidden(i, active) {
				continue
			}
			add(m.sideTabRow(i, i == active, width, now), sidebarTabRow{kind: sideRowTab, index: i})
		}
	}
	if ungrouped > 0 {
		if len(groups) > 0 {
			add(t.Tabs.Group.Render("Other"), sidebarTabRow{kind: sideRowNone})
		}
		for i, tab := range m.tabs {
			if tab.group == "" {
				add(m.sideTabRow(i, i == active, width, now), sidebarTabRow{kind: sideRowTab, index: i})
			}
		}
	}
	add(t.Tabs.New.Render("+ New session"), sidebarTabRow{kind: sideRowNew})
	return strings.Join(lines, "\n"), rows
}

// sideGroupRow renders a group header: "▾ name 3  1 needs you".
func (m *UI) sideGroupRow(name string, width int) string {
	t := m.com.Styles.Tabs
	arrow := "▾ "
	if m.tabx.collapsed[name] {
		arrow = "▸ "
	}
	members := m.tabGroupMembers(name)
	need := 0
	for _, i := range members {
		if !m.tabs[i].aside && m.tabStatusOf(m.tabs[i].id).needsYou() {
			need++
		}
	}
	line := t.Group.Render(arrow+name) + t.GroupCount.Render(" "+strconv.Itoa(len(members)))
	if need > 0 {
		line += lipgloss.NewStyle().Foreground(t.Needs).Render("  " + strconv.Itoa(need) + " " + needsLabel(need))
	}
	return ansi.Truncate(line, width, "…")
}

// sideTabRow renders one session: mark, number, title and age, with the
// session in view highlighted across the row.
func (m *UI) sideTabRow(i int, isActive bool, width int, now time.Time) string {
	t := m.com.Styles
	tab := m.tabs[i]
	st := m.tabStatusOf(tab.id)
	base := lipgloss.NewStyle()
	if isActive {
		base = t.Tabs.SidebarActive
	}
	title := base.Foreground(t.Tabs.Inactive.GetForeground())
	if isActive {
		title = title.Foreground(t.Tabs.Active.GetForeground()).Bold(true)
	}
	if tab.aside {
		title = title.Foreground(t.Tabs.Aside.GetForeground()).Italic(true)
	}
	if st != nil && st.unread {
		title = title.Bold(true)
	}
	muted := base.Foreground(t.Tabs.InactiveIndex.GetForeground())

	mark := " "
	markStyle := base
	if s, kind := m.tabMark(st, now); s != "" {
		mark = s
		markStyle = base.Foreground(st.markColor(m.markKindColor(kind), now))
		if kind == tabMarkKindNeeds || kind == tabMarkKindUnread {
			markStyle = markStyle.Bold(true)
		}
	}
	num := "  "
	if i < 9 {
		num = strconv.Itoa(i+1) + " "
	}
	age := tabAge(st, now)
	left := markStyle.Render(mark+" ") + muted.Render(num)
	room := width - lipgloss.Width(left) - lipgloss.Width(age) - 1
	name := ansi.Truncate(tab.title, max(room, 1), "…")
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(name)-lipgloss.Width(age))
	return left + title.Render(name) + base.Render(strings.Repeat(" ", gap)) + muted.Render(age)
}

// handleSidebarTabClick handles a click at screen row y over the sidebar.
// It reports whether the click hit the sessions list.
func (m *UI) handleSidebarTabClick(y int, middle bool) (tea.Cmd, bool) {
	side := m.tabx.side
	if side.start < 0 || y < side.top {
		return nil, false
	}
	line := y - side.top + m.sidebarOffset - side.start
	if line < 0 || line >= len(side.rows) {
		return nil, false
	}
	row := side.rows[line]
	switch row.kind {
	case sideRowTab:
		if middle {
			return m.closeTab(row.index), true
		}
		return m.switchToTab(row.index), true
	case sideRowGroup:
		if !middle {
			m.toggleGroupCollapsed(row.group)
		}
		return nil, true
	case sideRowNew:
		if middle {
			return nil, true
		}
		return m.newSessionFromTabs(), true
	}
	return nil, false
}
