package model

import (
	"context"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/crush/internal/ui/util"
)

// Tab extras: tabs can be renamed (alt+r), put in named groups (alt+g),
// set aside (alt+a), reordered (ctrl+shift+pgup/pgdn or by dragging) and
// folded by group. Groups are a view over the tab list: members of a
// group always sit next to each other, in the order of the list.

// tabExtras is the UI state behind tab groups, the inline prompt,
// dragging, saved tabs and Claude Code sessions.
type tabExtras struct {
	// collapsed holds the folded groups.
	collapsed map[string]bool
	// prompt is the open rename or group prompt, if any.
	prompt *tabPrompt
	// drag is the tab being dragged with the mouse.
	drag tabDrag
	// saved is the last tab list written to disk. restored is set once
	// the saved tabs were read back at start; nothing is written before.
	saved    string
	restored bool
	// side are the sidebar's session rows, for clicks.
	side sidebarTabRows
	// claude is the opt-in list of Claude Code sessions.
	claude claudeSessions
}

// tabPromptKind says what the inline prompt edits.
type tabPromptKind int

const (
	tabPromptRename tabPromptKind = iota
	tabPromptGroup
)

// tabPrompt is the one-line prompt drawn over the tab bar.
type tabPrompt struct {
	kind  tabPromptKind
	id    string // session ID of the tab being edited
	input textinput.Model
}

// tabDrag is a tab being dragged in the bar.
type tabDrag struct {
	id     string
	active bool
	moved  bool
}

// tabNameMax caps tab and group names, in runes.
const tabNameMax = 80

// cleanTabName makes a name safe to draw: escape sequences and other
// control characters are dropped and the length is capped. Names read
// from tabs.json or another program's files go through it.
func cleanTabName(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= tabNameMax {
			break
		}
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			r = ' '
		case !unicode.IsPrint(r):
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// label is the prompt's lead-in.
func (p *tabPrompt) label() string {
	if p.kind == tabPromptGroup {
		return " Group "
	}
	return " Rename "
}

// startTabPrompt opens the rename or group prompt for the tab in view.
func (m *UI) startTabPrompt(kind tabPromptKind) bool {
	i := m.activeTabIndex()
	if i < 0 {
		return false
	}
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 80
	// The prompt draws its own steady cursor; the real one stays hidden
	// while it is open (see Draw).
	in.SetVirtualCursor(true)
	st := m.com.Styles.TextInput
	st.Cursor.Blink = false
	in.SetStyles(st)
	switch kind {
	case tabPromptRename:
		in.SetValue(m.tabs[i].title)
		in.Placeholder = "session name"
	case tabPromptGroup:
		in.SetValue(m.tabs[i].group)
		in.Placeholder = "group name, empty to ungroup"
	}
	in.CursorEnd()
	in.Focus()
	m.tabx.prompt = &tabPrompt{kind: kind, id: m.tabs[i].id, input: in}
	m.updateLayoutAndSize() // vertical tabs: the bar shows for the prompt
	return true
}

// handleTabPromptKey feeds a key to the open prompt. It reports whether
// the prompt took the key; every key goes to the prompt while it is open.
func (m *UI) handleTabPromptKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	p := m.tabx.prompt
	if p == nil {
		return nil, false
	}
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+g":
		m.closeTabPrompt()
		return nil, true
	case "enter":
		m.closeTabPrompt()
		value := strings.TrimSpace(p.input.Value())
		i := m.tabIndex(p.id)
		if i < 0 {
			return nil, true
		}
		if p.kind == tabPromptGroup {
			m.setTabGroup(i, value)
			return nil, true
		}
		return m.renameTab(p.id, value), true
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd, true
}

// closeTabPrompt drops the prompt and gives its row back.
func (m *UI) closeTabPrompt() {
	m.tabx.prompt = nil
	m.updateLayoutAndSize()
}

