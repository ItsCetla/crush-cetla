package model

import (
	"image/color"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/crush/internal/session"
)

// Session tabs: every session opened during this run gets a tab in a
// one-row bar above the chat, so switching back and forth does not need
// the sessions dialog. Tabs live in memory only. The bar shows as soon as
// a session is open, so its "+" button is always there to start another.

const (
	// tabTitleMax caps a tab title's width in cells.
	tabTitleMax = 24
	// tabDraftTitle labels the unsent new session shown on the landing
	// page while other tabs are open.
	tabDraftTitle = "New session"
	// tabCloseMark is the clickable close mark on each tab.
	tabCloseMark = "×"
	// tabNewButton is the clickable new-session button after the tabs.
	tabNewButton = " + "
	// tabMoreLeft and tabMoreRight mark tabs scrolled out of view.
	tabMoreLeft  = "‹ "
	tabMoreRight = " ›"
)

// sessionTab is one open session in the tab bar.
type sessionTab struct {
	id    string
	title string
	// group names the group the tab belongs to, or "" for none. Tabs of
	// one group always sit next to each other.
	group string
	// aside marks a tab set aside: it stays open but never notifies and
	// is left out of "needs you".
	aside bool
	// dir is the session's working folder, or "" for the launch folder.
	dir string
}

// tabHitKind says what a click on part of the tab bar does.
type tabHitKind int

const (
	tabHitSelect tabHitKind = iota
	tabHitClose
	tabHitNew
	tabHitNeeds // the "N needs you" count: jump to the next such tab
	tabHitGroup // a group chip: fold or unfold the group
)

// tabHit is a clickable span of the tab bar, in cells relative to the
// bar's left edge. x1 is exclusive.
type tabHit struct {
	x0, x1 int
	kind   tabHitKind
	index  int
	group  string
}

// tabTitle returns the label shown for a session.
func tabTitle(s session.Session) string {
	title := cleanTabName(s.Title)
	if title == "" || strings.EqualFold(title, "new session") {
		return tabDraftTitle
	}
	return title
}

// tabBarVisible reports whether the current state shows the tab bar.
func (m *UI) tabBarVisible() bool {
	// The sidebar lists the tabs instead, but the bar still shows to
	// hold the rename / group prompt.
	if m.sidebarTabsVisible() && m.tabx.prompt == nil {
		return false
	}
	switch m.state {
	case uiChat, uiLanding:
		return len(m.tabs) >= 1
	}
	return false
}

// tabIndex returns the index of the tab for sessionID, or -1.
func (m *UI) tabIndex(sessionID string) int {
	for i, t := range m.tabs {
		if t.id == sessionID {
			return i
		}
	}
	return -1
}

// activeTabIndex returns the index of the tab being viewed, or -1 when
// the view is the landing page (a draft) or there is no session.
func (m *UI) activeTabIndex() int {
	if m.session == nil || m.state != uiChat {
		return -1
	}
	return m.tabIndex(m.session.ID)
}

// trackSessionTab adds a tab for s, or refreshes its title when it is
// already open.
func (m *UI) trackSessionTab(s *session.Session) {
	if s == nil || s.ID == "" || s.ParentSessionID != "" {
		return
	}
	if i := m.tabIndex(s.ID); i >= 0 {
		m.tabs[i].title = tabTitle(*s)
		m.tabs[i].dir = s.WorkingDir
		return
	}
	m.tabs = append(m.tabs, sessionTab{id: s.ID, title: tabTitle(*s), dir: s.WorkingDir})
}

// updateTabTitle refreshes the title of the tab for s, if one is open.
func (m *UI) updateTabTitle(s session.Session) {
	if i := m.tabIndex(s.ID); i >= 0 {
		m.tabs[i].title = tabTitle(s)
		m.tabs[i].dir = s.WorkingDir
	}
}

// removeTab drops the tab at i.
func (m *UI) removeTab(i int) {
	if i < 0 || i >= len(m.tabs) {
		return
	}
	m.dropTabStatus(m.tabs[i].id)
	m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
}

// neighbourTab returns the tab to show after the tab at i is removed
// from a list that had n tabs: the one to its right, else its left, or
// -1 when none is left. The returned index is into the shortened list.
func neighbourTab(i, n int) int {
	if n <= 1 {
		return -1
	}
	if i >= n-1 {
		return n - 2
	}
	return i
}

