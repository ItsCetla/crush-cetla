package model

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/notification"
)

// tabIDs lists the open tabs' session IDs in order.
func tabIDs(u *UI) []string {
	out := make([]string, 0, len(u.tabs))
	for _, t := range u.tabs {
		out = append(out, t.id)
	}
	return out
}

// testTabs builds a tab list from "id" or "id:group" entries.
func testTabs(specs ...string) []sessionTab {
	out := make([]sessionTab, 0, len(specs))
	for _, s := range specs {
		id, group, _ := strings.Cut(s, ":")
		out = append(out, sessionTab{id: id, title: id, group: group})
	}
	return out
}

func tabSpecs(tabs []sessionTab) []string {
	out := make([]string, 0, len(tabs))
	for _, t := range tabs {
		if t.group == "" {
			out = append(out, t.id)
		} else {
			out = append(out, t.id+":"+t.group)
		}
	}
	return out
}

// cellText is the plain text of rendered tab cells.
func cellText(cells []tabCell) string {
	var b strings.Builder
	for _, c := range cells {
		b.WriteString(ansi.Strip(c.label + c.close))
		b.WriteString(" ")
	}
	return b.String()
}

func notificationFor(msg string) notification.Notification {
	return notification.Notification{Title: "t", Message: msg}
}

func requestPerm(u *UI, sessionID, id string) {
	u.trackTabStatus(pubsub.Event[permission.PermissionRequest]{
		Payload: permission.PermissionRequest{ID: id, SessionID: sessionID, ToolCallID: "t-" + id},
	})
}

func grantPerm(u *UI, sessionID, id string) {
	u.trackTabStatus(pubsub.Event[permission.PermissionNotification]{
		Payload: permission.PermissionNotification{SessionID: sessionID, ToolCallID: "t-" + id, Granted: true},
	})
}

func TestAnnounceOncePerWait(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	requestPerm(u, "sb", "p1")
	_ = u.announceTabEdges()
	s := u.tabStatusOf("sb")
	require.True(t, s.announced, "the first request announces")

	// More requests in the same wait do not announce again.
	requestPerm(u, "sb", "p2")
	_ = u.announceTabEdges()
	require.True(t, s.announced)

	grantPerm(u, "sb", "p1")
	grantPerm(u, "sb", "p2")
	_ = u.announceTabEdges()
	require.False(t, s.announced, "the wait ended, so the next one announces")
}

func TestAsideTabIsMuted(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	requestPerm(u, "sb", "p1")
	u.toggleTabAside(1)
	require.True(t, u.tabs[1].aside)
	require.True(t, u.tabStatusOf("sb").announced, "setting a waiting tab aside must not announce it later")
	require.True(t, u.tabAside("sb"))
	require.Nil(t, u.announce("sb", notificationFor("x")))

	u.toggleTabAside(1)
	require.False(t, u.tabAside("sb"))
}

func TestDoneMarkIsCheck(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	u.trackTabStatus(turnFinished("sb"))
	settle(t, u, "sb")
	mark, kind := u.tabMark(u.tabStatusOf("sb"), time.Now())
	require.Equal(t, tabMarkKindUnread, kind)
	require.Equal(t, tabMarkUnread, mark)
	require.Equal(t, "✔", tabMarkUnread)
}

func TestMoveTab(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		tabs  []string
		i     int
		delta int
		want  []string
	}{
		{"plain swap right", []string{"a", "b", "c"}, 0, 1, []string{"b", "a", "c"}},
		{"plain swap left", []string{"a", "b", "c"}, 2, -1, []string{"a", "c", "b"}},
		{"ungrouped steps over a group right", []string{"a", "b:g", "c:g", "d"}, 0, 1, []string{"b:g", "c:g", "a", "d"}},
		{"ungrouped steps over a group left", []string{"a", "b:g", "c:g", "d"}, 3, -1, []string{"a", "d", "b:g", "c:g"}},
		{"grouped steps within its group", []string{"a", "b:g", "c:g"}, 1, 1, []string{"a", "c:g", "b:g"}},
		{"grouped at the edge leaves the group", []string{"a", "b:g", "c:g"}, 1, -1, []string{"a", "b", "c:g"}},
		{"last grouped tab at the end leaves", []string{"a", "b:g"}, 1, 1, []string{"a", "b"}},
		{"first tab cannot go left", []string{"a", "b"}, 0, -1, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := moveTab(testTabs(tt.tabs...), tt.i, tt.delta)
			require.Equal(t, tt.want, tabSpecs(got))
		})
	}
}

