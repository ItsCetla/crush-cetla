package model

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
)

// statusWorkspace answers the busy and queue probes made when a turn ends.
type statusWorkspace struct {
	tabsWorkspace

	busy   bool
	queued int
}

func (w *statusWorkspace) AgentIsSessionBusy(string) bool { return w.busy }

func (w *statusWorkspace) AgentQueuedPrompts(string) int { return w.queued }

func (w *statusWorkspace) ParseAgentToolSessionID(id string) (string, string, bool) {
	msgID, toolCallID, ok := strings.Cut(id, "$$")
	return msgID, toolCallID, ok
}

// newStatusTestUI is newTabsTestUI with a workspace that answers probes.
func newStatusTestUI(t *testing.T, titles ...string) (*UI, *statusWorkspace) {
	t.Helper()
	u, _ := newTabsTestUI(t, titles...)
	ws := &statusWorkspace{}
	u.com.Workspace = ws
	u.state = uiChat
	return u, ws
}

func created(sessionID, msgID string, role message.MessageRole) pubsub.Event[message.Message] {
	return pubsub.Event[message.Message]{
		Type:    pubsub.CreatedEvent,
		Payload: message.Message{ID: msgID, SessionID: sessionID, Role: role},
	}
}

func turnFinished(sessionID string) pubsub.Event[notify.Notification] {
	return pubsub.Event[notify.Notification]{
		Payload: notify.Notification{SessionID: sessionID, Type: notify.TypeAgentFinished},
	}
}

// settle runs the probe a finished turn schedules and applies its result.
func settle(t *testing.T, u *UI, sessionID string) {
	t.Helper()
	s := u.tabStatusOf(sessionID)
	require.NotNil(t, s)
	ws := u.com.Workspace
	u.trackTabStatus(tabSettleMsg{
		sessionID: sessionID,
		gen:       s.settleGen,
		busy:      ws.AgentIsSessionBusy(sessionID),
		queued:    ws.AgentQueuedPrompts(sessionID),
	})
}

func markOf(u *UI, sessionID string) tabMarkKind {
	_, kind := u.tabMark(u.tabStatusOf(sessionID), time.Now())
	return kind
}

func TestTabStatusBackgroundTurnEndsUnread(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	require.Equal(t, tabMarkKindWorking, markOf(u, "sb"))
	require.Equal(t, 200*time.Millisecond, u.tabTickInterval(), "a working tab spins")

	cmd := u.trackTabStatus(turnFinished("sb"))
	require.NotNil(t, cmd, "a finished turn schedules a settle probe")
	require.Equal(t, tabMarkKindWorking, markOf(u, "sb"), "not done until the probe settles")

	settle(t, u, "sb")
	require.Equal(t, tabMarkKindUnread, markOf(u, "sb"), "done in a background tab stays unread")
	require.Equal(t, tabAgeInterval, u.tabTickInterval())

	u.session = &session.Session{ID: "sb", Title: "Beta"}
	u.markViewedTabSeen()
	require.Equal(t, tabMarkKindRead, markOf(u, "sb"), "looking at the tab marks it read")
}

func TestTabStatusViewedTurnEndsRead(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sa", "m1", message.Assistant))
	u.trackTabStatus(turnFinished("sa"))
	settle(t, u, "sa")
	require.Equal(t, tabMarkKindRead, markOf(u, "sa"))
}

func TestTabStatusQueuedPromptsKeepWorking(t *testing.T) {
	t.Parallel()
	u, ws := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	u.trackTabStatus(turnFinished("sb"))
	ws.queued = 1
	settle(t, u, "sb")
	require.Equal(t, tabMarkKindWorking, markOf(u, "sb"), "a queued prompt keeps the tab working")

	ws.queued = 0
	ws.busy = true
	u.trackTabStatus(turnFinished("sb"))
	settle(t, u, "sb")
	require.Equal(t, tabMarkKindWorking, markOf(u, "sb"), "a busy agent keeps the tab working")

	ws.busy = false
	u.trackTabStatus(turnFinished("sb"))
	settle(t, u, "sb")
	require.Equal(t, tabMarkKindUnread, markOf(u, "sb"))
}

func TestTabStatusStaleSettleIgnored(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	u.trackTabStatus(turnFinished("sb"))
	stale := u.tabStatusOf("sb").settleGen
	// The next queued turn starts before the probe lands.
	u.trackTabStatus(created("sb", "m2", message.User))
	u.trackTabStatus(tabSettleMsg{sessionID: "sb", gen: stale})
	require.Equal(t, tabMarkKindWorking, markOf(u, "sb"))
}

