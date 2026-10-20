// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): packages/plugin/src/hooks/magic-context/ctx-reduce-nudge.ts, hook-handlers.ts (maybeInjectChannel1Nudge), tail-hygiene-walk.ts (stripChannel1ReminderSpans)

package magiccontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Channel 1 appends a ctx_reduce reminder to a tool result; channel 2 is a
// synthetic user message injected mid-run when spent output dominates the
// tail.
const (
	Channel1Sentinel          = "<system-reminder>"
	channel1ReminderOpen      = "\n\n<system-reminder>\n"
	channel1ReminderClose     = "\n</system-reminder>"
	channel1MinTokens         = 60_000
	channel1FloorTokens       = 25_000
	channel1RefireFloorTokens = 25_000
	sGentle                   = 0.2
	sFirm                     = 0.4
	sUrgent                   = 0.6
	channel2SeverityThreshold = 0.75
	channel2FloorTokens       = 50_000
	channel1StickyTurnGap     = 5
)

// Nudge levels.
const (
	LevelGentle = "gentle"
	LevelFirm   = "firm"
	LevelUrgent = "urgent"
	bandQuiet   = "quiet"
	bandCh2     = "channel2"
)

var levelRank = map[string]int{LevelGentle: 1, LevelFirm: 2, LevelUrgent: 3}

// Channel 2 lease states.
const (
	ch2Empty     = ""
	ch2Pending   = "pending"
	ch2Delivered = "delivered"
)

func channel1RefireTokens(tailTokens int) int {
	scaled := int(math.Round(0.08 * float64(max(0, tailTokens))))
	return max(channel1RefireFloorTokens, scaled)
}

// StripChannel1ReminderSpans removes trailing channel-1 reminders so they are
// never measured as tool output.
func StripChannel1ReminderSpans(output string) string {
	s := output
	for strings.HasSuffix(s, channel1ReminderClose) {
		i := strings.LastIndex(s, channel1ReminderOpen)
		if i < 0 {
			break
		}
		s = s[:i]
	}
	return s
}

func toolOutputTokens(output string) int {
	return EstimateTokens(StripChannel1ReminderSpans(output))
}

func severityOf(u, t int) float64 {
	return math.Min(1, math.Max(0, float64(u)/float64(max(t, 1))))
}

func channel1Band(u, t int) string {
	if t < channel1MinTokens || u < channel1FloorTokens {
		return bandQuiet
	}
	s := severityOf(u, t)
	switch {
	case s >= sUrgent:
		return LevelUrgent
	case s >= sFirm:
		return LevelFirm
	case s >= sGentle:
		return LevelGentle
	}
	return bandQuiet
}

func nudgeBand(u, t int) string {
	b := channel1Band(u, t)
	if b == LevelUrgent && u >= channel2FloorTokens && severityOf(u, t) >= channel2SeverityThreshold {
		return bandCh2
	}
	return b
}

// channel1Input mirrors upstream decideChannel1's input. Optional numbers use
// -1 for "undefined".
type channel1Input struct {
	BaselineU, BaselineT     int
	TurnDeltaU, TurnDeltaT   int
	LastNudgeUndropped       int
	LastNudgeLevel           string
	LastFireOrdinal          int // -1 = unset
	CurrentRealUserTurnCount int // -1 = unset
	HasRecentReduce          bool
	AgentDropsApplied        bool
	GracePending             bool
	GraceBaselineU           int // -1 = unset
	GracePreLevel            string
	GracePreLevelSet         bool
	Evaluable                bool
	GenerationInvalidated    bool
}

type channel1Decision struct {
	Fire                 bool
	Sticky               bool
	Level                string
	Band                 string
	Undropped, Tail      int
	Severity             float64
	Reason               string
	NextLastNudge        int
	NextLastNudgeLevel   string
	ClearPostReduceGrace bool
}

