// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for transform.ts, apply-operations.ts, emergency-drop.ts.

package magiccontext

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func testEngine(t *testing.T, sid string) *Engine {
	t.Helper()
	s := p3Store(t, sid)
	set := SettingsFromConfig(&config.MagicContextOptions{Enabled: true})
	set.AutoSearch = false
	now := time.Unix(1_800_000_000, 0)
	return &Engine{Store: s, Settings: set, Now: func() time.Time { return now }}
}

// convo builds n turns of user → assistant(view call) → tool result.
// Tool outputs are ~2000 tokens each.
func convo(sid string, n int, path func(i int) string) []message.Message {
	var out []message.Message
	for i := 1; i <= n; i++ {
		ts := int64(1_700_000_000 + i*10)
		call := fmt.Sprintf("call-%d", i)
		input := fmt.Sprintf(`{"file_path":%q}`, path(i))
		out = append(out,
			message.Message{
				ID: fmt.Sprintf("u%d", i), SessionID: sid, Role: message.User, CreatedAt: ts,
				Parts: []message.ContentPart{message.TextContent{Text: fmt.Sprintf("please look at file %d", i)}},
			},
			message.Message{
				ID: fmt.Sprintf("a%d", i), SessionID: sid, Role: message.Assistant, CreatedAt: ts + 1,
				Parts: []message.ContentPart{
					message.TextContent{Text: fmt.Sprintf("reading %d", i)},
					message.ToolCall{ID: call, Name: "view", Input: input, Finished: true},
				},
			},
			message.Message{
				ID: fmt.Sprintf("t%d", i), SessionID: sid, Role: message.Tool, CreatedAt: ts + 2,
				Parts: []message.ContentPart{message.ToolResult{ToolCallID: call, Name: "view", Content: strings.Repeat("x", 7000)}},
			},
		)
	}
	return out
}

func uniquePath(i int) string { return fmt.Sprintf("f%d.go", i) }

func findResult(msgs []message.Message, callID string) (message.ToolResult, bool) {
	for _, m := range msgs {
		for _, r := range m.ToolResults() {
			if r.ToolCallID == callID {
				return r, true
			}
		}
	}
	return message.ToolResult{}, false
}

func TestTransformDisabledPassThrough(t *testing.T) {
	e := &Engine{}
	msgs := convo("s1", 2, uniquePath)
	r := e.Transform(context.Background(), TransformInput{SessionID: "s1", Messages: msgs})
	require.False(t, r.Applied)
	require.Equal(t, msgs, r.Messages)
}

func TestTransformTagsAndHistory(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")
	msgs := convo("s1", 3, uniquePath)
	r := e.Transform(ctx, TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 10000, Reduce: true})
	require.True(t, r.Applied)
	require.Len(t, r.Messages, len(msgs)+2)
	require.Equal(t, M0MessageID, r.Messages[0].ID)
	require.Equal(t, M0EmptyBody, r.Messages[0].Content().Text)
	require.Equal(t, M1MessageID, r.Messages[1].ID)

	// Every user/assistant text and tool result gets a §N§ tag.
	require.True(t, strings.HasPrefix(r.Messages[2].Content().Text, "§1§ "), r.Messages[2].Content().Text)
	res, ok := findResult(r.Messages, "call-1")
	require.True(t, ok)
	require.Regexp(t, `^§\d+§ x`, res.Content)

	// The input is untouched.
	require.Equal(t, "please look at file 1", msgs[0].Content().Text)

	// Without Reduce no tags are shown, and tagging is stable.
	r2 := e.Transform(ctx, TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 10000})
	require.Equal(t, "please look at file 1", r2.Messages[2].Content().Text)
	tags, err := e.Store.LoadTags(ctx, "s1")
	require.NoError(t, err)
	require.Len(t, tags, 9)
}

