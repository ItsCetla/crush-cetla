// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tools/ctx-expand/constants.ts, tools/ctx-expand/mode.ts, tools/ctx-expand/tools.ts, tools/ctx-expand/render.ts, tools/ctx-expand/search-archive.ts, features/magic-context/tag-input.ts, tools/unwrap-imitated-reduced-args.ts.

package magiccontext

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
)

const (
	// CtxExpandToolName is the ctx_expand tool.
	CtxExpandToolName = "ctx_expand"
	// CtxExpandTokenBudget is CTX_EXPAND_TOKEN_BUDGET.
	CtxExpandTokenBudget = 15000
)

// CtxExpandDescription is CTX_EXPAND_DESCRIPTION.
const CtxExpandDescription = "Recover original content that is no longer on your desk. It takes two kinds of number, and they are never interchangeable:\n- `tag=N`: the number from a §N§ tag or a `[dropped §N§]` placeholder. Returns that one item whole: a text, or a tool call with its full input and output.\n- `message=N`, `start`/`end`: message ordinals, the positions shown in `<session-history>` headings (`## start-end`) and in `ctx_search` hits. An ordinal counts whole messages; a tag counts each text and tool result separately, so the same number points at different things.\n\nEarlier turns are summarized in <session-history> under `## start-end · date · title` headings; each heading stands for the raw messages in that ordinal range. When the summary isn't enough — exact wording, a value, an error message, the reasoning behind a decision — expand the range: ctx_expand(start=120, end=245). Also works around a ctx_search message hit: start=N-10, end=N+5. Ranges after the last compartment are your live tail — already visible, not expandable.\n\nReturns the raw transcript as [N] U:/A: lines, capped at ~15K tokens; an oversized range returns the head and says where to continue.\n\nFiner recovery:\n- verbose=true lists each message separately with its ordinal and a per-part preview (tool calls with output sizes) so you can pick one.\n- message=N returns that one message in full — every text part and every tool call's complete input and output — from stored history. This is the way back to a tool output you released with ctx_reduce; if the message was deleted from history it says so."

// TagInputError is TAG_INPUT_ERROR.
const TagInputError = `Error: tag must be one positive integer: 12, "12", "§12§", "§12", "tag 12", or "[dropped §12§]" (surrounding whitespace is allowed).`

var tagInputRe = regexp.MustCompile(`^(?:([0-9]+)|§([0-9]+)§?|tag\s+([0-9]+)|\[dropped\s+§([0-9]+)§\])$`)

const maxSafeInteger = 1<<53 - 1

// parseTagInput normalizes copied transcript handles without accepting
// embedded or multiple numbers.
func parseTagInput(v any) (int, error) {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && x > 0 && x <= maxSafeInteger {
			return int(x), nil
		}
	case string:
		if m := tagInputRe.FindStringSubmatch(strings.TrimSpace(x)); m != nil {
			for _, g := range m[1:] {
				if g == "" {
					continue
				}
				if n, err := strconv.ParseInt(g, 10, 64); err == nil && n > 0 && n <= maxSafeInteger {
					return int(n), nil
				}
				break
			}
		}
	}
	return 0, fmt.Errorf("%s", TagInputError)
}

type expandModeKind int

const (
	expandError expandModeKind = iota
	expandTag
	expandMessage
	expandRange
)

type expandMode struct {
	kind       expandModeKind
	n          int // tag or message
	start, end int
	verbose    bool
	err        string
}

func asInt(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || math.IsInf(f, 0) || math.Abs(f) > maxSafeInteger {
		return 0, false
	}
	return int(f), true
}

// isZero reports value === 0 (or "" when allowEmpty).
func isZeroArg(v any, allowEmpty bool) bool {
	if f, ok := v.(float64); ok {
		return f == 0
	}
	if s, ok := v.(string); ok && allowEmpty {
		return s == ""
	}
	return false
}