func decideChannel1(in channel1Input) channel1Decision {
	tail := max(0, in.BaselineT+in.TurnDeltaT)
	u := min(tail, max(0, in.BaselineU+in.TurnDeltaU))
	sev := severityOf(u, tail)
	prev := in.LastNudgeLevel
	lastNudge := max(0, in.LastNudgeUndropped)
	nextLevel := prev
	clearGrace := false
	growth := channel1RefireTokens(tail)
	graceSet := in.GraceBaselineU >= 0
	graceGrowth := 0
	if graceSet {
		graceGrowth = u - in.GraceBaselineU
	}
	cadenceGrowth := u - lastNudge
	stickyRemaining := 0
	if in.CurrentRealUserTurnCount >= 0 && in.LastFireOrdinal >= 0 && in.LastFireOrdinal <= in.CurrentRealUserTurnCount {
		stickyRemaining = max(0, channel1StickyTurnGap-(in.CurrentRealUserTurnCount-in.LastFireOrdinal))
	}
	measured := channel1Band(u, tail)
	quiet := func(reason, level string) channel1Decision {
		if level == "" {
			level = measured
			if measured == bandQuiet {
				level = LevelGentle
			}
		}
		return channel1Decision{
			Level: level, Band: measured, Undropped: u, Tail: tail, Severity: sev,
			Reason: reason, NextLastNudge: lastNudge, NextLastNudgeLevel: nextLevel,
			ClearPostReduceGrace: clearGrace,
		}
	}

	if !in.Evaluable || in.GenerationInvalidated {
		return quiet("baseline-unevaluable", "")
	}
	if in.HasRecentReduce {
		return quiet("recent-reduce-refresh", "")
	}
	if in.AgentDropsApplied {
		return quiet("agent-drops-applied", "")
	}
	if in.GracePending {
		return quiet("post-reduce-baseline-pending", "")
	}

	level := measured
	if measured == bandQuiet {
		level = ""
	}
	prevRank, curRank := levelRank[prev], levelRank[level]
	graceRelease := ""
	if graceSet {
		pre := prev
		if in.GracePreLevelSet {
			pre = in.GracePreLevel
		}
		regrowth := graceGrowth >= growth
		escalated := curRank > levelRank[pre]
		if !regrowth && !escalated {
			return quiet("post-reduce-compliance-grace", "")
		}
		clearGrace = true
		graceRelease = "post-reduce-band-escalation"
		if regrowth {
			graceRelease = "post-reduce-regrowth-reached"
		}
	}

	if level == "" {
		nextLevel = ""
		lastNudge = 0
		switch {
		case tail < channel1MinTokens:
			return quiet("tail-below-minimum", "")
		case u < channel1FloorTokens:
			return quiet("reclaimable-below-floor", "")
		}
		return quiet("ratio-below-gentle", "")
	}

	// A drop into a lower band is an observation, not a fresh crossing.
	if curRank < prevRank {
		nextLevel = level
		lastNudge = u
		return quiet("band-deescalation", level)
	}

	crossed := curRank > prevRank
	cadence := curRank == prevRank && cadenceGrowth >= growth
	if !crossed && !cadence {
		return quiet("cadence-growth", level)
	}
	if !crossed && stickyRemaining != 0 {
		return quiet("sticky-turn-floor", level)
	}
	reason := graceRelease
	if reason == "" {
		reason = "cadence-refire"
		if crossed {
			reason = "band-crossing"
		}
	}
	return channel1Decision{
		Fire: true, Sticky: !crossed, Level: level, Band: measured,
		Undropped: u, Tail: tail, Severity: sev, Reason: reason,
		NextLastNudge: u, NextLastNudgeLevel: level, ClearPostReduceGrace: clearGrace,
	}
}

type channel2Evaluation struct {
	Evaluable     bool
	ShouldTrigger bool
	Reclaimable   int
	Tail          int
	Severity      float64
	Band          string
	Reason        string
}

