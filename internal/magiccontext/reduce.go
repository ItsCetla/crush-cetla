// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tools/ctx-reduce/constants.ts, tools/ctx-reduce/tools.ts, tools/unwrap-imitated-reduced-args.ts.

package magiccontext

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
)

// CtxReduceDescription is CTX_REDUCE_DESCRIPTION.
const CtxReduceDescription = "Stamp an item on your desk as no longer needed for the work ahead. Not a delete: stamping QUEUES it, the item stays fully readable until Magic Context clears stamped items in one sweep, and the newest tags are protected so stamping recent output is harmless. A cleared item goes to the archive — a recent one leaves a `[dropped §N§]` placeholder, an older one leaves nothing — and `ctx_expand(tag=N)` is the way back. So the question before stamping is not \"have I finished reading this?\" but \"does this need to stay on my desk for what comes next?\" — a file you read and will keep editing stays; the grep that found it goes.\n\nStamp: file reads, search results and tool outputs the work ahead no longer needs; build/test output after you acted on it; repeated or redundant dumps; data written to disk; status/log output that only confirmed what you expected; a large block pasted inside a user message once you have used it.\nKeep: user messages (never stamp one for its directive), your own conversation text, unresolved errors, raw evidence you haven't extracted yet, and outputs whose exact wording may still matter.\n\nLook at each tag before stamping it; never blanket-stamp a range like \"1-50\". Many small targeted stamps beat one sweep. `drop` accepts \"3-5\", \"1,2,9\", \"1-5,8,12-15\"."

// CtxReduceParams are the ctx_reduce arguments.
type CtxReduceParams struct {
	Drop string `json:"drop,omitempty" description:"Tag IDs to drop: \"3-5\", \"1,2,9\", \"1-5,8,12-15\"."`
}

// imitatedArgs is the clamped shape of reduced tool calls that models
// sometimes copy: {"reduced":true,"summary":"{\"drop\":\"3-5\"}"}.
type imitatedArgs struct {
	Reduced *bool  `json:"reduced"`
	Summary string `json:"summary"`
}

// ReduceToolDeps wire ctx_reduce to a session.
type ReduceToolDeps struct {
	Store     *Store
	SessionID func(ctx context.Context) string
	// InputTokens returns the current prompt size (optional).
	InputTokens func(ctx context.Context, sessionID string) int64
	Now         func() time.Time
}

// FormatCtxReduceReply is formatCtxReduceReply.
func FormatCtxReduceReply(stamped, selfStamps int) string {
	var parts []string
	if stamped > 0 {
		s := "s"
		if stamped == 1 {
			s = ""
		}
		parts = append(parts, fmt.Sprintf("%d tag%s stamped.", stamped, s))
	}
	if selfStamps == 1 {
		parts = append(parts, "1 was a ctx_reduce call; leave those alone, they are cleaned up automatically.")
	} else if selfStamps > 1 {
		parts = append(parts, fmt.Sprintf("%d were ctx_reduce calls; leave those alone, they are cleaned up automatically.", selfStamps))
	}
	return strings.Join(parts, " ")
}

// unwrapReduceArgs decodes imitated reduced args from the raw call input.
func unwrapReduceArgs(p CtxReduceParams, raw string) CtxReduceParams {
	if p.Drop != "" || raw == "" {
		return p
	}
	var im imitatedArgs
	if json.Unmarshal([]byte(raw), &im) != nil || im.Reduced == nil || !*im.Reduced || im.Summary == "" {
		return p
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(im.Summary), &inner); err != nil {
		return p
	}
	out := CtxReduceParams{}
	for k, v := range inner {
		switch k {
		case "reduced", "summary":
		case "drop":
			s, ok := v.(string)
			if !ok || len(s) > 1<<20 {
				return p
			}
			out.Drop = s
		default:
			return p
		}
	}
	return out
}

func formatTagIDs(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("§%d§", id)
	}
	return strings.Join(parts, ", ")
}

// RunCtxReduce queues drops; it never mutates the prompt immediately.
func RunCtxReduce(ctx context.Context, d ReduceToolDeps, p CtxReduceParams, rawInput string) string {
	p = unwrapReduceArgs(p, rawInput)
	sid := d.SessionID(ctx)
	if p.Drop == "" {
		return "Error: 'drop' must be provided."
	}
	ids, err := ParseRangeString(p.Drop)
	if err != nil {
		return "Error: Invalid range syntax. " + err.Error()
	}
	tags, err := d.Store.LoadTags(ctx, sid)
	if err != nil {
		return "Error: Failed to queue ctx_reduce operations. " + err.Error()
	}
	byNum := make(map[int]Tag, len(tags))
	for _, t := range tags {
		byNum[t.Number] = t
	}
	var unknown []int
	for _, id := range ids {
		if _, ok := byNum[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return fmt.Sprintf("Error: Unknown tag(s) %s. Check available tags in conversation.", formatTagIDs(unknown))
	}
	selfStamps := 0
	var keep []int
	for _, id := range ids {
		if normalizeToolName(byNum[id].ToolName) == CtxReduceToolName {
			selfStamps++
			continue
		}
		keep = append(keep, id)
	}
	// Whitespace-only assistant text is never tagged here, so there is no
	// inert provider framing to skip.
	var conflicts []string
	for _, id := range keep {
		if byNum[id].Status == TagCompacted {
			conflicts = append(conflicts, fmt.Sprintf("§%d§ is from before compaction", id))
		}
	}
	if len(conflicts) > 0 {
		return "Error: Conflicting operations — " + strings.Join(conflicts, "; ") + "."
	}
	ops, err := d.Store.PendingOps(ctx, sid)
	if err != nil {
		return "Error: Failed to queue ctx_reduce operations. " + err.Error()
	}
	pending := map[int64]bool{}
	for _, op := range ops {
		if op.Operation == "drop" {
			pending[op.TagID] = true
		}
	}
	stamped := len(keep)
	var queue []Tag
	for _, id := range keep {
		t := byNum[id]
		if t.Status == TagDropped || pending[t.ID] {
			continue
		}
		queue = append(queue, t)
	}
	if len(queue) == 0 {
		if r := FormatCtxReduceReply(stamped, selfStamps); r != "" {
			return r
		}
		return "All requested tags were already queued or processed. No new action is needed."
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	if err := d.Store.Immediate(ctx, func(q querier) error {
		for _, t := range queue {
			if err := d.Store.QueuePendingOp(ctx, q, sid, t.ID, "drop", now.UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return "Error: Failed to queue ctx_reduce operations. " + err.Error()
	}
	var tokens int64
	if d.InputTokens != nil {
		tokens = d.InputTokens(ctx, sid)
	}
	if tokens <= 0 {
		if m, err := d.Store.Meta(ctx, sid); err == nil {
			tokens = m.LastInputTokens
		}
	}
	_ = d.Store.SetMeta(ctx, sid, map[string]any{"last_nudge_tokens": tokens})
	return FormatCtxReduceReply(stamped, selfStamps)
}

// NewCtxReduceTool builds the ctx_reduce tool.
func NewCtxReduceTool(d ReduceToolDeps) fantasy.AgentTool {
	return fantasy.NewAgentTool(CtxReduceToolName, CtxReduceDescription,
		func(ctx context.Context, p CtxReduceParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse(RunCtxReduce(ctx, d, p, call.Input)), nil
		})
}
