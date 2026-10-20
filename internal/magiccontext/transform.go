// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/transform.ts, apply-operations.ts, tool-drop-target.ts, heuristic-cleanup.ts, drop-stale-reduce-calls.ts.

package magiccontext

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/message"
)

// Drop modes persisted in mc_tags.drop_mode so replays stay byte-identical.
const (
	DropModeText     = "text"     // message text replaced by the placeholder
	DropModeSkeleton = "skeleton" // keep the call, replace the output
	DropModeFull     = "full"     // remove call and output
	DropModeFile     = "file"     // attachment removed

	skeletonRecentTools   = 20
	skeletonMaxInputBytes = 1024
	imageByteEstimate     = imageTokenEstimate * 4

	// M0MessageID and M1MessageID identify the synthetic history messages.
	M0MessageID = "magic-context-m0"
	M1MessageID = "magic-context-m1"

	// CtxReduceToolName is the ctx_reduce tool.
	CtxReduceToolName = "ctx_reduce"

	stalePassHistorianCooldown = time.Minute
)

// dedupSafeTools are read-only tools whose repeated identical calls can be
// collapsed to the newest one.
var dedupSafeTools = map[string]bool{
	"view": true, "read": true, "grep": true, "glob": true, "ls": true,
	"ast_grep_search": true, "lsp_diagnostics": true, "lsp_symbols": true,
	"lsp_references": true, "lsp_find_references": true, "lsp_definition": true,
	"lsp_goto_definition": true, "lsp_prepare_rename": true,
}

// Engine runs Magic Context over one agent's prompts.
type Engine struct {
	Store     *Store
	Settings  Settings
	Historian *Historian // nil = no compaction
	// ProjectKey scopes memories ("" = none); ProjectDir is the working
	// directory used for project docs and notes.
	ProjectKey string
	ProjectDir string
	Embed      Embedder
	Logf       func(format string, args ...any)
	Now        func() time.Time
	// OnHistorianDone runs after a background historian run ends.
	OnHistorianDone func(sessionID string, res HistorianResult, err error)
	// Dreamer generates text for /ctx-dream (nil = dream tasks that need a
	// model are skipped).
	Dreamer Generator

	mu       sync.Mutex
	running  map[string]chan struct{}
	failedAt map[string]time.Time
	stats    map[string]PassStats
	nudges   map[string]*nudgeLive
	// commitSeen is the last pass's commit detection per session.
	commitSeen map[string]bool
	// nudgeMu serializes nudge read-modify-write cycles on the store.
	nudgeMu sync.Mutex
	// cmd is /ctx-* command bookkeeping (guarded by mu).
	cmd cmdState
	// syncCommands runs long commands inline (tests).
	syncCommands bool
}

// TagRef names a tag for nudge hints.
type TagRef struct {
	Number int
	Tool   string
}

// PassStats are the measurements of the last pass that nudges read.
type PassStats struct {
	// TailTokens is the unsummarized visible history (T).
	TailTokens int
	// Reclaimable is the undropped, unqueued tool output (U).
	Reclaimable int
	// ReclaimableCount is how many tool outputs make up Reclaimable.
	ReclaimableCount int
	Oldest           []TagRef
	DropsApplied     bool
	Pct              float64
}

// TransformInput is one prompt to rewrite.
type TransformInput struct {
	SessionID string
	// Messages are every stored message of the session, oldest first.
	Messages     []message.Message
	ContextLimit int
	// InputTokens is the latest known prompt size (0 = use the last
	// recorded response).
	InputTokens int64
	ModelKey    string
	SystemHash  string
	// Reduce shows §N§ tags (ctx_reduce available).
	Reduce   bool
	Subagent bool
}

