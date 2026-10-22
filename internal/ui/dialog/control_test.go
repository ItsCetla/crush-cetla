package dialog

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newTestControl(t *testing.T, snap *control.Snapshot) (*Control, ActionControl) {
	t.Helper()
	s := styles.CharmtonePantera()
	return NewControl(&common.Common{Styles: &s}, snap, ControlPageOverview)
}

func TestControl_LoadsEveryPage(t *testing.T) {
	t.Parallel()
	_, act := newTestControl(t, nil)
	var ops []control.Op
	for _, c := range act.Cmds {
		ops = append(ops, c.Op)
	}
	require.Equal(t, []control.Op{control.OpUsage, control.OpActions, control.OpMissions, control.OpNetwork}, ops)
}

func TestControl_PageKeys(t *testing.T) {
	t.Parallel()
	c, _ := newTestControl(t, &control.Snapshot{})

	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, ControlPageCost, c.Page())
	c.HandleMsg(keyMsg('l'))
	require.Equal(t, ControlPageActions, c.Page())
	c.HandleMsg(keyMsg('h'))
	require.Equal(t, ControlPageCost, c.Page())
	c.HandleMsg(keyMsg('4'))
	require.Equal(t, ControlPageMissions, c.Page())
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, ControlPageNetwork, c.Page())
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, ControlPageOverview, c.Page(), "tab wraps around")
	c.HandleMsg(keyMsg('h'))
	require.Equal(t, ControlPageNetwork, c.Page(), "back wraps around")
	c.HandleMsg(keyMsg('4'))
	require.Equal(t, ControlPageMissions, c.Page())
	c.HandleMsg(keyMsg('5'))
	require.Equal(t, ControlPageNetwork, c.Page())
	c.HandleMsg(keyMsg('1'))
	require.Equal(t, ControlPageOverview, c.Page())
}

func TestControl_ActionKeys(t *testing.T) {
	t.Parallel()
	c, _ := newTestControl(t, nil)
	require.Nil(t, c.HandleMsg(keyMsg('e')), "edits needs a snapshot")

	c.SetSnapshot(&control.Snapshot{State: permission.ControlState{Edits: true}, At: time.Now()})
	require.Equal(t, ActionSetEdits{On: false}, c.HandleMsg(keyMsg('e')))
	require.Equal(t, ActionTakeBack{}, c.HandleMsg(keyMsg('T')))
	require.Equal(t, ActionTakeBack{}, c.HandleMsg(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}))
	_, ok := c.HandleMsg(keyMsg('r')).(ActionControl)
	require.True(t, ok)
	require.IsType(t, ActionClose{}, c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}

func TestControl_MissionReplies(t *testing.T) {
	t.Parallel()
	c, _ := newTestControl(t, &control.Snapshot{})
	c.HandleMsg(keyMsg('4'))

	c.SetReply(control.OpMissions, control.Reply{Missions: []control.Mission{
		{ID: "m1", Title: "One", Status: control.MissionActive},
		{ID: "m2", Title: "Two", Status: control.MissionActive},
	}}, nil)
	require.Len(t, c.missions, 2)

	// A created mission goes on top and opens.
	c.SetReply(control.OpMissionCreate, control.Reply{Mission: &control.Mission{ID: "m3", Title: "Three"}}, nil)
	require.Len(t, c.missions, 3)
	require.Equal(t, "m3", c.missions[0].ID)
	require.True(t, c.detail)

	// An update replaces in place.
	c.SetReply(control.OpMissionNote, control.Reply{Mission: &control.Mission{ID: "m2", Title: "Two!"}}, nil)
	require.Len(t, c.missions, 3)
	require.Equal(t, "Two!", c.missions[2].Title)

	// Delete needs x twice, then the reply removes it.
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.False(t, c.detail)
	require.Nil(t, c.HandleMsg(keyMsg('x')))
	require.Equal(t, "m3", c.confirmDel)
	act, ok := c.HandleMsg(keyMsg('x')).(ActionControl)
	require.True(t, ok)
	require.Equal(t, control.OpMissionDelete, act.Cmds[0].Op)
	require.Equal(t, "m3", act.Cmds[0].MissionID)
	c.SetReply(control.OpMissionDelete, control.Reply{}, nil)
	require.Len(t, c.missions, 2)
	require.Empty(t, c.confirmDel)

	// Any other key cancels a pending delete.
	c.HandleMsg(keyMsg('x'))
	c.HandleMsg(keyMsg('j'))
	require.Empty(t, c.confirmDel)

	// Errors show and clear on the next good reply.
	c.SetReply(control.OpMissions, control.Reply{}, errors.New("disk full"))
	require.Contains(t, ansi.Strip(c.footer(c.com.Styles, time.Now(), 80)), "disk full")
	c.SetReply(control.OpMissions, control.Reply{Missions: c.missions}, nil)
	require.NoError(t, c.err)
}

