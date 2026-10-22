package model

// Control cockpit state for the status bar and sidebar.
//
// The snapshot comes from the workspace's control hub, which in
// client/server mode is an HTTP round-trip, so it follows the same rules as
// workspace_cache.go: reads return the memoized value, state edges
// invalidate it, and a TTL backstop at the end of Update re-fetches it
// off-thread.

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// controlTTL is how long a control snapshot may go without a re-fetch
// being scheduled. Package var so tests can pin it.
var controlTTL = 5 * time.Second

// controlCache holds the memoized control snapshot.
type controlCache struct {
	snap      *control.Snapshot
	checkedAt time.Time
	inFlight  bool
	gen       uint64
	// edits mirrors the hub's edits mode. It's written through on
	// toggles so the status bar badge doesn't wait for a fetch.
	edits bool
	// unavailable is set once the hub reported it isn't there, so the UI
	// hides the control strip instead of showing an empty one.
	unavailable bool
}

// controlSnapshotMsg delivers an off-thread snapshot fetch.
type controlSnapshotMsg struct {
	gen        uint64
	forSession string
	snap       *control.Snapshot
	err        error
}

// controlReplyMsg delivers the reply to a control command sent from the
// UI (take back, edits toggle, mission edits from the cockpit).
type controlReplyMsg struct {
	op    control.Op
	reply control.Reply
	err   error
}

// controlCmd runs a control command off-thread.
func (m *UI) controlCmd(c control.Command) tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	ws := m.com.Workspace
	if c.SessionID == "" {
		c.SessionID = m.currentSessionID()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := ws.Control(ctx, c)
		return controlReplyMsg{op: c.Op, reply: r, err: err}
	}
}

// invalidateControl marks the snapshot stale and discards any in-flight
// fetch, so the next Update tail fetches a fresh one.
func (m *UI) invalidateControl() {
	m.ctl.checkedAt = time.Time{}
	m.ctl.gen++
}

// dispatchControlRefresh fetches the control snapshot off-thread. It
// returns nil while a fetch is already in flight. The closure captures
// only locals.
func (m *UI) dispatchControlRefresh() tea.Cmd {
	if m.ctl.inFlight || m.com == nil || m.com.Workspace == nil {
		return nil
	}
	m.ctl.inFlight = true
	ws := m.com.Workspace
	gen := m.ctl.gen
	sessionID := m.currentSessionID()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := ws.Control(ctx, control.Command{Op: control.OpSnapshot, SessionID: sessionID})
		return controlSnapshotMsg{gen: gen, forSession: sessionID, snap: r.Snapshot, err: err}
	}
}

// applyControlSnapshot stores a fetched snapshot. A result that raced a
// session switch or an invalidation is discarded and re-fetched.
func (m *UI) applyControlSnapshot(msg controlSnapshotMsg) tea.Cmd {
	m.ctl.inFlight = false
	if msg.gen != m.ctl.gen || msg.forSession != m.currentSessionID() {
		return m.dispatchControlRefresh()
	}
	// Set the time even on error so a missing hub isn't re-asked on every
	// message.
	m.ctl.checkedAt = time.Now()
	if msg.err != nil || msg.snap == nil {
		m.ctl.unavailable = true
		return nil
	}
	m.ctl.unavailable = false
	m.storeControlSnapshot(msg.snap)
	return nil
}

func (m *UI) storeControlSnapshot(s *control.Snapshot) {
	m.ctl.snap = s
	m.ctl.checkedAt = time.Now()
	m.ctl.edits = s.State.Edits
	if d, ok := m.dialog.Dialog(dialog.ControlID).(*dialog.Control); ok {
		d.SetSnapshot(s)
	}
	if s.State.Auto != m.yoloModeCached() {
		// Another client (or take back) changed Auto mode. Write it
		// through like applyBusyState does.
		m.yoloCache.set(s.State.Auto)
		m.busyFetchGen++
		m.setEditorPrompt(s.State.Auto)
	}
}

// handleControlReply routes a control reply to the cockpit when it's
// open, then applies it.
func (m *UI) handleControlReply(msg controlReplyMsg) tea.Cmd {
	cmd := m.applyControlReply(msg)
	if d, ok := m.dialog.Dialog(dialog.ControlID).(*dialog.Control); ok {
		d.SetReply(msg.op, msg.reply, msg.err)
	}
	return cmd
}