// resolveCtxExpandMode is resolveCtxExpandMode on the "positive" domain.
// args holds decoded JSON values; a missing key is undefined, a nil value is
// null.
func resolveCtxExpandMode(args map[string]any) expandMode {
	has := func(k string) bool { _, ok := args[k]; return ok }
	set := func(k string) bool { v, ok := args[k]; return ok && v != nil }
	if set("tag") && !(isZeroArg(args["tag"], true) && (has("message") || has("start"))) {
		n, err := parseTagInput(args["tag"])
		if err != nil {
			return expandMode{err: err.Error()}
		}
		for _, k := range []string{"message", "start", "end"} {
			if set(k) && !isZeroArg(args[k], false) {
				return expandMode{err: "Error: use tag alone, without message or start/end."}
			}
		}
		return expandMode{kind: expandTag, n: n}
	}
	const minOrd = 1
	messagePresent := set("message")
	msg, msgOK := asInt(args["message"])
	start, startOK := asInt(args["start"])
	end, endOK := asInt(args["end"])
	messageValid := msgOK && msg >= minOrd
	rangeValid := startOK && endOK && start >= minOrd && end >= start
	fillerPair := startOK && endOK && start == 0 && end == 0
	rangeNamed := rangeValid && !fillerPair
	if messageValid && !rangeNamed {
		return expandMode{kind: expandMessage, n: msg}
	}
	if messagePresent && !messageValid && !rangeNamed {
		return expandMode{err: "Error: message must be a positive integer."}
	}
	if rangeValid {
		v, _ := args["verbose"].(bool)
		return expandMode{kind: expandRange, start: start, end: end, verbose: v}
	}
	return expandMode{err: "Error: provide either message=<ordinal>, or start and end (positive integers, start <= end)."}
}

const maxDecodedStringLength = 1 << 20

// unwrapImitatedArgs is unwrapImitatedReducedArgs over decoded JSON args.
// schema maps field → "string" | "number" | "boolean".
func unwrapImitatedArgs(args map[string]any, primary []string, schema map[string]string) map[string]any {
	for _, k := range primary {
		if _, ok := args[k]; ok {
			return args
		}
	}
	reduced, _ := args["reduced"].(bool)
	summary, ok := args["summary"].(string)
	if !reduced || !ok {
		return args
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(summary), &parsed) != nil || parsed == nil {
		return args
	}
	for k, v := range parsed {
		switch k {
		case "reduced":
			if _, ok := v.(bool); !ok {
				return args
			}
			continue
		case "summary":
			if s, ok := v.(string); !ok || len(s) > maxDecodedStringLength {
				return args
			}
			continue
		}
		valid := false
		switch schema[k] {
		case "string":
			s, ok := v.(string)
			valid = ok && len(s) <= maxDecodedStringLength
		case "number":
			f, ok := v.(float64)
			valid = ok && !math.IsInf(f, 0) && !math.IsNaN(f)
		case "boolean":
			_, valid = v.(bool)
		}
		if !valid {
			return args
		}
	}
	return parsed
}

// ExpandToolDeps wire ctx_expand to a session.
type ExpandToolDeps struct {
	Store     *Store
	SessionID func(ctx context.Context) string
	// Messages returns every stored message of the session, oldest first.
	Messages func(ctx context.Context, sessionID string) ([]message.Message, error)
}

// expandHistory is the ordinal-addressed stored history of one session.
type expandHistory struct {
	msgs    []message.Message // sorted by ordinal
	ords    []int
	byOrd   map[int]int    // ordinal → index
	byID    map[string]int // message id → index
	calls   map[string]message.ToolCall
	results map[string]message.ToolResult
}

func (d ExpandToolDeps) history(ctx context.Context, sid string) (*expandHistory, error) {
	msgs, err := d.Messages(ctx, sid)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	ords, err := d.Store.EnsureOrdinals(ctx, sid, ids)
	if err != nil {
		return nil, err
	}
	h := &expandHistory{byOrd: map[int]int{}, byID: map[string]int{}, calls: map[string]message.ToolCall{}, results: map[string]message.ToolResult{}}
	for _, m := range msgs {
		if _, ok := ords[m.ID]; ok {
			h.msgs = append(h.msgs, m)
		}
	}
	sort.SliceStable(h.msgs, func(i, j int) bool { return ords[h.msgs[i].ID] < ords[h.msgs[j].ID] })
	for i, m := range h.msgs {
		o := ords[m.ID]
		h.ords = append(h.ords, o)
		h.byOrd[o] = i
		h.byID[m.ID] = i
		for _, c := range m.ToolCalls() {
			if c.ID != "" {
				h.calls[c.ID] = c
			}
		}
		for _, r := range m.ToolResults() {
			if r.ToolCallID != "" {
				h.results[r.ToolCallID] = r
			}
		}
	}
	return h, nil
}