func TestControl_MissionDetailKeys(t *testing.T) {
	t.Parallel()
	c, _ := newTestControl(t, &control.Snapshot{})
	c.HandleMsg(keyMsg('4'))
	c.SetReply(control.OpMissions, control.Reply{Missions: []control.Mission{{
		ID: "m1", Title: "One", Status: control.MissionActive,
		Criteria: []control.Criterion{{ID: 1, Text: "a", Done: true}, {ID: 2, Text: "b"}},
	}}}, nil)

	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, c.detail)

	act := c.HandleMsg(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}).(ActionControl)
	require.Equal(t, control.OpMissionUncheck, act.Cmds[0].Op)
	c.HandleMsg(keyMsg('j'))
	act = c.HandleMsg(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}).(ActionControl)
	require.Equal(t, control.OpMissionCheck, act.Cmds[0].Op)
	require.Equal(t, 2, act.Cmds[0].Index)

	act = c.HandleMsg(keyMsg('p')).(ActionControl)
	require.Equal(t, control.MissionPaused, act.Cmds[0].Status)
	act = c.HandleMsg(keyMsg('d')).(ActionControl)
	require.Equal(t, control.MissionDone, act.Cmds[0].Status)

	// Adding an item goes through the input line; an empty entry sends
	// nothing.
	c.HandleMsg(keyMsg('a'))
	require.Equal(t, controlInputCriterion, c.inputFor)
	require.Nil(t, c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	require.Equal(t, controlInputNone, c.inputFor)

	c.HandleMsg(keyMsg('n'))
	require.Equal(t, controlInputNote, c.inputFor)
	c.input.SetValue("Found the cause.")
	act = c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}).(ActionControl)
	require.Equal(t, control.OpMissionNote, act.Cmds[0].Op)
	require.Equal(t, "Found the cause.", act.Cmds[0].Text)
}

func TestControl_NarrowTabs(t *testing.T) {
	t.Parallel()
	c, _ := newTestControl(t, &control.Snapshot{})
	c.HandleMsg(keyMsg('2'))
	wide := ansi.Strip(c.tabs(c.com.Styles, 90))
	require.Contains(t, wide, "1 Overview")
	require.Contains(t, wide, "4 Missions")
	require.Contains(t, wide, "5 Network")
	narrow := ansi.Strip(c.tabs(c.com.Styles, 24))
	require.Contains(t, narrow, "2/5 Cost")
	require.Contains(t, narrow, "‹")
	require.Contains(t, narrow, "›")
	require.NotContains(t, narrow, "Overview")
}

func TestControl_RendersEveryPage(t *testing.T) {
	t.Parallel()
	now := time.Now()
	c, _ := newTestControl(t, &control.Snapshot{HasLedger: true, At: now})
	c.width, c.height = 70, 14
	for p := range 5 {
		c.HandleMsg(keyMsg(rune('1' + p)))
		out := ansi.Strip(c.content(now))
		require.Contains(t, out, "Control")
		require.Contains(t, out, "YOU")
		for _, l := range splitLines(out) {
			require.LessOrEqual(t, ansi.StringWidth(l), 70, "page %d line too wide: %q", p+1, l)
		}
	}
}

