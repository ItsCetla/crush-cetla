package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/limits"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

func TestWriteStatusEmpty(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	writeStatus(&b, statusReport{}, time.Now(), false)
	out := b.String()
	require.Contains(t, out, "Ta-Da status")
	require.Contains(t, out, "No usage recorded yet.")
	require.Contains(t, out, "None set. Add options.budget")
	require.Contains(t, out, "None yet in this project.")
	require.Contains(t, out, `cockpit (ctrl+\ or alt+c)`)
	require.NotContains(t, out, "Limits", "hidden unless options.tui.limit_rings is set")

	b.Reset()
	writeStatus(&b, statusReport{LimitsOn: true}, time.Now(), false)
	require.Contains(t, b.String(), "No readings yet")
	b.Reset()
	writeStatus(&b, statusReport{LimitsOn: true, LimitsError: "boom"}, time.Now(), false)
	require.Contains(t, b.String(), "Can't read the limits file: boom")
}

func sampleStatus(now time.Time) statusReport {
	return statusReport{
		Snapshot: control.Snapshot{
			HasLedger: true,
			Today:     control.Totals{Cost: 1.5, Tokens: 12000, Unpriced: 2},
			Month:     control.Totals{Cost: 20, Tokens: 900000, FlatCalls: 3, Flat: 4},
			Budgets: []control.BudgetStatus{
				{Window: control.WindowToday, Limit: 2, Spent: 1.5, Pct: 75, Warn: 75},
				{Window: control.WindowMonth, Limit: 10, Spent: 20, Pct: 200, Over: true},
			},
		},
		Report: control.Report{
			Daily:   []float64{0, 1, 2, 3, 1.5},
			Models:  []control.ModelCost{{Model: "sonnet", Cost: 1.5, Tokens: 12000, Calls: 4}},
			Burn:    0.25,
			HasBurn: true,
		},
		Actions: []permission.Action{
			{At: now.Add(-time.Minute), ToolName: "edit", Path: "main.go", Decision: permission.DecisionAllowed, Via: permission.ViaAuto},
			{At: now.Add(-2 * time.Minute), ToolName: "bash", Description: "rm -rf build", Decision: permission.DecisionDenied},
		},
		Missions: []control.Mission{
			{Title: "Ship it", Status: control.MissionActive, Criteria: []control.Criterion{{ID: 1, Done: true}, {ID: 2}}},
			{Title: "Old work", Status: control.MissionPaused},
		},
		LimitsOn: true,
		Limits: []limits.Provider{
			{
				Name: "Claude",
				Five: limits.Window{Known: true, Used: 84, Reset: now.Add(time.Hour)},
				Week: limits.Window{},
			},
			{Name: "Codex", Stale: true},
		},
	}
}

func TestWriteStatusFull(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var b bytes.Buffer
	writeStatus(&b, sampleStatus(now), now, false)
	out := b.String()
	require.Contains(t, out, "2 calls without a price")
	require.Contains(t, out, "list price on a flat plan (not counted)")
	require.Contains(t, out, "Burn rate")
	require.Contains(t, out, "14 days")
	require.Contains(t, out, "sonnet")
	require.Contains(t, out, "past 75%")
	require.Contains(t, out, "OVER: Ta-Da will ask before spending more")
	require.Contains(t, out, "✓")
	require.Contains(t, out, "✕")
	require.Contains(t, out, "(auto mode)")
	require.Contains(t, out, "Ship it  1/2")
	require.Contains(t, out, "(paused)")
	require.Contains(t, out, "Claude     ◕ 5h 84% (resets ")
	require.Contains(t, out, ") !, ◌ week —")
	require.Contains(t, out, "Codex      stale")
}

func TestWriteStatusPlainIsASCII(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var b bytes.Buffer
	writeStatus(&b, sampleStatus(now), now, true)
	out := b.String()
	for i, r := range out {
		require.Less(t, r, rune(0x80), "non-ASCII %q at %d in:\n%s", r, i, out)
	}
	require.NotContains(t, out, "14 days")
	require.Contains(t, out, "[")
	require.Contains(t, out, "+ ")
	require.Contains(t, out, "x ")
	require.Contains(t, out, "- Ship it  1/2")
	require.Contains(t, out, "Claude     5h 84%")
	require.Contains(t, out, "week ?")
}

func TestDecisionMark(t *testing.T) {
	t.Parallel()
	require.Equal(t, "-", decisionMark(permission.DecisionCanceled, false))
	require.Equal(t, "!", decisionMark(permission.Decision("other"), true))
}