// renameTab gives a session a new title, in the tab at once and in the
// database in the background.
func (m *UI) renameTab(sessionID, title string) tea.Cmd {
	title = cleanTabName(title)
	if title == "" {
		return nil
	}
	if i := m.tabIndex(sessionID); i >= 0 {
		m.tabs[i].title = title
	}
	if m.session != nil && m.session.ID == sessionID {
		m.session.Title = title
	}
	ws := m.com.Workspace
	if ws == nil {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		s, err := ws.GetSession(ctx, sessionID)
		if err != nil {
			return util.NewErrorMsg(err)
		}
		s.Title = title
		if _, err := ws.SaveSession(ctx, s); err != nil {
			return util.NewErrorMsg(err)
		}
		return nil
	}
}

// setTabGroup puts the tab at i in group name, or takes it out of its
// group when name is empty. A tab joining a group moves to the end of
// that group; a tab leaving one moves just past it.
func (m *UI) setTabGroup(i int, name string) {
	name = cleanTabName(name)
	if i < 0 || i >= len(m.tabs) || m.tabs[i].group == name {
		return
	}
	old := m.tabs[i].group
	tab := m.tabs[i]
	tab.group = name
	rest := append(append([]sessionTab(nil), m.tabs[:i]...), m.tabs[i+1:]...)
	at := i
	switch {
	case name != "":
		if last := lastInGroup(rest, name); last >= 0 {
			at = last + 1
		}
	case old != "":
		if last := lastInGroup(rest, old); last >= i {
			at = last + 1
		}
	}
	m.tabs = insertTab(rest, at, tab)
	normalizeTabGroups(m.tabs)
}

// toggleTabAside sets the tab at i aside, or brings it back.
func (m *UI) toggleTabAside(i int) {
	if i < 0 || i >= len(m.tabs) {
		return
	}
	m.tabs[i].aside = !m.tabs[i].aside
	if s := m.tabStatusOf(m.tabs[i].id); s != nil {
		// Setting a waiting tab aside must not announce it again later.
		s.announced = true
	}
}

// toggleGroupCollapsed folds or unfolds a group.
func (m *UI) toggleGroupCollapsed(name string) {
	if name == "" {
		return
	}
	if m.tabx.collapsed == nil {
		m.tabx.collapsed = make(map[string]bool)
	}
	if m.tabx.collapsed[name] {
		delete(m.tabx.collapsed, name)
	} else {
		m.tabx.collapsed[name] = true
	}
}

// moveActiveTab moves the tab in view one step left (-1) or right (1).
func (m *UI) moveActiveTab(delta int) bool {
	i := m.activeTabIndex()
	if i < 0 {
		return false
	}
	m.tabs = moveTab(m.tabs, i, delta)
	return true
}

// moveTab moves the tab at i one step by delta and returns the new list.
// An ungrouped tab steps over a whole group. A grouped tab steps within
// its group and, at the group's edge, leaves the group where it stands.
func moveTab(tabs []sessionTab, i, delta int) []sessionTab {
	if i < 0 || i >= len(tabs) || delta == 0 {
		return tabs
	}
	j := i + delta
	if j < 0 || j >= len(tabs) {
		if tabs[i].group != "" {
			tabs[i].group = ""
		}
		return tabs
	}
	tab := tabs[i]
	if tab.group != "" {
		if tabs[j].group == tab.group {
			tabs[i], tabs[j] = tabs[j], tabs[i]
			return tabs
		}
		tabs[i].group = ""
		return tabs
	}
	rest := append(append([]sessionTab(nil), tabs[:i]...), tabs[i+1:]...)
	at := j
	if g := tabs[j].group; g != "" {
		// Step over the neighbour's whole group.
		if delta > 0 {
			at = lastInGroup(rest, g) + 1
		} else {
			at = firstInGroup(rest, g)
		}
	}
	return insertTab(rest, at, tab)
}

