package dialog

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/control"
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
	require.Equal(t, []control.Op{control.OpUsage, control.OpActions, control.OpMissions}, ops)
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
	require.Equal(t, ControlPageOverview, c.Page(), "tab wraps around")
	c.HandleMsg(keyMsg('h'))
	require.Equal(t, ControlPageMissions, c.Page(), "back wraps around")
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
	wide := ansi.Strip(c.tabs(c.com.Styles, 80))
	require.Contains(t, wide, "1 Overview")
	require.Contains(t, wide, "4 Missions")
	narrow := ansi.Strip(c.tabs(c.com.Styles, 24))
	require.Contains(t, narrow, "2/4 Cost")
	require.Contains(t, narrow, "‹")
	require.Contains(t, narrow, "›")
	require.NotContains(t, narrow, "Overview")
}

func TestControl_RendersEveryPage(t *testing.T) {
	t.Parallel()
	now := time.Now()
	c, _ := newTestControl(t, &control.Snapshot{HasLedger: true, At: now})
	c.width, c.height = 70, 14
	for p := range 4 {
		c.HandleMsg(keyMsg(rune('1' + p)))
		out := ansi.Strip(c.content(now))
		require.Contains(t, out, "Control")
		require.Contains(t, out, "YOU")
		for _, l := range splitLines(out) {
			require.LessOrEqual(t, ansi.StringWidth(l), 70, "page %d line too wide: %q", p+1, l)
		}
	}
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
