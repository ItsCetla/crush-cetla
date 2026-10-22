package model

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestCostPillText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		snap  *control.Snapshot
		text  string
		level costLevel
	}{
		{"no snapshot", nil, "", costLevelPlain},
		{"no ledger", &control.Snapshot{}, "", costLevelPlain},
		{"no calls", &control.Snapshot{HasLedger: true}, "", costLevelPlain},
		{"flat only", &control.Snapshot{HasLedger: true, Today: control.Totals{FlatCalls: 3, Flat: 2}}, "", costLevelPlain},
		{"priced", &control.Snapshot{HasLedger: true, Today: control.Totals{Calls: 2, Cost: 1.25}}, "$1.25 today", costLevelPlain},
		{"some unpriced", &control.Snapshot{HasLedger: true, Today: control.Totals{Calls: 2, Cost: 1.25, Unpriced: 1}}, "$1.25+? today", costLevelPlain},
		{"only unpriced", &control.Snapshot{HasLedger: true, Today: control.Totals{Unpriced: 2}}, "$? today", costLevelPlain},
		{
			"warn", &control.Snapshot{
				HasLedger: true, Today: control.Totals{Calls: 1, Cost: 1},
				Budgets: []control.BudgetStatus{{Warn: 75}},
			}, "$1.00 today", costLevelWarn,
		},
		{
			"over wins", &control.Snapshot{
				HasLedger: true, Today: control.Totals{Calls: 1, Cost: 1},
				Budgets: []control.BudgetStatus{{Over: true}, {Warn: 90}},
			}, "$1.00 today", costLevelOver,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := newTestUI()
			u.ctl.snap = tc.snap
			text, level := u.costPillText()
			require.Equal(t, tc.text, text)
			require.Equal(t, tc.level, level)
		})
	}
}

func TestNetworkPillText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		session  bool
		snap     *control.Snapshot
		text     string
		external bool
	}{
		{"no session", false, &control.Snapshot{Network: &netlog.Totals{Hosts: 2, External: 2}}, "", false},
		{"no snapshot", true, nil, "", false},
		{"no receipt", true, &control.Snapshot{}, "", false},
		{"nothing yet", true, &control.Snapshot{Network: &netlog.Totals{}}, "", false},
		{"local only", true, &control.Snapshot{Network: &netlog.Totals{Hosts: 2}}, "local only", false},
		{"one host", true, &control.Snapshot{Network: &netlog.Totals{Hosts: 2, External: 1}}, "↑ 1 host", true},
		{"hosts", true, &control.Snapshot{Network: &netlog.Totals{Hosts: 4, External: 3}}, "↑ 3 hosts", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := newTestUI()
			if tc.session {
				u.session = &session.Session{ID: "s1"}
			}
			u.ctl.snap = tc.snap
			text, external := u.networkPillText()
			require.Equal(t, tc.text, text)
			require.Equal(t, tc.external, external)
		})
	}
}

func TestCostPillHiddenWhenUnavailable(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.ctl.snap = &control.Snapshot{HasLedger: true, Today: control.Totals{Calls: 1, Cost: 1}}
	u.ctl.unavailable = true
	text, _ := u.costPillText()
	require.Empty(t, text)
	require.Empty(t, u.controlInfo(30, time.Now()))
}

func TestControlInfo(t *testing.T) {
	t.Parallel()
	now := time.Now()
	u := newTestUI()
	u.ctl.snap = &control.Snapshot{}
	out := ansi.Strip(u.controlInfo(32, now))
	require.Contains(t, out, "Control")
	require.Contains(t, out, "You · the agent asks first")
	require.Contains(t, out, "Nothing yet this session")

	u.ctl.snap = &control.Snapshot{
		State:          permission.ControlState{Auto: true, Waiting: true},
		SessionActions: 7,
		Recent: []permission.Action{
			{At: now.Add(-time.Minute), ToolName: "bash", Description: "a very long command that will not fit in the sidebar at all", Decision: permission.DecisionAllowed},
		},
	}
	out = ansi.Strip(u.controlInfo(32, now))
	require.Contains(t, out, "Auto")
	require.Contains(t, out, "7")
	require.Contains(t, out, "A request is waiting")
	require.NotContains(t, out, "Nothing yet")
	require.Contains(t, out, "1m ago")
	for _, l := range strings.Split(out, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(l), 32, "line too wide: %q", l)
	}

	u.ctl.snap = &control.Snapshot{State: permission.ControlState{Edits: true}}
	require.Contains(t, ansi.Strip(u.controlInfo(40, now)), "Edits")
}

