// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for command-handler.ts, execute-status.ts, execute-flush.ts, wrapup-orchestrator.ts, recomp runners.

package magiccontext

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

var fakeRangeRe = regexp.MustCompile(`Messages (\d+)-(\d+):\n`)

// turnHistorian answers every historian prompt with one compartment per
// 3-message turn of the requested range.
func turnHistorian(calls *int) Generator {
	var mu sync.Mutex
	return func(_ context.Context, _, user string, _ int) (string, error) {
		mu.Lock()
		*calls++
		mu.Unlock()
		m := fakeRangeRe.FindAllStringSubmatch(user, -1)
		if m == nil {
			return "", fmt.Errorf("no range in prompt")
		}
		last := m[len(m)-1]
		s, _ := strconv.Atoi(last[1])
		e, _ := strconv.Atoi(last[2])
		var b strings.Builder
		b.WriteString("<output>\n<compartments>\n")
		for st := s; st <= e; st += 3 {
			en := min(st+2, e)
			fmt.Fprintf(&b, "<compartment start=\"%d\" end=\"%d\" title=\"turn %d\" importance=\"5\">\n<p1>\nRead file %d and summarized it in detail.\n</p1>\n<p2>\nRead file %d.\n</p2>\n<p3>\nFile read.\n</p3>\n<p4/>\n</compartment>\n", st, en, st, st, st)
		}
		b.WriteString("</compartments>\n</output>")
		return b.String(), nil
	}
}

type notices struct {
	mu  sync.Mutex
	all []string
}

func (n *notices) add(s string) { n.mu.Lock(); n.all = append(n.all, s); n.mu.Unlock() }

func (n *notices) last() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.all) == 0 {
		return ""
	}
	return n.all[len(n.all)-1]
}

func cmdEngine(t *testing.T, sid string, compaction bool) (*Engine, *int) {
	t.Helper()
	e := testEngine(t, sid)
	e.syncCommands = true
	e.Logf = t.Logf
	calls := 0
	if compaction {
		e.Historian = &Historian{
			Store: e.Store, Settings: e.Settings, Generate: turnHistorian(&calls), ContextWindow: 200000,
			Sleep: func(context.Context, time.Duration) error { return nil },
			Logf:  t.Logf,
		}
	}
	return e, &calls
}

func run(e *Engine, n *notices, sid, name, args string, msgs []message.Message) string {
	return e.RunCommand(context.Background(), CommandInput{
		Name: name, Args: args, SessionID: sid, Messages: msgs, ContextLimit: 100000, Notify: n.add,
	})
}

func TestCommandDisabledAndUnknown(t *testing.T) {
	e := &Engine{}
	require.Contains(t, e.RunCommand(context.Background(), CommandInput{Name: CmdStatus}), "Magic Context is disabled")
	e2, _ := cmdEngine(t, "s1", false)
	require.Contains(t, run(e2, &notices{}, "s1", "ctx-nope", "", nil), "Unknown Magic Context command")
	require.True(t, IsCommand("/ctx-recomp"))
	require.False(t, IsCommand("compact"))
}

func TestStatusSummaryWithoutCompaction(t *testing.T) {
	e, _ := cmdEngine(t, "s1", false)
	msgs := convo("s1", 3, uniquePath)
	require.NoError(t, e.Store.SetMeta(context.Background(), "s1", map[string]any{"last_input_tokens": 25000}))
	out := run(e, &notices{}, "s1", CmdStatus, "", msgs)
	require.True(t, strings.HasPrefix(out, "## Magic Context Status\n\n"), out)
	require.Contains(t, out, "- **Context:** 25.0% of usable context (25,000 / 100,000 tokens)")
	require.Contains(t, out, "- **Automatic compression:** Off")
	require.Contains(t, out, "- **History compression:** Off")
	require.Contains(t, out, "- **Reclaimable:** none")
	require.Contains(t, out, "- **Memory:** 0 memories · 0 notes")
	require.Contains(t, out, "- **Search indexing:** Off")
	require.NotContains(t, out, "## Magic Status")

	require.Equal(t, "Usage: `/ctx-status [diagnostics]`.", run(e, &notices{}, "s1", CmdStatus, "bogus", msgs))
}