func (h *expandHistory) ordinals() map[string]int {
	out := make(map[string]int, len(h.msgs))
	for i, m := range h.msgs {
		out[m.ID] = h.ords[i]
	}
	return out
}

func expandRoleLabel(r message.MessageRole) string {
	switch r {
	case message.Assistant:
		return "A (assistant)"
	case message.User:
		return "U (user)"
	}
	return string(r)
}

func verboseRoleLabel(m message.Message) string {
	if m.Role == message.Tool {
		return "tool results"
	}
	return expandRoleLabel(m.Role)
}

func truncateTrim(v string, n int) string {
	t := strings.TrimSpace(v)
	r := []rune(t)
	if len(r) <= n {
		return t
	}
	return string(r[:n]) + "…"
}

// expandKeyArg is keyArg: a best-effort one-line argument descriptor.
func expandKeyArg(input map[string]any) string {
	if input == nil {
		return ""
	}
	for _, k := range []string{"filePath", "file_path", "path", "pattern", "query", "symbol", "module", "action"} {
		if v, ok := input[k].(string); ok && v != "" {
			return truncateTrim(v, 60)
		}
	}
	if v, ok := input["description"].(string); ok {
		return truncateTrim(v, 60)
	}
	return ""
}

func toolInputObject(raw string) (map[string]any, string) {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil || obj == nil {
		return nil, ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, []byte(raw)) != nil {
		b, _ := json.Marshal(obj)
		return obj, string(b)
	}
	return obj, buf.String()
}

// renderToolFull renders a tool call with its result. Crush stores the call
// (assistant) and the result (tool message) separately, so both halves are
// joined here, like an OpenCode merged tool part.
func renderToolFull(name, callID string, call *message.ToolCall, result *message.ToolResult) string {
	if name == "" {
		name = "tool"
	}
	lines := []string{"  [tool: " + name}
	if callID != "" {
		lines[0] += " #" + callID
	}
	lines[0] += "]"
	if call != nil {
		if _, in := toolInputObject(call.Input); in != "" {
			lines = append(lines, "  input: "+in)
		}
	}
	if result != nil {
		lines = append(lines, "  output:\n"+StripChannel1ReminderSpans(result.Content))
	}
	return strings.Join(lines, "\n")
}

// renderPartFull is renderPartFull; ok=false for noise parts.
func (h *expandHistory) renderPartFull(p message.ContentPart) (string, bool) {
	switch x := p.(type) {
	case message.TextContent:
		if strings.TrimSpace(x.Text) == "" {
			return "", false
		}
		return "  [text]\n" + x.Text, true
	case message.ToolCall:
		var res *message.ToolResult
		if r, ok := h.results[x.ID]; ok {
			res = &r
		}
		return renderToolFull(x.Name, x.ID, &x, res), true
	case message.ToolResult:
		var call *message.ToolCall
		if c, ok := h.calls[x.ToolCallID]; ok {
			call = &c
		}
		name := x.Name
		if name == "" && call != nil {
			name = call.Name
		}
		return renderToolFull(name, x.ToolCallID, call, &x), true
	case message.BinaryContent:
		if x.Path != "" {
			return "  [file] " + x.Path, true
		}
		return "  [file]", true
	case message.ImageURLContent:
		if x.URL != "" {
			return "  [file] " + x.URL, true
		}
		return "  [file]", true
	}
	return "", false
}

// renderPartPreview is one bounded per-part preview line for verbose ranges.
func (h *expandHistory) renderPartPreview(p message.ContentPart) (string, bool) {
	switch x := p.(type) {
	case message.TextContent:
		t := truncateTrim(x.Text, 200)
		return "    • " + t, t != ""
	case message.ToolCall:
		obj, _ := toolInputObject(x.Input)
		head := x.Name
		if arg := expandKeyArg(obj); arg != "" {
			head += "(" + arg + ")"
		}
		return "    • tool " + head, true
	case message.ToolResult:
		head := x.Name
		if head == "" {
			head = "tool_result"
		}
		if c, ok := h.calls[x.ToolCallID]; ok {
			obj, _ := toolInputObject(c.Input)
			if arg := expandKeyArg(obj); arg != "" {
				head += "(" + arg + ")"
			}
		}
		return fmt.Sprintf("    • tool %s → output ~%d tok", head, EstimateTokens(StripChannel1ReminderSpans(x.Content))), true
	case message.ReasoningContent:
		return "    • [reasoning] " + truncateTrim(x.Thinking, 120), true
	case message.BinaryContent, message.ImageURLContent:
		return "    • [file]", true
	case message.Finish:
		return "", false
	}
	return "    • [part]", true
}