// TransformResult is the rewritten prompt.
type TransformResult struct {
	Messages []message.Message
	Render   RenderResult
	Pct      float64
	// Executed is true when queued drops were allowed to land.
	Executed  bool
	Dropped   int
	Emergency int
	// Historian is the trigger reason of a historian run started (or
	// awaited) by this pass.
	Historian string
	Applied   bool
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) logf(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

// Enabled reports whether the engine is active.
func (e *Engine) Enabled() bool { return e != nil && e.Settings.Enabled && e.Store != nil }

// Compaction reports whether a historian is configured.
func (e *Engine) Compaction() bool {
	return e.Enabled() && e.Historian != nil && e.Historian.Generate != nil
}

// Stats returns the last pass measurements of a session.
func (e *Engine) Stats(sessionID string) (PassStats, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.stats[sessionID]
	return s, ok
}

// HistorianRunning reports whether a background historian run is active.
func (e *Engine) HistorianRunning(sessionID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.running[sessionID]
	return ok
}

// WaitHistorian blocks until the session's running historian finishes.
func (e *Engine) WaitHistorian(ctx context.Context, sessionID string) {
	e.mu.Lock()
	ch := e.running[sessionID]
	e.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

// RecordResponse stores the usage of a finished model response.
func (e *Engine) RecordResponse(ctx context.Context, sessionID string, inputTokens int64, contextLimit int) error {
	if !e.Enabled() || inputTokens <= 0 {
		return nil
	}
	pct := 0.0
	if contextLimit > 0 {
		pct = float64(inputTokens) / float64(contextLimit) * 100
	}
	return e.Store.SetMeta(ctx, sessionID, map[string]any{
		"last_response_time":      e.now().UnixMilli(),
		"last_input_tokens":       inputTokens,
		"last_context_percentage": pct,
	})
}

// Transform rewrites the prompt. It fails open: on any error the input
// messages are returned unchanged.
func (e *Engine) Transform(ctx context.Context, in TransformInput) TransformResult {
	if !e.Enabled() || in.SessionID == "" || len(in.Messages) == 0 {
		return TransformResult{Messages: in.Messages}
	}
	out, err := e.transform(ctx, in)
	if err != nil {
		e.logf("magic-context: transform failed session=%s: %v", in.SessionID, err)
		return TransformResult{Messages: in.Messages}
	}
	return out
}

// passState is the working state of one transform pass.
type passState struct {
	in       TransformInput
	raw      []RawMessage
	ords     map[string]int
	idx      TokenIndex
	window   Window
	pct      float64
	input    int64
	tags     []Tag
	byKey    map[string]*Tag
	tagOrd   map[int64]int // tag id → ordinal of its message
	pending  map[int64]bool
	lastUser int // ordinal of the last meaningful user message
	// loopReasoning is true when an assistant message after lastUser has
	// reasoning (thinking signatures pin the current tool loop).
	loopReasoning bool
	protCutoff    int // first ordinal of the protected window
	protected     map[int64]bool
	recentTool    map[int64]bool // newest skeletonRecentTools tool tags
	callInput     map[string]string
}

func (e *Engine) transform(ctx context.Context, in TransformInput) (TransformResult, error) {
	sid := in.SessionID
	now := e.now()
	ids := make([]string, 0, len(in.Messages))
	for _, m := range in.Messages {
		ids = append(ids, m.ID)
	}
	ords, err := e.Store.EnsureOrdinals(ctx, sid, ids)
	if err != nil {
		return TransformResult{}, err
	}
	meta, err := e.Store.Meta(ctx, sid)
	if err != nil {
		return TransformResult{}, err
	}
	ps := &passState{in: in, ords: ords}
	ps.raw = BuildRawView(in.Messages, ords)
	if err := e.syncMessageIndex(ctx, sid, ps.raw); err != nil {
		e.logf("magic-context: message index sync failed session=%s: %v", sid, err)
	}
	ps.idx = NewTokenIndex(ps.raw)
	ps.input = in.InputTokens
	if ps.input <= 0 {
		ps.input = meta.LastInputTokens
	}
	limit := ResolveContextLimit(in.ContextLimit, ps.input, meta.LastContextPercentage)
	if ps.input > 0 && limit > 0 {
		ps.pct = float64(ps.input) / float64(limit) * 100
	}
	ps.window = e.Settings.WindowFor(limit)

	if err := e.ensureTags(ctx, ps); err != nil {
		return TransformResult{}, err
	}
	e.observeCommit(ctx, sid, in.Messages, in.Subagent)
	ops, err := e.Store.PendingOps(ctx, sid)
	if err != nil {
		return TransformResult{}, err
	}
	ps.pending = make(map[int64]bool, len(ops))
	for _, op := range ops {
		ps.pending[op.TagID] = true
	}
	e.computeProtection(ps)

	res := TransformResult{Pct: ps.pct}
	forceBand := ps.pct >= ps.window.ForceMaterializationPct()
	execute := e.Settings.ShouldExecute(ps.window, ps.pct, meta.LastResponseTime, now.UnixMilli())

	// Historian trigger.
	if e.Compaction() && !in.Subagent {
		comps, err := e.Store.Compartments(ctx, sid)
		if err != nil {
			return TransformResult{}, err
		}
		lastEnd := 0
		if len(comps) > 0 {
			lastEnd = comps[len(comps)-1].EndOrdinal
		}
		var active []Tag
		for _, t := range ps.tags {
			if t.Status == TagActive {
				active = append(active, t)
			}
		}
		dec := EvaluateTrigger(TriggerInput{
			Settings: e.Settings, Window: ps.window, UsagePct: ps.pct, Raw: ps.raw, LastEnd: lastEnd,
			ProjectedPostDropPct: ProjectedPostDropPct(ps.pct, active, ps.pending),
			DropsCanLand:         execute || forceBand,
		})
		if dec.Fire {
			if ch := e.startHistorian(sid, ps.raw, dec, ps.pct >= blockUntilDonePercentage); ch != nil {
				res.Historian = dec.Reason
				if dec.Block {
					wctx, cancel := context.WithTimeout(ctx, e.historianTimeout())
					select {
					case <-ch:
					case <-wctx.Done():
					}
					cancel()
					if meta, err = e.Store.Meta(ctx, sid); err != nil {
						return TransformResult{}, err
					}
				}
			}
		}
	}

	ro := RenderOptions{
		SessionID: sid, ProjectKey: e.ProjectKey, ProjectDir: e.ProjectDir, Settings: e.Settings,
		HistoryBudget: e.Settings.HistoryBudgetFor(ps.window), ModelKey: in.ModelKey, SystemHash: in.SystemHash,
		CacheExpired: e.cacheExpired(meta, now),
	}
	md, err := e.Store.MustMaterialize(ctx, ro)
	if err != nil {
		return TransformResult{}, err
	}
	historianRunning := e.HistorianRunning(sid)
	cacheBusting := execute || forceBand || md.Value
	gate := cacheBusting && (!historianRunning || forceBand || md.Value)
	res.Executed = gate

	if gate {
		n, err := e.applyPending(ctx, ps)
		if err != nil {
			return TransformResult{}, err
		}
		res.Dropped += n
		n, err = e.dedupTools(ctx, ps)
		if err != nil {
			return TransformResult{}, err
		}
		res.Dropped += n
	}
	if forceBand {
		n, err := e.emergencyDrop(ctx, ps, meta)
		if err != nil {
			return TransformResult{}, err
		}
		res.Emergency = n
	} else if meta.EmergencyLatched {
		if err := e.Store.SetMeta(ctx, sid, map[string]any{"emergency_latched": false}); err != nil {
			return TransformResult{}, err
		}
	}

	stripped := make(map[string]bool, len(meta.StrippedReduceIDs))
	for _, id := range meta.StrippedReduceIDs {
		stripped[id] = true
	}
	if cacheBusting {
		if fresh := e.staleReduceCalls(ps, stripped); len(fresh) > 0 {
			all := append(append([]string{}, meta.StrippedReduceIDs...), fresh...)
			if err := e.Store.SetMeta(ctx, sid, map[string]any{"stripped_reduce_ids": all}); err != nil {
				return TransformResult{}, err
			}
			for _, id := range fresh {
				stripped[id] = true
			}
		}
	}

	ro.CacheBusting = cacheBusting
	if forceBand {
		ro.Force, ro.ForceReason = true, "force_band"
	} else if meta.MaterializePending != "" && cacheBusting {
		ro.Force, ro.ForceReason = true, "historian_publish"
	}
	rr, err := e.Store.InjectM0M1(ctx, ro)
	if err != nil {
		return TransformResult{}, err
	}
	res.Render = rr
	boundary := 0
	if rr.Boundary != "" {
		boundary = ords[rr.Boundary]
	}

	// The current user message: search hint, turn counter, deferred notes.
	extras, err := e.userExtras(ctx, ps, meta, boundary, now)
	if err != nil {
		return TransformResult{}, err
	}

	res.Messages = e.rewrite(ps, boundary, stripped, extras, rr)
	res.Applied = true
	st := e.recordStats(ps, boundary, res.Dropped+res.Emergency > 0)
	if in.Reduce {
		turns := 0
		for _, m := range ps.raw {
			if m.MeaningfulUser() {
				turns++
			}
		}
		e.refreshNudges(ctx, sid, st, turns)
	}
	return res, nil
}

func (e *Engine) historianTimeout() time.Duration {
	if e.Settings.HistorianTimeout > 0 {
		return e.Settings.HistorianTimeout
	}
	return DefaultHistorianTimeout
}

func (e *Engine) cacheExpired(m SessionMeta, now time.Time) bool {
	ttl := e.Settings.CacheTTL
	if ttl < 0 || m.LastResponseTime == 0 {
		return false
	}
	return now.UnixMilli()-m.LastResponseTime > ttl.Milliseconds()
}

// startHistorian launches one background run per session; it returns the
// done channel, or nil when nothing was started.
func (e *Engine) startHistorian(sid string, raw []RawMessage, dec TriggerDecision, emergency bool) chan struct{} {
	e.mu.Lock()
	if e.running == nil {
		e.running = map[string]chan struct{}{}
		e.failedAt = map[string]time.Time{}
	}
	if ch, ok := e.running[sid]; ok {
		e.mu.Unlock()
		return ch
	}
	if t, ok := e.failedAt[sid]; ok && e.now().Sub(t) < stalePassHistorianCooldown && !emergency {
		e.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	e.running[sid] = ch
	e.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), e.historianTimeout())
		defer cancel()
		res, err := e.Historian.Run(ctx, HistorianRun{SessionID: sid, Raw: raw, Boundary: dec.Boundary, Emergency: emergency})
		e.mu.Lock()
		delete(e.running, sid)
		if err != nil {
			e.failedAt[sid] = e.now()
		} else {
			delete(e.failedAt, sid)
		}
		e.mu.Unlock()
		close(ch)
		if err != nil {
			e.logf("magic-context: historian failed session=%s: %v", sid, err)
		}
		if e.OnHistorianDone != nil {
			e.OnHistorianDone(sid, res, err)
		}
	}()
	return ch
}

