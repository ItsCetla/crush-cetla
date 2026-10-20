package control

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunMissionToolFlow(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	ms, err := NewMissions(t.TempDir())
	require.NoError(t, err)

	out, err := RunMissionTool(ctx, ms, "s1", MissionParams{})
	require.NoError(t, err)
	require.Contains(t, out, "no mission")

	_, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "note", Text: "x"})
	require.Error(t, err)

	out, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "list"})
	require.NoError(t, err)
	require.Equal(t, "No missions in this project yet.", out)

	out, err = RunMissionTool(ctx, ms, "s1", MissionParams{
		Action:   "create",
		Title:    "Ship it",
		Goal:     "Release v1",
		Criteria: []string{"tests pass"},
	})
	require.NoError(t, err)
	require.Contains(t, out, "Ship it")

	_, err = RunMissionTool(ctx, ms, "", MissionParams{Action: "create", Title: "x"})
	require.Error(t, err)

	out, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "add_criterion", Text: "docs updated"})
	require.NoError(t, err)
	require.Contains(t, out, "docs updated")

	_, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "check", ID: 1})
	require.Error(t, err, "check needs a verification note")

	_, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "check", ID: 1, Text: "go test ./... is green"})
	require.NoError(t, err)

	_, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "check", ID: 99, Text: "x"})
	require.Error(t, err)

	_, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "complete", Text: "done"})
	require.ErrorContains(t, err, "docs updated")

	_, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "check", ID: 2, Text: "README edited"})
	require.NoError(t, err)

	_, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "note", Text: "Chose the simple path."})
	require.NoError(t, err)

	_, err = RunMissionTool(ctx, ms, "s1", MissionParams{Action: "complete", Text: "Released."})
	require.NoError(t, err)

	all, err := ms.List()
	require.NoError(t, err)
	require.Len(t, all, 1)
	m := all[0]
	require.Equal(t, MissionDone, m.Status)
	require.Equal(t, "Released.", m.Summary)
	done, total := m.Progress()
	require.Equal(t, 2, done)
	require.Equal(t, 2, total)
	require.NotEmpty(t, m.Notes)
	require.Equal(t, "claude", m.Notes[len(m.Notes)-1].By)

	out, err = RunMissionTool(ctx, ms, "s2", MissionParams{Action: "list"})
	require.NoError(t, err)
	require.Contains(t, out, "Ship it (2/2)")

	_, err = RunMissionTool(ctx, ms, "s2", MissionParams{Action: "attach"})
	require.Error(t, err)
	out, err = RunMissionTool(ctx, ms, "s2", MissionParams{Action: "attach", MissionID: m.ID})
	require.NoError(t, err)
	require.Contains(t, out, "Attached")

	_, err = RunMissionTool(ctx, ms, "s2", MissionParams{Action: "uncheck", ID: 1, Text: "flaky"})
	require.NoError(t, err)
	got, err := ms.Get(m.ID)
	require.NoError(t, err)
	require.False(t, got.Criteria[0].Done)

	_, err = RunMissionTool(ctx, ms, "s2", MissionParams{Action: "fly"})
	require.ErrorContains(t, err, "unknown mission action")
}

func TestRunMissionToolNilStore(t *testing.T) {
	t.Parallel()
	_, err := RunMissionTool(t.Context(), nil, "s1", MissionParams{})
	require.Error(t, err)
}