func noMessageAt(ord int) string {
	return fmt.Sprintf("No message at ordinal %d in this session's stored history — it was deleted ", ord) +
		"(session prune/revert) or the ordinal is wrong, so it can't be recovered. " +
		"Re-run the tool if you still need the data."
}

// renderMessageByOrdinal is full untruncated recovery of one message.
func (h *expandHistory) renderMessageByOrdinal(ord int) string {
	i, ok := h.byOrd[ord]
	if !ok {
		return noMessageAt(ord)
	}
	m := h.msgs[i]
	lines := []string{fmt.Sprintf("[%d] %s — full recovery:", ord, expandRoleLabel(m.Role)), ""}
	var rendered []string
	for _, p := range m.Parts {
		if s, ok := h.renderPartFull(p); ok {
			rendered = append(rendered, s)
		}
	}
	if len(rendered) == 0 {
		lines = append(lines, "  (no recoverable content — message had only structural/reasoning parts)")
	} else {
		lines = append(lines, rendered...)
	}
	return strings.Join(lines, "\n")
}

// renderVerboseRange lists each message in [start,end] with a per-part
// preview, bounded by budget.
func (h *expandHistory) renderVerboseRange(start, end, budget int) (text string, lastOrd int, truncated bool) {
	var out []string
	used := 0
	lastOrd = start - 1
	for i, m := range h.msgs {
		ord := h.ords[i]
		if ord < start {
			continue
		}
		if ord > end {
			break
		}
		block := fmt.Sprintf("[%d] %s", ord, verboseRoleLabel(m))
		var parts []string
		for _, p := range m.Parts {
			if s, ok := h.renderPartPreview(p); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			block += "\n" + strings.Join(parts, "\n")
		}
		bt := EstimateTokens(block)
		if used+bt > budget && len(out) > 0 {
			truncated = true
			break
		}
		out = append(out, block)
		used += bt
		lastOrd = ord
	}
	return strings.Join(out, "\n\n"), lastOrd, truncated
}

var textPartKeyRe = regexp.MustCompile(`^(.*):(p|file)(\d+)$`)

// renderItemByTag resolves a transcript handle by persisted ownership, never
// by message ordinal.
func (h *expandHistory) renderItemByTag(tag Tag, ok bool, n int) string {
	if !ok {
		return fmt.Sprintf("no tag %d in this session; if %d came from a <session-history> heading or a ctx_search hit, it is an ordinal: use message=%d", n, n, n)
	}
	if tag.Type == TagTool {
		if tag.ToolCallID == "" || tag.MessageID == "" {
			return fmt.Sprintf("Tag %d's tool owner is unknown; its original call cannot be resolved safely.", n)
		}
		if _, ok := h.byID[tag.MessageID]; !ok {
			return fmt.Sprintf("Tag %d's original tool owner is no longer in stored history.", n)
		}
		c, hasCall := h.calls[tag.ToolCallID]
		r, hasRes := h.results[tag.ToolCallID]
		if !hasCall && !hasRes {
			return fmt.Sprintf("Tag %d's original tool call is no longer in stored history.", n)
		}
		var cp *message.ToolCall
		var rp *message.ToolResult
		name := tag.ToolName
		if hasCall {
			cp = &c
			if name == "" {
				name = c.Name
			}
		}
		if hasRes {
			rp = &r
			if name == "" {
				name = r.Name
			}
		}
		return renderToolFull(name, tag.ToolCallID, cp, rp)
	}
	owner, index := tag.MessageID, 0
	if m := textPartKeyRe.FindStringSubmatch(tag.PartKey); m != nil {
		owner = m[1]
		index, _ = strconv.Atoi(m[3])
	} else if tag.Type == TagMessage {
		if i, ok := h.byID[owner]; ok {
			index = max(firstText(h.msgs[i]), 0)
		}
	}
	i, found := h.byID[owner]
	if !found {
		return fmt.Sprintf("Tag %d's original text is no longer in stored history.", n)
	}
	parts := h.msgs[i].Parts
	if index < 0 || index >= len(parts) {
		return fmt.Sprintf("Tag %d's original text part is no longer in stored history.", n)
	}
	s, _ := h.renderPartFull(parts[index])
	return s
}