func evaluateChannel2(n *nudgeLive) channel2Evaluation {
	if n == nil {
		return channel2Evaluation{Band: bandQuiet, Reason: "baseline-unavailable"}
	}
	if !n.evaluable {
		return channel2Evaluation{Band: bandQuiet, Reason: "baseline-unevaluable"}
	}
	if n.invalidated {
		return channel2Evaluation{Band: bandQuiet, Reason: "generation-invalidated"}
	}
	tail := max(0, n.baselineT+n.turnDeltaT)
	u := min(tail, max(0, n.baselineU+n.turnDeltaU))
	sev := severityOf(u, tail)
	reason := "ceiling-threshold-met"
	switch {
	case tail < channel1MinTokens:
		reason = "tail-below-minimum"
	case u < channel2FloorTokens:
		reason = "reclaimable-below-floor"
	case sev < channel2SeverityThreshold:
		reason = "ratio-below-ceiling"
	}
	return channel2Evaluation{
		Evaluable: true, ShouldTrigger: reason == "ceiling-threshold-met",
		Reclaimable: u, Tail: tail, Severity: sev, Band: nudgeBand(u, tail), Reason: reason,
	}
}

func approxThousands(tokens int) string {
	return fmt.Sprintf("%dk", int(math.Round(float64(tokens)/1000)))
}

func formatOldestReclaimableHint(hint []TagRef) string {
	if len(hint) == 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	for i, t := range hint {
		if i == 4 {
			break
		}
		name := t.Tool
		if name == "" {
			name = "tool"
		}
		parts = append(parts, fmt.Sprintf("§%d§ %s", t.Number, name))
	}
	return "\noldest reclaimable: " + strings.Join(parts, " · ") + "."
}

func formatReclaimableOutputSummary(count, tokens int) string {
	count = max(0, count)
	outputs := "spent tool outputs"
	if count == 1 {
		outputs = "1 spent tool output"
	} else if count > 1 {
		outputs = fmt.Sprintf("%d spent tool outputs", count)
	}
	return fmt.Sprintf("%s (~%s tokens)", outputs, approxThousands(tokens))
}

func buildChannel2Reminder(undropped, count int, hint []TagRef) string {
	summary := formatReclaimableOutputSummary(count, undropped)
	return "<system-reminder>\n" +
		"Your next step: call ctx_reduce on the outputs you've already used (" + summary + "). Then continue your task." + formatOldestReclaimableHint(hint) + "\n" +
		"</system-reminder>"
}

func buildChannel1Reminder(level string, undropped, count int, hint []TagRef, sticky bool) string {
	summary := formatReclaimableOutputSummary(count, undropped)
	hintText := formatOldestReclaimableHint(hint)
	if sticky {
		return "\n\n<system-reminder>\nStill unstamped: " + summary + ". Stamp the ones you've used with ctx_reduce now." + hintText + "\n</system-reminder>"
	}
	var body string
	switch level {
	case LevelGentle:
		body = "Housekeeping: " + summary + " are reclaimable. Stamp each output with ctx_reduce as soon as you've taken what you need from it; don't wait for the task to finish."
	case LevelFirm:
		body = summary + " are reclaimable. Make a ctx_reduce pass now over the outputs you've already used, then continue."
	default:
		body = summary + " are still unstamped. Call ctx_reduce now, before your next tool call, on every output you've already used."
	}
	return "\n\n<system-reminder>\n" + body + hintText + "\n</system-reminder>"
}

// ---------------------------------------------------------------------------
// State

// nudgeLive is the in-memory per-session baseline (upstream Channel1State).
type nudgeLive struct {
	baselineU, baselineT   int
	turnDeltaU, turnDeltaT int
	toolsRatio             float64
	realUserTurns          int
	reducedSinceRefresh    bool
	agentDropsApplied      bool
	evaluable              bool
	invalidated            bool
	count                  int
	oldest                 []TagRef
}

// channel1State is the durable channel-1 blob.
type channel1State struct {
	Level          string  `json:"level"`
	Ordinal        *int    `json:"ordinal,omitempty"`
	GracePending   bool    `json:"postReduceGracePending,omitempty"`
	GraceBaselineU *int    `json:"postReduceGraceBaselineU,omitempty"`
	GracePreLevel  *string `json:"postReduceGracePreLevel,omitempty"`
}

