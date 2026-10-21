package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/ui/notification"
)

// Tab status marks: each tab shows what its session is doing, so work in
// a background tab is visible without switching to it.
//
//	⠋  working (the agent is running, or prompts are queued behind it)
//	◎  needs you (a permission or question is waiting)
//	✓  done; bold until you look at the tab, faint after
//	✗  the last turn failed
//
// The state is kept from agent events, not polled. A turn only counts as
// done once nothing is queued behind it, so a session working through a
// queue stays "working" and sends one notification at the end.

const (
	tabMarkNeeds  = "◎"
	tabMarkDone   = "✓"
	tabMarkFailed = "✗"
	// tabSettleDelay gives a queued prompt time to start before a
	// finished turn is called done, so a queue does not flash "done"
	// between its turns.
	tabSettleDelay = 400 * time.Millisecond
	// tabSpinInterval is how often the working mark turns.
	tabSpinInterval = 200 * time.Millisecond
	// tabAgeInterval is how often ages ("4m") refresh when nothing spins.
	tabAgeInterval = 30 * time.Second
	// tabMsgOwnersMax bounds the message-to-session map kept to place
	// sub-agent requests on their parent's tab.
	tabMsgOwnersMax = 2048
)

// tabSpinFrames are the frames of the working mark.
var tabSpinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// tabPhase is where a session's agent is.
type tabPhase int

const (
	tabIdle tabPhase = iota
	tabWorking
	tabDone
	tabFailed
)

// tabStatus is what one session's tab shows.
type tabStatus struct {
	phase tabPhase
	// unread is set when a turn ended while the tab was not in view.
	unread bool
	// perms holds the tool call IDs waiting for a permission decision.
	perms map[string]struct{}
	// asking is set while a question form waits for the user.
	asking bool
	// since is when the phase or needs last changed.
	since time.Time
	// settleGen discards settle probes made stale by a later event.
	settleGen uint64
	// finished records that the agent reported a finished turn since
	// the last settle, so the settle sends the "done" notification.
	finished bool
	// failed records that a run ended with an error since the last
	// settle. cancelled records a cancel.
	failed    bool
	cancelled bool
}

// needsYou reports whether the session waits for the user.
func (s *tabStatus) needsYou() bool {
	return s != nil && (len(s.perms) > 0 || s.asking)
}

// tabSettleMsg carries a probe of whether a session that just ended a
// turn still has work: an active run or queued prompts.
type tabSettleMsg struct {
	sessionID string
	gen       uint64
	busy      bool
	queued    int
}

// tabTickMsg turns the working mark and refreshes ages.
type tabTickMsg struct{}

// tabStatusFor returns the status for sessionID, creating it.
func (m *UI) tabStatusFor(sessionID string) *tabStatus {
	if m.tabStatuses == nil {
		m.tabStatuses = make(map[string]*tabStatus)
	}
	s, ok := m.tabStatuses[sessionID]
	if !ok {
		s = &tabStatus{}
		m.tabStatuses[sessionID] = s
	}
	return s
}

// tabStatusOf returns the status for sessionID, or nil.
func (m *UI) tabStatusOf(sessionID string) *tabStatus {
	return m.tabStatuses[sessionID]
}

// tabOwner maps a session ID to the top-level session whose tab shows
// it: sub-agent sessions belong to the session that ran the agent tool.
func (m *UI) tabOwner(sessionID string) string {
	if !isChildSession(sessionID) || m.com == nil || m.com.Workspace == nil {
		return sessionID
	}
	msgID, _, ok := m.com.Workspace.ParseAgentToolSessionID(sessionID)
	if !ok {
		return sessionID
	}
	if owner, ok := m.tabMsgOwners[msgID]; ok {
		return owner
	}
	return ""
}

// isChildSession reports whether sessionID belongs to a sub-agent run.
// Sub-agent session IDs are "messageID$$toolCallID".
func isChildSession(sessionID string) bool {
	return strings.Contains(sessionID, "$$")
}

// noteMessageOwner remembers which session a message belongs to.
func (m *UI) noteMessageOwner(msg message.Message) {
	if msg.ID == "" || msg.SessionID == "" {
		return
	}
	if m.tabMsgOwners == nil || len(m.tabMsgOwners) >= tabMsgOwnersMax {
		m.tabMsgOwners = make(map[string]string)
	}
	m.tabMsgOwners[msg.ID] = msg.SessionID
}

// viewingSession reports whether the user is looking at sessionID now.
func (m *UI) viewingSession(sessionID string) bool {
	return m.state == uiChat && m.session != nil && m.session.ID == sessionID &&
		(m.notifyWindowFocused || !m.caps.ReportFocusEvents)
}

// setTabPhase moves a session to phase and stamps the change.
func (s *tabStatus) setPhase(p tabPhase, now time.Time) {
	if s.phase != p {
		s.phase = p
		s.since = now
	}
}

