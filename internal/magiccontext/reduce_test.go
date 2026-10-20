// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for tools/ctx-reduce/tools.ts.

package magiccontext

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func reduceSetup(t *testing.T) (*Engine, ReduceToolDeps, []Tag) {
	t.Helper()
	ctx := context.Background()
	e := testEngine(t, "s1")
	e.Transform(ctx, TransformInput{SessionID: "s1", Messages: convo("s1", 3, uniquePath), ContextLimit: 100000, InputTokens: 10000, Reduce: true})
	tags, err := e.Store.LoadTags(ctx, "s1")
	require.NoError(t, err)
	d := ReduceToolDeps{Store: e.Store, SessionID: func(context.Context) string { return "s1" }, Now: e.Now}
	return e, d, tags
}

func toolTag(t *testing.T, tags []Tag, callID string) Tag {
	t.Helper()
	for _, tg := range tags {
		if tg.ToolCallID == callID {
			return tg
		}
	}
	t.Fatalf("no tag for %s", callID)
	return Tag{}
}

func TestCtxReduceReplyFormat(t *testing.T) {
	require.Equal(t, "1 tag stamped.", FormatCtxReduceReply(1, 0))
	require.Equal(t, "2 tags stamped.", FormatCtxReduceReply(2, 0))
	require.Equal(t, "2 tags stamped. 1 was a ctx_reduce call; leave those alone, they are cleaned up automatically.", FormatCtxReduceReply(2, 1))
	require.Equal(t, "3 were ctx_reduce calls; leave those alone, they are cleaned up automatically.", FormatCtxReduceReply(0, 3))
	require.Equal(t, "", FormatCtxReduceReply(0, 0))
}

func TestCtxReduceErrors(t *testing.T) {
	ctx := context.Background()
	_, d, _ := reduceSetup(t)
	require.Equal(t, "Error: 'drop' must be provided.", RunCtxReduce(ctx, d, CtxReduceParams{}, `{}`))
	require.Contains(t, RunCtxReduce(ctx, d, CtxReduceParams{Drop: "a-b"}, ""), "Error: Invalid range syntax. ")
	require.Equal(t, "Error: Unknown tag(s) §50§, §51§. Check available tags in conversation.", RunCtxReduce(ctx, d, CtxReduceParams{Drop: "50,51"}, ""))
}

func TestCtxReduceQueuesAndDedups(t *testing.T) {
	ctx := context.Background()
	e, d, tags := reduceSetup(t)
	require.NoError(t, e.Store.SetMeta(ctx, "s1", map[string]any{"last_input_tokens": 12345}))
	t1 := toolTag(t, tags, "call-1")
	t2 := toolTag(t, tags, "call-2")
	drop := formatNums(t1.Number, t2.Number)

	require.Equal(t, "2 tags stamped.", RunCtxReduce(ctx, d, CtxReduceParams{Drop: drop}, ""))
	ops, err := e.Store.PendingOps(ctx, "s1")
	require.NoError(t, err)
	require.Len(t, ops, 2)
	m, err := e.Store.Meta(ctx, "s1")
	require.NoError(t, err)
	require.EqualValues(t, 12345, m.LastNudgeTokens)

	// Re-stamping counts the tags but queues nothing new.
	require.Equal(t, "2 tags stamped.", RunCtxReduce(ctx, d, CtxReduceParams{Drop: drop}, ""))
	ops, _ = e.Store.PendingOps(ctx, "s1")
	require.Len(t, ops, 2)
}

func TestCtxReduceCompactedConflict(t *testing.T) {
	ctx := context.Background()
	e, d, tags := reduceSetup(t)
	t1 := toolTag(t, tags, "call-1")
	_, err := e.Store.DB().ExecContext(ctx, `UPDATE mc_tags SET status = 'compacted' WHERE id = ?`, t1.ID)
	require.NoError(t, err)
	got := RunCtxReduce(ctx, d, CtxReduceParams{Drop: formatNums(t1.Number)}, "")
	require.Equal(t, "Error: Conflicting operations — §"+itoa(t1.Number)+"§ is from before compaction.", got)
}

func TestCtxReduceSelfStampsAndDropped(t *testing.T) {
	ctx := context.Background()
	e, d, tags := reduceSetup(t)
	t1 := toolTag(t, tags, "call-1")
	t2 := toolTag(t, tags, "call-2")
	_, err := e.Store.DB().ExecContext(ctx, `UPDATE mc_tags SET tool_name = 'ctx_reduce' WHERE id = ?`, t2.ID)
	require.NoError(t, err)
	require.NoError(t, e.Store.Immediate(ctx, func(q querier) error {
		return e.Store.MarkTagsDropped(ctx, q, "s1", map[int64]string{t1.ID: DropModeFull})
	}))
	got := RunCtxReduce(ctx, d, CtxReduceParams{Drop: formatNums(t1.Number, t2.Number)}, "")
	require.Equal(t, "1 tag stamped. 1 was a ctx_reduce call; leave those alone, they are cleaned up automatically.", got)
	ops, _ := e.Store.PendingOps(ctx, "s1")
	require.Empty(t, ops)
}

func TestCtxReduceUnwrapsImitatedArgs(t *testing.T) {
	ctx := context.Background()
	e, d, tags := reduceSetup(t)
	t1 := toolTag(t, tags, "call-1")
	inner, _ := json.Marshal(map[string]string{"drop": formatNums(t1.Number)})
	raw, _ := json.Marshal(map[string]any{"reduced": true, "summary": string(inner)})
	require.Equal(t, "1 tag stamped.", RunCtxReduce(ctx, d, CtxReduceParams{}, string(raw)))
	ops, _ := e.Store.PendingOps(ctx, "s1")
	require.Len(t, ops, 1)

	// Unknown inner keys are not unwrapped.
	bad, _ := json.Marshal(map[string]any{"reduced": true, "summary": `{"drop":"1","x":1}`})
	require.Equal(t, "Error: 'drop' must be provided.", RunCtxReduce(ctx, d, CtxReduceParams{}, string(bad)))
}

func TestCtxReduceToolSchemaHidesImitatedFields(t *testing.T) {
	tool := NewCtxReduceTool(ReduceToolDeps{})
	b, err := json.Marshal(tool.Info())
	require.NoError(t, err)
	require.Contains(t, string(b), `"drop"`)
	require.NotContains(t, string(b), `"reduced"`)
	require.NotContains(t, string(b), `"summary"`)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func formatNums(ns ...int) string {
	s := ""
	for i, n := range ns {
		if i > 0 {
			s += ","
		}
		s += itoa(n)
	}
	return s
}
