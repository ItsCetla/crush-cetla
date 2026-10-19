package model

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/dialog"
)

// tabsWorkspace records which sessions the UI asked to load.
type tabsWorkspace struct {
	historyWorkspace

	mu     sync.Mutex
	loaded []string
}

func (w *tabsWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loaded = append(w.loaded, id)
	return session.Session{}, errors.New("stub")
}

func (w *tabsWorkspace) AgentIsReady() bool { return false }

func (w *tabsWorkspace) WorkingDir() string { return "/tmp/tada-tabs-test" }

// newTabsTestUI returns a chat UI viewing the first of the given session
// titles, with a tab open for each of them.
func newTabsTestUI(t *testing.T, titles ...string) (*UI, *tabsWorkspace) {
	t.Helper()
	u := newTestUI()
	ws := &tabsWorkspace{}
	u.com.Workspace = ws
	u.dialog = dialog.NewOverlay()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	u.keyMap = DefaultKeyMap()
	for i, title := range titles {
		s := &session.Session{ID: "s" + string(rune('a'+i)), Title: title}
		u.trackSessionTab(s)
		if i == 0 {
			u.session = s
		}
	}
	u.updateLayoutAndSize()
	return u, ws
}

// loadedBy runs the session-loading part of cmd and returns the session
// IDs it asked the workspace for.
func loadedBy(t *testing.T, ws *tabsWorkspace, cmd tea.Cmd) []string {
	t.Helper()
	require.NotNil(t, cmd)
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok, "expected a batch, got %T", msg)
	require.NotEmpty(t, batch)
	// The first command of loadSession's batch is the load itself.
	_ = batch[0]()
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return append([]string(nil), ws.loaded...)
}

func TestTabBarShownWithOneTab(t *testing.T) {
	t.Parallel()

	plain := newTestUI()
	plain.updateLayoutAndSize()

	u, _ := newTabsTestUI(t, "Alpha")
	require.Equal(t, 1, u.layout.tabs.Dy(), "one tab still shows the bar")
	require.Equal(t, plain.layout.main.Min.Y+2, u.layout.main.Min.Y)

	view, hits := u.renderTabBar(u.layout.tabs.Dx())
	require.Contains(t, ansi.Strip(view), "Alpha")
	require.Contains(t, ansi.Strip(view), strings.TrimSpace(tabNewButton), "the + button is there with one tab")
	require.Equal(t, tabHitNew, hits[len(hits)-1].kind)
}

func TestTabBarHiddenWithoutSession(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.updateLayoutAndSize()
	require.Equal(t, 0, u.layout.tabs.Dy(), "a fresh start has nothing to tab")
}

func TestTabBarShownWithTwoTabs(t *testing.T) {
	t.Parallel()

	plain := newTestUI()
	plain.updateLayoutAndSize()

	u, _ := newTabsTestUI(t, "Alpha", "Beta")
	require.Equal(t, 1, u.layout.tabs.Dy())
	require.Equal(t, plain.layout.main.Min.Y+2, u.layout.main.Min.Y, "tab bar takes a row plus a gap")
	require.Equal(t, plain.layout.main.Max.Y, u.layout.main.Max.Y)
	require.Equal(t, u.layout.main.Min.X, u.layout.tabs.Min.X)
	require.False(t, u.layout.tabs.Overlaps(u.layout.sidebar), "tab bar stays in the main column")
}

func TestTabBarShownInCompactMode(t *testing.T) {
	t.Parallel()

	plain := newTestUI()
	plain.forceCompactMode = true
	plain.updateLayoutAndSize()

	u, _ := newTabsTestUI(t, "Alpha", "Beta")
	u.forceCompactMode = true
	u.updateLayoutAndSize()
	require.Equal(t, 1, u.layout.tabs.Dy())
	require.Greater(t, u.layout.tabs.Min.Y, u.layout.header.Min.Y, "tab bar sits under the compact header")
	require.Equal(t, plain.layout.main.Min.Y+1, u.layout.main.Min.Y)
}