// switchToTab shows the session in the tab at i.
func (m *UI) switchToTab(i int) tea.Cmd {
	if i < 0 || i >= len(m.tabs) {
		return nil
	}
	if i == m.activeTabIndex() {
		return nil
	}
	return m.loadSession(m.tabs[i].id)
}

// cycleTab moves to the next (delta 1) or previous (delta -1) tab,
// wrapping around. From the landing page it moves to the first or last
// tab.
func (m *UI) cycleTab(delta int) tea.Cmd {
	n := len(m.tabs)
	if n == 0 {
		return nil
	}
	cur := m.activeTabIndex()
	if cur < 0 {
		if delta > 0 {
			return m.switchToTab(0)
		}
		return m.switchToTab(n - 1)
	}
	return m.switchToTab(((cur+delta)%n + n) % n)
}

// closeTab closes the tab at i. Closing the tab being viewed shows its
// neighbour, or the landing page when it was the last tab. Closing a tab
// only hides it; the session itself is kept.
func (m *UI) closeTab(i int) (cmd tea.Cmd) {
	if i < 0 || i >= len(m.tabs) {
		return nil
	}
	if hint := m.worktreeCloseHint(m.tabs[i]); hint != nil {
		defer func() { cmd = tea.Batch(cmd, hint) }()
	}
	if i != m.activeTabIndex() {
		m.removeTab(i)
		m.updateLayoutAndSize()
		return nil
	}
	next := neighbourTab(i, len(m.tabs))
	if next < 0 {
		m.removeTab(i)
		return m.newSession()
	}
	m.removeTab(i)
	return m.loadSession(m.tabs[next].id)
}

// closeActiveTab closes the tab being viewed.
func (m *UI) closeActiveTab() tea.Cmd {
	return m.closeTab(m.activeTabIndex())
}

// newSessionFromTabs starts a new session from the tab bar's "+".
func (m *UI) newSessionFromTabs() tea.Cmd {
	if !m.hasSession() {
		return nil
	}
	m.focus = uiFocusEditor
	return m.newSession()
}

// handleTabBarClick handles a click at x (relative to the bar). middle
// reports a middle-button click, which closes the tab under it.
func (m *UI) handleTabBarClick(x int, middle bool) tea.Cmd {
	for _, h := range m.tabHits {
		if x < h.x0 || x >= h.x1 {
			continue
		}
		switch {
		case h.kind == tabHitNeeds:
			if middle {
				return nil
			}
			return m.jumpToNeedingTab()
		case h.kind == tabHitNew:
			if middle {
				return nil
			}
			return m.newSessionFromTabs()
		case h.kind == tabHitGroup:
			if !middle {
				m.toggleGroupCollapsed(h.group)
			}
			return nil
		case h.kind == tabHitClose || middle:
			return m.closeTab(h.index)
		default:
			m.startTabDrag(h.index)
			return m.switchToTab(h.index)
		}
	}
	return nil
}

// tabCell is one rendered tab, ready to lay out.
type tabCell struct {
	index int    // index into m.tabs, or -1 for the landing draft or a chip
	group string // set on a group chip
	title string
	label string // rendered title part (index and title)
	close string // rendered close mark, empty for the draft
	width int
}

