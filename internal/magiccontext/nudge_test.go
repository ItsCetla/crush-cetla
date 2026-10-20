// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for hooks/magic-context/ctx-reduce-nudge.ts.

package magiccontext

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func ch1(u, t int) channel1Input {
	return channel1Input{
		BaselineU: u, BaselineT: t, LastFireOrdinal: -1, CurrentRealUserTurnCount: 1,
		GraceBaselineU: -1, Evaluable: true,
	}
}

func TestNudgeBands(t *testing.T) {
	require.Equal(t, bandQuiet, channel1Band(40_000, 50_000))  // tail below 60k
	require.Equal(t, bandQuiet, channel1Band(20_000, 80_000))  // below floor
	require.Equal(t, bandQuiet, channel1Band(25_000, 200_000)) // ratio < 0.2
	require.Equal(t, LevelGentle, channel1Band(25_000, 100_000))
	require.Equal(t, LevelFirm, channel1Band(40_000, 100_000))
	require.Equal(t, LevelUrgent, channel1Band(60_000, 100_000))
	require.Equal(t, LevelUrgent, nudgeBand(70_000, 100_000))
	require.Equal(t, bandCh2, nudgeBand(75_000, 100_000))
	require.Equal(t, 25_000, channel1RefireTokens(100_000))
	require.Equal(t, 40_000, channel1RefireTokens(500_000))
}

func TestDecideChannel1(t *testing.T) {
	d := decideChannel1(ch1(30_000, 100_000))
	require.True(t, d.Fire)
	require.False(t, d.Sticky)
	require.Equal(t, LevelGentle, d.Level)
	require.Equal(t, "band-crossing", d.Reason)
	require.Equal(t, 30_000, d.NextLastNudge)

	// Same band, not enough growth.
	in := ch1(40_000, 200_000)
	in.LastNudgeLevel, in.LastNudgeUndropped = LevelGentle, 30_000
	d = decideChannel1(in)
	require.False(t, d.Fire)
	require.Equal(t, "cadence-growth", d.Reason)

	// Same band, enough growth, sticky floor not reached.
	in.BaselineU = 56_000
	in.LastFireOrdinal, in.CurrentRealUserTurnCount = 1, 3
	d = decideChannel1(in)
	require.False(t, d.Fire)
	require.Equal(t, "sticky-turn-floor", d.Reason)

	// Gap reached: calm sticky re-fire.
	in.CurrentRealUserTurnCount = 6
	d = decideChannel1(in)
	require.True(t, d.Fire)
	require.True(t, d.Sticky)
	require.Equal(t, "cadence-refire", d.Reason)

	// De-escalation is recorded quietly.
	in = ch1(30_000, 100_000)
	in.LastNudgeLevel = LevelUrgent
	d = decideChannel1(in)
	require.False(t, d.Fire)
	require.Equal(t, "band-deescalation", d.Reason)
	require.Equal(t, LevelGentle, d.NextLastNudgeLevel)

	// Quiet resets the cadence.
	in = ch1(10_000, 100_000)
	in.LastNudgeLevel, in.LastNudgeUndropped = LevelFirm, 50_000
	d = decideChannel1(in)
	require.False(t, d.Fire)
	require.Equal(t, "", d.NextLastNudgeLevel)
	require.Equal(t, 0, d.NextLastNudge)

	// Holds.
	in = ch1(60_000, 100_000)
	in.HasRecentReduce = true
	require.Equal(t, "recent-reduce-refresh", decideChannel1(in).Reason)
	in = ch1(60_000, 100_000)
	in.Evaluable = false
	require.Equal(t, "baseline-unevaluable", decideChannel1(in).Reason)
	in = ch1(60_000, 100_000)
	in.AgentDropsApplied = true
	require.Equal(t, "agent-drops-applied", decideChannel1(in).Reason)

	// Post-reduce grace: held until regrowth or escalation.
	in = ch1(45_000, 100_000)
	in.LastNudgeLevel, in.GraceBaselineU, in.GracePreLevel, in.GracePreLevelSet = LevelFirm, 40_000, LevelFirm, true
	d = decideChannel1(in)
	require.False(t, d.Fire)
	require.Equal(t, "post-reduce-compliance-grace", d.Reason)
	in.BaselineU = 61_000 // escalates to urgent
	d = decideChannel1(in)
	require.True(t, d.Fire)
	require.True(t, d.ClearPostReduceGrace)
	require.Equal(t, "post-reduce-band-escalation", d.Reason)
}