func TestStatusWithCompactionAndDiagnostics(t *testing.T) {
	ctx := context.Background()
	e, _ := cmdEngine(t, "s1", true)
	msgs := convo("s1", 3, uniquePath)
	e.Transform(ctx, TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 10000, Reduce: true})
	drop := queueDrop(t, e, "s1", "call-1")
	addComp(t, e.Store, "s1", 1, "alpha", "t1")

	out := run(e, &notices{}, "s1", CmdStatus, "DIAGNOSTICS", msgs)
	require.Contains(t, out, "- **History compression:** Ready · 1 history block")
	require.Regexp(t, `- \*\*Automatic compression:\*\* at \d+\.\d% of usable context`, out)
	require.Contains(t, out, "## Magic Status\n\n**Session:** s1\n**Tag counter:** 9")
	require.Contains(t, out, "### Pending Queue\n- Drops: 1\n- Total queued: 1")
	require.Contains(t, out, "- Compartments: 1")
	require.Contains(t, out, fmt.Sprintf("### Queued Operations\n- §%d§ → drop", drop.Number))
	require.Contains(t, out, "- Last response: never")
}

func TestFlush(t *testing.T) {
	ctx := context.Background()
	e, _ := cmdEngine(t, "s1", false)
	msgs := convo("s1", 3, uniquePath)
	require.Equal(t, "No pending operations to flush.", run(e, &notices{}, "s1", CmdFlush, "", msgs))
	e.Transform(ctx, TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 10000, Reduce: true})
	tg := queueDrop(t, e, "s1", "call-2")
	before, _ := e.Store.Meta(ctx, "s1")
	require.Equal(t, "Flushed: 1 dropped. Changes take effect on next message.", run(e, &notices{}, "s1", CmdFlush, "", msgs))
	got, ok, err := e.Store.TagByNumber(ctx, "s1", tg.Number)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, TagDropped, got.Status)
	ops, _ := e.Store.PendingOps(ctx, "s1")
	require.Empty(t, ops)
	after, _ := e.Store.Meta(ctx, "s1")
	require.Greater(t, after.RenderEpoch, before.RenderEpoch)
}

func TestCompactionCommandsRefuseWithoutHistorian(t *testing.T) {
	e, _ := cmdEngine(t, "s1", false)
	for _, c := range []string{CmdRecomp, CmdWrapup} {
		require.Equal(t, noCompactionReply(c), run(e, &notices{}, "s1", c, "", nil))
	}
}

func TestRecompArgValidation(t *testing.T) {
	e, _ := cmdEngine(t, "s1", true)
	n := &notices{}
	out := run(e, n, "s1", CmdRecomp, "abc", nil)
	require.True(t, strings.HasPrefix(out, "## Magic Recomp — Invalid Arguments\n\nInvalid /ctx-recomp arguments: `abc`.\n\nUsage:"), out)
	require.Equal(t, "## Magic Recomp — Invalid Arguments\n\nStart must be >= 1 (got 0).", run(e, n, "s1", CmdRecomp, "0-5", nil))
	require.Equal(t, "## Magic Recomp — Invalid Arguments\n\nEnd must be >= start (got 9-5).", run(e, n, "s1", CmdRecomp, "9 - 5", nil))
	require.Contains(t, run(e, n, "s1", CmdRecomp, "--upgrade", nil), "Nothing to upgrade")
}

func TestFullRecompConfirmAndRun(t *testing.T) {
	ctx := context.Background()
	e, calls := cmdEngine(t, "s1", true)
	msgs := convo("s1", 12, uniquePath)
	n := &notices{}

	first := run(e, n, "s1", CmdRecomp, "", msgs)
	require.Contains(t, first, "## ⚠️ Recomp Confirmation Required")
	require.Contains(t, first, "You currently have **0** compartments.")
	require.Zero(t, *calls)

	// A different key resets the confirmation instead of running.
	require.Contains(t, run(e, n, "s1", CmdRecomp, "1-3", msgs), "No compartments exist yet")
	require.Contains(t, run(e, n, "s1", CmdRecomp, "", msgs), "Confirmation Required")

	require.Equal(t, "", run(e, n, "s1", CmdRecomp, "", msgs))
	final := n.last()
	require.True(t, strings.HasPrefix(final, "## Magic Recomp — Complete\n\n"), final)
	require.Contains(t, final, "Covered raw history 1-")
	require.Positive(t, *calls)

	cs, err := e.Store.Compartments(ctx, "s1")
	require.NoError(t, err)
	require.NotEmpty(t, cs)
	require.Equal(t, 1, cs[0].StartOrdinal)
	staged, _ := e.Store.RecompCompartments(ctx, "s1")
	require.Empty(t, staged)
	meta, _ := e.Store.Meta(ctx, "s1")
	require.NotEmpty(t, meta.MaterializePending)
	require.False(t, meta.CompartmentInProgress)
	require.Empty(t, e.commandRunning("s1"))
}