func TestTabBarRendersTabsInOrder(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta", "Gamma")
	view, hits := u.renderTabBar(100)
	plain := ansi.Strip(view)

	require.LessOrEqual(t, ansi.StringWidth(view), 100)
	a := strings.Index(plain, "1 Alpha")
	b := strings.Index(plain, "2 Beta")
	c := strings.Index(plain, "3 Gamma")
	require.True(t, a >= 0 && a < b && b < c, "tabs out of order: %q", plain)
	require.Contains(t, plain, "+")
	require.Equal(t, 3, strings.Count(plain, tabCloseMark))

	// select + close per tab, then the "+" button
	require.Len(t, hits, 7)
	require.Equal(t, tabHitNew, hits[6].kind)
	for i := 1; i < len(hits); i++ {
		require.GreaterOrEqual(t, hits[i].x0, hits[i-1].x1, "hit spans overlap")
	}
}

func TestTabBarLongTitleIsTruncated(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, strings.Repeat("very long title ", 6), "Beta")
	view, _ := u.renderTabBar(140)
	plain := ansi.Strip(view)
	require.Contains(t, plain, "…")
	require.Contains(t, plain, "2 Beta")
}

func TestTabBarOverflowKeepsActiveTabVisible(t *testing.T) {
	t.Parallel()

	titles := make([]string, 12)
	for i := range titles {
		titles[i] = "Session number " + string(rune('A'+i))
	}
	u, _ := newTabsTestUI(t, titles...)
	u.session = &session.Session{ID: u.tabs[10].id}

	view, hits := u.renderTabBar(70)
	plain := ansi.Strip(view)
	require.LessOrEqual(t, ansi.StringWidth(view), 70)
	require.Contains(t, plain, "Session number K", "active tab scrolled out of view")
	require.Contains(t, plain, strings.TrimSpace(tabMoreLeft))
	require.Equal(t, tabHitNew, hits[len(hits)-1].kind, "+ button must stay visible")
}

func TestTabBarNarrowWidthNeverOverflows(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta", "Gamma")
	for _, w := range []int{1, 5, 12, 20, 33} {
		view, _ := u.renderTabBar(w)
		require.LessOrEqual(t, ansi.StringWidth(view), w, "width %d", w)
	}
}

func TestTabBarDrawnOnScreen(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta")
	scr := uv.NewScreenBuffer(u.width, u.height)
	u.drawTabBar(scr, u.layout.tabs)

	row := ansi.Strip(scr.Line(u.layout.tabs.Min.Y).String())
	require.Contains(t, row, "1 Alpha")
	require.Contains(t, row, "2 Beta")
	require.NotEmpty(t, u.tabHits)
}

func TestTabClickSwitchesSession(t *testing.T) {
	t.Parallel()

	u, ws := newTabsTestUI(t, "Alpha", "Beta")
	_, u.tabHits = u.renderTabBar(u.layout.tabs.Dx())
	var beta tabHit
	for _, h := range u.tabHits {
		if h.kind == tabHitSelect && h.index == 1 {
			beta = h
		}
	}

	_, cmd := u.Update(tea.MouseClickMsg(tea.Mouse{
		X:      u.layout.tabs.Min.X + beta.x0,
		Y:      u.layout.tabs.Min.Y,
		Button: uv.MouseLeft,
	}))
	require.Equal(t, []string{"sb"}, loadedBy(t, ws, cmd))
}

func TestTabClickOnActiveTabDoesNothing(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta")
	_, u.tabHits = u.renderTabBar(u.layout.tabs.Dx())
	require.Nil(t, u.handleTabBarClick(u.tabHits[0].x0, false))
}

func TestTabCloseMarkClosesBackgroundTab(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta", "Gamma")
	_, u.tabHits = u.renderTabBar(u.layout.tabs.Dx())
	var closeBeta tabHit
	for _, h := range u.tabHits {
		if h.kind == tabHitClose && h.index == 1 {
			closeBeta = h
		}
	}
	require.Nil(t, u.handleTabBarClick(closeBeta.x0, false))
	require.Len(t, u.tabs, 2)
	require.Equal(t, "sa", u.session.ID, "closing a background tab keeps the view")
	require.Equal(t, "Gamma", u.tabs[1].title)
}

