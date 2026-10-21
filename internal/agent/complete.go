package agent

import (
	"context"
	"errors"
	"strings"

	"charm.land/fantasy"
)

// defaultCompleteTokens bounds a one-off answer when the model doesn't
// reason out loud.
const defaultCompleteTokens int64 = 1024

// Complete sends one prompt to a model outside any session and returns
// the answer with thinking blocks removed. It is for side jobs like
// asking the main AI which role each of the user's machines suits.
func Complete(ctx context.Context, m Model, system, prompt string, maxTokens int64) (string, error) {
	if m.Model == nil {
		return "", errors.New("no model is configured")
	}
	if m.ClaudeCode {
		return "", errors.New("the Claude Code engine can't answer one-off prompts")
	}
	if maxTokens <= 0 {
		maxTokens = defaultCompleteTokens
	}
	if m.CatwalkCfg.CanReason && m.CatwalkCfg.DefaultMaxTokens > maxTokens {
		maxTokens = m.CatwalkCfg.DefaultMaxTokens
	}
	a := fantasy.NewAgent(
		m.Model,
		fantasy.WithSystemPrompt(system+"\n /no_think"),
		fantasy.WithMaxOutputTokens(maxTokens),
		fantasy.WithUserAgent(userAgent),
	)
	resp, err := a.Stream(ctx, fantasy.AgentStreamCall{Prompt: prompt})
	if err != nil {
		return "", err
	}
	text := thinkTagRegex.ReplaceAllString(resp.Response.Content.Text(), "")
	text = orphanThinkTagRegex.ReplaceAllString(text, "")
	return strings.TrimSpace(text), nil
}