func TestRecompConfirmExpires(t *testing.T) {
	e, calls := cmdEngine(t, "s1", true)
	now := time.Unix(1_800_000_000, 0)
	e.Now = func() time.Time { return now }
	msgs := convo("s1", 6, uniquePath)
	n := &notices{}
	require.Contains(t, run(e, n, "s1", CmdRecomp, "", msgs), "Confirmation Required")
	now = now.Add(61 * time.Second)
	require.Contains(t, run(e, n, "s1", CmdRecomp, "", msgs), "Confirmation Required")
	require.Zero(t, *calls)
}

func TestPartialRecomp(t *testing.T) {
	ctx := context.Background()
	e, _ := cmdEngine(t, "s1", true)
	msgs := convo("s1", 12, uniquePath)
	n := &notices{}
	run(e, n, "s1", CmdRecomp, "", msgs)
	run(e, n, "s1", CmdRecomp, "", msgs)
	live, err := e.Store.Compartments(ctx, "s1")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(live), 3)

	confirm := run(e, n, "s1", CmdRecomp, "5-6", msgs)
	require.Contains(t, confirm, "## ⚠️ Partial Recomp Confirmation Required")
	require.Contains(t, confirm, "Snapped to compartment boundaries: **messages 4-6**")
	require.Contains(t, confirm, "This will **rebuild 1 compartment(s)** in the snapped range.")

	require.Equal(t, "", run(e, n, "s1", CmdRecomp, "5-6", msgs))
	final := n.last()
	require.True(t, strings.HasPrefix(final, "## Magic Recomp — Partial Complete\n\n"), final)
	require.Contains(t, final, "Rebuilt compartments covering messages 4-6 using 1 historian pass(es).")
	require.Contains(t, final, fmt.Sprintf("Preserved 1 prior compartment(s) and %d tail compartment(s) unchanged.", len(live)-2))
	after, _ := e.Store.Compartments(ctx, "s1")
	require.Len(t, after, len(live))
	for i, c := range after {
		require.Equal(t, i, c.Sequence)
	}
	meta, _ := e.Store.Meta(ctx, "s1")
	require.Empty(t, meta.RecompPartialRange)
}

func TestPartialRecompStagingConflicts(t *testing.T) {
	ctx := context.Background()
	e, _ := cmdEngine(t, "s1", true)
	msgs := convo("s1", 12, uniquePath)
	n := &notices{}
	run(e, n, "s1", CmdRecomp, "", msgs)
	run(e, n, "s1", CmdRecomp, "", msgs)

	// Leave an unfinished partial staged for 1-3.
	_, err := e.Store.InsertCompartments(ctx, e.Store.db, "s1", true, []Compartment{{Sequence: 0, StartOrdinal: 1, EndOrdinal: 3, Title: "x", Content: "y"}})
	require.NoError(t, err)
	require.NoError(t, e.Store.SetMeta(ctx, "s1", map[string]any{"recomp_partial_range": "1-3"}))
	run(e, n, "s1", CmdRecomp, "4-6", msgs)
	run(e, n, "s1", CmdRecomp, "4-6", msgs)
	require.Contains(t, n.last(), "An unfinished partial recomp is already staged for range 1-3, which does not match the requested range 4-6.")

	// /ctx-flush cancels the staging.
	require.Equal(t, "No pending operations to flush.\nCancelled the unfinished staged recomp.", run(e, n, "s1", CmdFlush, "", msgs))
	staged, _ := e.Store.RecompCompartments(ctx, "s1")
	require.Empty(t, staged)
}