func queueDrop(t *testing.T, e *Engine, sid, callID string) Tag {
	t.Helper()
	ctx := context.Background()
	tags, err := e.Store.LoadTags(ctx, sid)
	require.NoError(t, err)
	for _, tg := range tags {
		if tg.ToolCallID == callID {
			require.NoError(t, e.Store.Immediate(ctx, func(q querier) error {
				return e.Store.QueuePendingOp(ctx, q, sid, tg.ID, "drop", 1)
			}))
			return tg
		}
	}
	t.Fatalf("no tag for %s", callID)
	return Tag{}
}

func TestTransformPendingDropsWaitForThreshold(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")
	msgs := convo("s1", 10, uniquePath)
	in := TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 30000, Reduce: true}
	e.Transform(ctx, in)
	old := queueDrop(t, e, "s1", "call-1")
	recent := queueDrop(t, e, "s1", "call-10")

	// Below the execute threshold the drop stays queued.
	r := e.Transform(ctx, in)
	require.False(t, r.Executed)
	res, _ := findResult(r.Messages, "call-1")
	require.NotEqual(t, TagPlaceholder(old.Number), res.Content)

	// At 70% the old drop lands (a recent small call → skeleton); the
	// protected tail keeps call-10.
	in.InputTokens = 70000
	r = e.Transform(ctx, in)
	require.True(t, r.Executed)
	require.Equal(t, 1, r.Dropped)
	res, ok := findResult(r.Messages, "call-1")
	require.True(t, ok)
	require.Equal(t, TagPlaceholder(old.Number), res.Content)
	res, ok = findResult(r.Messages, "call-10")
	require.True(t, ok)
	require.NotEqual(t, TagPlaceholder(recent.Number), res.Content)

	// Drops replay identically even below the threshold.
	in.InputTokens = 10000
	r2 := e.Transform(ctx, in)
	res, _ = findResult(r2.Messages, "call-1")
	require.Equal(t, TagPlaceholder(old.Number), res.Content)
}

func TestTransformSkeletonDrop(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")
	msgs := convo("s1", 30, uniquePath)
	in := TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 70000}
	e.Transform(ctx, in)
	// call-1 is beyond the 20 newest tool tags → full; call-15 → skeleton.
	queueDrop(t, e, "s1", "call-1")
	mid := queueDrop(t, e, "s1", "call-15")
	r := e.Transform(ctx, in)
	require.Equal(t, 2, r.Dropped)
	_, ok := findResult(r.Messages, "call-1")
	require.False(t, ok)
	res, ok := findResult(r.Messages, "call-15")
	require.True(t, ok)
	require.Equal(t, TagPlaceholder(mid.Number), res.Content)
}

func TestTransformDedupReadOnlyTools(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")
	msgs := convo("s1", 8, func(int) string { return "same.go" })
	r := e.Transform(ctx, TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 70000})
	require.True(t, r.Executed)
	require.Positive(t, r.Dropped)
	res, ok := findResult(r.Messages, "call-1")
	require.True(t, ok)
	require.True(t, strings.HasPrefix(res.Content, "[dropped §"), res.Content)
	res, ok = findResult(r.Messages, "call-8")
	require.False(t, strings.HasPrefix(res.Content, "[dropped"))
	require.True(t, ok, "newest copy stays")
}

func TestTransformEmergencyDropAndLatch(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")
	msgs := convo("s1", 30, uniquePath)
	in := TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 88000}
	r := e.Transform(ctx, in)
	require.Positive(t, r.Emergency)
	_, ok := findResult(r.Messages, "call-1")
	require.False(t, ok, "oldest view output goes first")
	_, ok = findResult(r.Messages, "call-30")
	require.True(t, ok, "protected tail stays")
	meta, err := e.Store.Meta(ctx, "s1")
	require.NoError(t, err)
	require.True(t, meta.EmergencyLatched)

	// Latched: the next force-band pass does not drop again.
	r = e.Transform(ctx, in)
	require.Zero(t, r.Emergency)

	// Leaving the band rearms the latch.
	in.InputTokens = 50000
	e.Transform(ctx, in)
	meta, err = e.Store.Meta(ctx, "s1")
	require.NoError(t, err)
	require.False(t, meta.EmergencyLatched)
}

