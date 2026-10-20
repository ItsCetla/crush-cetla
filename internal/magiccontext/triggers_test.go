// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for packages/plugin/src/hooks/magic-context/compartment-trigger.ts, note-nudger.ts

package magiccontext

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// rawTurns builds n alternating user/assistant raw messages of tokens each.
func rawTurns(n, tokens int) []RawMessage {
	out := make([]RawMessage, 0, n)
	for i := 1; i <= n; i++ {
		role := message.User
		if i%2 == 0 {
			role = message.Assistant
		}
		out = append(out, RawMessage{
			Ordinal: i, ID: fmt.Sprintf("m%d", i), Role: role,
			Texts: []string{fmt.Sprintf("message %d %s", i, strings.Repeat("word ", tokens*4/5))}, Tokens: tokens,
		})
	}
	return out
}

func triggerSettings() Settings {
	return SettingsFromConfig(&config.MagicContextOptions{Enabled: true})
}

func TestResolveContextLimit(t *testing.T) {
	t.Parallel()
	require.Equal(t, 200000, ResolveContextLimit(200000, 5000, 10))
	require.Equal(t, 100000, ResolveContextLimit(0, 50000, 50))
	require.Equal(t, defaultTriggerContextLimit, ResolveContextLimit(0, 0, 0))
	require.Equal(t, defaultTriggerContextLimit, ResolveContextLimit(0, 1000, 0))
}

func TestEvaluateTriggerNothingNew(t *testing.T) {
	t.Parallel()
	set := triggerSettings()
	raw := rawTurns(40, 2000)
	d := EvaluateTrigger(TriggerInput{
		Settings: set, Window: set.WindowFor(100000), UsagePct: 99,
		Raw: raw, LastEnd: 40, ProjectedPostDropPct: -1,
	})
	require.False(t, d.Fire, "everything is already summarized")
}

func TestEvaluateTriggerQuietBelowThreshold(t *testing.T) {
	t.Parallel()
	set := triggerSettings()
	w := set.WindowFor(1_000_000)
	// A short session far below the execute threshold.
	d := EvaluateTrigger(TriggerInput{
		Settings: set, Window: w, UsagePct: 5,
		Raw: rawTurns(6, 200), ProjectedPostDropPct: -1,
	})
	require.False(t, d.Fire)
	require.Empty(t, d.Reason)
}

func TestEvaluateTriggerTailSize(t *testing.T) {
	t.Parallel()
	set := triggerSettings()
	w := set.WindowFor(200000)
	// Plenty of unsummarized history above the protected tail, low usage.
	raw := rawTurns(400, 1500)
	d := EvaluateTrigger(TriggerInput{
		Settings: set, Window: w, UsagePct: 20,
		Raw: raw, ProjectedPostDropPct: -1,
	})
	require.True(t, d.Fire)
	require.Equal(t, ReasonTailSize, d.Reason)
	require.False(t, d.Block)
	require.True(t, runnableWindow(d.Boundary))
	require.GreaterOrEqual(t, d.Boundary.Offset, 1)
}

func TestEvaluateTriggerForceBandBlocks(t *testing.T) {
	t.Parallel()
	set := triggerSettings()
	w := set.WindowFor(200000)
	raw := rawTurns(400, 1500)
	d := EvaluateTrigger(TriggerInput{
		Settings: set, Window: w, UsagePct: 96,
		Raw: raw, ProjectedPostDropPct: -1,
	})
	require.True(t, d.Fire)
	require.Equal(t, ReasonForceBand, d.Reason)
	require.True(t, d.Block, "≥95% usage waits for the historian")

	d = EvaluateTrigger(TriggerInput{
		Settings: set, Window: w, UsagePct: w.ForceMaterializationPct() + 0.5,
		Raw: raw, ProjectedPostDropPct: -1,
	})
	require.True(t, d.Fire)
	require.Equal(t, ReasonForceBand, d.Reason)
	require.Equal(t, w.ForceMaterializationPct()+0.5 >= blockUntilDonePercentage, d.Block)
}

func TestEvaluateTriggerForceBandSkippedWhenDropsSuffice(t *testing.T) {
	t.Parallel()
	set := triggerSettings()
	w := set.WindowFor(200000)
	d := EvaluateTrigger(TriggerInput{
		Settings: set, Window: w, UsagePct: 96,
		Raw: rawTurns(400, 1500), ProjectedPostDropPct: w.Exec * 0.5, DropsCanLand: true,
	})
	require.False(t, d.Fire, "queued drops already bring usage under target")
}