// ---------------------------------------------------------------------------
// Tags

func textPartKey(msgID string, idx int) string { return msgID + ":p" + strconv.Itoa(idx) }
func toolPartKey(callID string) string         { return "tool:" + callID }
func filePartKey(msgID string, idx int) string { return msgID + ":file" + strconv.Itoa(idx) }

// firstText returns the index of the first text part (the one sent to the
// model), or -1.
func firstText(m message.Message) int {
	for i, p := range m.Parts {
		if _, ok := p.(message.TextContent); ok {
			return i
		}
	}
	return -1
}

func resultBytes(r message.ToolResult) int {
	n := len(StripChannel1ReminderSpans(r.Content))
	if r.Data != "" {
		n += imageByteEstimate
	}
	return n
}

func (e *Engine) ensureTags(ctx context.Context, ps *passState) error {
	sid := ps.in.SessionID
	existing, err := e.Store.LoadTags(ctx, sid)
	if err != nil {
		return err
	}
	have := make(map[string]Tag, len(existing))
	for _, t := range existing {
		have[t.PartKey] = t
	}
	ps.callInput = map[string]string{}
	for _, m := range ps.in.Messages {
		for _, c := range m.ToolCalls() {
			ps.callInput[c.ID] = c.Input
		}
	}
	var missing []Tag
	type sized struct {
		key         string
		size, input int
	}
	var sizes []sized
	want := func(t Tag) {
		if old, ok := have[t.PartKey]; ok {
			if old.Status == TagActive && (old.ByteSize != t.ByteSize || old.InputByteSize != t.InputByteSize) {
				sizes = append(sizes, sized{t.PartKey, t.ByteSize, t.InputByteSize})
			}
			return
		}
		have[t.PartKey] = t
		missing = append(missing, t)
	}
	for _, m := range ps.in.Messages {
		if _, ok := ps.ords[m.ID]; !ok {
			continue
		}
		switch m.Role {
		case message.User, message.Assistant:
			if i := firstText(m); i >= 0 {
				txt := m.Parts[i].(message.TextContent).Text
				if strings.TrimSpace(txt) != "" {
					want(Tag{MessageID: m.ID, PartKey: textPartKey(m.ID, i), Type: TagMessage, ByteSize: len(txt)})
				}
			}
			if m.Role == message.User {
				for i, p := range m.Parts {
					if b, ok := p.(message.BinaryContent); ok && !strings.HasPrefix(b.MIMEType, "text/") {
						want(Tag{MessageID: m.ID, PartKey: filePartKey(m.ID, i), Type: TagFile, ByteSize: imageByteEstimate})
					}
				}
			}
		case message.Tool:
			for _, r := range m.ToolResults() {
				if r.ToolCallID == "" {
					continue
				}
				want(Tag{
					MessageID: m.ID, PartKey: toolPartKey(r.ToolCallID), Type: TagTool, ToolName: r.Name,
					ToolCallID: r.ToolCallID, ByteSize: resultBytes(r), InputByteSize: len(ps.callInput[r.ToolCallID]),
				})
			}
		}
	}
	if len(missing) > 0 {
		inserted, err := e.Store.InsertTags(ctx, sid, missing)
		if err != nil {
			return err
		}
		for _, t := range inserted {
			have[t.PartKey] = t
		}
	}
	for _, s := range sizes {
		t := have[s.key]
		if err := e.Store.UpdateTagSizes(ctx, t.ID, s.size, s.input); err != nil {
			return err
		}
		t.ByteSize, t.InputByteSize = s.size, s.input
		have[s.key] = t
	}
	ps.tags = ps.tags[:0]
	for _, t := range have {
		if t.ID != 0 {
			ps.tags = append(ps.tags, t)
		}
	}
	sort.Slice(ps.tags, func(i, j int) bool { return ps.tags[i].Number < ps.tags[j].Number })
	ps.byKey = make(map[string]*Tag, len(ps.tags))
	ps.tagOrd = make(map[int64]int, len(ps.tags))
	for i := range ps.tags {
		t := &ps.tags[i]
		ps.byKey[t.PartKey] = t
		ps.tagOrd[t.ID] = ps.ords[t.MessageID]
	}
	return nil
}