func TestControl_NetworkPage(t *testing.T) {
	t.Parallel()
	now := time.Now()
	c, _ := newTestControl(t, &control.Snapshot{At: now})
	c.width, c.height = 70, 20
	c.HandleMsg(keyMsg('5'))

	out := ansi.Strip(c.content(now))
	require.Contains(t, out, "Loading…")

	c.SetReply(control.OpNetwork, control.Reply{Network: &netlog.Report{
		SessionID: "s1",
		Hosts: []netlog.Host{
			{
				Host: "api.anthropic.com", Purposes: []netlog.Purpose{netlog.PurposeProvider},
				Requests: 3, BytesOut: 42_000, BytesIn: 9_100, LastSeen: now.Add(-time.Minute),
			},
			{
				Host: "127.0.0.1", Purposes: []netlog.Purpose{netlog.PurposeMCP}, Local: true,
				Requests: 1, BytesOut: 200, BytesIn: 300, LastSeen: now,
			},
		},
		Totals: netlog.Totals{Hosts: 2, External: 1, Requests: 4, BytesOut: 42_200, BytesIn: 9_400},
		Background: []netlog.Host{{
			Host: "api.github.com", Purposes: []netlog.Purpose{netlog.PurposeUpdate},
			Requests: 1, BytesOut: 120, BytesIn: 2_048, LastSeen: now,
		}},
	}}, nil)
	out = ansi.Strip(c.content(now))
	require.Contains(t, out, "This session talked to 2 hosts")
	require.Contains(t, out, "1 off this machine")
	require.Contains(t, out, "api.anthropic.com")
	require.Contains(t, out, "model")
	require.Contains(t, out, "41 KB")
	require.Contains(t, out, "127.0.0.1")
	require.Contains(t, out, "Ta-Da itself")
	require.Contains(t, out, "api.github.com")
	require.Contains(t, out, "never")
	for _, l := range splitLines(out) {
		require.LessOrEqual(t, ansi.StringWidth(l), 70, "line too wide: %q", l)
	}

	// Scrolling moves the host rows, not the summary.
	c.HandleMsg(keyMsg('j'))
	out = ansi.Strip(c.content(now))
	require.Contains(t, out, "This session talked to 2 hosts")
	require.NotContains(t, out, "api.anthropic.com")

	c.SetReply(control.OpNetwork, control.Reply{Network: &netlog.Report{
		Hosts:  []netlog.Host{{Host: "localhost", Local: true, Requests: 2, LastSeen: now}},
		Totals: netlog.Totals{Hosts: 1, Requests: 2},
	}}, nil)
	c.HandleMsg(keyMsg('k'))
	out = ansi.Strip(c.content(now))
	require.Contains(t, out, "Local only.")
	require.Contains(t, out, "Ta-Da talked to 1 host,")

	c.SetReply(control.OpNetwork, control.Reply{Network: &netlog.Report{SessionID: "s1"}}, nil)
	require.Contains(t, ansi.Strip(c.content(now)), "This session hasn't made a network request.")
	c.SetReply(control.OpNetwork, control.Reply{Network: &netlog.Report{}}, nil)
	require.Contains(t, ansi.Strip(c.content(now)), "Nothing has left this machine.")
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func TestControl_MissionChecks(t *testing.T) {
	t.Parallel()
	now := time.Now()
	c, _ := newTestControl(t, &control.Snapshot{At: now})
	c.width, c.height = 70, 14
	c.HandleMsg(keyMsg('4'))
	c.SetReply(control.OpMissions, control.Reply{Missions: []control.Mission{{
		ID: "m1", Title: "One", Status: control.MissionActive,
		Criteria: []control.Criterion{
			{ID: 1, Text: "Tests pass", Check: "go test ./...", LastRunAt: now.Add(-time.Minute), LastExit: 1, LastOutput: "…\n--- FAIL: TestX\nmore"},
			{ID: 2, Text: "Builds", Check: "go build ./...", Done: true, CheckedBy: control.CheckedByCheck, LastRunAt: now},
			{ID: 3, Text: "Docs read well"},
		},
	}}}, nil)
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, c.detail)

	out := ansi.Strip(c.content(now))
	require.Contains(t, out, "☐ ⟳ Tests pass")
	require.Contains(t, out, "✗ exit 1 · ")
	require.Contains(t, out, "--- FAIL: TestX")
	require.Contains(t, out, "☑ ⟳ Builds")
	require.Contains(t, out, "✓ passed")
	require.Contains(t, out, "☐ Docs read well")
	for _, l := range splitLines(out) {
		require.LessOrEqual(t, ansi.StringWidth(l), 70, "line too wide: %q", l)
	}

	// Ticking a self-checking criterion runs its check.
	act := c.HandleMsg(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}).(ActionControl)
	require.Equal(t, control.OpMissionCheck, act.Cmds[0].Op)
	require.Equal(t, "Running the check…", c.notice)

	// v runs every check.
	act = c.HandleMsg(keyMsg('v')).(ActionControl)
	require.Equal(t, []control.Command{{Op: control.OpMissionVerify, MissionID: "m1"}}, act.Cmds)
	require.Equal(t, "Running checks…", c.notice)
	c.SetReply(control.OpMissionVerify, control.Reply{Message: "All 2 checks passed."}, nil)
	require.Equal(t, "All 2 checks passed.", c.notice)

	// On a short page only the selected criterion keeps its result line.
	c.height = 6
	short := ansi.Strip(c.content(now))
	require.Contains(t, short, "✗ exit 1")
	require.NotContains(t, short, "✓ passed")
}

func TestEscapeHidden(t *testing.T) {
	t.Parallel()
	require.Equal(t, "go test ./...", escapeHidden("go test ./..."))
	require.Equal(t, `exit 0\x1b[2K\x0dexit 1`, escapeHidden("exit 0\x1b[2K\rexit 1"))
	require.Equal(t, `echo \u202esh`, escapeHidden("echo \u202esh"))
}