// renderTabBar renders the tab bar for the given width and returns it
// with its clickable spans.
func (m *UI) renderTabBar(width int) (string, []tabHit) {
	if width <= 0 {
		return "", nil
	}
	t := m.com.Styles.Tabs
	if p := m.tabx.prompt; p != nil {
		label := t.Prompt.Render(p.label())
		p.input.SetWidth(max(width-ansi.StringWidth(label)-2, 1))
		return ansi.Truncate(label+" "+p.input.View(), width, ""), nil
	}
	active := m.activeTabIndex()
	now := time.Now()

	needsBtn := ""
	if n := len(m.tabsNeedingYou()); n > 0 {
		needsBtn = t.NeedsCount.Render(" " + tabMarkNeeds + " " + strconv.Itoa(n) + " " + needsLabel(n) + " ")
	}
	newW := ansi.StringWidth(tabNewButton) + ansi.StringWidth(needsBtn)

	// Ages are the first thing to go when the tabs do not fit.
	cells := m.tabCells(active, now, true)
	if tabCellsWidth(cells)+newW > width {
		cells = m.tabCells(active, now, false)
	}
	if active < 0 && m.state == uiLanding {
		label := t.Active.Italic(true).Render(" " + tabDraftTitle + " ")
		cells = append(cells, tabCell{index: -1, label: label, width: ansi.StringWidth(label)})
	}
	if len(cells) == 0 {
		return "", nil
	}

	focus := len(cells) - 1
	for i, c := range cells {
		if active >= 0 && c.index == active {
			focus = i
		}
	}

	total := newW + tabCellsWidth(cells)

	start, end := 0, len(cells)
	if total > width {
		// Show a window of tabs around the focused one, reserving room
		// for the scroll marks and the "+" button.
		budget := width - newW - ansi.StringWidth(tabMoreLeft) - ansi.StringWidth(tabMoreRight)
		start, end = focus, focus+1
		used := cells[focus].width + 1
		for {
			grew := false
			if end < len(cells) && used+cells[end].width+1 <= budget {
				used += cells[end].width + 1
				end++
				grew = true
			}
			if start > 0 && used+cells[start-1].width+1 <= budget {
				start--
				used += cells[start].width + 1
				grew = true
			}
			if !grew {
				break
			}
		}
	}

	var b strings.Builder
	var hits []tabHit
	x := 0
	write := func(s string) {
		b.WriteString(s)
		x += ansi.StringWidth(s)
	}
	if start > 0 {
		write(t.More.Render(tabMoreLeft))
	}
	for i := start; i < end; i++ {
		c := cells[i]
		x0 := x
		write(c.label)
		if c.group != "" {
			hits = append(hits, tabHit{x0: x0, x1: x, kind: tabHitGroup, index: -1, group: c.group})
		}
		if c.index >= 0 {
			hits = append(hits, tabHit{x0: x0, x1: x, kind: tabHitSelect, index: c.index})
			cx := x
			write(c.close)
			hits = append(hits, tabHit{x0: cx, x1: x, kind: tabHitClose, index: c.index})
		}
		write(" ")
	}
	if end < len(cells) {
		write(t.More.Render(tabMoreRight))
	}
	nx := x
	write(t.New.Render(tabNewButton))
	hits = append(hits, tabHit{x0: nx, x1: x, kind: tabHitNew, index: -1})
	if needsBtn != "" {
		bx := x
		write(needsBtn)
		hits = append(hits, tabHit{x0: bx, x1: x, kind: tabHitNeeds, index: -1})
	}

	return ansi.Truncate(b.String(), width, ""), hits
}

// tabCells renders each open tab, with a chip before each group. A
// folded group shows only its chip and, when it is in view, the active
// tab. withAge adds how long ago each status changed.
func (m *UI) tabCells(active int, now time.Time, withAge bool) []tabCell {
	cells := make([]tabCell, 0, len(m.tabs)+1)
	for i, tab := range m.tabs {
		if tab.group != "" && (i == 0 || m.tabs[i-1].group != tab.group) {
			cells = append(cells, m.groupChip(tab.group, now))
		}
		if m.tabHidden(i, active) {
			continue
		}
		cells = append(cells, m.tabCell(i, i == active, now, withAge))
	}
	return cells
}

// groupChip renders the chip that starts a group: "▾ name" when open,
// "▸ name 3" plus the most urgent member mark when folded.
func (m *UI) groupChip(name string, now time.Time) tabCell {
	t := m.com.Styles.Tabs
	label := ansi.Truncate(name, tabTitleMax, "…")
	var b strings.Builder
	if !m.tabx.collapsed[name] {
		b.WriteString(t.Group.Render(" ▾ " + label + " "))
	} else {
		b.WriteString(t.Group.Render(" ▸ " + label))
		b.WriteString(t.GroupCount.Render(" " + strconv.Itoa(len(m.tabGroupMembers(name)))))
		top := m.groupTopStatus(name)
		if mark, kind := m.tabMark(top, now); mark != "" {
			c := top.markColor(m.markKindColor(kind), now)
			b.WriteString(t.Group.Foreground(c).Render(" " + mark))
		}
		b.WriteString(t.Group.Render(" "))
	}
	label = b.String()
	return tabCell{index: -1, group: name, title: name, label: label, width: ansi.StringWidth(label)}
}

