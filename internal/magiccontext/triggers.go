// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): packages/plugin/src/hooks/magic-context/note-nudger.ts, hook-handlers.ts, transform.ts, tag-messages.ts
package magiccontext

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/charmbracelet/crush/internal/message"
)

// CommitLookback is how many trailing messages the commit detector scans.
const CommitLookback = 5

// todosToolName is the host's todo tool.
const todosToolName = "todos"

// HasRecentAssistantCommit reports a commit hash plus a commit verb in an
// assistant text part among the last CommitLookback messages.
func HasRecentAssistantCommit(msgs []message.Message) bool {
	for i := max(0, len(msgs)-CommitLookback); i < len(msgs); i++ {
		m := msgs[i]
		if m.Role != message.Assistant {
			continue
		}
		for _, p := range m.Parts {
			if t, ok := p.(message.TextContent); ok && TextMentionsCommit(t.Text) {
				return true
			}
		}
	}
	return false
}

// observeCommit fires commit_detected on an absent→present edge. The
// first pass of a session only sets the baseline, so a restart never
// re-fires for a commit that was already visible.
func (e *Engine) observeCommit(ctx context.Context, sid string, msgs []message.Message, subagent bool) {
	seen := HasRecentAssistantCommit(msgs)
	e.mu.Lock()
	if e.commitSeen == nil {
		e.commitSeen = map[string]bool{}
	}
	prev, had := e.commitSeen[sid]
	e.commitSeen[sid] = seen
	e.mu.Unlock()
	if subagent || !had || !seen || prev {
		return
	}
	if err := e.Store.TriggerNoteNudge(ctx, sid, "commit_detected"); err != nil {
		e.logf("magic-context: commit note trigger failed session=%s: %v", sid, err)
	}
}

// ObserveToolResult runs the note-nudge side effects of a finished tool
// call: a todo list with every item done arms todos_complete, and a
// ctx_note call clears the pending trigger and the cooldown.
func (e *Engine) ObserveToolResult(ctx context.Context, sid, tool, metadata string, isError, subagent bool) {
	if !e.Enabled() || isError {
		return
	}
	switch tool {
	case CtxNoteToolName:
		if err := e.Store.MarkNoteNudgeDelivered(ctx, sid, "", "", e.now()); err != nil {
			e.logf("magic-context: note trigger clear failed session=%s: %v", sid, err)
		}
	case todosToolName:
		if subagent || !todosAllDone(metadata) {
			return
		}
		if err := e.Store.TriggerNoteNudge(ctx, sid, "todos_complete"); err != nil {
			e.logf("magic-context: todos note trigger failed session=%s: %v", sid, err)
		}
	}
}

// todosAllDone reads the todos tool metadata: a non-empty list whose items
// are all completed or cancelled.
func todosAllDone(metadata string) bool {
	var md struct {
		Todos []struct {
			Status string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal([]byte(metadata), &md) != nil || len(md.Todos) == 0 {
		return false
	}
	for _, t := range md.Todos {
		switch strings.ToLower(t.Status) {
		case "completed", "cancelled", "canceled":
		default:
			return false
		}
	}
	return true
}