func TestEmergencyTiers(t *testing.T) {
	require.Equal(t, 1, emergencyTier("mcp_read"))
	require.Equal(t, 1, emergencyTier("View"))
	require.Equal(t, 2, emergencyTier("grep"))
	require.Equal(t, 3, emergencyTier("bash"))
}

func TestTransformStripsStaleReduceCalls(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")
	msgs := convo("s1", 12, uniquePath)
	var withReduce []message.Message
	for i, m := range msgs {
		withReduce = append(withReduce, m)
		if i < 5*3 && m.Role == message.Tool && i/3 < 5 {
			n := i / 3
			id := fmt.Sprintf("reduce-%d", n)
			withReduce = append(withReduce,
				message.Message{
					ID: fmt.Sprintf("ra%d", n), SessionID: "s1", Role: message.Assistant, CreatedAt: m.CreatedAt,
					Parts: []message.ContentPart{message.ToolCall{ID: id, Name: "ctx_reduce", Input: `{"drop":"1"}`, Finished: true}},
				},
				message.Message{
					ID: fmt.Sprintf("rt%d", n), SessionID: "s1", Role: message.Tool, CreatedAt: m.CreatedAt,
					Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Name: "ctx_reduce", Content: "1 tag(s) stamped."}},
				},
			)
		}
	}
	r := e.Transform(ctx, TransformInput{SessionID: "s1", Messages: withReduce, ContextLimit: 100000, InputTokens: 70000})
	_, ok := findResult(r.Messages, "reduce-0")
	require.False(t, ok)
	_, ok = findResult(r.Messages, "reduce-1")
	require.False(t, ok)
	_, ok = findResult(r.Messages, "reduce-4")
	require.True(t, ok, "newest 3 ctx_reduce calls are kept")
	for _, m := range r.Messages {
		require.NotEmpty(t, m.Parts)
	}
}

func TestTransformCutsAtCompartmentBoundary(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")
	msgs := convo("s1", 6, uniquePath)
	e.Transform(ctx, TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 10000})
	ords, err := e.Store.EnsureOrdinals(ctx, "s1", []string{"t3"})
	require.NoError(t, err)
	_, err = e.Store.InsertCompartments(ctx, e.Store.DB(), "s1", false, []Compartment{{
		Sequence: 1, StartOrdinal: 1, EndOrdinal: ords["t3"], StartMessageID: "u1", EndMessageID: "t3",
		Title: "first files", Content: "Looked at files one to three.",
	}})
	require.NoError(t, err)
	// A quiet pass replays the cached (empty) history byte-identically.
	r := e.Transform(ctx, TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 10000})
	require.Equal(t, M0EmptyBody, r.Messages[0].Content().Text)
	require.Equal(t, "u1", r.Messages[2].ID)
	// A cache-busting pass brings the compartment in and cuts the prompt.
	r = e.Transform(ctx, TransformInput{SessionID: "s1", Messages: msgs, ContextLimit: 100000, InputTokens: 70000})
	require.Contains(t, r.Messages[0].Content().Text+r.Messages[1].Content().Text, "first files")
	require.Equal(t, "u4", r.Messages[2].ID)
}

func TestRecordResponse(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, "s1")
	require.NoError(t, e.RecordResponse(ctx, "s1", 50000, 200000))
	m, err := e.Store.Meta(ctx, "s1")
	require.NoError(t, err)
	require.EqualValues(t, 50000, m.LastInputTokens)
	require.InDelta(t, 25.0, m.LastContextPercentage, 0.01)
	require.Equal(t, e.now().UnixMilli(), m.LastResponseTime)
}