// computeProtection fills the protected window, protected tools, the
// current-turn boundary and the recent-tool set.
func (e *Engine) computeProtection(ps *passState) {
	for _, m := range ps.raw {
		if m.MeaningfulUser() {
			ps.lastUser = m.Ordinal
		}
	}
	for _, m := range ps.in.Messages {
		if m.Role == message.Assistant && ps.ords[m.ID] > ps.lastUser && m.ReasoningContent().Thinking != "" {
			ps.loopReasoning = true
		}
	}
	if ps.pct >= EmergencyPercentage {
		ps.protCutoff = ps.idx.Count() + 1
	} else {
		ps.protCutoff = ps.idx.SuffixStartFor(e.Settings.ProtectedTokensFor(ps.window))
	}
	ps.protected = map[int64]bool{}
	ps.recentTool = map[int64]bool{}
	seen := map[string]int{}
	recent := 0
	for i := len(ps.tags) - 1; i >= 0; i-- {
		t := ps.tags[i]
		if t.Type != TagTool {
			continue
		}
		if recent < skeletonRecentTools {
			ps.recentTool[t.ID] = true
			recent++
		}
		name := normalizeToolName(t.ToolName)
		if n := e.Settings.ProtectedToolCount(name); n > 0 && seen[name] < n {
			ps.protected[t.ID] = true
		}
		seen[name]++
	}
}