func TestSnapRange(t *testing.T) {
	cs := []Compartment{
		{Sequence: 0, StartOrdinal: 1, EndOrdinal: 10},
		{Sequence: 1, StartOrdinal: 11, EndOrdinal: 20},
		{Sequence: 2, StartOrdinal: 21, EndOrdinal: 30},
	}
	s, err := snapRange(cs, 12, 22)
	require.NoError(t, err)
	require.Equal(t, 11, s.start)
	require.Equal(t, 30, s.end)
	require.Len(t, s.prior, 1)
	require.Len(t, s.inRange, 2)
	require.Empty(t, s.tail)
	_, err = snapRange(cs, 31, 40)
	require.EqualError(t, err, "Range 31-40 starts after the last compartment (which ends at message 30). Nothing to rebuild.")
	_, err = snapRange(nil, 1, 2)
	require.ErrorContains(t, err, "No compartments exist yet")
}

func TestWrapup(t *testing.T) {
	ctx := context.Background()
	e, calls := cmdEngine(t, "s1", true)
	msgs := convo("s1", 12, uniquePath)
	n := &notices{}

	require.Equal(t, "## Magic Wrapup — Invalid Arguments\n\nmessages_to_keep must be a positive integer.", run(e, n, "s1", CmdWrapup, "-2", msgs))
	require.Equal(t, "## Magic Wrapup — Invalid Arguments\n\n"+wrapupUsage, run(e, n, "s1", CmdWrapup, "1 2", msgs))
	require.Equal(t, "Nothing to wrap up — only 36 messages above the last compartment.", run(e, n, "s1", CmdWrapup, "40", msgs))
	require.Zero(t, *calls)

	require.Equal(t, "", run(e, n, "s1", CmdWrapup, "6", msgs))
	final := n.last()
	require.True(t, strings.HasPrefix(final, "## Magic Wrapup\n\nWrapped up "), final)
	cs, err := e.Store.Compartments(ctx, "s1")
	require.NoError(t, err)
	require.NotEmpty(t, cs)
	require.LessOrEqual(t, lastEndOf(cs), 36-6)
	require.Empty(t, e.commandRunning("s1"))
}

func TestDreamCommand(t *testing.T) {
	e, _ := cmdEngine(t, "s1", false)
	n := &notices{}
	require.Equal(t, "## /ctx-dream\n\nDreaming is not configured for this project.", run(e, n, "s1", CmdDream, "", nil))
	e.Settings.Dreamer = false
	require.Contains(t, run(e, n, "s1", CmdDream, "", nil), "not configured")
	e.Settings.Dreamer = true
	e.Dreamer = fakeGen("", nil)
	out := run(e, n, "s1", CmdDream, "nope", nil)
	require.True(t, strings.HasPrefix(out, `## /ctx-dream`+"\n\n"+`Unknown task "nope". Valid tasks: `), out)
}

func TestSummarizeDream(t *testing.T) {
	out := summarizeDream(DreamReport{Tasks: []DreamTaskResult{
		{Task: "consolidate", Status: DreamStatusOK, Detail: "merged 2"},
		{Task: "verify", Status: DreamStatusFailed, Err: "boom"},
		{Task: "evaluate-smart-notes", Status: DreamStatusSkipped, Detail: "no notes"},
		{Task: "archive-stale", Status: DreamStatusSkipped, Detail: "lease held by another runner"},
	}})
	require.Equal(t, strings.Join([]string{
		"## /ctx-dream", "",
		"Ran: consolidate", "Details:", "- consolidate: merged 2",
		"Failed: verify", "Failure details:", "- verify: boom",
		"Skipped: evaluate-smart-notes (no notes)",
		"Busy: archive-stale — another dream task holds this domain's lease; retry in a minute",
	}, "\n"), out)
	require.Contains(t, summarizeDream(DreamReport{}), "No enabled dream tasks to run.")
}

func TestFmtHelpers(t *testing.T) {
	require.Equal(t, "1,234,567", fmtInt(1234567))
	require.Equal(t, "999", fmtInt(999))
	require.Equal(t, "512B", fmtBytes(512))
	require.Equal(t, "1.5KB", fmtBytes(1536))
	require.Equal(t, "5m", fmtTTL(5*time.Minute))
	require.Equal(t, "90s", fmtTTL(90*time.Second))
}
