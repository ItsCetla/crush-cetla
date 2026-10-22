package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/gitreview"
)

//go:embed templates/commit_message.md
var commitMessagePrompt []byte

// maxCommitDiff bounds the diff sent to the model. The review screen
// already trims it; this guards callers that don't.
const maxCommitDiff = 16000

// ErrNoCommitModel means no configured model can write a commit message,
// for example when the only model is the Claude Code engine.
var ErrNoCommitModel = errors.New("no model can write a commit message")

// CommitMessageGenerator is implemented by coordinators that can draft a
// commit message. It is a separate interface so test fakes of
// [Coordinator] don't need to implement it.
type CommitMessageGenerator interface {
	GenerateCommitMessage(ctx context.Context, sessionID, diff, log string) (string, error)
}

var _ CommitMessageGenerator = (*coordinator)(nil)

// GenerateCommitMessage drafts a commit message for diff with the current
// agent's small model, falling back to its large one.
func (c *coordinator) GenerateCommitMessage(ctx context.Context, sessionID, diff, log string) (string, error) {
	sa, ok := c.currentAgent().(*sessionAgent)
	if !ok {
		return "", ErrNoCommitModel
	}
	return sa.GenerateCommitMessage(ctx, sessionID, diff, log)
}

// GenerateCommitMessage drafts a semantic commit message for diff, using
// log (recent one line commits) as a style guide. It tries the small model
// first, then the large one, and skips the Claude Code engine, which can't
// answer one-off prompts.
func (a *sessionAgent) GenerateCommitMessage(ctx context.Context, sessionID, diff, log string) (string, error) {
	if strings.TrimSpace(diff) == "" {
		return "", errors.New("there are no changes to describe")
	}

	type modelAttempt struct {
		name  string
		model Model
	}
	var attempts []modelAttempt
	for _, at := range []modelAttempt{{"small", a.smallModel.Get()}, {"large", a.largeModel.Get()}} {
		if at.model.Model != nil && !at.model.ClaudeCode {
			attempts = append(attempts, at)
		}
	}
	if len(attempts) == 0 {
		return "", ErrNoCommitModel
	}

	systemPromptPrefix := a.systemPromptPrefix.Get()
	streamCall := fantasy.AgentStreamCall{
		Prompt: commitMessageUserPrompt(diff, log),
		PrepareStep: func(callCtx context.Context, opts fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			prepared.Messages = opts.Messages
			if systemPromptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{
					fantasy.NewSystemMessage(systemPromptPrefix),
				}, prepared.Messages...)
			}
			return callCtx, prepared, nil
		},
	}
	if sessionID != "" {
		streamCall.Headers = sessionHeaders(sessionID)
	}

	var lastErr error
	for _, at := range attempts {
		tok := int64(400)
		if at.model.CatwalkCfg.CanReason && at.model.CatwalkCfg.DefaultMaxTokens > tok {
			tok = at.model.CatwalkCfg.DefaultMaxTokens
		}
		ag := fantasy.NewAgent(
			at.model.Model,
			fantasy.WithSystemPrompt(string(commitMessagePrompt)+"\n /no_think"),
			fantasy.WithMaxOutputTokens(tok),
			fantasy.WithUserAgent(userAgent),
		)
		resp, err := ag.Stream(ctx, streamCall)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			slog.Error("Error generating commit message with "+at.name+" model; trying next", "err", err)
			lastErr = err
			continue
		}
		if sessionID != "" {
			var openrouterCost *float64
			for _, step := range resp.Steps {
				if c := a.openrouterCost(step.ProviderMetadata); c != nil {
					sum := *c
					if openrouterCost != nil {
						sum += *openrouterCost
					}
					openrouterCost = &sum
				}
			}
			a.recordUsage(at.model, sessionID, "commit", resp.TotalUsage, openrouterCost, false)
		}
		if resp.Response.FinishReason == fantasy.FinishReasonLength {
			slog.Error("Commit message generation hit token limit with " + at.name + " model; trying next")
			lastErr = errors.New("the model ran out of room writing the message")
			continue
		}
		if msg := cleanCommitMessage(resp.Response.Content.Text()); msg != "" {
			return msg, nil
		}
		lastErr = errors.New("the model returned an empty message")
	}
	return "", fmt.Errorf("generate commit message: %w", lastErr)
}

// commitMessageUserPrompt builds the request the model sees.
func commitMessageUserPrompt(diff, log string) string {
	var b strings.Builder
	if log = strings.TrimSpace(log); log != "" {
		b.WriteString("Recent commits, newest first:\n\n")
		b.WriteString(log)
		b.WriteString("\n\n")
	}
	b.WriteString("Write the commit message for these changes:\n\n")
	b.WriteString(gitreview.TruncateDiff(diff, maxCommitDiff))
	b.WriteString("\n <think>\n\n</think>")
	return b.String()
}

// cleanCommitMessage strips what models wrap around the message: thinking
// blocks, code fences, quotes and a "Commit message:" label.
func cleanCommitMessage(text string) string {
	text = thinkTagRegex.ReplaceAllString(text, "")
	text = orphanThinkTagRegex.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)

	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		lines = lines[1:]
		if n := len(lines); n > 0 && strings.HasPrefix(strings.TrimSpace(lines[n-1]), "```") {
			lines = lines[:n-1]
		}
		text = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	for _, label := range []string{"commit message:", "message:"} {
		if len(text) >= len(label) && strings.EqualFold(text[:len(label)], label) {
			text = strings.TrimSpace(text[len(label):])
		}
	}
	if len(text) >= 2 && (text[0] == '"' && text[len(text)-1] == '"' || text[0] == '`' && text[len(text)-1] == '`') {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}

	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