type nudgeDurable struct {
	lastUndropped int
	ch1           channel1State
	ch2           string
}

func (s *Store) nudgeState(ctx context.Context, sid string) (nudgeDurable, error) {
	var d nudgeDurable
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT last_nudge_undropped, channel1_nudge_state, channel2_nudge_state FROM mc_session_meta WHERE session_id = ?`, sid).
		Scan(&d.lastUndropped, &raw, &d.ch2)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &d.ch1)
	}
	return d, nil
}

func (s *Store) setNudgeState(ctx context.Context, sid string, d nudgeDurable) error {
	b, err := json.Marshal(d.ch1)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO mc_session_meta (session_id) VALUES (?)`, sid); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE mc_session_meta SET last_nudge_undropped = ?, channel1_nudge_state = ?, channel2_nudge_state = ? WHERE session_id = ?`,
		d.lastUndropped, string(b), d.ch2, sid)
	return err
}

func (e *Engine) live(sid string) *nudgeLive {
	if e.nudges == nil {
		e.nudges = map[string]*nudgeLive{}
	}
	n := e.nudges[sid]
	if n == nil {
		n = &nudgeLive{toolsRatio: 1}
		e.nudges[sid] = n
	}
	return n
}

// refreshNudges runs at the end of a pass with ctx_reduce available: it
// resets the baseline to the measured tail and arms channel 2.
func (e *Engine) refreshNudges(ctx context.Context, sid string, st PassStats, realUserTurns int) {
	e.nudgeMu.Lock()
	defer e.nudgeMu.Unlock()
	e.mu.Lock()
	n := e.live(sid)
	prevU := n.baselineU
	n.baselineU, n.baselineT = st.Reclaimable, st.TailTokens
	n.turnDeltaU, n.turnDeltaT = 0, 0
	n.realUserTurns = realUserTurns
	n.reducedSinceRefresh = false
	n.agentDropsApplied = st.DropsApplied
	n.evaluable, n.invalidated = true, false
	n.count, n.oldest = st.ReclaimableCount, st.Oldest
	snap := *n
	e.mu.Unlock()

	d, err := e.Store.nudgeState(ctx, sid)
	if err != nil {
		return
	}
	before := d
	// Capture the post-reduce grace baseline from the first measured pass.
	if d.ch1.GracePending {
		u := st.Reclaimable
		d.ch1.GracePending = false
		d.ch1.GraceBaselineU = &u
	}
	ev := evaluateChannel2(&snap)
	switch {
	case d.ch2 == ch2Empty && ev.ShouldTrigger:
		d.ch2 = ch2Pending
	case d.ch2 == ch2Pending && !ev.ShouldTrigger:
		d.ch2 = ch2Empty
	case d.ch2 == ch2Delivered && st.Reclaimable < prevU && st.Reclaimable < channel1FloorTokens:
		// Rearm once the reminder was obeyed and the spent output collapsed.
		d.ch2 = ch2Empty
	}
	if !nudgeEqual(before, d) {
		_ = e.Store.setNudgeState(ctx, sid, d)
	}
}

func nudgeEqual(a, b nudgeDurable) bool {
	ja, _ := json.Marshal(a.ch1)
	jb, _ := json.Marshal(b.ch1)
	return a.lastUndropped == b.lastUndropped && a.ch2 == b.ch2 && string(ja) == string(jb)
}

// ToolOutputNudge is called after a tool finishes and before its result is
// stored. It returns the channel-1 reminder to append, or "".
func (e *Engine) ToolOutputNudge(ctx context.Context, sid, tool, output string) string {
	if e == nil {
		return ""
	}
	e.nudgeMu.Lock()
	defer e.nudgeMu.Unlock()
	e.mu.Lock()
	n := e.nudges[sid]
	if n == nil {
		// No baseline: ctx_reduce is off for this session.
		e.mu.Unlock()
		return ""
	}
	if tool == CtxReduceToolName {
		n.reducedSinceRefresh = true
		n.evaluable = false
		n.invalidated = true
		e.mu.Unlock()
		d, err := e.Store.nudgeState(ctx, sid)
		if err != nil {
			return ""
		}
		pre := d.ch1.Level
		d.ch1.GracePending = true
		d.ch1.GraceBaselineU = nil
		d.ch1.GracePreLevel = &pre
		_ = e.Store.setNudgeState(ctx, sid, d)
		return ""
	}
	if output == "" || strings.Contains(output, Channel1Sentinel) {
		e.mu.Unlock()
		return ""
	}
	// The just-completed output grows T but not U (it is inside the
	// recency reserve).
	n.turnDeltaT += int(float64(toolOutputTokens(output)) * n.toolsRatio)
	snap := *n
	e.mu.Unlock()

	d, err := e.Store.nudgeState(ctx, sid)
	if err != nil {
		return ""
	}
	in := channel1Input{
		BaselineU: snap.baselineU, BaselineT: snap.baselineT,
		TurnDeltaU: snap.turnDeltaU, TurnDeltaT: snap.turnDeltaT,
		LastNudgeUndropped: d.lastUndropped, LastNudgeLevel: d.ch1.Level,
		LastFireOrdinal: -1, CurrentRealUserTurnCount: snap.realUserTurns,
		HasRecentReduce: snap.reducedSinceRefresh, AgentDropsApplied: snap.agentDropsApplied,
		GracePending: d.ch1.GracePending, GraceBaselineU: -1,
		Evaluable: snap.evaluable, GenerationInvalidated: snap.invalidated,
	}
	if d.ch1.Ordinal != nil {
		in.LastFireOrdinal = *d.ch1.Ordinal
	}
	if d.ch1.GraceBaselineU != nil {
		in.GraceBaselineU = *d.ch1.GraceBaselineU
	}
	if d.ch1.GracePreLevel != nil {
		in.GracePreLevel, in.GracePreLevelSet = *d.ch1.GracePreLevel, true
	}
	dec := decideChannel1(in)
	e.logf("magic-context: channel1 U=%d T=%d band=%s verdict=%v reason=%s", dec.Undropped, dec.Tail, dec.Band, dec.Fire, dec.Reason)
	d.lastUndropped = dec.NextLastNudge
	d.ch1.Level = dec.NextLastNudgeLevel
	if dec.ClearPostReduceGrace {
		d.ch1.GracePending, d.ch1.GraceBaselineU, d.ch1.GracePreLevel = false, nil, nil
	}
	if !dec.Fire {
		_ = e.Store.setNudgeState(ctx, sid, d)
		return ""
	}
	ord := snap.realUserTurns
	d.ch1.Ordinal = &ord
	_ = e.Store.setNudgeState(ctx, sid, d)
	return buildChannel1Reminder(dec.Level, dec.Undropped, snap.count, snap.oldest, dec.Sticky)
}

// TakeChannel2 claims a pending channel-2 reminder. The caller injects the
// text as a user message before the next step.
func (e *Engine) TakeChannel2(ctx context.Context, sid string) (string, bool) {
	if e == nil {
		return "", false
	}
	e.nudgeMu.Lock()
	defer e.nudgeMu.Unlock()
	e.mu.Lock()
	n := e.nudges[sid]
	if n == nil {
		e.mu.Unlock()
		return "", false
	}
	snap := *n
	e.mu.Unlock()
	if snap.reducedSinceRefresh {
		return "", false
	}
	d, err := e.Store.nudgeState(ctx, sid)
	if err != nil || d.ch2 != ch2Pending {
		return "", false
	}
	ev := evaluateChannel2(&snap)
	if !ev.ShouldTrigger {
		d.ch2 = ch2Empty
		_ = e.Store.setNudgeState(ctx, sid, d)
		return "", false
	}
	d.ch2 = ch2Delivered
	if err := e.Store.setNudgeState(ctx, sid, d); err != nil {
		return "", false
	}
	return buildChannel2Reminder(ev.Reclaimable, snap.count, snap.oldest), true
}
