package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func TestContextOverflowDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		want   bool
		window int64
	}{
		{"nil", nil, false, 0},
		{"canceled", context.Canceled, false, 0},
		{"unrelated", errors.New("connection refused"), false, 0},
		{
			"parsed by fantasy",
			&fantasy.ProviderError{StatusCode: 400, ContextTooLargeErr: true, ContextMaxTokens: 200000},
			true, 200000,
		},
		{
			"openai compatible wording",
			&fantasy.ProviderError{StatusCode: 400, Message: "This model's maximum context length is 8192 tokens. However, you requested 9000 tokens."},
			true, 8192,
		},
		{
			"llama.cpp",
			errors.New("the request exceeds the available context size, try increasing it"),
			true, 0,
		},
		{
			"lm studio",
			&fantasy.ProviderError{StatusCode: 400, Message: "Trying to keep the first 5000 tokens when context the overflows. However, the model is loaded with context length of only 4096 tokens"},
			true, 4096,
		},
		{
			"rate limit is not an overflow",
			&fantasy.ProviderError{StatusCode: 429, Message: "Request too large: maximum context length is 8192 tokens per minute"},
			false, 0,
		},
		{
			"wrapped",
			fmt.Errorf("stream: %w", &fantasy.ProviderError{StatusCode: 400, Message: "prompt is too long: 210000 tokens > 200000 maximum"}),
			true, 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, window := contextOverflow(tt.err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.window, window)
		})
	}
}

func TestContextWindowLearnedOnlyWhenCatalogHasNone(t *testing.T) {
	t.Parallel()

	a := &sessionAgent{}
	unknown := Model{}
	unknown.ModelCfg.Provider, unknown.ModelCfg.Model = "local", "qwen"
	require.Zero(t, a.contextWindow(unknown))

	a.learnContextWindow(unknown, 32768)
	require.Equal(t, int64(32768), a.contextWindow(unknown))

	known := unknown
	known.CatwalkCfg.ContextWindow = 128000
	require.Equal(t, int64(128000), a.contextWindow(known), "the catalog value wins")

	other := Model{}
	other.ModelCfg.Provider, other.ModelCfg.Model = "local", "llama"
	require.Zero(t, a.contextWindow(other), "learned windows are per model")
}

func TestTruncateMiddle(t *testing.T) {
	t.Parallel()

	require.Equal(t, "short", truncateMiddle("short", 100))
	long := strings.Repeat("é", 5000) // 2 bytes each
	out := truncateMiddle(long, 1000)
	require.Less(t, len(out), 1200)
	require.Contains(t, out, "characters left out")
	require.True(t, utf8.ValidString(out))
}

func TestTrimForSummary(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 40000)
	turn := func(n int) []fantasy.Message {
		id := fmt.Sprintf("call-%d", n)
		return []fantasy.Message{
			{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{fantasy.TextPart{Text: fmt.Sprintf("request %d", n)}}},
			{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.ToolCallPart{ToolCallID: id, ToolName: "view", Input: "{}"}}},
			{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: id, Output: fantasy.ToolResultOutputContentText{Text: big}}}},
			{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: fmt.Sprintf("answer %d", n)}}},
		}
	}
	history := []fantasy.Message{fantasy.NewSystemMessage("system")}
	for n := range 10 {
		history = append(history, turn(n)...)
	}
	before := estimateMessageTokens(history)

	t.Run("cuts long tool results", func(t *testing.T) {
		t.Parallel()
		out, dropped := trimForSummary(history, before)
		require.False(t, dropped)
		require.Len(t, out, len(history))
		require.Less(t, estimateMessageTokens(out), before/5)
		// The input is left alone.
		require.Equal(t, big, history[3].Content[0].(fantasy.ToolResultPart).Output.(fantasy.ToolResultOutputContentText).Text)
	})

	t.Run("drops the oldest turns", func(t *testing.T) {
		t.Parallel()
		out, dropped := trimForSummary(history, 3000)
		require.True(t, dropped)
		require.LessOrEqual(t, estimateMessageTokens(out), int64(3000))
		require.Equal(t, fantasy.MessageRoleSystem, out[0].Role)
		require.Equal(t, fantasy.MessageRoleUser, out[1].Role)
		require.Equal(t, "request 9", out[len(out)-4].Content[0].(fantasy.TextPart).Text, "the newest turn stays")
		requireToolResultsFollowCalls(t, out)
	})

	t.Run("drops steps inside one long turn", func(t *testing.T) {
		t.Parallel()
		one := []fantasy.Message{{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "do it"}}}}
		for n := range 10 {
			id := fmt.Sprintf("c%d", n)
			one = append(one,
				fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.ToolCallPart{ToolCallID: id, ToolName: "view", Input: "{}"}}},
				fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: id, Output: fantasy.ToolResultOutputContentText{Text: big}}}},
			)
		}
		out, dropped := trimForSummary(one, 2500)
		require.True(t, dropped)
		require.LessOrEqual(t, estimateMessageTokens(out), int64(2500))
		require.Equal(t, "do it", out[0].Content[0].(fantasy.TextPart).Text)
		requireToolResultsFollowCalls(t, out)
	})
}