// dropMode picks how a tag is dropped (decided once, then persisted).
func (ps *passState) dropMode(t Tag) string {
	switch t.Type {
	case TagMessage:
		return DropModeText
	case TagFile:
		return DropModeFile
	}
	if ps.tagOrd[t.ID] > ps.lastUser && ps.loopReasoning {
		return DropModeSkeleton
	}
	if ps.recentTool[t.ID] && t.InputByteSize <= skeletonMaxInputBytes {
		return DropModeSkeleton
	}
	return DropModeFull
}

// droppable reports whether a queued drop of t may land now.
func (ps *passState) droppable(t Tag) bool {
	if t.Status != TagActive {
		return false
	}
	ord := ps.tagOrd[t.ID]
	if ord == 0 || ord >= ps.protCutoff || ps.protected[t.ID] {
		return false
	}
	// Thinking protection: the current tool loop keeps its message text.
	if t.Type == TagMessage && ord > ps.lastUser {
		return false
	}
	return true
}

func (e *Engine) markDropped(ctx context.Context, ps *passState, drops map[int64]string) error {
	if len(drops) == 0 {
		return nil
	}
	if err := e.Store.Immediate(ctx, func(q querier) error {
		return e.Store.MarkTagsDropped(ctx, q, ps.in.SessionID, drops)
	}); err != nil {
		return err
	}
	for i := range ps.tags {
		t := &ps.tags[i]
		if mode, ok := drops[t.ID]; ok {
			t.Status = TagDropped
			if t.DropMode == "" {
				t.DropMode = mode
			}
			delete(ps.pending, t.ID)
		}
	}
	return nil
}