func TestNormalizeTabGroups(t *testing.T) {
	t.Parallel()
	tabs := testTabs("a:x", "b", "c:y", "d:x", "e", "f:y")
	normalizeTabGroups(tabs)
	require.Equal(t, []string{"a:x", "d:x", "b", "c:y", "f:y", "e"}, tabSpecs(tabs))
}

func TestSetTabGroup(t *testing.T) {
	t.Parallel()
	u, _ := newTabsTestUI(t, "A", "B", "C", "D")

	u.setTabGroup(0, "work")
	u.setTabGroup(2, "work")
	require.Equal(t, []string{"sa:work", "sc:work", "sb", "sd"}, tabSpecs(u.tabs), "joining moves to the end of the group")

	u.setTabGroup(0, "")
	require.Equal(t, []string{"sc:work", "sa", "sb", "sd"}, tabSpecs(u.tabs), "leaving moves just past the group")
}

func TestMoveTabToTakesNeighbourGroup(t *testing.T) {
	t.Parallel()
	u, _ := newTabsTestUI(t, "A", "B", "C")
	u.setTabGroup(1, "g")
	u.setTabGroup(2, "g")
	require.Equal(t, []string{"sa", "sb:g", "sc:g"}, tabSpecs(u.tabs))

	require.True(t, u.moveTabTo("sa", 1))
	require.Equal(t, []string{"sb:g", "sa:g", "sc:g"}, tabSpecs(u.tabs))
	require.False(t, u.moveTabTo("sa", 1), "already there")
	require.False(t, u.moveTabTo("nope", 0))
}

func TestCollapsedGroupHidesMembers(t *testing.T) {
	t.Parallel()
	u, _ := newTabsTestUI(t, "Alpha", "Beta", "Gamma")
	u.setTabGroup(1, "work")
	u.setTabGroup(2, "work")
	u.toggleGroupCollapsed("work")

	active := u.activeTabIndex()
	require.Equal(t, 0, active)
	require.True(t, u.tabHidden(1, active))
	require.True(t, u.tabHidden(2, active))
	require.False(t, u.tabHidden(1, 1), "the tab in view stays visible")

	plain := cellText(u.tabCells(active, time.Now(), false))
	require.Contains(t, plain, "work")
	require.NotContains(t, plain, "Beta")
	require.NotContains(t, plain, "Gamma")

	u.toggleGroupCollapsed("work")
	plain = cellText(u.tabCells(active, time.Now(), false))
	require.Contains(t, plain, "Beta")
}

func TestSaveTabsWaitsForRestore(t *testing.T) {
	t.Parallel()
	u, _ := newTabsTestUI(t, "Alpha")
	require.Nil(t, u.saveTabsIfChanged(), "nothing is written before the saved tabs were read")
}

func TestTabsEncodeAndRestore(t *testing.T) {
	t.Parallel()
	u, _ := newTabsTestUI(t, "Alpha", "Beta", "Gamma")
	u.setTabGroup(1, "work")
	u.setTabGroup(2, "work")
	u.toggleTabAside(0)
	u.toggleGroupCollapsed("work")
	u.toggleGroupCollapsed("gone") // no members, so not saved

	var saved savedTabs
	require.NoError(t, json.Unmarshal([]byte(u.encodeTabs()), &saved))
	require.Equal(t, 1, saved.Version)
	require.Equal(t, []string{"work"}, saved.Collapsed)
	require.Len(t, saved.Tabs, 3)
	require.True(t, saved.Tabs[0].Aside)
	require.Equal(t, "work", saved.Tabs[1].Group)

	// A fresh start with one tab already open: saved tabs go first, the
	// open one keeps its live title and is not doubled.
	v, _ := newTabsTestUI(t, "Beta (live)", "Delta")
	v.tabs[0].id, v.tabs[1].id = "sb", "sd"
	msg := tabsRestoredMsg{collapsed: saved.Collapsed}
	for _, st := range saved.Tabs {
		msg.tabs = append(msg.tabs, sessionTab{id: st.ID, title: st.Title, group: st.Group, aside: st.Aside})
	}
	v.handleTabsRestored(msg)
	require.True(t, v.tabx.restored)
	require.Equal(t, []string{"sa", "sb:work", "sc:work", "sd"}, tabSpecs(v.tabs))
	require.Equal(t, "Beta (live)", v.tabs[1].title)
	require.True(t, v.tabs[0].aside)
	require.True(t, v.tabx.collapsed["work"])
}