// requireToolResultsFollowCalls checks every tool result still answers a
// tool call made earlier in the history.
func requireToolResultsFollowCalls(t *testing.T, msgs []fantasy.Message) {
	t.Helper()
	calls := map[string]bool{}
	for _, m := range msgs {
		for _, p := range m.Content {
			switch p := p.(type) {
			case fantasy.ToolCallPart:
				calls[p.ToolCallID] = true
			case fantasy.ToolResultPart:
				require.True(t, calls[p.ToolCallID], "tool result %s has no call", p.ToolCallID)
			}
		}
	}
}

// overflowModel fails the first `overflows` Stream calls with a context
// overflow and answers the rest with text.
type overflowModel struct {
	mu        sync.Mutex
	overflows int
	prompts   []string
}

func (m *overflowModel) Provider() string { return "fake" }
func (m *overflowModel) Model() string    { return "fake-model" }

func (m *overflowModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return nil, errors.New("not implemented")
}

func (m *overflowModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	var last string
	if n := len(call.Prompt); n > 0 {
		for _, p := range call.Prompt[n-1].Content {
			if tp, ok := p.(fantasy.TextPart); ok {
				last += tp.Text
			}
		}
	}
	m.prompts = append(m.prompts, last)
	fail := len(m.prompts) <= m.overflows
	m.mu.Unlock()
	if fail {
		return nil, &fantasy.ProviderError{
			StatusCode: 400,
			Message:    "This model's maximum context length is 4096 tokens. However, your messages resulted in 5000 tokens.",
		}
	}
	return (&finishStreamModel{text: "ok"}).Stream(context.Background(), call)
}

func (m *overflowModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *overflowModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *overflowModel) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

func newOverflowAgent(t *testing.T, large *overflowModel) (*sessionAgent, fakeEnv) {
	t.Helper()
	env := testEnv(t)
	sa := NewSessionAgent(SessionAgentOptions{
		// No catalog window: the agent has to learn it from the error.
		LargeModel: Model{Model: large},
		SmallModel: Model{Model: &finishStreamModel{text: "title"}, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}},
		IsYolo:     true,
		Sessions:   env.sessions,
		Messages:   env.messages,
	}).(*sessionAgent)
	return sa, env
}

func TestRun_ContextOverflowSummarizesAndRetries(t *testing.T) {
	t.Parallel()

	large := &overflowModel{overflows: 1}
	sa, env := newOverflowAgent(t, large)
	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "fix the bug"})
	require.NoError(t, err)

	prompts := large.seen()
	require.Len(t, prompts, 3, "turn, summary, retry")
	require.Equal(t, "fix the bug", prompts[0])
	require.Contains(t, prompts[2], "no longer fit in the context window")
	require.Contains(t, prompts[2], "fix the bug")

	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.NotEmpty(t, got.SummaryMessageID)
	require.Equal(t, int64(4096), sa.contextWindow(Model{Model: large}), "the window from the error is remembered")

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var finishes []string
	for _, m := range msgs {
		if f := m.FinishPart(); f != nil && m.Role == message.Assistant {
			finishes = append(finishes, f.Message)
		}
	}
	require.Contains(t, finishes, "Context full")
}

func TestRun_ContextOverflowRetriesOnlyOnce(t *testing.T) {
	t.Parallel()

	// Every request overflows, including the summary and its trimmed retry.
	large := &overflowModel{overflows: 100}
	sa, env := newOverflowAgent(t, large)
	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "fix the bug"})
	ok, _ := contextOverflow(err)
	require.True(t, ok, "the overflow error comes back when summarizing can't help: %v", err)
	require.Len(t, large.seen(), 3, "turn, summary, trimmed summary; no endless loop")
}

func TestRun_ContextOverflowNotRetriedWhenAutoSummarizeOff(t *testing.T) {
	t.Parallel()

	large := &overflowModel{overflows: 1}
	env := testEnv(t)
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel:           Model{Model: large},
		SmallModel:           Model{Model: &finishStreamModel{text: "title"}},
		IsYolo:               true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		DisableAutoSummarize: true,
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "fix the bug"})
	require.Error(t, err)
	require.Len(t, large.seen(), 1)
}