func TestTabStatusCancelClearsMark(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	u.trackTabStatus(pubsub.Event[notify.RunComplete]{Payload: notify.RunComplete{SessionID: "sb", Cancelled: true}})
	settle(t, u, "sb")
	require.Equal(t, tabMarkNone, markOf(u, "sb"))
}

func TestTabStatusErrorFails(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	u.trackTabStatus(pubsub.Event[notify.RunComplete]{Payload: notify.RunComplete{SessionID: "sb", Error: "boom"}})
	settle(t, u, "sb")
	require.Equal(t, tabMarkKindFailed, markOf(u, "sb"))
}

func TestTabStatusSubAgentPermissionNeedsParent(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.Assistant))
	child := "m1$$call-1"
	u.trackTabStatus(pubsub.Event[permission.PermissionRequest]{
		Payload: permission.PermissionRequest{ID: "p1", SessionID: child, ToolCallID: "t1"},
	})
	require.Equal(t, tabMarkKindNeeds, markOf(u, "sb"), "a sub-agent's request marks its parent's tab")
	require.Nil(t, u.tabStatusOf(child), "sub-agent sessions get no tab status of their own")
	require.Equal(t, []int{1}, u.tabsNeedingYou())

	// A sub-agent finishing does not end its parent's turn.
	require.Nil(t, u.trackTabStatus(turnFinished(child)))

	view, hits := u.renderTabBar(200)
	plain := ansi.Strip(view)
	require.Contains(t, plain, tabMarkNeeds+" 1 needs you")
	u.tabHits = hits
	last := hits[len(hits)-1]
	require.Equal(t, tabHitNeeds, last.kind)
	require.NotNil(t, u.handleTabBarClick(last.x0, false), "clicking the count jumps to the tab")

	u.trackTabStatus(pubsub.Event[permission.PermissionNotification]{
		Payload: permission.PermissionNotification{SessionID: child, ToolCallID: "t1", Granted: true},
	})
	require.Equal(t, tabMarkKindWorking, markOf(u, "sb"))
	require.Empty(t, u.tabsNeedingYou())
}

func TestTabStatusQuestionNeedsYou(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta", "Gamma")

	u.trackTabStatus(pubsub.Event[question.Request]{Payload: question.Request{ID: "q", SessionID: "sc"}})
	require.Equal(t, []int{2}, u.tabsNeedingYou())
	require.Equal(t, "need you", needsLabel(2))

	u.trackTabStatus(pubsub.Event[question.Notification]{Payload: question.Notification{SessionID: "sc"}})
	require.Empty(t, u.tabsNeedingYou())
}

func TestTabStatusRunEndClearsStrandedRequests(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	// A cancelled run publishes no decision for its open request.
	u.trackTabStatus(created("sb", "m1", message.User))
	u.trackTabStatus(pubsub.Event[permission.PermissionRequest]{
		Payload: permission.PermissionRequest{ID: "p1", SessionID: "sb", ToolCallID: "t1"},
	})
	u.trackTabStatus(pubsub.Event[notify.RunComplete]{Payload: notify.RunComplete{SessionID: "sb", Cancelled: true}})
	settle(t, u, "sb")
	require.Empty(t, u.tabsNeedingYou())
}

func TestTabBarShowsMarkAndAge(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	u.trackTabStatus(turnFinished("sb"))
	settle(t, u, "sb")
	u.tabStatusOf("sb").since = time.Now().Add(-5 * time.Minute)

	view, _ := u.renderTabBar(200)
	require.Contains(t, ansi.Strip(view), tabMarkDone+" 2 Beta 5m ×")

	narrow, _ := u.renderTabBar(28)
	require.NotContains(t, ansi.Strip(narrow), "5m", "ages go first when the bar is tight")
	require.LessOrEqual(t, ansi.StringWidth(narrow), 28)
}

func TestTabStatusClosedTabForgotten(t *testing.T) {
	t.Parallel()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")

	u.trackTabStatus(created("sb", "m1", message.User))
	u.removeTab(1)
	require.Nil(t, u.tabStatusOf("sb"))
}

func TestShortAge(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		10 * time.Second: "now",
		4 * time.Minute:  "4m",
		3 * time.Hour:    "3h",
		50 * time.Hour:   "2d",
	} {
		require.Equal(t, want, shortAge(d), d.String())
	}
}