// MessageIndexRange returns indexed message rows within [start,end].
func (s *Store) MessageIndexRange(ctx context.Context, sessionID string, start, end int) ([]IndexedMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ordinal, message_id, role, content, created_at FROM mc_message_index
WHERE session_id = ? AND ordinal BETWEEN ? AND ? ORDER BY ordinal`, sessionID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexedMessage
	for rows.Next() {
		var r IndexedMessage
		if err := rows.Scan(&r.Ordinal, &r.MessageID, &r.Role, &r.Content, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// renderSearchArchive recovers indexed text when stored history no longer
// holds the cited messages. ok=false when nothing was recovered.
func (d ExpandToolDeps) renderSearchArchive(ctx context.Context, sid string, h *expandHistory, start, end, budget int) (string, bool) {
	rows, err := d.Store.MessageIndexRange(ctx, sid, start, end)
	if err != nil || len(rows) == 0 {
		return "", false
	}
	var blocks []string
	tokens := 0
	lastOrd := start - 1
	truncated := false
	for _, r := range rows {
		if r.Content == "" {
			continue
		}
		if i, ok := h.byOrd[r.Ordinal]; ok && h.msgs[i].ID == r.MessageID {
			continue
		}
		role := "A"
		if r.Role == "user" {
			role = "U"
		}
		block := fmt.Sprintf("[%d] %s: %s", r.Ordinal, role, r.Content)
		cost := EstimateTokens(block)
		if len(blocks) > 0 && tokens+cost > budget {
			truncated = true
			break
		}
		blocks = append(blocks, block)
		tokens += cost
		lastOrd = r.Ordinal
	}
	if len(blocks) == 0 {
		return "", false
	}
	text := append([]string{"From the search archive: indexed text only; no surrounding messages or tool output.", ""}, blocks...)
	if truncated {
		text = append(text, "", fmt.Sprintf("Truncated at message %d (budget: ~%d tokens). Call ctx_expand(start=%d, end=%d) for more archived text.", lastOrd, budget, lastOrd+1, end))
	}
	return strings.Join(text, "\n"), true
}

// lastCompartmentEnd is getLastCompartmentEndMessage (-1 = none).
func (s *Store) lastCompartmentEnd(ctx context.Context, sid string) int {
	cs, err := s.Compartments(ctx, sid)
	if err != nil || len(cs) == 0 {
		return -1
	}
	last := -1
	for _, c := range cs {
		last = max(last, c.EndOrdinal)
	}
	return last
}

// RunCtxExpand executes ctx_expand over raw JSON args.
func RunCtxExpand(ctx context.Context, d ExpandToolDeps, rawInput string) string {
	args := map[string]any{}
	if strings.TrimSpace(rawInput) != "" {
		if err := json.Unmarshal([]byte(rawInput), &args); err != nil || args == nil {
			args = map[string]any{}
		}
	}
	args = unwrapImitatedArgs(args, []string{"tag", "message", "start"}, map[string]string{
		"start": "number", "end": "number", "verbose": "boolean", "message": "number",
	})
	sid := d.SessionID(ctx)
	mode := resolveCtxExpandMode(args)
	if mode.kind == expandError {
		return mode.err
	}
	h, err := d.history(ctx, sid)
	if err != nil {
		return "Error: " + err.Error()
	}
	switch mode.kind {
	case expandTag:
		tag, ok, err := d.Store.TagByNumber(ctx, sid, mode.n)
		if err != nil {
			return "Error: " + err.Error()
		}
		return h.renderItemByTag(tag, ok, mode.n)
	case expandMessage:
		if s, ok := d.renderSearchArchive(ctx, sid, h, mode.n, mode.n, CtxExpandTokenBudget); ok {
			return s
		}
		return h.renderMessageByOrdinal(mode.n)
	}
	start, end := mode.start, mode.end
	// Clamp to the last compartment boundary: anything after it is the live
	// tail the agent already sees.
	lastEnd := d.Store.lastCompartmentEnd(ctx, sid)
	if lastEnd >= 0 && start > lastEnd {
		return fmt.Sprintf("Range %d-%d is entirely within the live tail (after the last compacted message %d); those messages are already visible in context.", start, end, lastEnd)
	}
	effEnd := end
	if lastEnd >= 0 {
		effEnd = min(end, lastEnd)
	}
	if s, ok := d.renderSearchArchive(ctx, sid, h, start, effEnd, CtxExpandTokenBudget); ok {
		return s
	}
	if mode.verbose {
		text, lastOrd, truncated := h.renderVerboseRange(start, effEnd, CtxExpandTokenBudget)
		if text == "" {
			return fmt.Sprintf("No messages found in range %d-%d. The range may be outside this session's history.", start, effEnd)
		}
		out := []string{fmt.Sprintf("Messages %d-%d (verbose). Recover any one in full with ctx_expand(message=<ordinal>):", start, lastOrd), "", text}
		if truncated {
			out = append(out, "", fmt.Sprintf("Truncated at message %d (budget: ~%d tokens). Call again with start=%d end=%d verbose=true for more.", lastOrd, CtxExpandTokenBudget, lastOrd+1, effEnd))
		}
		return strings.Join(out, "\n")
	}
	raw := BuildRawView(h.msgs, h.ordinals())
	ch := ReadChunk(raw, start, effEnd+1, CtxExpandTokenBudget)
	if ch.Text == "" || ch.MessageCount == 0 {
		return fmt.Sprintf("No messages found in range %d-%d. The range may be outside this session's history.", start, end)
	}
	lines := []string{fmt.Sprintf("Messages %d-%d (%d messages, ~%d tokens):", ch.StartIndex, ch.EndIndex, ch.MessageCount, ch.TokenEstimate), "", ch.Text}
	if ch.EndIndex < effEnd {
		lines = append(lines, "", fmt.Sprintf("Truncated at message %d (budget: ~%d tokens). Call again with start=%d end=%d for more.", ch.EndIndex, CtxExpandTokenBudget, ch.EndIndex+1, effEnd))
	}
	return strings.Join(lines, "\n")
}

// ctxExpandTool is a hand-built AgentTool: tag is number|string, which the
// reflected schema cannot express.
type ctxExpandTool struct {
	deps ExpandToolDeps
	opts fantasy.ProviderOptions
}

// NewCtxExpandTool builds the ctx_expand tool.
func NewCtxExpandTool(d ExpandToolDeps) fantasy.AgentTool { return &ctxExpandTool{deps: d} }

func (t *ctxExpandTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        CtxExpandToolName,
		Description: CtxExpandDescription,
		Parameters: map[string]any{
			"tag": map[string]any{
				"anyOf":       []any{map[string]any{"type": "number"}, map[string]any{"type": "string"}},
				"description": "Tag number from a §N§ tag or a [dropped §N§] placeholder, not a message ordinal. Returns that one item in full. Use alone.",
			},
			"start": map[string]any{
				"type":        "number",
				"description": "First message ordinal of the range (a <session-history> heading's start, or a ctx_search hit), not a tag number.",
			},
			"end": map[string]any{
				"type":        "number",
				"description": "Last message ordinal of the range, inclusive, not a tag number.",
			},
			"verbose": map[string]any{
				"type":        "boolean",
				"description": "With start/end: one entry per message with ordinal and per-part preview instead of the transcript.",
			},
			"message": map[string]any{
				"type":        "number",
				"description": "Message ordinal from a <session-history> heading or a ctx_search hit, not a tag number. Returns that one message in full. Use alone.",
			},
		},
		Required: []string{},
	}
}

func (t *ctxExpandTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.NewTextResponse(RunCtxExpand(ctx, t.deps, call.Input)), nil
}

func (t *ctxExpandTool) ProviderOptions() fantasy.ProviderOptions     { return t.opts }
func (t *ctxExpandTool) SetProviderOptions(o fantasy.ProviderOptions) { t.opts = o }
