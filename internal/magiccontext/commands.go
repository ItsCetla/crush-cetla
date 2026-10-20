// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/command-handler.ts, execute-status.ts, execute-flush.ts, wrapup-orchestrator.ts, recomp-orchestrator.ts, compartment-runner-recomp.ts, compartment-runner-partial-recomp.ts, shared/status-summary.ts, dreamer/manual-summary.ts

package magiccontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/message"
)

// Slash command names (without the leading slash).
const (
	CmdStatus = "ctx-status"
	CmdFlush  = "ctx-flush"
	CmdRecomp = "ctx-recomp"
	CmdWrapup = "ctx-wrapup"
	CmdDream  = "ctx-dream"
)

// CommandInfo describes one /ctx-* command for command palettes.
type CommandInfo struct {
	Name        string
	Usage       string
	Description string
	// NeedsCompaction is true when the command needs a historian model.
	NeedsCompaction bool
}

// Commands lists the /ctx-* commands in display order.
var Commands = []CommandInfo{
	{Name: CmdStatus, Usage: "/ctx-status [diagnostics]", Description: "Show Magic Context status for this session"},
	{Name: CmdFlush, Usage: "/ctx-flush", Description: "Apply queued context drops now"},
	{Name: CmdRecomp, Usage: "/ctx-recomp [<start>-<end>]", Description: "Rebuild compressed history from raw messages", NeedsCompaction: true},
	{Name: CmdWrapup, Usage: "/ctx-wrapup [messages_to_keep]", Description: "Compact history now, keeping the last N messages raw", NeedsCompaction: true},
	{Name: CmdDream, Usage: "/ctx-dream [task]", Description: "Run memory maintenance (dreamer) now"},
}

// IsCommand reports whether name (with or without "/") is a /ctx-* command.
func IsCommand(name string) bool {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	for _, c := range Commands {
		if c.Name == name {
			return true
		}
	}
	return false
}

// CommandInput is one /ctx-* invocation.
type CommandInput struct {
	Name      string
	Args      string
	SessionID string
	// Messages is the full session history (oldest first).
	Messages     []message.Message
	ContextLimit int
	// Notify shows a user-facing notice for long-running commands (progress
	// and the final result). It must be safe to call from any goroutine.
	Notify func(text string)
}

const (
	statusUnavailable = "Magic Context status is temporarily unavailable. Retry /ctx-status in a moment. (MC-S01)"
	dreamUnknown      = "Memory maintenance could not finish. Run /ctx-dream again. (MC-D07)"

	recompConfirmWindow = 60 * time.Second
	recompUsage         = "Usage:\n" +
		"- `/ctx-recomp` — full rebuild from message 1 to the protected tail\n" +
		"- `/ctx-recomp <start>-<end>` — partial rebuild of a message range (e.g. `/ctx-recomp 1-11322`)\n" +
		"- `/ctx-recomp --upgrade` — upgrade legacy v1 compartments to v2 layout (Wave 3 runner)"
	wrapupDefaultKeep = 20
	wrapupUsage       = "Usage: `/ctx-wrapup [messages_to_keep]` where messages_to_keep is a positive integer."
)

var (
	recompRangeRe = regexp.MustCompile(`^(\d+)\s*-\s*(\d+)$`)
	// Lease wait for /ctx-wrapup (tests shorten these).
	wrapupLeaseWait = 10 * time.Minute
	wrapupLeasePoll = time.Second
)

type recompConfirm struct {
	key string
	at  time.Time
}

// cmdState is the per-engine command bookkeeping.
type cmdState struct {
	running  map[string]string // session → "recomp" | "wrapup"
	confirms map[string]recompConfirm
}

func noCompactionReply(cmd string) string {
	return "Magic Context compaction is unavailable (no historian model configured) — /" + cmd + " manages compacted history and has no effect in this mode."
}

// RunCommand executes a /ctx-* command and returns the immediate reply.
// Replies are user notices only; they never reach the model. Long jobs
// (recomp, wrapup, dream) continue in the background and post their result
// through in.Notify.
func (e *Engine) RunCommand(ctx context.Context, in CommandInput) string {
	name := strings.TrimPrefix(strings.TrimSpace(in.Name), "/")
	args := strings.TrimSpace(in.Args)
	if !e.Enabled() {
		return "Magic Context is disabled. Enable it with `options.magic_context.enabled` in your config."
	}
	if in.Notify == nil {
		in.Notify = func(string) {}
	}
	switch name {
	case CmdStatus:
		return e.cmdStatus(ctx, in, args)
	case CmdFlush:
		if args != "" {
			return "Usage: `/ctx-flush` (no arguments)."
		}
		return e.cmdFlush(ctx, in.SessionID)
	case CmdRecomp:
		return e.cmdRecomp(ctx, in, args)
	case CmdWrapup:
		return e.cmdWrapup(ctx, in, args)
	case CmdDream:
		return e.cmdDream(ctx, in, args)
	}
	return fmt.Sprintf("Unknown Magic Context command `/%s`.", name)
}

// background runs fn detached from the caller's cancellation and posts its
// result through notify. syncCommands runs it inline (tests).
func (e *Engine) background(ctx context.Context, notify func(string), fn func(ctx context.Context) string) {
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				e.logf("magic-context: command panicked: %v", r)
			}
		}()
		if out := fn(context.WithoutCancel(ctx)); out != "" {
			notify(out)
		}
	}
	if e.syncCommands {
		run()
		return
	}
	go run()
}

// claimCommand marks a long command as running for the session; it returns
// the kind already running ("" = claimed).
func (e *Engine) claimCommand(sid, kind string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd.running == nil {
		e.cmd.running = map[string]string{}
	}
	if k, ok := e.cmd.running[sid]; ok {
		return k
	}
	e.cmd.running[sid] = kind
	return ""
}

func (e *Engine) releaseCommand(sid string) {
	e.mu.Lock()
	delete(e.cmd.running, sid)
	e.mu.Unlock()
}

func (e *Engine) commandRunning(sid string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cmd.running[sid]
}

// cmdView is the shared session snapshot commands work from.
type cmdView struct {
	raw    []RawMessage
	ords   map[string]int
	meta   SessionMeta
	input  int64
	limit  int
	pct    float64
	window Window
}

func (e *Engine) loadView(ctx context.Context, sid string, msgs []message.Message, ctxLimit int) (cmdView, error) {
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	var v cmdView
	ords, err := e.Store.EnsureOrdinals(ctx, sid, ids)
	if err != nil {
		return v, err
	}
	meta, err := e.Store.Meta(ctx, sid)
	if err != nil {
		return v, err
	}
	v.ords, v.meta = ords, meta
	v.raw = BuildRawView(msgs, ords)
	v.input = meta.LastInputTokens
	v.limit = ResolveContextLimit(ctxLimit, v.input, meta.LastContextPercentage)
	if v.input > 0 && v.limit > 0 {
		v.pct = float64(v.input) / float64(v.limit) * 100
	}
	v.window = e.Settings.WindowFor(v.limit)
	return v, nil
}