func TestProjectedPostDropPct(t *testing.T) {
	t.Parallel()
	require.InDelta(t, -1, ProjectedPostDropPct(80, nil, nil), 0)
	active := []Tag{
		{ID: 1, ByteSize: 600, InputByteSize: 200},
		{ID: 2, ByteSize: 100, InputByteSize: 100},
	}
	require.InDelta(t, 80, ProjectedPostDropPct(80, active, nil), 1e-9)
	require.InDelta(t, 16, ProjectedPostDropPct(80, active, map[int64]bool{1: true}), 1e-9)
	require.InDelta(t, 0, ProjectedPostDropPct(80, active, map[int64]bool{1: true, 2: true}), 1e-9)
}

func TestTodosAllDone(t *testing.T) {
	t.Parallel()
	require.False(t, todosAllDone(""))
	require.False(t, todosAllDone(`{"todos":[]}`))
	require.False(t, todosAllDone(`{"todos":[{"status":"completed"},{"status":"in_progress"}]}`))
	require.True(t, todosAllDone(`{"todos":[{"status":"completed"},{"status":"Cancelled"}]}`))
	require.True(t, todosAllDone(`{"todos":[{"status":"canceled"}]}`))
	require.False(t, todosAllDone(`not json`))
}

func assistantText(id, text string) message.Message {
	return message.Message{ID: id, Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func TestHasRecentAssistantCommit(t *testing.T) {
	t.Parallel()
	commit := "Committed the fix as a1b2c3d."
	require.True(t, TextMentionsCommit(commit))

	msgs := []message.Message{assistantText("a1", commit)}
	require.True(t, HasRecentAssistantCommit(msgs))

	// User text never counts.
	user := message.Message{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: commit}}}
	require.False(t, HasRecentAssistantCommit([]message.Message{user}))

	// A commit older than the lookback is ignored.
	old := []message.Message{assistantText("a0", commit)}
	for i := range CommitLookback {
		old = append(old, assistantText(fmt.Sprintf("a%d", i+1), "still working"))
	}
	require.False(t, HasRecentAssistantCommit(old))
}

func TestObserveCommitFiresOnEdgeOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sid := "s-commit"
	e := testEngine(t, sid)
	trigger := func() string {
		m, err := e.Store.Meta(ctx, sid)
		require.NoError(t, err)
		return m.NoteNudgeTrigger
	}
	quiet := []message.Message{assistantText("a1", "looking around")}
	withCommit := append(quiet, assistantText("a2", "Committed as deadbeef1."))

	// The first pass only sets the baseline, even with a commit visible.
	e.observeCommit(ctx, sid, withCommit, false)
	require.Empty(t, trigger())

	e.observeCommit(ctx, sid, quiet, false)
	require.Empty(t, trigger())

	// absent → present fires.
	e.observeCommit(ctx, sid, withCommit, false)
	require.True(t, strings.HasPrefix(trigger(), "commit_detected"), trigger())

	// Staying present does not re-fire after the trigger is cleared.
	require.NoError(t, e.Store.MarkNoteNudgeDelivered(ctx, sid, "", "", e.now()))
	e.observeCommit(ctx, sid, withCommit, false)
	require.Empty(t, trigger())
}

func TestObserveCommitSkipsSubagents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sid := "s-sub"
	e := testEngine(t, sid)
	quiet := []message.Message{assistantText("a1", "looking around")}
	e.observeCommit(ctx, sid, quiet, true)
	e.observeCommit(ctx, sid, append(quiet, assistantText("a2", "Committed as deadbeef1.")), true)
	m, err := e.Store.Meta(ctx, sid)
	require.NoError(t, err)
	require.Empty(t, m.NoteNudgeTrigger)
}

func TestObserveToolResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sid := "s-tools"
	e := testEngine(t, sid)
	trigger := func() string {
		m, err := e.Store.Meta(ctx, sid)
		require.NoError(t, err)
		return m.NoteNudgeTrigger
	}
	done := `{"todos":[{"status":"completed"}]}`

	e.ObserveToolResult(ctx, sid, todosToolName, `{"todos":[{"status":"pending"}]}`, false, false)
	require.Empty(t, trigger())
	e.ObserveToolResult(ctx, sid, todosToolName, done, true, false)
	require.Empty(t, trigger(), "errored tool results are ignored")
	e.ObserveToolResult(ctx, sid, todosToolName, done, false, true)
	require.Empty(t, trigger(), "subagents never arm the nudge")

	e.ObserveToolResult(ctx, sid, todosToolName, done, false, false)
	require.Equal(t, "todos_complete", trigger())

	// A ctx_note call clears the pending trigger.
	e.ObserveToolResult(ctx, sid, CtxNoteToolName, "", false, false)
	require.Empty(t, trigger())
}