func TestMissionInfo(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.ctl.snap = &control.Snapshot{}
	require.Empty(t, u.missionInfo(30))

	u.ctl.snap = &control.Snapshot{Mission: &control.Mission{
		Title:  "Ship the release",
		Status: control.MissionActive,
		Criteria: []control.Criterion{
			{ID: 1, Text: "tests pass", Done: true},
			{ID: 2, Text: "docs updated"},
		},
	}}
	out := ansi.Strip(u.missionInfo(30))
	require.Contains(t, out, "Mission")
	require.Contains(t, out, "1/2")
	require.Contains(t, out, "Ship the release")
	require.Contains(t, out, "50%")
	require.Contains(t, out, "next: docs updated")
	require.NotContains(t, out, "active")

	u.ctl.snap.Mission.Status = control.MissionPaused
	require.Contains(t, ansi.Strip(u.missionInfo(30)), "paused")
}

// controlWorkspace adds a recording control hub to testWorkspace.
type controlWorkspace struct {
	*testWorkspace
	cmds []control.Command
}

func (w *controlWorkspace) Control(_ context.Context, cmd control.Command) (control.Reply, error) {
	w.cmds = append(w.cmds, cmd)
	return control.Reply{}, nil
}

// runModeCmd runs cmd, following batches, and applies any mode switch it
// produces.
func runModeCmd(u *UI, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runModeCmd(u, c)
		}
	case modeSwitchedMsg:
		u.modeSwitching = false
		u.applyModeSwitch(msg)
	}
}

func TestToggleInputModeWithControlHub(t *testing.T) {
	t.Parallel()
	ui, ws := newPlanUI(t, "sess-1")
	cw := &controlWorkspace{testWorkspace: ws}
	ui.com.Workspace = cw
	ui.mode = uiInputModeCode

	for i, want := range []permissionMode{
		permissionModePlan,
		permissionModeEdits,
		permissionModeAuto,
		permissionModeManual,
	} {
		runModeCmd(ui, ui.toggleInputMode())
		require.Equal(t, want, ui.permissionMode(), "step %d", i+1)
	}

	// Edits went on entering Edits mode and off leaving it.
	var edits []bool
	for _, c := range cw.cmds {
		if c.Op == control.OpSetEdits {
			edits = append(edits, *c.Edits)
		}
	}
	require.Equal(t, []bool{true, false}, edits)
}

func TestCockpitWaiting(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	require.Zero(t, u.cockpitWaiting())

	u.ctl.snap = &control.Snapshot{State: permission.ControlState{Waiting: true}}
	require.Equal(t, 1, u.cockpitWaiting(), "the hub's flag counts when no tab knows")

	u.tabStatusFor("a").perms = map[string]struct{}{"t1": {}, "t2": {}}
	u.tabStatusFor("b").asking = true
	u.tabStatusFor("c")
	require.Equal(t, 3, u.cockpitWaiting())
}

func TestMissionPillText(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	require.Empty(t, u.missionPillText())

	m := &control.Mission{
		Title:  "Ship it",
		Status: control.MissionActive,
		Criteria: []control.Criterion{
			{ID: 1, Text: "tests pass", Done: true},
			{ID: 2, Text: "docs"},
		},
	}
	u.ctl.snap = &control.Snapshot{Mission: m}
	require.Equal(t, "◆ 1/2", u.missionPillText())

	m.Status = control.MissionPaused
	require.Equal(t, "◆ 1/2", u.missionPillText())

	m.Status = control.MissionDone
	require.Empty(t, u.missionPillText(), "a closed mission has no pill")

	m.Status = control.MissionActive
	m.Criteria = nil
	require.Empty(t, u.missionPillText(), "a mission without criteria has no pill")

	u.ctl.unavailable = true
	require.Empty(t, u.missionPillText())
}