// applyPending lands the queued ctx_reduce drops outside the protections.
func (e *Engine) applyPending(ctx context.Context, ps *passState) (int, error) {
	drops := map[int64]string{}
	for _, t := range ps.tags {
		if ps.pending[t.ID] && ps.droppable(t) {
			drops[t.ID] = ps.dropMode(t)
		}
	}
	return len(drops), e.markDropped(ctx, ps, drops)
}

func canonicalArgs(input string) string {
	var v any
	if err := json.Unmarshal([]byte(input), &v); err != nil {
		return strings.TrimSpace(input)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// dedupTools drops older identical calls of read-only tools below the
// protected window, keeping the newest.
func (e *Engine) dedupTools(ctx context.Context, ps *passState) (int, error) {
	newest := map[string]bool{}
	drops := map[int64]string{}
	for i := len(ps.tags) - 1; i >= 0; i-- {
		t := ps.tags[i]
		if t.Type != TagTool || !dedupSafeTools[normalizeToolName(t.ToolName)] {
			continue
		}
		fp := normalizeToolName(t.ToolName) + ":" + canonicalArgs(ps.callInput[t.ToolCallID])
		if !newest[fp] {
			newest[fp] = true
			continue
		}
		if ps.droppable(t) {
			drops[t.ID] = ps.dropMode(t)
		}
	}
	return len(drops), e.markDropped(ctx, ps, drops)
}

// staleReduceCalls returns the ctx_reduce call ids to strip: every call
// below the protected window except the newest protected ones.
func (e *Engine) staleReduceCalls(ps *passState, stripped map[string]bool) []string {
	keep := e.Settings.ProtectedToolCount(CtxReduceToolName)
	var calls []string
	ordOf := map[string]int{}
	for _, m := range ps.in.Messages {
		if m.Role != message.Assistant {
			continue
		}
		for _, c := range m.ToolCalls() {
			if normalizeToolName(c.Name) == CtxReduceToolName && c.ID != "" {
				calls = append(calls, c.ID)
				ordOf[c.ID] = ps.ords[m.ID]
			}
		}
	}
	var out []string
	for i := 0; i < len(calls)-keep; i++ {
		id := calls[i]
		if stripped[id] || ordOf[id] >= ps.protCutoff {
			continue
		}
		out = append(out, id)
	}
	return out
}

// ---------------------------------------------------------------------------
// User-message extras

type userExtras struct {
	msgID  string // message the search hint goes on
	hint   string
	notes  map[string]string // message id → deferred-notes nudge text
	turnUp bool
}

func noteReadVisible(msgs []message.Message, boundary int, ords map[string]int) bool {
	for _, m := range msgs {
		if m.Role != message.Assistant || ords[m.ID] <= boundary {
			continue
		}
		for _, c := range m.ToolCalls() {
			if c.Name == "ctx_note" && strings.Contains(c.Input, `"read"`) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) userExtras(ctx context.Context, ps *passState, meta SessionMeta, boundary int, now time.Time) (userExtras, error) {
	sid := ps.in.SessionID
	ux := userExtras{notes: map[string]string{}}
	var last *message.Message
	lastIdx := -1
	for i := range ps.in.Messages {
		m := &ps.in.Messages[i]
		if m.Role == message.User {
			if rm := BuildRawView([]message.Message{*m}, ps.ords); len(rm) == 1 && rm[0].MeaningfulUser() {
				last, lastIdx = m, i
			}
		}
	}
	firstServe := last != nil && lastIdx == len(ps.in.Messages)-1 && last.ID != meta.SearchHintAnchorID
	if firstServe {
		text := ""
		if i := firstText(*last); i >= 0 {
			text = last.Parts[i].(message.TextContent).Text
		}
		hint := ""
		if e.Settings.AutoSearch && e.ProjectKey != "" {
			h, err := e.Store.AutoSearchHint(ctx, AutoSearchHintRequest{
				ProjectKey: e.ProjectKey, SessionID: sid, Prompt: text, Settings: e.Settings, Embedder: e.Embed,
				VisibleMemoryIDs: meta.CachedM0MemoryIDs,
			})
			if err != nil {
				e.logf("magic-context: auto search hint failed: %v", err)
			} else {
				hint = h
			}
		}
		if err := e.Store.SetMeta(ctx, sid, map[string]any{
			"search_hint_anchor_id": last.ID,
			"search_hint_text":      hint,
			"user_turns":            meta.UserTurns + 1,
		}); err != nil {
			return ux, err
		}
		meta.SearchHintAnchorID, meta.SearchHintText = last.ID, hint
		ux.turnUp = true
	}
	if last != nil && last.ID == meta.SearchHintAnchorID {
		ux.msgID, ux.hint = last.ID, meta.SearchHintText
	}

	if last != nil && !ps.in.Subagent {
		if firstServe {
			text, err := e.Store.PeekNoteNudge(ctx, NoteNudgePeek{
				SessionID: sid, ProjectPath: e.ProjectDir, CurrentUserMessageID: last.ID,
				NoteReadStillVisible: noteReadVisible(ps.in.Messages, boundary, ps.ords), Now: now,
			})
			if err != nil {
				return ux, err
			}
			if text != "" {
				if err := e.Store.MarkNoteNudgeDelivered(ctx, sid, text, last.ID, now); err != nil {
					return ux, err
				}
				if meta, err = e.Store.Meta(ctx, sid); err != nil {
					return ux, err
				}
			}
		}
		for _, a := range NoteNudgeAnchors(meta) {
			if a.Text != "" {
				ux.notes[a.MessageID] = a.Text
			}
		}
	}
	return ux, nil
}

// ---------------------------------------------------------------------------
// Rewrite

func (e *Engine) rewrite(ps *passState, boundary int, stripped map[string]bool, ux userExtras, rr RenderResult) []message.Message {
	sid := ps.in.SessionID
	out := make([]message.Message, 0, len(ps.in.Messages)+2)
	var created int64
	for _, m := range ps.in.Messages {
		if ps.ords[m.ID] > boundary {
			created = m.CreatedAt
			break
		}
	}
	syn := func(id, text string) message.Message {
		return message.Message{
			ID: id, SessionID: sid, Role: message.User, CreatedAt: created, UpdatedAt: created,
			Parts: []message.ContentPart{message.TextContent{Text: text}},
		}
	}
	out = append(out, syn(M0MessageID, rr.M0), syn(M1MessageID, rr.M1))

	reduce := ps.in.Reduce
	for _, m := range ps.in.Messages {
		ord, ok := ps.ords[m.ID]
		if !ok || ord <= boundary {
			continue
		}
		parts := make([]message.ContentPart, 0, len(m.Parts))
		textIdx := firstText(m)
		for i, p := range m.Parts {
			switch c := p.(type) {
			case message.TextContent:
				if i == textIdx && (m.Role == message.User || m.Role == message.Assistant) {
					if t := ps.byKey[textPartKey(m.ID, i)]; t != nil {
						if t.Status == TagDropped {
							c.Text = TagPlaceholder(t.Number)
						} else if reduce {
							c.Text = PrependTag(t.Number, c.Text)
						}
					}
					if m.Role == message.User {
						if m.ID == ux.msgID && ux.hint != "" {
							c.Text += ux.hint
						}
						if n := ux.notes[m.ID]; n != "" {
							c.Text += RenderNoteNudge(n)
						}
					}
				}
				parts = append(parts, c)
			case message.ToolCall:
				if stripped[c.ID] {
					continue
				}
				if t := ps.byKey[toolPartKey(c.ID)]; t != nil && t.Status == TagDropped && t.DropMode == DropModeFull {
					continue
				}
				parts = append(parts, c)
			case message.ToolResult:
				if stripped[c.ToolCallID] {
					continue
				}
				if t := ps.byKey[toolPartKey(c.ToolCallID)]; t != nil {
					switch {
					case t.Status == TagDropped && t.DropMode == DropModeFull:
						continue
					case t.Status == TagDropped:
						c.Content = TagPlaceholder(t.Number)
						c.Data, c.MIMEType, c.IsError = "", "", false
					case reduce:
						c.Content = PrependTag(t.Number, c.Content)
					}
				}
				parts = append(parts, c)
			case message.BinaryContent:
				if t := ps.byKey[filePartKey(m.ID, i)]; t != nil && t.Status == TagDropped {
					continue
				}
				parts = append(parts, c)
			default:
				parts = append(parts, p)
			}
		}
		if len(parts) == 0 {
			continue
		}
		m.Parts = parts
		out = append(out, m)
	}
	return out
}

func (e *Engine) recordStats(ps *passState, boundary int, dropped bool) PassStats {
	st := PassStats{Pct: ps.pct, DropsApplied: dropped}
	if boundary < ps.idx.Count() {
		st.TailTokens = ps.idx.Suffix(boundary + 1)
	}
	for _, t := range ps.tags {
		if t.Type != TagTool || t.Status != TagActive || ps.pending[t.ID] || ps.protected[t.ID] {
			continue
		}
		ord := ps.tagOrd[t.ID]
		if ord <= boundary || ord >= ps.protCutoff {
			continue
		}
		st.Reclaimable += tagReclaimTokens(t)
		st.ReclaimableCount++
		if len(st.Oldest) < 4 {
			st.Oldest = append(st.Oldest, TagRef{Number: t.Number, Tool: t.ToolName})
		}
	}
	e.mu.Lock()
	if e.stats == nil {
		e.stats = map[string]PassStats{}
	}
	e.stats[ps.in.SessionID] = st
	e.mu.Unlock()
	return st
}

// tagReclaimTokens estimates the tokens a drop of t frees.
func tagReclaimTokens(t Tag) int {
	return int(float64(t.ByteSize+t.InputByteSize) * emergencyTokensPerByte)
}