// ---------------------------------------------------------------------------
// /ctx-status

func fmtInt(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func fmtBytes(n int) string {
	switch {
	case n < 1024:
		return strconv.Itoa(n) + "B"
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmtInt(int64(n)) + " " + word + "s"
}

func fmtTTL(d time.Duration) string {
	switch {
	case d < 0:
		return "never"
	case d == 0:
		return "0s"
	case d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d%time.Minute == 0:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
}

// cacheLifetime is the user-facing cache TTL line.
func (e *Engine) cacheLifetime() string {
	if e.Settings.CacheTTL < 0 {
		return "never (external cache-keep)"
	}
	return fmtTTL(e.Settings.CacheTTL)
}

func (e *Engine) cmdStatus(ctx context.Context, in CommandInput, args string) string {
	mode := strings.ToLower(args)
	if mode != "" && mode != "diagnostics" {
		return "Usage: `/ctx-status [diagnostics]`."
	}
	out, err := e.statusSummary(ctx, in)
	if err != nil {
		e.logf("magic-context: status failed session=%s: %v", in.SessionID, err)
		return statusUnavailable
	}
	if mode == "diagnostics" {
		diag, err := e.statusDiagnostics(ctx, in)
		if err != nil {
			e.logf("magic-context: status diagnostics failed session=%s: %v", in.SessionID, err)
			return out + "\n\nError: " + statusUnavailable
		}
		out += "\n\n" + diag
	}
	return out
}

func (e *Engine) statusSummary(ctx context.Context, in CommandInput) (string, error) {
	sid := in.SessionID
	v, err := e.loadView(ctx, sid, in.Messages, in.ContextLimit)
	if err != nil {
		return "", err
	}
	cs, err := e.Store.Compartments(ctx, sid)
	if err != nil {
		return "", err
	}
	limit := "?"
	if v.limit > 0 {
		limit = fmtInt(int64(v.limit))
	}
	lines := []string{"## Magic Context Status", ""}
	add := func(label, value string) { lines = append(lines, "- **"+label+":** "+value) }
	add("Context", fmt.Sprintf("%.1f%% of usable context (%s / %s tokens)", v.pct, fmtInt(v.input), limit))
	add("Cache lifetime", e.cacheLifetime())
	if e.Compaction() {
		add("Automatic compression", fmt.Sprintf("at %.1f%% of usable context", v.window.Exec))
	} else {
		add("Automatic compression", "Off")
	}
	blocks := plural(len(cs), "history block")
	switch {
	case !e.Compaction():
		add("History compression", "Off")
	case v.meta.CompartmentInProgress || e.HistorianRunning(sid) || e.commandRunning(sid) != "":
		add("History compression", "Compressing history · "+blocks)
	case len(cs) > 0:
		add("History compression", "Ready · "+blocks)
	default:
		add("History compression", "Waiting for enough conversation history")
	}
	st, _ := e.Stats(sid)
	if st.ReclaimableCount <= 0 {
		add("Reclaimable", "none")
	} else {
		add("Reclaimable", fmt.Sprintf("%s (~%dk tokens)", plural(st.ReclaimableCount, "spent tool output"), int(math.Round(float64(max(0, st.Reclaimable))/1000))))
	}
	memCount := 0
	if e.ProjectKey != "" {
		mems, err := e.Store.ProjectMemories(ctx, e.ProjectKey)
		if err != nil {
			return "", err
		}
		memCount = len(mems)
	}
	notes, err := e.Store.GetNotes(ctx, NoteQuery{SessionID: sid, Type: NoteTypeSession, Statuses: []string{NoteStatusActive}})
	if err != nil {
		return "", err
	}
	noteCount := len(notes)
	if e.ProjectKey != "" {
		smart, err := e.Store.GetNotes(ctx, NoteQuery{ProjectPath: e.ProjectKey, Type: NoteTypeSmart, Statuses: []string{NoteStatusReady}})
		if err != nil {
			return "", err
		}
		noteCount += len(smart)
	}
	add("Memory", fmt.Sprintf("%s memories · %s notes", fmtInt(int64(memCount)), fmtInt(int64(noteCount))))
	add("Search indexing", e.indexingText(ctx, memCount))
	return strings.Join(lines, "\n"), nil
}

// indexingText reports embedding coverage of the project's memories.
func (e *Engine) indexingText(ctx context.Context, total int) string {
	if e.Settings.EmbeddingEndpoint == "" || e.Settings.EmbeddingModel == "" || e.ProjectKey == "" {
		return "Off"
	}
	var indexed int
	err := e.Store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mc_memory_embeddings e JOIN mc_memories m ON m.id = e.memory_id
WHERE e.model_id = ? AND m.project_path = ? AND m.status IN ('active','permanent') AND m.superseded_by_memory_id = 0
AND (m.expires_at = 0 OR m.expires_at > ?)`,
		e.Settings.EmbeddingModel, e.ProjectKey, nowMS()).Scan(&indexed)
	if err != nil {
		return "Off"
	}
	coverage := fmt.Sprintf("%s / %s memories indexed", fmtInt(int64(indexed)), fmtInt(int64(total)))
	if indexed >= total {
		return "Ready · " + coverage
	}
	return "Waiting · " + coverage
}

func (e *Engine) statusDiagnostics(ctx context.Context, in CommandInput) (string, error) {
	sid := in.SessionID
	v, err := e.loadView(ctx, sid, in.Messages, in.ContextLimit)
	if err != nil {
		return "", err
	}
	tags, err := e.Store.LoadTags(ctx, sid)
	if err != nil {
		return "", err
	}
	ops, err := e.Store.PendingOps(ctx, sid)
	if err != nil {
		return "", err
	}
	cs, err := e.Store.Compartments(ctx, sid)
	if err != nil {
		return "", err
	}
	now := e.now()
	maxTag, active, activeBytes, dropped := 0, 0, 0, 0
	byID := make(map[int64]Tag, len(tags))
	for _, t := range tags {
		byID[t.ID] = t
		maxTag = max(maxTag, t.Number)
		switch t.Status {
		case TagActive:
			active++
			activeBytes += t.ByteSize
		case TagDropped:
			dropped++
		}
	}
	drops := 0
	for _, op := range ops {
		if op.Operation == "drop" {
			drops++
		}
	}
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("## Magic Status")
	w("")
	w("**Session:** %s", sid)
	w("**Tag counter:** %d", maxTag)
	w("")
	w("### Tags")
	w("- Active: %s (~%s)", fmtInt(int64(active)), fmtBytes(activeBytes))
	w("- Dropped: %s", fmtInt(int64(dropped)))
	w("- Total: %s", fmtInt(int64(len(tags))))
	w("")
	w("### Pending Queue")
	w("- Drops: %d", drops)
	w("- Total queued: %d", len(ops))
	w("")
	w("### Cache TTL")
	w("- Cache TTL: %s", e.cacheLifetime())
	if v.meta.LastResponseTime > 0 {
		w("- Last response: %ds ago", max(0, (now.UnixMilli()-v.meta.LastResponseTime)/1000))
	} else {
		w("- Last response: never")
	}
	ttl := e.Settings.CacheTTL
	switch {
	case ttl < 0:
		w("- Remaining: never (MC never assumes expiry — external cache-keep)")
	case v.meta.LastResponseTime == 0 || e.cacheExpired(v.meta, now):
		w("- Remaining: expired")
	default:
		rem := (v.meta.LastResponseTime + ttl.Milliseconds() - now.UnixMilli()) / 1000
		w("- Remaining: %ds", max(0, rem))
	}
	switch {
	case e.cacheExpired(v.meta, now):
		w("- Queue will auto-execute: yes (cache expired)")
	case ttl < 0:
		w("- Queue will auto-execute: when context >= %s%%", fmtPct(v.window.Exec))
	default:
		w("- Queue will auto-execute: when TTL expires or context >= %s%%", fmtPct(v.window.Exec))
	}
	w("")
	w("### Execute Threshold")
	switch {
	case e.Settings.ExecuteThresholdTokens > 0 && v.limit > 0:
		w("- Execute threshold: %s tokens (%.1f%% of %s) [token-mode]", fmtInt(int64(e.Settings.ExecuteThresholdTokens)), v.window.Exec, fmtInt(int64(v.limit)))
	case v.limit > 0:
		w("- Execute threshold: %s%% (%s of %s)", fmtPct(v.window.Exec), fmtInt(int64(v.window.Usable)), fmtInt(int64(v.limit)))
	default:
		w("- Execute threshold: %s%%", fmtPct(v.window.Exec))
	}
	w("- Last input tokens: %s tokens", fmtInt(v.input))
	if st, ok := e.Stats(sid); ok && st.TailTokens > 0 {
		w("")
		w("### Tail Hygiene")
		w("- Reclaimable / eligible: %.1f%% · %s / %s tok", float64(st.Reclaimable)/float64(st.TailTokens)*100, fmtInt(int64(st.Reclaimable)), fmtInt(int64(st.TailTokens)))
		w("- Reasoning is excluded from both terms.")
	}
	if v.pct > 0 || v.input > 0 {
		w("")
		w("### Context Usage")
		w("- Last percentage: %.1f%%", v.pct)
		w("- Last input tokens: %s", fmtInt(v.input))
		if v.limit > 0 {
			w("- Resolved context limit: %s", fmtInt(int64(v.limit)))
		} else {
			w("- Resolved context limit: unknown")
		}
		w("- Proactive compartment evaluation: %s%%", fmtPct(math.Max(0, v.window.Exec-proactiveTriggerOffset)))
		w("- Post-drop target for historian: %d%% (%s%% * %.2f)", int(math.Round(v.window.Exec*postDropTargetRatio)), fmtPct(v.window.Exec), postDropTargetRatio)
		cluster := "disabled"
		if e.Settings.CommitClusters {
			cluster = fmt.Sprintf("enabled (min %d clusters)", e.Settings.MinCommitClusters)
		}
		w("- Commit cluster trigger: %s, tail-size trigger: > 3x compartment budget", cluster)
	}
	w("")
	w("### History Compression")
	w("- Compartments: %d", len(cs))
	block := 0
	for _, c := range cs {
		block += EstimateTokens(fmt.Sprintf("## %d-%d · %s\n%s\n", c.StartOrdinal, c.EndOrdinal, c.Title, c.Content))
	}
	w("- History block: ~%s tokens", fmtInt(int64(block)))
	if e.Settings.HistoryBudget > 0 && v.limit > 0 {
		budget := int(math.Floor(float64(v.limit) * math.Min(v.window.Exec, 80) / 100 * e.Settings.HistoryBudget))
		used := 0
		if budget > 0 {
			used = int(math.Round(float64(block) / float64(budget) * 100))
		}
		w("- History budget: ~%s tokens (%d%% used)", fmtInt(int64(budget)), used)
		w("- Older compartments demote tiers automatically at render time to fit the budget")
	} else {
		w("- History budget: not configured (history_budget_percentage not set)")
	}
	if len(ops) > 0 {
		w("")
		w("### Queued Operations")
		for _, op := range ops {
			if t, ok := byID[op.TagID]; ok {
				w("- §%d§ → %s", t.Number, op.Operation)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func fmtPct(p float64) string {
	if p == math.Trunc(p) {
		return strconv.Itoa(int(p))
	}
	return strconv.FormatFloat(p, 'f', 1, 64)
}

// ---------------------------------------------------------------------------
// /ctx-flush

func (e *Engine) cmdFlush(ctx context.Context, sid string) string {
	ops, err := e.Store.PendingOps(ctx, sid)
	if err != nil {
		return "Error: Failed to flush context operations. " + err.Error()
	}
	cancelled, err := e.cancelStagedRecomp(ctx, sid)
	if err != nil {
		return "Error: Failed to flush context operations. " + err.Error()
	}
	note := ""
	if cancelled {
		note = "\nCancelled the unfinished staged recomp."
	}
	if len(ops) == 0 {
		return "No pending operations to flush." + note
	}
	tags, err := e.Store.LoadTags(ctx, sid)
	if err != nil {
		return "Error: Failed to flush context operations. " + err.Error()
	}
	byID := make(map[int64]Tag, len(tags))
	for _, t := range tags {
		byID[t.ID] = t
	}
	drops := map[int64]string{}
	for _, op := range ops {
		t, ok := byID[op.TagID]
		if !ok || op.Operation != "drop" {
			continue
		}
		switch t.Type {
		case TagMessage:
			drops[t.ID] = DropModeText
		case TagFile:
			drops[t.ID] = DropModeFile
		default:
			drops[t.ID] = DropModeFull
		}
	}
	err = e.Store.Immediate(ctx, func(q querier) error {
		if err := e.Store.MarkTagsDropped(ctx, q, sid, drops); err != nil {
			return err
		}
		// Ops whose tag vanished are cleared too.
		if _, err := q.ExecContext(ctx, `DELETE FROM mc_pending_ops WHERE session_id = ?`, sid); err != nil {
			return err
		}
		return e.Store.BumpRenderEpoch(ctx, q, sid)
	})
	if err != nil {
		return "Error: Failed to flush context operations. " + err.Error()
	}
	return fmt.Sprintf("Flushed: %d dropped. Changes take effect on next message.", len(drops)) + note
}

// cancelStagedRecomp clears an unfinished recomp's staging rows.
func (e *Engine) cancelStagedRecomp(ctx context.Context, sid string) (bool, error) {
	if e.commandRunning(sid) == "recomp" {
		return false, nil
	}
	staged, err := e.Store.RecompCompartments(ctx, sid)
	if err != nil || len(staged) == 0 {
		return false, err
	}
	err = e.Store.Immediate(ctx, func(q querier) error {
		if err := e.Store.DeleteCompartments(ctx, q, sid, true); err != nil {
			return err
		}
		return e.Store.setMeta(ctx, q, sid, map[string]any{"recomp_partial_range": "", "recomp_pass_count": 0})
	})
	return err == nil, err
}

// ---------------------------------------------------------------------------
// /ctx-recomp

type recompArgs struct {
	partial    bool
	start, end int
	key        string
}

func parseRecompArgs(args string) (recompArgs, string) {
	if args == "" {
		return recompArgs{}, ""
	}
	m := recompRangeRe.FindStringSubmatch(args)
	if m == nil {
		return recompArgs{}, fmt.Sprintf("Invalid /ctx-recomp arguments: `%s`.\n\n%s", args, recompUsage)
	}
	s, err1 := strconv.Atoi(m[1])
	en, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return recompArgs{}, fmt.Sprintf("Invalid /ctx-recomp arguments: `%s`.\n\n%s", args, recompUsage)
	}
	if s < 1 {
		return recompArgs{}, fmt.Sprintf("Start must be >= 1 (got %d).", s)
	}
	if en < s {
		return recompArgs{}, fmt.Sprintf("End must be >= start (got %d-%d).", s, en)
	}
	return recompArgs{partial: true, start: s, end: en, key: fmt.Sprintf("%d-%d", s, en)}, ""
}

func (e *Engine) cmdRecomp(ctx context.Context, in CommandInput, args string) string {
	sid := in.SessionID
	if args == "--upgrade" {
		return "## Magic Recomp Upgrade\n\nNothing to upgrade: this session has no legacy compartments."
	}
	ra, bad := parseRecompArgs(args)
	if bad != "" {
		return "## Magic Recomp — Invalid Arguments\n\n" + bad
	}
	if !e.Compaction() {
		return noCompactionReply(CmdRecomp)
	}
	if e.commandRunning(sid) != "" || e.HistorianRunning(sid) {
		return "## Magic Recomp — Skipped\n\nHistory compression is already running for this session. Wait for it to finish, then try /ctx-recomp again."
	}
	if !e.consumeConfirm(sid, ra.key) {
		cs, err := e.Store.Compartments(ctx, sid)
		if err != nil {
			e.dropConfirm(sid)
			return "## Magic Recomp — Failed\n\n" + failRecompUnavailable
		}
		if !ra.partial {
			return recompConfirmText(len(cs))
		}
		snap, err := snapRange(cs, ra.start, ra.end)
		if err != nil {
			e.dropConfirm(sid)
			return "## Magic Recomp — Failed\n\n" + err.Error()
		}
		return partialConfirmText(ra.key, snap)
	}
	if k := e.claimCommand(sid, "recomp"); k != "" {
		return "## Magic Recomp — Skipped\n\nHistory compression is already running for this session. Wait for it to finish, then try /ctx-recomp again."
	}
	msgs, limit := in.Messages, in.ContextLimit
	started := "## Magic Recomp\n\nRecomp started. Rebuilding the compressed history from raw session history now; saved memories are kept as they are."
	if ra.partial {
		started = fmt.Sprintf("## Magic Recomp\n\nPartial recomp started for range %s. Rebuilding the matching compartments now (facts unchanged).", ra.key)
	}
	e.background(ctx, in.Notify, func(ctx context.Context) string {
		defer e.releaseCommand(sid)
		if ra.partial {
			return e.partialRecomp(ctx, sid, msgs, limit, ra, in.Notify)
		}
		return e.fullRecomp(ctx, sid, msgs, limit, in.Notify)
	})
	if e.syncCommands {
		return ""
	}
	return started
}

// consumeConfirm reports whether a matching confirmation is pending (and
// clears it); otherwise it records a new pending confirmation.
func (e *Engine) consumeConfirm(sid, key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd.confirms == nil {
		e.cmd.confirms = map[string]recompConfirm{}
	}
	now := e.now()
	if c, ok := e.cmd.confirms[sid]; ok && c.key == key && now.Sub(c.at) <= recompConfirmWindow {
		delete(e.cmd.confirms, sid)
		return true
	}
	e.cmd.confirms[sid] = recompConfirm{key: key, at: now}
	return false
}

func (e *Engine) dropConfirm(sid string) {
	e.mu.Lock()
	delete(e.cmd.confirms, sid)
	e.mu.Unlock()
}

func recompConfirmText(n int) string {
	return strings.Join([]string{
		"## ⚠️ Recomp Confirmation Required",
		"",
		fmt.Sprintf("You currently have **%d** compartments.", n),
		"Running /ctx-recomp will **rebuild the compressed history** from raw session history. Saved memories are not changed.",
		"",
		"This operation:",
		"- May take a long time (minutes to hours for long sessions)",
		"- Will consume significant tokens on your historian model",
		"- Cannot be interrupted cleanly once started",
		"",
		"Tip: to rebuild only a specific message range, use `/ctx-recomp <start>-<end>` (e.g. `/ctx-recomp 1-11322`).",
		"",
		"**To confirm, run `/ctx-recomp` again within 60 seconds.**",
	}, "\n")
}

func partialConfirmText(key string, s snappedRange) string {
	return strings.Join([]string{
		"## ⚠️ Partial Recomp Confirmation Required",
		"",
		"Requested range: `" + key + "`",
		fmt.Sprintf("Snapped to compartment boundaries: **messages %d-%d**", s.start, s.end),
		"",
		fmt.Sprintf("This will **rebuild %d compartment(s)** in the snapped range.", len(s.inRange)),
		fmt.Sprintf("**%d compartment(s)** outside the range will be preserved unchanged.", len(s.prior)+len(s.tail)),
		"Facts will not be re-extracted.",
		"",
		"This operation:",
		"- May take several minutes to tens of minutes depending on range size",
		"- Will consume historian-model tokens for each chunk",
		"- Is resumable if interrupted (staging preserved on failure)",
		"",
		"**To confirm, run `/ctx-recomp " + key + "` again within 60 seconds.**",
	}, "\n")
}

type snappedRange struct {
	start, end           int
	prior, inRange, tail []Compartment
}

// snapRange widens [start,end] to whole compartments.
func snapRange(cs []Compartment, start, end int) (snappedRange, error) {
	cs = append([]Compartment(nil), cs...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].Sequence < cs[j].Sequence })
	if len(cs) == 0 {
		return snappedRange{}, errors.New("No compartments exist yet for this session. Run `/ctx-recomp` (full) first, then use partial recomp to refine specific ranges.")
	}
	if last := cs[len(cs)-1].EndOrdinal; start > last {
		return snappedRange{}, fmt.Errorf("Range %d-%d starts after the last compartment (which ends at message %d). Nothing to rebuild.", start, end, last)
	}
	first, lastIdx := -1, -1
	for i, c := range cs {
		if first < 0 && c.EndOrdinal >= start {
			first = i
		}
		if c.StartOrdinal <= end {
			lastIdx = i
		}
	}
	if first < 0 || lastIdx < first {
		return snappedRange{}, fmt.Errorf("Range %d-%d does not overlap any compartment.", start, end)
	}
	return snappedRange{
		start: cs[first].StartOrdinal, end: cs[lastIdx].EndOrdinal,
		prior: cs[:first], inRange: cs[first : lastIdx+1], tail: cs[lastIdx+1:],
	}, nil
}

// leaseGuard holds a compartment lease and renews it until released.
type leaseGuard struct {
	e      *Engine
	sid    string
	holder string
	stop   chan struct{}
}

func (e *Engine) acquireLease(ctx context.Context, sid string) (*leaseGuard, error) {
	holder := newLeaseHolder()
	ok, err := e.Store.AcquireLease(ctx, sid, holder, historianLeaseTTL)
	if err != nil || !ok {
		return nil, err
	}
	g := &leaseGuard{e: e, sid: sid, holder: holder, stop: make(chan struct{})}
	go func() {
		t := time.NewTicker(historianLeaseTTL / 3)
		defer t.Stop()
		for {
			select {
			case <-g.stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_ = e.Store.RenewLease(ctx, sid, holder, historianLeaseTTL)
			}
		}
	}()
	return g, nil
}

func (g *leaseGuard) release(ctx context.Context) {
	close(g.stop)
	_ = g.e.Store.ReleaseLease(context.WithoutCancel(ctx), g.sid, g.holder)
}

// recompFailed logs err and returns the generic recomp failure reply.
func (e *Engine) recompFailed(sid string, err error) string {
	e.logf("magic-context: recomp failed session=%s: %v", sid, err)
	return "## Magic Recomp — Failed\n\n" + failRecompUnavailable
}

func lastEndOf(cs []Compartment) int {
	if len(cs) == 0 {
		return 0
	}
	return cs[len(cs)-1].EndOrdinal
}

var errPromoteLeaseLost = errors.New("lease lost before promote")

// promoteStaged swaps the staged compartment set in as the live one.
func (e *Engine) promoteStaged(ctx context.Context, sid, holder string, set []Compartment, raw []RawMessage, dropFacts bool) error {
	set = append([]Compartment(nil), set...)
	for i := range set {
		set[i].ID = 0
	}
	last := set[len(set)-1]
	var compacted []string
	for _, m := range raw {
		if m.Ordinal <= last.EndOrdinal {
			compacted = append(compacted, m.ID)
		}
	}
	return e.Store.Immediate(ctx, func(q querier) error {
		held, err := e.Store.LeaseHeld(ctx, q, sid, holder)
		if err != nil {
			return err
		}
		if !held {
			return errPromoteLeaseLost
		}
		if err := e.Store.DeleteCompartments(ctx, q, sid, false); err != nil {
			return err
		}
		if dropFacts {
			if _, err := q.ExecContext(ctx, `DELETE FROM mc_session_facts WHERE session_id = ?`, sid); err != nil {
				return err
			}
		}
		if _, err := e.Store.InsertCompartments(ctx, q, sid, false, set); err != nil {
			return err
		}
		if err := e.Store.DeleteCompartments(ctx, q, sid, true); err != nil {
			return err
		}
		if err := e.Store.MarkTagsCompacted(ctx, q, sid, compacted); err != nil {
			return err
		}
		if err := e.Store.BumpRenderEpoch(ctx, q, sid); err != nil {
			return err
		}
		pending, _ := json.Marshal(PendingMaterialization{End: last.EndOrdinal, Seq: last.Sequence, EndMessageID: last.EndMessageID, PublishedAt: nowMS()})
		return e.Store.setMeta(ctx, q, sid, map[string]any{
			"cached_m0":            "",
			"cached_m1":            "",
			"materialize_pending":  string(pending),
			"recomp_pass_count":    0,
			"recomp_partial_range": "",
		})
	})
}

// beginRecomp acquires the lease and marks compression in progress; the
// returned func undoes both.
func (e *Engine) beginRecomp(ctx context.Context, sid string) (*leaseGuard, func(), error) {
	g, err := e.acquireLease(ctx, sid)
	if err != nil || g == nil {
		return nil, nil, err
	}
	_ = e.Store.SetMeta(ctx, sid, map[string]any{"compartment_in_progress": true})
	return g, func() {
		_ = e.Store.SetMeta(context.WithoutCancel(ctx), sid, map[string]any{"compartment_in_progress": false})
		g.release(ctx)
	}, nil
}

func (e *Engine) fullRecomp(ctx context.Context, sid string, msgs []message.Message, ctxLimit int, notify func(string)) string {
	g, done, err := e.beginRecomp(ctx, sid)
	if err != nil {
		e.logf("magic-context: recomp lease failed session=%s: %v", sid, err)
	}
	if g == nil {
		return "## Magic Recomp — Skipped\n\nCould not acquire the compartment-state lease for this session."
	}
	defer done()
	v, err := e.loadView(ctx, sid, msgs, ctxLimit)
	if err != nil {
		return e.recompFailed(sid, err)
	}
	if len(v.raw) == 0 {
		return "## Magic Recomp\n\nNo raw history exists, so nothing was rebuilt."
	}
	count := NewTokenIndex(v.raw).Count()
	b := ResolveProtectedTail(TailRequest{Raw: v.raw, Window: v.window, UsagePct: v.pct, FullRecomp: true})
	pts := min(b.ProtectedTailStart, count+1)

	staged, err := e.Store.RecompCompartments(ctx, sid)
	if err != nil {
		return e.recompFailed(sid, err)
	}
	resumed := false
	passCount := 0
	if len(staged) > 0 && v.meta.RecompPartialRange == "" {
		resumed = true
		passCount = v.meta.RecompPassCount
		notify(fmt.Sprintf("## Magic Recomp — Resumed\n\nFound %d staged compartment(s) from %d previous pass(es), covering messages 1-%d. Resuming from message %d.",
			len(staged), passCount, lastEndOf(staged), lastEndOf(staged)+1))
	} else {
		err := e.Store.Immediate(ctx, func(q querier) error {
			if err := e.Store.DeleteCompartments(ctx, q, sid, true); err != nil {
				return err
			}
			return e.Store.setMeta(ctx, q, sid, map[string]any{"recomp_pass_count": 0, "recomp_partial_range": ""})
		})
		if err != nil {
			return e.recompFailed(sid, err)
		}
		staged = nil
	}

	defaultBudget := HistorianChunkTokens(e.Historian.contextWindow())
	budget, attempt := 0, 1
	for {
		offset := lastEndOf(staged) + 1
		if offset >= pts {
			break
		}
		notify(fmt.Sprintf("## Magic Recomp\n\nHistorian pass %d, attempt %d started for messages %d-%d.", passCount+1, attempt, offset, pts-1))
		res, err := e.Historian.Run(ctx, HistorianRun{
			SessionID: sid, Raw: v.raw, Recomp: true, Holder: g.holder, ChunkBudget: budget,
			Boundary: TailBoundary{Offset: offset, ProtectedTailStart: pts, EligibleEnd: pts},
		})
		if err != nil {
			e.logf("magic-context: recomp pass failed session=%s: %v", sid, err)
			cur := budget
			if cur == 0 {
				cur = defaultBudget
			}
			if nb := reducedRecompTokenBudget(cur); nb > 0 && ctx.Err() == nil && !errors.Is(err, errLeaseLost) {
				budget = nb
				attempt++
				notify("## Magic Recomp\n\nHistory compression is retrying with a smaller set of messages. " + failRecompUnavailable)
				continue
			}
			if len(staged) > 0 {
				if out, ok := e.finishFullRecomp(ctx, sid, g.holder, v.raw, count, pts, passCount, resumed); !ok {
					return out
				}
				return fmt.Sprintf("## Magic Recomp — Partial\n\nRebuilt %d history block(s) across %d successful pass(es).\n%s", len(staged), passCount, failRecompUnavailable)
			}
			return "## Magic Recomp — Failed\n\n" + failRecompUnavailable
		}
		if res.Skipped {
			if len(staged) > 0 {
				out, _ := e.finishFullRecomp(ctx, sid, g.holder, v.raw, count, pts, passCount, resumed)
				return out
			}
			return fmt.Sprintf("## Magic Recomp — Failed\n\nRecomp stopped because raw history %d-%d could not be turned into a valid historian chunk. Nothing was written.", offset, pts-1)
		}
		if res.LastEnd < offset {
			return fmt.Sprintf("## Magic Recomp — Failed\n\nRecomp made no forward progress after messages %d-%d. Nothing was written.", offset, pts-1)
		}
		passCount++
		_ = e.Store.SetMeta(ctx, sid, map[string]any{"recomp_pass_count": passCount})
		budget, attempt = 0, 1
		if staged, err = e.Store.RecompCompartments(ctx, sid); err != nil {
			return e.recompFailed(sid, err)
		}
	}
	if len(staged) == 0 {
		return "## Magic Recomp\n\nNo raw history exists before the protected tail, so nothing was rebuilt."
	}
	out, _ := e.finishFullRecomp(ctx, sid, g.holder, v.raw, count, pts, passCount, resumed)
	return out
}

// finishFullRecomp validates and promotes the staged set; ok is false when
// it failed (out is then the failure reply).
func (e *Engine) finishFullRecomp(ctx context.Context, sid, holder string, raw []RawMessage, count, pts, passCount int, resumed bool) (string, bool) {
	staged, err := e.Store.RecompCompartments(ctx, sid)
	if err != nil || len(staged) == 0 {
		return "## Magic Recomp — Failed\n\n" + failRecompUnavailable, false
	}
	if msg := ValidateStoredCompartments(staged, NonNarrativeRanges(raw)); msg != "" {
		e.logf("magic-context: recomp validation failed session=%s: %s", sid, msg)
		_ = e.Store.Immediate(ctx, func(q querier) error {
			if err := e.Store.DeleteCompartments(ctx, q, sid, true); err != nil {
				return err
			}
			return e.Store.setMeta(ctx, q, sid, map[string]any{"recomp_pass_count": 0})
		})
		return "## Magic Recomp — Failed\n\n" + failRecompUnavailable, false
	}
	if err := e.promoteStaged(ctx, sid, holder, staged, raw, true); err != nil {
		if errors.Is(err, errPromoteLeaseLost) {
			return "## Magic Recomp — Skipped\n\nAnother process acquired the compartment-state lease before recomp could publish. No state was written.", false
		}
		e.logf("magic-context: recomp promote failed session=%s: %v", sid, err)
		return "## Magic Recomp — Failed\n\n" + failRecompUnavailable, false
	}
	var b strings.Builder
	b.WriteString("## Magic Recomp — Complete\n\n")
	if resumed {
		b.WriteString("Resumed from previous interrupted run.\n")
	}
	fmt.Fprintf(&b, "Rebuilt %d compartment(s) across %d historian pass(es).\n", len(staged), passCount)
	fmt.Fprintf(&b, "Covered raw history 1-%d out of %d total messages, stopping before protected tail at %d.", lastEndOf(staged), count, pts)
	return b.String(), true
}

func resequence(cs []Compartment, from int) []Compartment {
	out := make([]Compartment, len(cs))
	for i, c := range cs {
		c.ID = 0
		c.Sequence = from + i
		out[i] = c
	}
	return out
}

func (e *Engine) partialRecomp(ctx context.Context, sid string, msgs []message.Message, ctxLimit int, ra recompArgs, notify func(string)) string {
	g, done, err := e.beginRecomp(ctx, sid)
	if err != nil {
		e.logf("magic-context: recomp lease failed session=%s: %v", sid, err)
	}
	if g == nil {
		return "## Magic Recomp — Skipped\n\nCould not acquire the compartment-state lease for this session."
	}
	defer done()
	v, err := e.loadView(ctx, sid, msgs, ctxLimit)
	if err != nil {
		return e.recompFailed(sid, err)
	}
	live, err := e.Store.Compartments(ctx, sid)
	if err != nil {
		return e.recompFailed(sid, err)
	}
	snap, err := snapRange(live, ra.start, ra.end)
	if err != nil {
		return "## Magic Recomp — Failed\n\n" + err.Error()
	}
	staged, err := e.Store.RecompCompartments(ctx, sid)
	if err != nil {
		return e.recompFailed(sid, err)
	}
	resumed := false
	passCount := 0
	offset := snap.start
	switch {
	case len(staged) > 0 && v.meta.RecompPartialRange != "" && v.meta.RecompPartialRange != ra.key:
		return fmt.Sprintf("## Magic Recomp — Failed\n\nAn unfinished partial recomp is already staged for range %s, which does not match the requested range %s.\n\nResume that range by running `/ctx-recomp` with the same original arguments,\nor cancel it by running `/ctx-flush` before starting a new partial recomp.", v.meta.RecompPartialRange, ra.key)
	case len(staged) > 0 && v.meta.RecompPartialRange == "":
		return "## Magic Recomp — Failed\n\nAn unfinished full recomp is already staged for this session.\nResume it by running `/ctx-recomp` without arguments,\nor cancel it before starting a partial recomp."
	case len(staged) > 0:
		resumed = true
		passCount = v.meta.RecompPassCount
		if le := lastEndOf(staged); le >= snap.start {
			offset = le + 1
		}
		built := 0
		for _, c := range staged {
			if c.StartOrdinal >= snap.start {
				built++
			}
		}
		notify(fmt.Sprintf("## Magic Recomp — Resumed (Partial)\n\nFound %d newly built compartment(s) from %d previous pass(es), covering messages %d-%d. Resuming from message %d toward %d.",
			built, passCount, snap.start, offset-1, offset, snap.end))
	default:
		prior := resequence(snap.prior, 0)
		err := e.Store.Immediate(ctx, func(q querier) error {
			if err := e.Store.DeleteCompartments(ctx, q, sid, true); err != nil {
				return err
			}
			if len(prior) > 0 {
				if _, err := e.Store.InsertCompartments(ctx, q, sid, true, prior); err != nil {
					return err
				}
			}
			return e.Store.setMeta(ctx, q, sid, map[string]any{"recomp_partial_range": ra.key, "recomp_pass_count": 0})
		})
		if err != nil {
			return e.recompFailed(sid, err)
		}
		notify(fmt.Sprintf("## Magic Recomp — Partial\n\nSnapped to compartment boundaries: rebuilding messages %d-%d (%d tail compartment(s) preserved).", snap.start, snap.end, len(snap.tail)))
	}

	defaultBudget := HistorianChunkTokens(e.Historian.contextWindow())
	budget, attempt := 0, 1
	for offset <= snap.end {
		notify(fmt.Sprintf("## Magic Recomp — Partial\n\nHistorian pass %d, attempt %d started for messages %d-%d.", passCount+1, attempt, offset, snap.end))
		res, err := e.Historian.Run(ctx, HistorianRun{
			SessionID: sid, Raw: v.raw, Recomp: true, Holder: g.holder, ChunkBudget: budget,
			Boundary: TailBoundary{Offset: offset, ProtectedTailStart: snap.end + 1, EligibleEnd: snap.end + 1},
		})
		if err != nil {
			e.logf("magic-context: partial recomp pass failed session=%s: %v", sid, err)
			cur := budget
			if cur == 0 {
				cur = defaultBudget
			}
			if nb := reducedRecompTokenBudget(cur); nb > 0 && ctx.Err() == nil && !errors.Is(err, errLeaseLost) {
				budget = nb
				attempt++
				notify("## Magic Recomp — Partial\n\nHistory compression is retrying with a smaller set of messages. " + failRecompUnavailable)
				continue
			}
			return "## Magic Recomp — Failed\n\n" + failRecompUnavailable
		}
		if res.Skipped {
			return fmt.Sprintf("## Magic Recomp — Failed\n\nRecomp stopped because raw history %d-%d could not be turned into a valid historian chunk. Partial recomp preserved original state (staging kept for retry).", offset, snap.end)
		}
		if res.LastEnd < offset {
			return fmt.Sprintf("## Magic Recomp — Failed\n\nPartial recomp made no forward progress after messages %d-%d. Staging kept for retry.", offset, snap.end)
		}
		passCount++
		_ = e.Store.SetMeta(ctx, sid, map[string]any{"recomp_pass_count": passCount})
		budget, attempt = 0, 1
		offset = res.LastEnd + 1
	}

	staged, err = e.Store.RecompCompartments(ctx, sid)
	if err != nil {
		return e.recompFailed(sid, err)
	}
	merged := append(resequence(staged, 0), resequence(snap.tail, len(staged))...)
	if len(merged) == 0 || ValidateStoredCompartments(merged, NonNarrativeRanges(v.raw)) != "" {
		return "## Magic Recomp — Failed\n\nPartial recomp completed historian passes but the final compartment set failed validation. Original state preserved (staging kept for inspection)."
	}
	if err := e.promoteStaged(ctx, sid, g.holder, merged, v.raw, false); err != nil {
		if errors.Is(err, errPromoteLeaseLost) {
			return "## Magic Recomp — Skipped\n\nAnother process acquired the compartment-state lease before recomp could publish. No state was written."
		}
		e.logf("magic-context: partial recomp promote failed session=%s: %v", sid, err)
		return "## Magic Recomp — Failed\n\n" + failRecompUnavailable
	}
	var b strings.Builder
	b.WriteString("## Magic Recomp — Partial Complete\n\n")
	if resumed {
		b.WriteString("Resumed from previous interrupted partial run.\n")
	}
	fmt.Fprintf(&b, "Rebuilt compartments covering messages %d-%d using %d historian pass(es).\n", snap.start, snap.end, passCount)
	fmt.Fprintf(&b, "Preserved %d prior compartment(s) and %d tail compartment(s) unchanged.\n", len(snap.prior), len(snap.tail))
	fmt.Fprintf(&b, "Total compartments: %d.", len(merged))
	return b.String()
}

// ---------------------------------------------------------------------------
// /ctx-wrapup

func parseWrapupArgs(args string) (int, string) {
	if args == "" {
		return wrapupDefaultKeep, ""
	}
	if strings.ContainsAny(args, " \t") {
		return 0, wrapupUsage
	}
	n, err := strconv.Atoi(args)
	if err != nil || n <= 0 {
		return 0, "messages_to_keep must be a positive integer."
	}
	return n, ""
}

func (e *Engine) cmdWrapup(ctx context.Context, in CommandInput, args string) string {
	sid := in.SessionID
	keep, bad := parseWrapupArgs(args)
	if bad != "" {
		return "## Magic Wrapup — Invalid Arguments\n\n" + bad
	}
	if !e.Compaction() {
		return noCompactionReply(CmdWrapup)
	}
	keep = max(1, keep)
	if k := e.commandRunning(sid); k == "wrapup" {
		return "## Magic Wrapup — Skipped\n\n/ctx-wrapup is already running for this session. Wait for it to finish, then run /ctx-wrapup again if more history remains."
	} else if k != "" {
		return "## Magic Wrapup — Skipped\n\nAnother Magic Context rebuild is already running for this session."
	}
	v, err := e.loadView(ctx, sid, in.Messages, in.ContextLimit)
	if err != nil {
		return "## Magic Wrapup — Failed\n\n" + err.Error()
	}
	cs, err := e.Store.Compartments(ctx, sid)
	if err != nil {
		return "## Magic Wrapup — Failed\n\n" + err.Error()
	}
	lastEnd := lastEndOf(cs)
	idx := NewTokenIndex(v.raw)
	count := idx.Count()
	plan := ResolveProtectedTail(TailRequest{Raw: v.raw, LastEnd: lastEnd, Window: v.window, UsagePct: v.pct, KeepLastRaw: keep})
	rawAbove := max(0, count-lastEnd)
	if rawAbove <= keep || !runnableWindow(plan) {
		return fmt.Sprintf("Nothing to wrap up — only %d messages above the last compartment.", rawAbove)
	}
	target := plan.EligibleEnd
	eligible := idx.Range(plan.Offset, target)
	expected := max(1, int(math.Ceil(float64(eligible)/float64(HistorianChunkTokens(e.Historian.contextWindow())))))
	if k := e.claimCommand(sid, "wrapup"); k != "" {
		return "## Magic Wrapup — Skipped\n\n/ctx-wrapup is already running for this session. Wait for it to finish, then run /ctx-wrapup again if more history remains."
	}
	started := fmt.Sprintf("Magic Wrapup started — compacting about %s of history. This can take a few minutes; the result posts here when done.", plural(expected, "chunk"))
	raw, window := v.raw, v.window
	e.background(ctx, in.Notify, func(ctx context.Context) string {
		defer e.releaseCommand(sid)
		return e.runWrapup(ctx, sid, raw, window, keep, lastEnd, len(cs), target)
	})
	if e.syncCommands {
		return ""
	}
	return started
}

// waitLease polls for the compartment lease up to wrapupLeaseWait.
func (e *Engine) waitLease(ctx context.Context, sid string) (*leaseGuard, error) {
	deadline := e.now().Add(wrapupLeaseWait)
	for {
		g, err := e.acquireLease(ctx, sid)
		if err != nil || g != nil {
			return g, err
		}
		if !e.now().Before(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wrapupLeasePoll):
		}
	}
}

func (e *Engine) runWrapup(ctx context.Context, sid string, raw []RawMessage, window Window, keep, startEnd, startCount, target int) string {
	if e.HistorianRunning(sid) {
		wctx, cancel := context.WithTimeout(ctx, wrapupLeaseWait)
		e.WaitHistorian(wctx, sid)
		cancel()
	}
	lastEnd := startEnd
	reason := ""
	for reason == "" {
		cs, err := e.Store.Compartments(ctx, sid)
		if err != nil {
			reason = "The historian failed on the current chunk."
			break
		}
		lastEnd = lastEndOf(cs)
		if lastEnd+1 >= target {
			break
		}
		meta, _ := e.Store.Meta(ctx, sid)
		pct := 0.0
		if window.ContextLimit > 0 {
			pct = float64(meta.LastInputTokens) / float64(window.ContextLimit) * 100
		}
		plan := ResolveProtectedTail(TailRequest{Raw: raw, LastEnd: lastEnd, Window: window, UsagePct: pct, KeepLastRaw: keep})
		plan.EligibleEnd = min(plan.EligibleEnd, target)
		if !runnableWindow(plan) {
			reason = "The historian made no forward progress on the current chunk."
			break
		}
		g, err := e.waitLease(ctx, sid)
		if err != nil || g == nil {
			reason = "Timed out waiting for another process to release the compartment-state lease."
			break
		}
		before := meta.HistorianFailureCount
		_, runErr := e.Historian.Run(ctx, HistorianRun{SessionID: sid, Raw: raw, Boundary: plan, Holder: g.holder, ForceKeepLast: plan.EligibleEnd >= target})
		g.release(ctx)
		if runErr != nil {
			e.logf("magic-context: wrapup pass failed session=%s: %v", sid, runErr)
		}
		after, _ := e.Store.Compartments(ctx, sid)
		if lastEndOf(after) <= lastEnd {
			am, _ := e.Store.Meta(ctx, sid)
			if am.HistorianFailureCount > before || runErr != nil {
				reason = "The historian failed on the current chunk."
			} else {
				reason = "The historian made no forward progress on the current chunk."
			}
		}
	}
	cs, _ := e.Store.Compartments(ctx, sid)
	lastEnd = lastEndOf(cs)
	wrapped := max(0, lastEnd-startEnd)
	created := max(0, len(cs)-startCount)
	if reason != "" {
		return fmt.Sprintf("## Magic Wrapup — Partial\n\nWrapped up through message %d (%d message(s) into %d compartment(s)). %s Run /ctx-wrapup again to continue.", lastEnd, wrapped, created, reason)
	}
	_ = e.Store.SetMeta(ctx, sid, map[string]any{"emergency_latched": false})
	out := fmt.Sprintf("## Magic Wrapup\n\nWrapped up %d messages into %d compartments. The compacted history is queued and materializes on your next message.", wrapped, created)
	if meta, err := e.Store.Meta(ctx, sid); err == nil && !e.cacheExpired(meta, e.now()) {
		out += " If you want it applied on the very next message, run /ctx-flush first."
	}
	return out
}

// ---------------------------------------------------------------------------
// /ctx-dream

func (e *Engine) cmdDream(ctx context.Context, in CommandInput, args string) string {
	if !e.Settings.Dreamer || e.Dreamer == nil {
		return "## /ctx-dream\n\nDreaming is not configured for this project."
	}
	var tasks []string
	start := "Starting dream run..."
	if args != "" {
		task := strings.ToLower(strings.ReplaceAll(args, "_", "-"))
		found := false
		for _, t := range DreamTaskOrder {
			if t == task || strings.ReplaceAll(t, "_", "-") == task {
				tasks, found = []string{t}, true
				break
			}
		}
		if !found {
			return fmt.Sprintf("## /ctx-dream\n\nUnknown task %q. Valid tasks: %s.", args, strings.Join(DreamTaskOrder, ", "))
		}
		start = fmt.Sprintf("Running dream task %q...", tasks[0])
	} else {
		for _, t := range DreamTaskOrder {
			if DreamTaskSchedule(e.Settings, t) != "" {
				tasks = append(tasks, t)
			}
		}
	}
	deps := DreamDeps{
		Store: e.Store, Settings: e.Settings, ProjectPath: e.ProjectKey,
		Generator: e.Dreamer, Embedder: e.Embed, EmbeddingModelID: e.Settings.EmbeddingModel, Now: e.Now,
	}
	e.background(ctx, in.Notify, func(ctx context.Context) string {
		rep, err := RunDream(ctx, deps, tasks)
		if err != nil {
			e.logf("magic-context: dream failed: %v", err)
			return "## /ctx-dream\n\n" + dreamUnknown
		}
		return summarizeDream(rep)
	})
	if e.syncCommands {
		return ""
	}
	return "## /ctx-dream\n\n" + start
}

func summarizeDream(rep DreamReport) string {
	var ran, details, failed, failDetails, skipped, busy []string
	for _, t := range rep.Tasks {
		switch t.Status {
		case DreamStatusOK:
			ran = append(ran, t.Task)
			if t.Detail != "" {
				details = append(details, t.Task+": "+t.Detail)
			}
		case DreamStatusFailed:
			failed = append(failed, t.Task)
			if t.Err != "" {
				failDetails = append(failDetails, t.Task+": "+t.Err)
			}
		case DreamStatusSkipped:
			if t.Detail == "lease held by another runner" {
				busy = append(busy, t.Task)
			} else {
				skipped = append(skipped, t.Task+" ("+t.Detail+")")
			}
		}
	}
	lines := []string{"## /ctx-dream", ""}
	if len(ran) > 0 {
		lines = append(lines, "Ran: "+strings.Join(ran, ", "))
	}
	if len(details) > 0 {
		lines = append(lines, "Details:")
		for _, d := range details {
			lines = append(lines, "- "+d)
		}
	}
	if len(failed) > 0 {
		lines = append(lines, "Failed: "+strings.Join(failed, ", "))
	}
	if len(failDetails) > 0 {
		lines = append(lines, "Failure details:")
		for _, d := range failDetails {
			lines = append(lines, "- "+d)
		}
	}
	if len(skipped) > 0 {
		lines = append(lines, "Skipped: "+strings.Join(skipped, "; "))
	}
	if len(busy) > 0 {
		lines = append(lines, "Busy: "+strings.Join(busy, ", ")+" — another dream task holds this domain's lease; retry in a minute")
	}
	if len(ran)+len(failed)+len(skipped)+len(busy) == 0 {
		lines = append(lines, "No enabled dream tasks to run.")
	}
	return strings.Join(lines, "\n")
}