// trackTabStatus updates tab marks from an agent event and returns any
// follow-up command. It runs before Update's own handling.
func (m *UI) trackTabStatus(msg tea.Msg) tea.Cmd {
	now := time.Now()
	switch msg := msg.(type) {
	case pubsub.Event[message.Message]:
		if msg.Type != pubsub.CreatedEvent {
			return nil
		}
		m.noteMessageOwner(msg.Payload)
		if msg.Payload.Role == message.User || msg.Payload.Role == message.Assistant {
			id := m.tabOwner(msg.Payload.SessionID)
			if id == "" {
				return nil
			}
			s := m.tabStatusFor(id)
			s.settleGen++ // a run is going; drop any pending settle
			s.setPhase(tabWorking, now)
			s.unread = false
		}
	case pubsub.Event[notify.Notification]:
		if isChildSession(msg.Payload.SessionID) {
			return nil // a sub-agent's turn is part of its parent's
		}
		switch msg.Payload.Type {
		case notify.TypeAgentFinished:
			s := m.tabStatusFor(msg.Payload.SessionID)
			s.finished = true
			return m.settleTab(msg.Payload.SessionID)
		case notify.TypeAgentError:
			s := m.tabStatusFor(msg.Payload.SessionID)
			s.failed = true
			return m.settleTab(msg.Payload.SessionID)
		}
	case pubsub.Event[notify.RunComplete]:
		if isChildSession(msg.Payload.SessionID) {
			return nil
		}
		s := m.tabStatusFor(msg.Payload.SessionID)
		switch {
		case msg.Payload.Cancelled:
			s.cancelled = true
		case msg.Payload.Error != "":
			s.failed = true
		}
		return m.settleTab(msg.Payload.SessionID)
	case pubsub.Event[permission.PermissionRequest]:
		id := m.tabOwner(msg.Payload.SessionID)
		if id == "" {
			return nil
		}
		s := m.tabStatusFor(id)
		if s.perms == nil {
			s.perms = make(map[string]struct{})
		}
		key := msg.Payload.ToolCallID
		if key == "" {
			key = msg.Payload.ID
		}
		s.perms[key] = struct{}{}
		s.since = now
	case pubsub.Event[permission.PermissionNotification]:
		if !msg.Payload.Granted && !msg.Payload.Denied {
			return nil
		}
		id := m.tabOwner(msg.Payload.SessionID)
		if s := m.tabStatusOf(id); s != nil {
			if _, ok := s.perms[msg.Payload.ToolCallID]; ok {
				delete(s.perms, msg.Payload.ToolCallID)
				s.since = now
			}
		}
	case pubsub.Event[question.Request]:
		id := m.tabOwner(msg.Payload.SessionID)
		if id == "" {
			return nil
		}
		s := m.tabStatusFor(id)
		s.asking = true
		s.since = now
	case pubsub.Event[question.Notification]:
		// Only one question is pending at a time, so any resolution
		// clears every tab's question.
		for _, s := range m.tabStatuses {
			if s.asking {
				s.asking = false
				s.since = now
			}
		}
	case tabSettleMsg:
		return m.applyTabSettle(msg, now)
	case tabTickMsg:
		m.tabTickArmed = false
	}
	return nil
}

// settleTab schedules a check of whether sessionID has more work before
// its tab is called done. Probes run off the UI goroutine because in
// client/server mode each one is an HTTP call.
func (m *UI) settleTab(sessionID string) tea.Cmd {
	if sessionID == "" {
		return nil
	}
	s := m.tabStatusFor(sessionID)
	s.settleGen++
	gen := s.settleGen
	ws := m.com.Workspace
	return tea.Tick(tabSettleDelay, func(time.Time) tea.Msg {
		msg := tabSettleMsg{sessionID: sessionID, gen: gen}
		if ws != nil {
			msg.busy = ws.AgentIsSessionBusy(sessionID)
			msg.queued = ws.AgentQueuedPrompts(sessionID)
		}
		return msg
	})
}

// applyTabSettle records how a session's work ended, once no run is
// active and nothing is queued, and sends the "done" notification.
func (m *UI) applyTabSettle(msg tabSettleMsg, now time.Time) tea.Cmd {
	s := m.tabStatusOf(msg.sessionID)
	if s == nil || msg.gen != s.settleGen {
		return nil
	}
	if msg.busy || msg.queued > 0 {
		s.setPhase(tabWorking, now)
		return nil
	}
	finished, failed, cancelled := s.finished, s.failed, s.cancelled
	s.finished, s.failed, s.cancelled = false, false, false
	// The run is over, so nothing it asked for is still waiting.
	s.perms = nil
	s.asking = false
	switch {
	case cancelled && !failed:
		s.setPhase(tabIdle, now)
		s.unread = false
		return nil
	case failed:
		s.setPhase(tabFailed, now)
	default:
		s.setPhase(tabDone, now)
	}
	s.since = now
	s.unread = !m.viewingSession(msg.sessionID)
	if !finished || failed {
		return nil
	}
	return m.sendNotification(notification.Notification{
		Title:   "Ta-Da is waiting...",
		Message: fmt.Sprintf("Agent's turn completed in \"%s\"", m.sessionTitleFor(msg.sessionID)),
	})
}