func TestNudgeText(t *testing.T) {
	hint := []TagRef{{Number: 3, Tool: "view"}, {Number: 5}}
	require.Equal(t, "\n\n<system-reminder>\nHousekeeping: 2 spent tool outputs (~31k tokens) are reclaimable. Stamp each output with ctx_reduce as soon as you've taken what you need from it; don't wait for the task to finish.\noldest reclaimable: §3§ view · §5§ tool.\n</system-reminder>",
		buildChannel1Reminder(LevelGentle, 30_600, 2, hint, false))
	require.Equal(t, "\n\n<system-reminder>\n1 spent tool output (~40k tokens) are reclaimable. Make a ctx_reduce pass now over the outputs you've already used, then continue.\n</system-reminder>",
		buildChannel1Reminder(LevelFirm, 40_000, 1, nil, false))
	require.Equal(t, "\n\n<system-reminder>\nspent tool outputs (~60k tokens) are still unstamped. Call ctx_reduce now, before your next tool call, on every output you've already used.\n</system-reminder>",
		buildChannel1Reminder(LevelUrgent, 60_000, 0, nil, false))
	require.Equal(t, "\n\n<system-reminder>\nStill unstamped: 4 spent tool outputs (~50k tokens). Stamp the ones you've used with ctx_reduce now.\n</system-reminder>",
		buildChannel1Reminder(LevelUrgent, 50_000, 4, nil, true))
	require.Equal(t, "<system-reminder>\nYour next step: call ctx_reduce on the outputs you've already used (9 spent tool outputs (~80k tokens)). Then continue your task.\noldest reclaimable: §3§ view · §5§ tool.\n</system-reminder>",
		buildChannel2Reminder(80_000, 9, hint))

	out := "result" + buildChannel1Reminder(LevelFirm, 40_000, 1, nil, false) + buildChannel1Reminder(LevelFirm, 40_000, 1, nil, true)
	require.Equal(t, "result", StripChannel1ReminderSpans(out))
	require.Equal(t, "x\n</system-reminder>", StripChannel1ReminderSpans("x\n</system-reminder>"))
}

func TestToolOutputNudgeFlow(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")

	// No baseline yet: ctx_reduce is off for this session.
	require.Empty(t, e.ToolOutputNudge(ctx, "s1", "view", "x"))

	e.refreshNudges(ctx, "s1", PassStats{TailTokens: 100_000, Reclaimable: 30_000, ReclaimableCount: 3, Oldest: []TagRef{{Number: 2, Tool: "view"}}}, 1)
	got := e.ToolOutputNudge(ctx, "s1", "view", "some output")
	require.Contains(t, got, "Housekeeping: 3 spent tool outputs (~30k tokens)")
	require.Contains(t, got, "§2§ view")

	// Already nudged output is skipped; same band does not refire.
	require.Empty(t, e.ToolOutputNudge(ctx, "s1", "view", "x"+got))
	require.Empty(t, e.ToolOutputNudge(ctx, "s1", "view", "more"))

	d, err := e.Store.nudgeState(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, LevelGentle, d.ch1.Level)
	require.Equal(t, 1, *d.ch1.Ordinal)

	// ctx_reduce opens post-reduce grace and holds until the next refresh.
	require.Empty(t, e.ToolOutputNudge(ctx, "s1", CtxReduceToolName, "ok"))
	require.Empty(t, e.ToolOutputNudge(ctx, "s1", "view", strings.Repeat("y", 100)))
	d, _ = e.Store.nudgeState(ctx, "s1")
	require.True(t, d.ch1.GracePending)
	require.Equal(t, LevelGentle, *d.ch1.GracePreLevel)

	e.refreshNudges(ctx, "s1", PassStats{TailTokens: 100_000, Reclaimable: 5_000}, 2)
	d, _ = e.Store.nudgeState(ctx, "s1")
	require.False(t, d.ch1.GracePending)
	require.Equal(t, 5_000, *d.ch1.GraceBaselineU)
}

func TestChannel2Lease(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")
	_, ok := e.TakeChannel2(ctx, "s1")
	require.False(t, ok)

	e.refreshNudges(ctx, "s1", PassStats{TailTokens: 100_000, Reclaimable: 80_000, ReclaimableCount: 7}, 1)
	d, _ := e.Store.nudgeState(ctx, "s1")
	require.Equal(t, ch2Pending, d.ch2)

	text, ok := e.TakeChannel2(ctx, "s1")
	require.True(t, ok)
	require.Contains(t, text, "Your next step: call ctx_reduce on the outputs you've already used (7 spent tool outputs (~80k tokens))")
	_, ok = e.TakeChannel2(ctx, "s1")
	require.False(t, ok)

	// Delivered stays delivered while the output is still large...
	e.refreshNudges(ctx, "s1", PassStats{TailTokens: 100_000, Reclaimable: 79_000}, 1)
	d, _ = e.Store.nudgeState(ctx, "s1")
	require.Equal(t, ch2Delivered, d.ch2)
	// ...and rearms after it collapses.
	e.refreshNudges(ctx, "s1", PassStats{TailTokens: 100_000, Reclaimable: 10_000}, 1)
	d, _ = e.Store.nudgeState(ctx, "s1")
	require.Equal(t, ch2Empty, d.ch2)

	// A pending lease that no longer holds is withdrawn on take.
	e.refreshNudges(ctx, "s1", PassStats{TailTokens: 100_000, Reclaimable: 80_000}, 1)
	e.mu.Lock()
	e.nudges["s1"].turnDeltaT = 100_000
	e.mu.Unlock()
	_, ok = e.TakeChannel2(ctx, "s1")
	require.False(t, ok)
	d, _ = e.Store.nudgeState(ctx, "s1")
	require.Equal(t, ch2Empty, d.ch2)
}