func TestMiddleClickClosesTab(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta")
	_, u.tabHits = u.renderTabBar(u.layout.tabs.Dx())
	var beta tabHit
	for _, h := range u.tabHits {
		if h.kind == tabHitSelect && h.index == 1 {
			beta = h
		}
	}
	_ = u.handleTabBarClick(beta.x0, true)
	require.Len(t, u.tabs, 1)
	u.updateLayoutAndSize()
	require.Equal(t, 1, u.layout.tabs.Dy(), "bar stays with one tab so + is reachable")
}

func TestCloseActiveTabShowsNeighbour(t *testing.T) {
	t.Parallel()

	u, ws := newTabsTestUI(t, "Alpha", "Beta", "Gamma")
	u.session = &session.Session{ID: "sb"}
	cmd := u.closeActiveTab()
	require.Equal(t, []string{"sc"}, loadedBy(t, ws, cmd), "right neighbour first")
	require.Len(t, u.tabs, 2)
}

func TestCloseLastActiveTabShowsLeftNeighbour(t *testing.T) {
	t.Parallel()

	u, ws := newTabsTestUI(t, "Alpha", "Beta")
	u.session = &session.Session{ID: "sb"}
	cmd := u.closeActiveTab()
	require.Equal(t, []string{"sa"}, loadedBy(t, ws, cmd))
}

func TestCloseOnlyTabGoesToLanding(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha")
	_ = u.closeActiveTab()
	require.Empty(t, u.tabs)
	require.Equal(t, uiLanding, u.state)
	require.Nil(t, u.session)
}

func TestCloseOnlyTabBlockedWhileBusy(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha")
	u.agentBusyCache.set(true)
	require.NotNil(t, u.closeActiveTab())
	require.Len(t, u.tabs, 1)
	require.Equal(t, uiChat, u.state)
}

func TestTabKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
		want string
	}{
		{"alt+2 jumps to tab 2", tea.KeyPressMsg{Code: '2', Mod: tea.ModAlt}, "sb"},
		{"alt+3 jumps to tab 3", tea.KeyPressMsg{Code: '3', Mod: tea.ModAlt}, "sc"},
		{"ctrl+pgdown goes to next tab", tea.KeyPressMsg{Code: tea.KeyPgDown, Mod: tea.ModCtrl}, "sb"},
		{"ctrl+pgup wraps to last tab", tea.KeyPressMsg{Code: tea.KeyPgUp, Mod: tea.ModCtrl}, "sc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, ws := newTabsTestUI(t, "Alpha", "Beta", "Gamma")
			cmd := u.handleKeyPressMsg(tt.key)
			require.Equal(t, []string{tt.want}, loadedBy(t, ws, cmd))
		})
	}
}

func TestTabJumpPastLastTabIsIgnored(t *testing.T) {
	t.Parallel()

	u, ws := newTabsTestUI(t, "Alpha", "Beta")
	_ = u.handleKeyPressMsg(tea.KeyPressMsg{Code: '7', Mod: tea.ModAlt})
	require.Empty(t, ws.loaded)
	require.Equal(t, "sa", u.session.ID)
}

func TestAltWClosesActiveTab(t *testing.T) {
	t.Parallel()

	u, ws := newTabsTestUI(t, "Alpha", "Beta")
	cmd := u.handleKeyPressMsg(tea.KeyPressMsg{Code: 'w', Mod: tea.ModAlt})
	require.Equal(t, []string{"sb"}, loadedBy(t, ws, cmd))
	require.Len(t, u.tabs, 1)
}

func TestSessionTitleUpdateRenamesTab(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "New Session")
	require.Equal(t, tabDraftTitle, u.tabs[1].title)
	_, _ = u.Update(pubsub.Event[session.Session]{
		Type:    pubsub.UpdatedEvent,
		Payload: session.Session{ID: "sb", Title: "Fix the parser"},
	})
	require.Equal(t, "Fix the parser", u.tabs[1].title)
}

func TestDeletedSessionRemovesItsTab(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta", "Gamma")
	_, _ = u.Update(pubsub.Event[session.Session]{
		Type:    pubsub.DeletedEvent,
		Payload: session.Session{ID: "sb"},
	})
	require.Len(t, u.tabs, 2)
	require.Equal(t, -1, u.tabIndex("sb"))
	require.Equal(t, "sa", u.session.ID)
}