// sessionTitleFor returns the tab title for sessionID.
func (m *UI) sessionTitleFor(sessionID string) string {
	if i := m.tabIndex(sessionID); i >= 0 {
		return m.tabs[i].title
	}
	if m.session != nil && m.session.ID == sessionID {
		return tabTitle(*m.session)
	}
	return tabDraftTitle
}

// markViewedTabSeen clears the unread mark of the tab in view.
func (m *UI) markViewedTabSeen() {
	if m.session == nil {
		return
	}
	if s := m.tabStatusOf(m.session.ID); s != nil && s.unread && m.viewingSession(m.session.ID) {
		s.unread = false
	}
}

// tabsNeedingYou lists the indexes of tabs waiting for the user.
func (m *UI) tabsNeedingYou() []int {
	var out []int
	for i, t := range m.tabs {
		if m.tabStatusOf(t.id).needsYou() {
			out = append(out, i)
		}
	}
	return out
}

// jumpToNeedingTab shows the next tab, after the one in view, that waits
// for the user.
func (m *UI) jumpToNeedingTab() tea.Cmd {
	needs := m.tabsNeedingYou()
	if len(needs) == 0 {
		return nil
	}
	cur := m.activeTabIndex()
	for _, i := range needs {
		if i > cur {
			return m.switchToTab(i)
		}
	}
	return m.switchToTab(needs[0])
}

// tabTickInterval returns how often the tab bar must redraw on its own,
// or 0 when nothing on it changes with time.
func (m *UI) tabTickInterval() time.Duration {
	interval := time.Duration(0)
	for _, t := range m.tabs {
		s := m.tabStatusOf(t.id)
		if s == nil {
			continue
		}
		switch {
		case s.phase == tabWorking && !s.needsYou():
			return tabSpinInterval
		case s.phase != tabIdle || s.needsYou():
			interval = tabAgeInterval
		}
	}
	return interval
}

// armTabTick starts the tab bar's clock when something on it moves.
// Update calls it once at the end of every message.
func (m *UI) armTabTick() tea.Cmd {
	if m.tabTickArmed || !m.tabBarVisible() {
		return nil
	}
	d := m.tabTickInterval()
	if d == 0 {
		return nil
	}
	m.tabTickArmed = true
	return tea.Tick(d, func(time.Time) tea.Msg { return tabTickMsg{} })
}

// tabMark returns the status mark for a tab and whether it is set.
func (m *UI) tabMark(s *tabStatus, now time.Time) (string, tabMarkKind) {
	switch {
	case s == nil:
		return "", tabMarkNone
	case s.needsYou():
		return tabMarkNeeds, tabMarkKindNeeds
	case s.phase == tabWorking:
		frame := int(now.UnixMilli()/int64(tabSpinInterval/time.Millisecond)) % len(tabSpinFrames)
		return tabSpinFrames[frame], tabMarkKindWorking
	case s.phase == tabFailed:
		return tabMarkFailed, tabMarkKindFailed
	case s.phase == tabDone && s.unread:
		return tabMarkDone, tabMarkKindUnread
	case s.phase == tabDone:
		return tabMarkDone, tabMarkKindRead
	}
	return "", tabMarkNone
}

// tabMarkKind picks the style of a tab's status mark.
type tabMarkKind int

const (
	tabMarkNone tabMarkKind = iota
	tabMarkKindWorking
	tabMarkKindNeeds
	tabMarkKindUnread
	tabMarkKindRead
	tabMarkKindFailed
)

// tabAge formats how long ago a tab's status changed: "now", "4m", "2h",
// "3d". It is empty when the tab has no status worth dating.
func tabAge(s *tabStatus, now time.Time) string {
	if s == nil || s.since.IsZero() {
		return ""
	}
	if !s.needsYou() && (s.phase == tabIdle || s.phase == tabWorking) {
		return ""
	}
	return shortAge(now.Sub(s.since))
}

// shortAge formats d in the largest whole unit.
func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d"
}

// needsLabel follows the count of sessions waiting for the user.
func needsLabel(n int) string {
	if n == 1 {
		return "needs you"
	}
	return "need you"
}

// dropTabStatus forgets a closed tab's status.
func (m *UI) dropTabStatus(sessionID string) {
	delete(m.tabStatuses, sessionID)
}
