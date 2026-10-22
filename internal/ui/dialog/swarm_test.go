package dialog

import (
	"errors"
	"image"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

var swarmTestNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func newSwarmTestCommon() *common.Common {
	s := styles.CharmtonePantera()
	return &common.Common{Styles: &s}
}

func drawSwarmDialog(d Dialog, w, h int) string {
	scr := uv.NewScreenBuffer(w, h)
	d.Draw(scr, image.Rect(0, 0, w, h))
	return ansi.Strip(scr.Render())
}

func testSwarmPlan() control.SwarmPlan {
	return control.SwarmPlan{
		MissionID:    "m1",
		MissionTitle: "Ship the login page",
		Open:         3,
		Assignments: []control.SwarmAssignment{
			{
				Machine: "alpha", Model: "qwen-coder", Criteria: []int{1, 2},
				CriteriaText: []string{"The form validates email", "Errors show inline"},
				Branch:       "swarm/ship-the-login-page/alpha", Status: control.SwarmPlanned,
			},
			{
				Machine: "bravo", Model: "llama", Criteria: []int{3},
				CriteriaText: []string{"Tests pass"},
				Branch:       "swarm/ship-the-login-page/bravo", Status: control.SwarmPlanned,
			},
		},
		Skipped: []control.SwarmSkip{{Machine: "charlie", Reason: "offline: connection refused"}},
	}
}

func testSwarmRun() control.SwarmRun {
	return control.SwarmRun{
		ID:           "20261022-110000-abcd",
		MissionID:    "m1",
		MissionTitle: "Ship the login page",
		Target:       "main",
		Created:      swarmTestNow.Add(-time.Hour),
		Assignments: []control.SwarmAssignment{
			{
				Machine: "alpha", Model: "qwen-coder", Criteria: []int{1, 2},
				Branch: "swarm/ship-the-login-page/alpha", Status: control.SwarmDone,
				Started: swarmTestNow.Add(-50 * time.Minute), Finished: swarmTestNow.Add(-38 * time.Minute),
				Cost: 0.0123, Added: 42, Removed: 7, Files: []string{"auth.go", "login.go", "login_test.go"}, Summary: "Added validation.",
				Checks: []control.SwarmCheck{{Criterion: 1, Command: "go test ./auth", Passed: true, Duration: 2 * time.Second}},
			},
			{
				Machine: "bravo", Model: "llama", Criteria: []int{3},
				Branch: "swarm/ship-the-login-page/bravo", Status: control.SwarmRunning,
				Started: swarmTestNow.Add(-5 * time.Minute),
			},
			{
				Machine: "charlie", Model: "mistral", Criteria: []int{4},
				Branch: "swarm/ship-the-login-page/charlie", Status: control.SwarmFailed,
				Error: "model went quiet",
			},
		},
	}
}

func newTestMergeBoard(t *testing.T) *MergeBoard {
	t.Helper()
	d, act := NewMergeBoard(newSwarmTestCommon(), "")
	require.Equal(t, []control.Command{{Op: control.OpSwarmRuns}}, act.Cmds)
	d.now = func() time.Time { return swarmTestNow }
	return d
}

func TestSwarmConfirmGolden(t *testing.T) {
	t.Parallel()

	d := NewSwarmConfirm(newSwarmTestCommon(), testSwarmPlan())
	golden.RequireEqual(t, []byte(drawSwarmDialog(d, 90, 30)))
}

func TestSwarmConfirmEmptyGolden(t *testing.T) {
	t.Parallel()

	plan := control.SwarmPlan{
		MissionID: "m1", MissionTitle: "Ship the login page", Open: 2,
		Skipped: []control.SwarmSkip{{Machine: "alpha", Reason: "disabled"}},
	}
	d := NewSwarmConfirm(newSwarmTestCommon(), plan)
	golden.RequireEqual(t, []byte(drawSwarmDialog(d, 90, 20)))

	// With nothing to run, y does nothing and esc closes.
	require.Nil(t, d.HandleMsg(keyMsg('y')))
	require.Equal(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}

func TestSwarmConfirmKeys(t *testing.T) {
	t.Parallel()

	d := NewSwarmConfirm(newSwarmTestCommon(), testSwarmPlan())
	require.Equal(t, ActionSwarmLaunch{
		MissionID: "m1",
		Assign:    map[string][]int{"alpha": {1, 2}, "bravo": {3}},
	}, d.HandleMsg(keyMsg('y')))
	require.Equal(t, ActionClose{}, d.HandleMsg(keyMsg('n')))
}

func TestMergeBoardGolden(t *testing.T) {
	t.Parallel()

	d := newTestMergeBoard(t)
	d.SetReply(control.OpSwarmRuns, control.Reply{Swarms: []control.SwarmRun{testSwarmRun()}}, nil)
	require.True(t, d.Running())
	golden.RequireEqual(t, []byte(drawSwarmDialog(d, 110, 40)))
}

func TestMergeBoardEmptyGolden(t *testing.T) {
	t.Parallel()

	d := newTestMergeBoard(t)
	d.SetReply(control.OpSwarmRuns, control.Reply{}, nil)
	require.False(t, d.Running())
	golden.RequireEqual(t, []byte(drawSwarmDialog(d, 110, 30)))
}

func TestMergeBoardDiscardArmedGolden(t *testing.T) {
	t.Parallel()

	d := newTestMergeBoard(t)
	d.SetReply(control.OpSwarmRuns, control.Reply{Swarms: []control.SwarmRun{testSwarmRun()}}, nil)
	require.Nil(t, d.HandleMsg(keyMsg('d')))
	golden.RequireEqual(t, []byte(drawSwarmDialog(d, 110, 40)))

	// y discards the armed card.
	act := d.HandleMsg(keyMsg('y'))
	require.Equal(t, ActionControl{Cmds: []control.Command{{
		Op: control.OpSwarmDiscard, RunID: "20261022-110000-abcd", Machine: "alpha",
	}}}, act)
}

func TestMergeBoardKeys(t *testing.T) {
	t.Parallel()

	d := newTestMergeBoard(t)
	d.SetReply(control.OpSwarmRuns, control.Reply{Swarms: []control.SwarmRun{testSwarmRun()}}, nil)

	// A finished card merges.
	act := d.HandleMsg(keyMsg('m'))
	require.Equal(t, ActionControl{Cmds: []control.Command{{
		Op: control.OpSwarmMerge, RunID: "20261022-110000-abcd", Machine: "alpha",
	}}}, act)
	// Nothing else goes out while it's busy.
	require.Nil(t, d.HandleMsg(keyMsg('m')))
	d.SetReply(control.OpSwarmMerge, control.Reply{Message: "Merged."}, nil)

	// A running card can't be merged or discarded.
	d.HandleMsg(keyMsg('j'))
	require.Nil(t, d.HandleMsg(keyMsg('m')))
	require.Contains(t, d.err, "still working")
	require.Nil(t, d.HandleMsg(keyMsg('d')))
	require.Empty(t, d.discard)

	// A failed card can't be merged, and any key but y keeps it.
	d.HandleMsg(keyMsg('j'))
	require.Nil(t, d.HandleMsg(keyMsg('m')))
	require.Contains(t, d.err, "nothing to merge")
	require.Nil(t, d.HandleMsg(keyMsg('d')))
	require.NotEmpty(t, d.discard)
	require.Nil(t, d.HandleMsg(keyMsg('n')))
	require.Empty(t, d.discard)
	require.Equal(t, "Kept it.", d.notice)

	// Enter asks for the diff once.
	act = d.HandleMsg(enter())
	require.Equal(t, ActionControl{Cmds: []control.Command{{
		Op: control.OpSwarmDiff, RunID: "20261022-110000-abcd", Machine: "charlie",
	}}}, act)
	require.Equal(t, ActionClose{}, func() Action {
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
		return d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	}())
}

func TestMergeBoardIgnoresDiffForAnotherCard(t *testing.T) {
	t.Parallel()

	const runID = "20261022-110000-abcd"
	d := newTestMergeBoard(t)
	d.SetReply(control.OpSwarmRuns, control.Reply{Swarms: []control.SwarmRun{testSwarmRun()}}, nil)

	// Ask for alpha's diff, then move on to bravo's before it comes back.
	act := d.HandleMsg(enter())
	require.Equal(t, ActionControl{Cmds: []control.Command{{Op: control.OpSwarmDiff, RunID: runID, Machine: "alpha"}}}, act)
	d.HandleMsg(enter())
	d.HandleMsg(keyMsg('j'))
	act = d.HandleMsg(enter())
	require.Equal(t, ActionControl{Cmds: []control.Command{{Op: control.OpSwarmDiff, RunID: runID, Machine: "bravo"}}}, act)

	// Alpha's late diff, or its error, doesn't land on bravo's card.
	alpha := []control.SwarmFileDiff{{Path: "auth.go", New: "package auth\n"}}
	d.SetReply(control.OpSwarmDiff, control.Reply{Diff: alpha, RunID: runID, Machine: "alpha"}, nil)
	require.False(t, d.diffLoaded)
	require.Nil(t, d.diff)
	d.SetReply(control.OpSwarmDiff, control.Reply{RunID: runID, Machine: "alpha"}, errors.New("boom"))
	require.False(t, d.diffLoaded)
	require.Empty(t, d.diffErr)

	bravo := []control.SwarmFileDiff{{Path: "login.go", New: "package login\n"}}
	d.SetReply(control.OpSwarmDiff, control.Reply{Diff: bravo, RunID: runID, Machine: "bravo"}, nil)
	require.True(t, d.diffLoaded)
	require.Equal(t, bravo, d.diff)
}
