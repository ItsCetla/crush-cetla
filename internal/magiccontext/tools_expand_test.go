// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for tools/ctx-expand/tools.ts.

package magiccontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func expandSetup(t *testing.T, n int) (*Engine, ExpandToolDeps, *[]message.Message) {
	t.Helper()
	e := testEngine(t, "s1")
	msgs := convo("s1", n, uniquePath)
	d := ExpandToolDeps{
		Store:     e.Store,
		SessionID: func(context.Context) string { return "s1" },
		Messages: func(context.Context, string) ([]message.Message, error) {
			return msgs, nil
		},
	}
	return e, d, &msgs
}

func expandArgs(t *testing.T, s string) map[string]any {
	t.Helper()
	m := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

func TestParseTagInput(t *testing.T) {
	for in, want := range map[string]int{`12`: 12, `"12"`: 12, `"§12§"`: 12, `"§12"`: 12, `"tag 12"`: 12, `" [dropped §12§] "`: 12} {
		var v any
		require.NoError(t, json.Unmarshal([]byte(in), &v))
		n, err := parseTagInput(v)
		require.NoError(t, err, in)
		require.Equal(t, want, n, in)
	}
	for _, in := range []string{`0`, `-3`, `1.5`, `"0"`, `"12 13"`, `"x12"`, `"§1§§2§"`, `null`, `true`} {
		var v any
		require.NoError(t, json.Unmarshal([]byte(in), &v))
		_, err := parseTagInput(v)
		require.EqualError(t, err, TagInputError, in)
	}
}

func TestResolveCtxExpandMode(t *testing.T) {
	cases := []struct {
		in   string
		kind expandModeKind
		n    int
		err  string
	}{
		{`{"tag":5}`, expandTag, 5, ""},
		{`{"tag":"§5§","start":0,"end":0}`, expandTag, 5, ""},
		{`{"tag":5,"message":3}`, expandError, 0, "Error: use tag alone, without message or start/end."},
		{`{"tag":0,"message":3}`, expandMessage, 3, ""},
		{`{"tag":"","start":2,"end":4}`, expandRange, 0, ""},
		{`{"tag":"nope"}`, expandError, 0, TagInputError},
		{`{"message":7}`, expandMessage, 7, ""},
		{`{"message":7,"start":0,"end":0}`, expandMessage, 7, ""},
		{`{"message":7,"start":2,"end":4}`, expandRange, 0, ""},
		{`{"message":0}`, expandError, 0, "Error: message must be a positive integer."},
		{`{"message":0,"start":2,"end":4}`, expandRange, 0, ""},
		{`{"start":5,"end":2}`, expandError, 0, "Error: provide either message=<ordinal>, or start and end (positive integers, start <= end)."},
		{`{}`, expandError, 0, "Error: provide either message=<ordinal>, or start and end (positive integers, start <= end)."},
	}
	for _, c := range cases {
		m := resolveCtxExpandMode(expandArgs(t, c.in))
		require.Equal(t, c.kind, m.kind, c.in)
		require.Equal(t, c.err, m.err, c.in)
		if c.n != 0 {
			require.Equal(t, c.n, m.n, c.in)
		}
	}
	m := resolveCtxExpandMode(expandArgs(t, `{"start":2,"end":4,"verbose":true}`))
	require.True(t, m.verbose)
	require.Equal(t, 2, m.start)
	require.Equal(t, 4, m.end)
}

func TestUnwrapImitatedExpandArgs(t *testing.T) {
	ctx := context.Background()
	_, d, _ := expandSetup(t, 2)
	got := RunCtxExpand(ctx, d, `{"reduced":true,"summary":"{\"message\":1}"}`)
	require.True(t, strings.HasPrefix(got, "[1] U (user) — full recovery:"), got)
	// tag is not in the unwrap schema, so the original args stay.
	got = RunCtxExpand(ctx, d, `{"reduced":true,"summary":"{\"tag\":1}"}`)
	require.True(t, strings.HasPrefix(got, "Error: provide either"), got)
}

func TestCtxExpandMessage(t *testing.T) {
	ctx := context.Background()
	_, d, _ := expandSetup(t, 2)
	// a1 is ordinal 2: text plus the view call joined with its result.
	got := RunCtxExpand(ctx, d, `{"message":2}`)
	require.Contains(t, got, "[2] A (assistant) — full recovery:\n\n  [text]\nreading 1")
	require.Contains(t, got, `  [tool: view #call-1]`+"\n"+`  input: {"file_path":"f1.go"}`+"\n  output:\n"+strings.Repeat("x", 7000))
	// The tool-role message renders the same joined tool.
	got = RunCtxExpand(ctx, d, `{"message":3}`)
	require.Contains(t, got, "[3] tool — full recovery:")
	require.Contains(t, got, `input: {"file_path":"f1.go"}`)
	got = RunCtxExpand(ctx, d, `{"message":99}`)
	require.True(t, strings.HasPrefix(got, "No message at ordinal 99 in this session's stored history"), got)
}

func TestCtxExpandTag(t *testing.T) {
	ctx := context.Background()
	e, d, msgs := expandSetup(t, 2)
	e.Transform(ctx, TransformInput{SessionID: "s1", Messages: *msgs, ContextLimit: 100000, InputTokens: 10000})
	tags, err := e.Store.LoadTags(ctx, "s1")
	require.NoError(t, err)
	tt := toolTag(t, tags, "call-2")
	got := RunCtxExpand(ctx, d, `{"tag":"§`+itoa(tt.Number)+`§"}`)
	require.True(t, strings.HasPrefix(got, "  [tool: view #call-2]\n  input: {\"file_path\":\"f2.go\"}"), got)
	var text Tag
	for _, tg := range tags {
		if tg.Type == TagMessage && tg.MessageID == "u2" {
			text = tg
		}
	}
	require.NotZero(t, text.Number)
	require.Equal(t, "  [text]\nplease look at file 2", RunCtxExpand(ctx, d, `{"tag":`+itoa(text.Number)+`}`))
	require.Equal(t, "no tag 999 in this session; if 999 came from a <session-history> heading or a ctx_search hit, it is an ordinal: use message=999",
		RunCtxExpand(ctx, d, `{"tag":999}`))
	// Owner gone from stored history.
	*msgs = (*msgs)[:3]
	require.Equal(t, "Tag "+itoa(tt.Number)+"'s original tool owner is no longer in stored history.", RunCtxExpand(ctx, d, `{"tag":`+itoa(tt.Number)+`}`))
	require.Equal(t, "Tag "+itoa(text.Number)+"'s original text is no longer in stored history.", RunCtxExpand(ctx, d, `{"tag":`+itoa(text.Number)+`}`))
}

func TestCtxExpandRange(t *testing.T) {
	ctx := context.Background()
	_, d, _ := expandSetup(t, 3)
	got := RunCtxExpand(ctx, d, `{"start":1,"end":6}`)
	require.True(t, strings.HasPrefix(got, "Messages 1-"), got)
	require.Contains(t, got, "please look at file 1")
	require.Contains(t, got, "please look at file 2")
	require.NotContains(t, got, "please look at file 3")
	got = RunCtxExpand(ctx, d, `{"start":50,"end":60}`)
	require.Equal(t, "No messages found in range 50-60. The range may be outside this session's history.", got)

	got = RunCtxExpand(ctx, d, `{"start":1,"end":3,"verbose":true}`)
	require.Equal(t, "Messages 1-3 (verbose). Recover any one in full with ctx_expand(message=<ordinal>):\n\n"+
		"[1] U (user)\n    • please look at file 1\n\n"+
		"[2] A (assistant)\n    • reading 1\n    • tool view(f1.go)\n\n"+
		"[3] tool results\n    • tool view(f1.go) → output ~"+itoa(EstimateTokens(strings.Repeat("x", 7000)))+" tok", got)
}

func TestCtxExpandRangeBudgetTruncates(t *testing.T) {
	ctx := context.Background()
	_, d, msgs := expandSetup(t, 12)
	for i := range *msgs { // ~2000-token user texts: 12 of them exceed 15K
		if (*msgs)[i].Role == message.User {
			(*msgs)[i].Parts = []message.ContentPart{message.TextContent{Text: strings.Repeat("word ", 1600)}}
		}
	}
	got := RunCtxExpand(ctx, d, `{"start":1,"end":36}`)
	require.Contains(t, got, "Truncated at message ")
	require.Contains(t, got, "(budget: ~15000 tokens). Call again with start=")
	require.True(t, strings.HasSuffix(got, " end=36 for more."), got[len(got)-80:])
}

func TestCtxExpandLiveTailClamp(t *testing.T) {
	ctx := context.Background()
	e, d, _ := expandSetup(t, 3)
	_, err := e.Store.InsertCompartments(ctx, e.Store.db, "s1", false, []Compartment{{
		Sequence: 1, StartOrdinal: 1, EndOrdinal: 3, StartMessageID: "u1", EndMessageID: "t1", Title: "one", Content: "c",
	}})
	require.NoError(t, err)
	require.Equal(t, "Range 4-9 is entirely within the live tail (after the last compacted message 3); those messages are already visible in context.",
		RunCtxExpand(ctx, d, `{"start":4,"end":9}`))
	got := RunCtxExpand(ctx, d, `{"start":1,"end":9}`)
	require.True(t, strings.HasPrefix(got, "Messages 1-3 (3 messages"), got)
	require.NotContains(t, got, "file 2")
}

func TestCtxExpandSearchArchive(t *testing.T) {
	ctx := context.Background()
	e, d, msgs := expandSetup(t, 2)
	require.NoError(t, e.Store.UpsertMessageIndex(ctx, "s1", []IndexedMessage{
		{Ordinal: 1, MessageID: "u1", Role: "user", Content: "please look at file 1", CreatedAt: 1},
		{Ordinal: 2, MessageID: "gone-a", Role: "assistant", Content: "an answer that was pruned", CreatedAt: 2},
	}))
	// Ordinal 1 is still live with the same id: not served from the archive.
	got := RunCtxExpand(ctx, d, `{"message":1}`)
	require.True(t, strings.HasPrefix(got, "[1] U (user) — full recovery:"), got)
	// Ordinal 2 was replaced: the archive copy is the only record.
	*msgs = append((*msgs)[:1], (*msgs)[2:]...)
	got = RunCtxExpand(ctx, d, `{"message":2}`)
	require.Equal(t, "From the search archive: indexed text only; no surrounding messages or tool output.\n\n[2] A: an answer that was pruned", got)
}

func TestCtxExpandToolInfo(t *testing.T) {
	info := NewCtxExpandTool(ExpandToolDeps{}).Info()
	require.Equal(t, CtxExpandToolName, info.Name)
	require.True(t, strings.HasPrefix(info.Description, "Recover original content that is no longer on your desk."))
	tag := info.Parameters["tag"].(map[string]any)
	require.Len(t, tag["anyOf"], 2)
	require.Empty(t, info.Required)
}