// markKindColor is the theme color of a mark kind.
func (m *UI) markKindColor(kind tabMarkKind) color.Color {
	t := m.com.Styles.Tabs
	return tabMarkColor(t.MarkWorking, t.MarkNeeds, t.MarkDone, t.MarkRead, t.MarkFailed, kind)
}

// tabCell renders the tab at i.
func (m *UI) tabCell(i int, isActive bool, now time.Time, withAge bool) tabCell {
	t := m.com.Styles.Tabs
	tab := m.tabs[i]
	titleStyle, indexStyle, closeStyle := t.Inactive, t.InactiveIndex, t.InactiveClose
	if isActive {
		titleStyle, indexStyle, closeStyle = t.Active, t.ActiveIndex, t.ActiveClose
	}
	st := m.tabStatusOf(tab.id)
	if tab.aside {
		if isActive {
			titleStyle = titleStyle.Italic(true)
		} else {
			titleStyle = t.Aside
		}
	}
	// A tab waiting for the user gets a warm wash so it shows from
	// across the bar, even without reading its mark.
	if st.needsYou() && !tab.aside {
		wash := t.NeedsWash
		if isActive {
			wash = t.NeedsWashActive
		}
		if wash != nil {
			titleStyle = titleStyle.Background(wash)
			indexStyle = indexStyle.Background(wash)
			closeStyle = closeStyle.Background(wash)
		}
	}
	var b strings.Builder
	if mark, kind := m.tabMark(st, now); mark != "" {
		markStyle := titleStyle.Bold(false).Italic(false).Foreground(st.markColor(m.markKindColor(kind), now))
		if kind == tabMarkKindUnread || kind == tabMarkKindNeeds {
			markStyle = markStyle.Bold(true)
		}
		b.WriteString(markStyle.Render(" " + mark))
	}
	if i < 9 {
		b.WriteString(indexStyle.Render(" "+strconv.Itoa(i+1)) + titleStyle.Render(" "))
	} else {
		b.WriteString(titleStyle.Render(" "))
	}
	title := ansi.Truncate(tab.title, tabTitleMax, "…")
	if st != nil && st.unread {
		titleStyle = titleStyle.Bold(true)
	}
	b.WriteString(titleStyle.Render(title + " "))
	if wt, ok := m.tabWorktree(tab); ok {
		mark := "⎇ " + ansi.Truncate(cmpDir(wt.branch, wt.name), tabWorktreeMax, "…")
		if wt.dirty > 0 {
			mark += "*"
		}
		b.WriteString(indexStyle.Render(mark + " "))
	}
	if withAge {
		if age := tabAge(st, now); age != "" {
			b.WriteString(indexStyle.Render(age + " "))
		}
	}
	label := b.String()
	closeMark := closeStyle.Render(tabCloseMark + " ")
	return tabCell{
		index: i,
		title: title,
		label: label,
		close: closeMark,
		width: ansi.StringWidth(label) + ansi.StringWidth(closeMark),
	}
}

// tabCellsWidth is the width of cells laid out with one-cell gaps.
func tabCellsWidth(cells []tabCell) int {
	w := 0
	for _, c := range cells {
		w += c.width + 1
	}
	return w
}

// tabMarkColor picks the color for a status mark.
func tabMarkColor(working, needs, done, read, failed color.Color, kind tabMarkKind) color.Color {
	switch kind {
	case tabMarkKindWorking:
		return working
	case tabMarkKindNeeds:
		return needs
	case tabMarkKindUnread:
		return done
	case tabMarkKindFailed:
		return failed
	}
	return read
}

// drawTabBar draws the tab bar into area and remembers its click spans.
func (m *UI) drawTabBar(scr uv.Screen, area uv.Rectangle) {
	if area.Dy() <= 0 || area.Dx() <= 0 {
		m.tabHits = nil
		return
	}
	view, hits := m.renderTabBar(area.Dx())
	m.tabHits = hits
	uv.NewStyledString(view).Draw(scr, area)
}