func TestDeletedActiveSessionShowsNeighbour(t *testing.T) {
	t.Parallel()

	u, ws := newTabsTestUI(t, "Alpha", "Beta")
	_, cmd := u.Update(pubsub.Event[session.Session]{
		Type:    pubsub.DeletedEvent,
		Payload: session.Session{ID: "sa"},
	})
	require.Len(t, u.tabs, 1)
	// Update batches its commands; dig out the load.
	require.NotNil(t, cmd)
	found := false
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil || found {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, sub := range msg {
				walk(sub)
			}
		}
		ws.mu.Lock()
		found = len(ws.loaded) > 0
		ws.mu.Unlock()
	}
	walk(cmd)
	require.Equal(t, []string{"sb"}, ws.loaded)
}

func TestChildSessionsDoNotGetTabs(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha")
	u.trackSessionTab(&session.Session{ID: "child", ParentSessionID: "sa", Title: "agent"})
	require.Len(t, u.tabs, 1)
}

func TestReopeningSessionReusesItsTab(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta")
	u.trackSessionTab(&session.Session{ID: "sa", Title: "Alpha renamed"})
	require.Len(t, u.tabs, 2)
	require.Equal(t, "Alpha renamed", u.tabs[0].title)
}

func TestLandingShowsDraftTab(t *testing.T) {
	t.Parallel()

	u, ws := newTabsTestUI(t, "Alpha")
	_ = u.newSession()
	require.Equal(t, uiLanding, u.state)
	require.Len(t, u.tabs, 1, "starting a new session keeps the old tab")
	u.updateLayoutAndSize()
	require.Equal(t, 1, u.layout.tabs.Dy(), "landing shows the bar to get back")

	view, _ := u.renderTabBar(u.layout.tabs.Dx())
	plain := ansi.Strip(view)
	require.Contains(t, plain, "1 Alpha")
	require.Contains(t, plain, tabDraftTitle)
	require.Greater(t, strings.Index(plain, tabDraftTitle), strings.Index(plain, "Alpha"))

	cmd := u.handleKeyPressMsg(tea.KeyPressMsg{Code: '1', Mod: tea.ModAlt})
	require.Equal(t, []string{"sa"}, loadedBy(t, ws, cmd))
}

func TestLandingWithoutTabsHasNoBar(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.state = uiLanding
	u.updateLayoutAndSize()
	require.Equal(t, 0, u.layout.tabs.Dy())
}

func TestNewTabButtonStartsNewSession(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta")
	_, u.tabHits = u.renderTabBar(u.layout.tabs.Dx())
	plus := u.tabHits[len(u.tabHits)-1]
	require.Equal(t, tabHitNew, plus.kind)
	_ = u.handleTabBarClick(plus.x0, false)
	require.Equal(t, uiLanding, u.state)
	require.Len(t, u.tabs, 2)
}

func TestNewTabButtonBlockedWhileBusy(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "Alpha", "Beta")
	u.agentBusyCache.set(true)
	require.NotNil(t, u.newSessionFromTabs())
	require.Equal(t, uiChat, u.state)
}

func TestNeighbourTab(t *testing.T) {
	t.Parallel()

	tests := []struct {
		i, n, want int
	}{
		{0, 1, -1},
		{0, 2, 0},
		{1, 2, 0},
		{1, 3, 1},
		{2, 3, 1},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, neighbourTab(tt.i, tt.n), "i=%d n=%d", tt.i, tt.n)
	}
}

func TestTabsInFullHelpOnlyWithTabs(t *testing.T) {
	t.Parallel()

	has := func(u *UI) bool {
		for _, row := range u.FullHelp() {
			for _, b := range row {
				if b.Help().Key == "alt+1-9" {
					return true
				}
			}
		}
		return false
	}
	one, _ := newTabsTestUI(t, "Alpha")
	two, _ := newTabsTestUI(t, "Alpha", "Beta")
	require.False(t, has(one))
	require.True(t, has(two))
}