// restoreWorkspace keeps a data directory and knows a few sessions.
type restoreWorkspace struct {
	statusWorkspace
	cfg      *config.Config
	sessions map[string]session.Session
}

func (w *restoreWorkspace) Config() *config.Config { return w.cfg }

func (w *restoreWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	if s, ok := w.sessions[id]; ok {
		return s, nil
	}
	return w.statusWorkspace.GetSession(context.Background(), id)
}

func TestRestoreTabsReadsFileAndSkipsMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	u, _ := newTabsTestUI(t, "Alpha")
	ws := &restoreWorkspace{
		cfg: &config.Config{Options: &config.Options{DataDirectory: dir}},
		sessions: map[string]session.Session{
			"s1":  {ID: "s1", Title: "One"},
			"kid": {ID: "kid", Title: "Child", ParentSessionID: "s1"},
		},
	}
	u.com.Workspace = ws

	data := `{"version":1,"tabs":[{"id":"s1","group":"g"},{"id":"deleted"},{"id":"kid"}],"collapsed":["g"]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, tabsFileName), []byte(data), 0o600))

	cmd := u.restoreTabs()
	require.NotNil(t, cmd)
	msg, ok := cmd().(tabsRestoredMsg)
	require.True(t, ok)
	require.Equal(t, []string{"s1:g"}, tabSpecs(msg.tabs), "deleted and child sessions are skipped")
	require.Equal(t, "One", msg.tabs[0].title)

	u.handleTabsRestored(msg)
	save := u.saveTabsIfChanged()
	require.NotNil(t, save, "the merged list differs from the file")
	_ = save()
	got, err := os.ReadFile(filepath.Join(dir, tabsFileName))
	require.NoError(t, err)
	require.Contains(t, string(got), `"id": "sa"`)
	require.Nil(t, u.saveTabsIfChanged(), "unchanged tabs are not written again")

	off := false
	ws.cfg.Options.TUI = &config.TUIOptions{RestoreTabs: &off}
	require.Empty(t, u.tabsFilePath(), "restore_tabs = false turns saving off")
}

func TestTabPromptRenames(t *testing.T) {
	t.Parallel()
	u, _ := newTabsTestUI(t, "Alpha", "Beta")

	u.handleKeyPressMsg(tea.KeyPressMsg{Code: 'r', Mod: tea.ModAlt})
	require.NotNil(t, u.tabx.prompt)
	require.Equal(t, "Alpha", u.tabx.prompt.input.Value())

	for range len("Alpha") {
		u.handleKeyPressMsg(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "Notes" {
		u.handleKeyPressMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	require.Equal(t, "Notes", u.tabx.prompt.input.Value(), "keys go to the prompt, not the editor")
	u.handleKeyPressMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Nil(t, u.tabx.prompt)
	require.Equal(t, "Notes", u.tabs[0].title)
	require.Equal(t, "Notes", u.session.Title)
}

func TestTabPromptGroups(t *testing.T) {
	t.Parallel()
	u, _ := newTabsTestUI(t, "Alpha", "Beta")

	u.handleKeyPressMsg(tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})
	require.NotNil(t, u.tabx.prompt)
	for _, r := range "ops" {
		u.handleKeyPressMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	u.handleKeyPressMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "ops", u.tabs[0].group)
}

func TestTabPromptCancels(t *testing.T) {
	t.Parallel()
	for _, k := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		u, _ := newTabsTestUI(t, "Alpha", "Beta")
		u.handleKeyPressMsg(tea.KeyPressMsg{Code: 'r', Mod: tea.ModAlt})
		u.handleKeyPressMsg(tea.KeyPressMsg{Code: 'x', Text: "x"})
		u.handleKeyPressMsg(k)
		require.Nil(t, u.tabx.prompt, k.String())
		require.Equal(t, "Alpha", u.tabs[0].title, k.String())
		require.False(t, u.dialog.HasDialogs(), "%s cancels the prompt instead of asking to quit", k.String())
	}
}

func TestTabMoveAndAsideKeys(t *testing.T) {
	t.Parallel()
	u, _ := newTabsTestUI(t, "Alpha", "Beta", "Gamma")

	u.handleKeyPressMsg(tea.KeyPressMsg{Code: tea.KeyPgDown, Mod: tea.ModCtrl | tea.ModShift})
	require.Equal(t, []string{"sb", "sa", "sc"}, tabIDs(u))
	u.handleKeyPressMsg(tea.KeyPressMsg{Code: tea.KeyPgUp, Mod: tea.ModCtrl | tea.ModShift})
	require.Equal(t, []string{"sa", "sb", "sc"}, tabIDs(u))

	u.handleKeyPressMsg(tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
	require.True(t, u.tabs[0].aside)
}

func TestSessionStateText(t *testing.T) {
	t.Parallel()
	now := time.Now()
	muted := lipgloss.NewStyle()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")
	state := func() string { return ansi.Strip(u.sessionStateText(muted, now)) }

	require.Empty(t, state(), "a fresh idle session shows nothing")
	u.session.UpdatedAt = now.Add(-3 * time.Hour).Unix()
	require.Equal(t, "Inactive · 3h", state())

	u.trackTabStatus(created("sa", "m1", message.User))
	u.tabStatusOf("sa").since = now.Add(-30 * time.Second)
	require.True(t, strings.HasSuffix(state(), " Working"), state())
	u.tabStatusOf("sa").since = now.Add(-80 * time.Minute)
	require.True(t, strings.HasSuffix(state(), " Working · 1h 20m"), state())

	requestPerm(u, "sa", "p1")
	u.tabStatusOf("sa").since = now.Add(-2 * time.Minute)
	require.Equal(t, tabMarkNeeds+" Needs you · 2m ago", state())

	// Another tab waiting shows as a badge next to the state.
	require.Empty(t, u.otherTabsNeedingText())
	requestPerm(u, "sb", "p2")
	require.Equal(t, tabMarkNeeds+" 1", ansi.Strip(u.otherTabsNeedingText()))
}

func TestAgoAndRunningAge(t *testing.T) {
	t.Parallel()
	now := time.Now()
	require.Empty(t, agoText(time.Time{}, now))
	require.Equal(t, "just now", agoText(now.Add(-10*time.Second), now))
	require.Equal(t, "4m ago", agoText(now.Add(-4*time.Minute), now))

	for d, want := range map[time.Duration]string{
		5 * time.Minute:              "5m",
		time.Hour:                    "1h",
		time.Hour + 20*time.Minute:   "1h 20m",
		30*time.Hour + 5*time.Minute: "1d",
	} {
		require.Equal(t, want, runningAge(d), d.String())
	}
}

func TestCleanTabName(t *testing.T) {
	t.Parallel()
	require.Equal(t, "work", cleanTabName("  work  "))
	require.Equal(t, "redgreen", cleanTabName("\x1b[31mred\x1b[0m\x1b]0;title\x07green"))
	require.Equal(t, "a b", cleanTabName("a\tb\x00\x9b"))
	require.Equal(t, strings.Repeat("x", tabNameMax), cleanTabName(strings.Repeat("x", 200)))
}

func TestRestoredGroupNamesAreCleaned(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	u, _ := newTabsTestUI(t, "Alpha")
	u.com.Workspace = &restoreWorkspace{
		cfg:      &config.Config{Options: &config.Options{DataDirectory: dir}},
		sessions: map[string]session.Session{"s1": {ID: "s1", Title: "One\x1b[2J"}},
	}
	data := `{"version":1,"tabs":[{"id":"s1","group":"g\u001b[2J"}],"collapsed":["g\u001b[2J","\u001b"]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, tabsFileName), []byte(data), 0o600))

	msg, ok := u.restoreTabs()().(tabsRestoredMsg)
	require.True(t, ok)
	require.Equal(t, "g", msg.tabs[0].group)
	require.Equal(t, "One", msg.tabs[0].title)
	require.Equal(t, []string{"g"}, msg.collapsed)
}