// applyControlReply handles the answer to a control command.
func (m *UI) applyControlReply(msg controlReplyMsg) tea.Cmd {
	if msg.err != nil {
		m.invalidateControl()
		return util.ReportError(msg.err)
	}
	if msg.reply.Snapshot != nil {
		m.ctl.gen++
		m.ctl.inFlight = false
		m.storeControlSnapshot(msg.reply.Snapshot)
	} else {
		m.invalidateControl()
	}
	if msg.op == control.OpTakeBack {
		m.yoloCache.set(false)
		m.busyFetchGen++
		m.ctl.edits = false
		m.setEditorPrompt(false)
		if m.mode == uiInputModePlan {
			// Take back doesn't leave plan mode; plan never acts.
			return util.ReportInfo(msg.reply.Message)
		}
		return util.CmdHandler(util.InfoMsg{Type: util.InfoTypeManual, Msg: msg.reply.Message, TTL: 6 * time.Second})
	}
	if msg.reply.Message != "" {
		return util.ReportInfo(msg.reply.Message)
	}
	return nil
}

// openControlDialog opens the cockpit on page, or switches an open one to
// it, and loads its pages off-thread.
func (m *UI) openControlDialog(page dialog.ControlPage) tea.Cmd {
	if m.ctl.unavailable {
		return util.ReportWarn("Control isn't available in this workspace.")
	}
	if m.dialog.ContainsDialog(dialog.ControlID) {
		m.dialog.CloseDialog(dialog.ControlID)
	}
	d, load := dialog.NewControl(m.com, m.controlSnapshot(), page)
	m.dialog.OpenDialog(d)
	return m.runControlCmds(load.Cmds)
}

// runControlCmds runs control commands concurrently. Each reply comes back
// as its own controlReplyMsg.
func (m *UI) runControlCmds(cs []control.Command) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(cs))
	for _, c := range cs {
		cmds = append(cmds, m.controlCmd(c))
	}
	return tea.Batch(cmds...)
}

// takeBackCmd turns Auto and Edits off and denies anything waiting. It
// works while the agent is busy; the denial is what stops it.
func (m *UI) takeBackCmd() tea.Cmd {
	m.yoloCache.set(false)
	m.busyFetchGen++
	m.ctl.edits = false
	m.setEditorPrompt(false)
	return m.controlCmd(control.Command{Op: control.OpTakeBack})
}

// editsModeCached reports the memoized edits mode.
func (m *UI) editsModeCached() bool {
	return m.ctl.edits
}

// controlSnapshot returns the memoized snapshot, or nil.
func (m *UI) controlSnapshot() *control.Snapshot {
	if m.ctl.unavailable {
		return nil
	}
	return m.ctl.snap
}

// staleControlRefreshCmd is the control part of the TTL backstop.
func (m *UI) staleControlRefreshCmd() tea.Cmd {
	if m.ctl.checkedAt.IsZero() || time.Since(m.ctl.checkedAt) >= controlTTL {
		return m.dispatchControlRefresh()
	}
	return nil
}

// networkPillText returns the status bar's network pill: how many hosts
// off this machine the session reached, or "local only" when everything
// stayed on it. It's empty before the session has touched the network,
// and with no session open, where the totals would span the whole process.
func (m *UI) networkPillText() (string, bool) {
	if m.currentSessionID() == "" {
		return "", false
	}
	s := m.controlSnapshot()
	if s == nil || s.Network == nil || s.Network.Hosts == 0 {
		return "", false
	}
	if n := s.Network.External; n > 0 {
		if n == 1 {
			return "↑ 1 host", true
		}
		return fmt.Sprintf("↑ %d hosts", n), true
	}
	return "local only", false
}

// costPillText returns the status bar's spend pill: today's counted
// spend, "+?" when some of today's calls had no price, and the color of
// the most urgent budget. It's empty without a ledger or any calls today.
func (m *UI) costPillText() (string, costLevel) {
	s := m.controlSnapshot()
	if s == nil || !s.HasLedger {
		return "", costLevelPlain
	}
	var text string
	switch {
	case s.Today.Calls > 0:
		text = control.Money(s.Today.Cost)
		if s.Today.Unpriced > 0 {
			text += "+?"
		}
	case s.Today.Unpriced > 0:
		// Nothing priced: say so rather than show $0.
		text = "$?"
	default:
		// Only flat-rate calls, which aren't spend.
		return "", costLevelPlain
	}
	level := costLevelPlain
	for _, b := range s.Budgets {
		switch {
		case b.Over:
			level = costLevelOver
		case b.Warn > 0 && level < costLevelWarn:
			level = costLevelWarn
		}
	}
	return text + " today", level
}