// moveTabTo moves the tab with sessionID to index to, taking on the
// group of the tab it lands next to. Dragging uses it.
func (m *UI) moveTabTo(sessionID string, to int) bool {
	i := m.tabIndex(sessionID)
	if i < 0 || to < 0 || to >= len(m.tabs) || i == to {
		return false
	}
	tab := m.tabs[i]
	tab.group = m.tabs[to].group
	rest := append(append([]sessionTab(nil), m.tabs[:i]...), m.tabs[i+1:]...)
	m.tabs = insertTab(rest, to, tab)
	normalizeTabGroups(m.tabs)
	return true
}

// normalizeTabGroups reorders tabs in place so each group's members sit
// together where its first member is, keeping every other order.
func normalizeTabGroups(tabs []sessionTab) {
	out := make([]sessionTab, 0, len(tabs))
	done := make(map[string]bool)
	for _, t := range tabs {
		if t.group == "" {
			out = append(out, t)
			continue
		}
		if done[t.group] {
			continue
		}
		done[t.group] = true
		for _, u := range tabs {
			if u.group == t.group {
				out = append(out, u)
			}
		}
	}
	copy(tabs, out)
}

func firstInGroup(tabs []sessionTab, name string) int {
	for i, t := range tabs {
		if t.group == name {
			return i
		}
	}
	return -1
}

func lastInGroup(tabs []sessionTab, name string) int {
	for i := len(tabs) - 1; i >= 0; i-- {
		if tabs[i].group == name {
			return i
		}
	}
	return -1
}

func insertTab(tabs []sessionTab, at int, t sessionTab) []sessionTab {
	at = min(max(at, 0), len(tabs))
	tabs = append(tabs, sessionTab{})
	copy(tabs[at+1:], tabs[at:])
	tabs[at] = t
	return tabs
}

// tabGroupMembers returns the indexes of the tabs in group name.
func (m *UI) tabGroupMembers(name string) []int {
	var out []int
	for i, t := range m.tabs {
		if t.group == name {
			out = append(out, i)
		}
	}
	return out
}

// tabRank orders statuses by how much they want attention.
func tabRank(s *tabStatus) int {
	switch {
	case s == nil:
		return 0
	case s.needsYou():
		return 5
	case s.phase == tabWorking:
		return 4
	case s.phase == tabFailed:
		return 3
	case s.phase == tabDone && s.unread:
		return 2
	case s.phase == tabDone:
		return 1
	}
	return 0
}

// groupTopStatus returns the member status that most wants attention.
func (m *UI) groupTopStatus(name string) *tabStatus {
	var top *tabStatus
	for _, i := range m.tabGroupMembers(name) {
		if s := m.tabStatusOf(m.tabs[i].id); tabRank(s) > tabRank(top) {
			top = s
		}
	}
	return top
}

// tabHidden reports whether the tab at i is folded away: its group is
// collapsed and it is not the tab in view.
func (m *UI) tabHidden(i, active int) bool {
	g := m.tabs[i].group
	return g != "" && m.tabx.collapsed[g] && i != active
}

// startTabDrag starts dragging the tab at i.
func (m *UI) startTabDrag(i int) {
	if i < 0 || i >= len(m.tabs) {
		return
	}
	m.tabx.drag = tabDrag{id: m.tabs[i].id, active: true}
}

// dragTabTo moves the dragged tab over the tab under x in the bar.
func (m *UI) dragTabTo(x int) bool {
	d := &m.tabx.drag
	if !d.active {
		return false
	}
	for _, h := range m.tabHits {
		if h.kind != tabHitSelect || x < h.x0 || x >= h.x1 {
			continue
		}
		if m.moveTabTo(d.id, h.index) {
			d.moved = true
			return true
		}
		return false
	}
	return false
}

// endTabDrag stops dragging.
func (m *UI) endTabDrag() {
	m.tabx.drag = tabDrag{}
}
