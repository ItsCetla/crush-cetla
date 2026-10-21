package fleet

import (
	"context"
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func suggestStatuses() []Status {
	return []Status{
		{Machine: Machine{Name: "coder", Model: "qwen2.5-coder:14b", MachineConfig: config.MachineConfig{Hardware: "RTX 4090", Slots: 2}}, Online: true},
		{Machine: Machine{Name: "big", Model: "llama3.1:70b", MachineConfig: config.MachineConfig{ContextWindow: 131072}}, Online: true},
		{Machine: Machine{Name: "farm", Model: "qwen3:8b", MachineConfig: config.MachineConfig{Slots: 6}}},
		{Machine: Machine{Name: "mini", Model: "gemma3:4b", MachineConfig: config.MachineConfig{Disabled: true}}},
	}
}

func TestSuggestPrompt(t *testing.T) {
	t.Parallel()
	system, prompt := SuggestPrompt(suggestStatuses(), "anthropic/claude-sonnet")
	require.Contains(t, system, "JSON only")
	require.Contains(t, prompt, "runs on anthropic/claude-sonnet")
	require.Contains(t, prompt, "- coder: hardware RTX 4090, model qwen2.5-coder:14b, 2 slot(s), online")
	require.Contains(t, prompt, "- big: model llama3.1:70b, context window 131072 tokens, 1 slot(s), online")
	require.Contains(t, prompt, "- farm: model qwen3:8b, 6 slot(s), offline")
	require.Contains(t, prompt, "- mini: model gemma3:4b, 1 slot(s), disabled")
}

func TestParseSuggestions(t *testing.T) {
	t.Parallel()
	text := "<think>hmm</think>Here you go:\n```json\n{\"coder\": {\"role\": \"code review\", \"reason\": \"it's a code model\"}, \"BIG\": {\"role\": \"long reads\"}, \"ghost\": {\"role\": \"x\"}}\n```"
	sugs, err := ParseSuggestions(text, suggestStatuses())
	require.NoError(t, err)
	require.Equal(t, []Suggestion{
		{Name: "coder", Role: "code review", Reason: "it's a code model"},
		{Name: "big", Role: "long reads"},
	}, sugs)

	_, err = ParseSuggestions("no json here", suggestStatuses())
	require.Error(t, err)
	_, err = ParseSuggestions(`{"ghost": {"role": "x"}}`, suggestStatuses())
	require.Error(t, err)
}

func TestHeuristicSuggestions(t *testing.T) {
	t.Parallel()
	sugs := HeuristicSuggestions(suggestStatuses())
	require.Len(t, sugs, 4)
	require.Equal(t, roleCodeReview, sugs[0].Role)
	require.Equal(t, roleLongReads, sugs[1].Role)
	require.Equal(t, roleParallel, sugs[2].Role)
	require.Contains(t, sugs[2].Reason, "offline")
	require.Equal(t, roleLookups, sugs[3].Role)
	require.Contains(t, sugs[3].Reason, "disabled")
}

func TestSuggest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	statuses := suggestStatuses()

	sugs, fromAI, err := Suggest(ctx, statuses, "main", func(_ context.Context, system, prompt string) (string, error) {
		require.Contains(t, prompt, "- coder:")
		return `{"coder": {"role": "tests", "reason": "fast"}}`, nil
	})
	require.NoError(t, err)
	require.True(t, fromAI)
	require.Equal(t, []Suggestion{{Name: "coder", Role: "tests", Reason: "fast"}}, sugs)

	sugs, fromAI, err = Suggest(ctx, statuses, "main", func(context.Context, string, string) (string, error) {
		return "", errors.New("model is busy")
	})
	require.EqualError(t, err, "model is busy")
	require.False(t, fromAI)
	require.Len(t, sugs, 4)

	sugs, fromAI, err = Suggest(ctx, statuses, "main", nil)
	require.NoError(t, err)
	require.False(t, fromAI)
	require.Len(t, sugs, 4)

	sugs, _, err = Suggest(ctx, nil, "main", nil)
	require.NoError(t, err)
	require.Nil(t, sugs)
}
