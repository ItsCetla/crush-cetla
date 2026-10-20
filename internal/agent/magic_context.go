// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): plugin/messages-transform.ts, plugin/tool-registry.ts, hooks/magic-context/hook.ts, hooks/magic-context/transform.ts.

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/magiccontext"
	"github.com/charmbracelet/crush/internal/message"
)

// magicContextActive reports whether Magic Context rewrites this agent's
// prompts. Sub-agents always use the plain path.
func (a *sessionAgent) magicContextActive() bool {
	return a.mc.Enabled() && !a.isSubAgent && !a.largeModel.Get().ClaudeCode
}

// magicContextCompaction reports whether Magic Context owns compaction, in
// which case auto-summarize stays off.
func (a *sessionAgent) magicContextCompaction() bool {
	return a.magicContextActive() && a.mc.Compaction()
}

// systemHash fingerprints the system prompt so the engine can tell when the
// cached prefix changed.
func systemHash(systemPrompt string) string {
	sum := sha256.Sum256([]byte(systemPrompt))
	return hex.EncodeToString(sum[:8])
}

func hasToolNamed(list []fantasy.AgentTool, name string) bool {
	for _, t := range list {
		if t.Info().Name == name {
			return true
		}
	}
	return false
}

// magicContextMessages rebuilds the step prompt from the stored session
// through the Magic Context transform. The leading system messages of
// current are kept; everything after them is replaced. It returns false
// when the stored session could not be read, so the caller keeps the
// prompt fantasy built.
func (a *sessionAgent) magicContextMessages(
	ctx context.Context,
	sessionID string,
	current []fantasy.Message,
	stepTools []fantasy.AgentTool,
	model Model,
	systemPrompt string,
) ([]fantasy.Message, bool) {
	// Channel 2: a persisted, hidden user reminder asking for ctx_reduce.
	if text, ok := a.mc.TakeChannel2(ctx, sessionID); ok && text != "" {
		if _, err := a.createUserMessage(ctx, SessionAgentCall{
			SessionID:         sessionID,
			Prompt:            text,
			HiddenUserMessage: true,
		}); err != nil {
			slog.Warn("magic-context: failed to store channel-2 reminder", "error", err)
		}
	}
	if err := a.messages.FlushAll(ctx); err != nil {
		slog.Warn("magic-context: flush before transform failed", "error", err)
	}
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		slog.Warn("magic-context: failed to load session", "error", err)
		return nil, false
	}
	msgs, err := a.getSessionMessages(ctx, sess)
	if err != nil {
		slog.Warn("magic-context: failed to load messages", "error", err)
		return nil, false
	}
	res := a.mc.Transform(ctx, magiccontext.TransformInput{
		SessionID:    sessionID,
		Messages:     msgs,
		ContextLimit: int(model.CatwalkCfg.ContextWindow),
		ModelKey:     model.ModelCfg.Provider + "/" + model.ModelCfg.Model,
		SystemHash:   systemHash(systemPrompt),
		Reduce:       hasToolNamed(stepTools, magiccontext.CtxReduceToolName),
	})
	history, _ := a.preparePrompt(res.Messages, model.CatwalkCfg.SupportsImages)
	out := make([]fantasy.Message, 0, len(history)+1)
	for _, m := range current {
		if m.Role != fantasy.MessageRoleSystem {
			break
		}
		out = append(out, m)
	}
	return append(out, history...), true
}

// magicContextToolResult appends a tool-output reminder to a successful
// tool result and feeds the result to the note triggers.
func (a *sessionAgent) magicContextToolResult(ctx context.Context, sessionID string, tr *message.ToolResult) {
	if !a.magicContextActive() || tr == nil {
		return
	}
	if !tr.IsError {
		if nudge := a.mc.ToolOutputNudge(ctx, sessionID, tr.Name, tr.Content); nudge != "" {
			tr.Content += nudge
		}
	}
	a.mc.ObserveToolResult(ctx, sessionID, tr.Name, tr.Metadata, tr.IsError, false)
}

// magicContextStepFinish strips §N§ tags the model echoed into its reply
// and records the prompt size for the next pass.
func (a *sessionAgent) magicContextStepFinish(ctx context.Context, sessionID string, msg *message.Message, usage fantasy.Usage, contextWindow int64) {
	if !a.magicContextActive() || msg == nil {
		return
	}
	for i, p := range msg.Parts {
		if tc, ok := p.(message.TextContent); ok {
			tc.Text = magiccontext.StripPersistedText(tc.Text)
			msg.Parts[i] = tc
		}
	}
	if err := a.mc.RecordResponse(ctx, sessionID, contextTokens(usage), int(contextWindow)); err != nil {
		slog.Warn("magic-context: failed to record response", "error", err)
	}
}
