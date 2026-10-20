// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/inject-compartments.ts (renderM0, mustMaterialize, materializeM0, renderM1WithMetadata, softRefreshCachedM1, injectM0M1) and features/magic-context/project-docs-hash.ts.

package magiccontext

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Wire text of the two synthetic history messages.
const (
	M0EmptyBody        = "<session-history></session-history>"
	M1EmptyPlaceholder = "<session-history-since>(no new content since last materialization)</session-history-since>"

	maxForcedMemoriesPerDelta = 10
	maxProjectDocBytes        = 256 * 1024

	// m1 pressure backstop: a soft-refreshed delta this large is folded into
	// a fresh m0 instead of growing forever.
	m1PressureMaxUpdates     = 40
	m1PressureHistoryRatio   = 0.20
	m1PressureM0Ratio        = 0.15
	m1PressureMinM0Tokens    = 500
	DefaultUserProfileBudget = DefaultUserProfileBudgetTokens
)

// ProjectDocFiles are injected into m0 in this order.
var ProjectDocFiles = []string{"ARCHITECTURE.md", "STRUCTURE.md"}

var memoryTagRe = regexp.MustCompile(`<(project-memory|user-profile|new-user-profile|memory-updates|new-memories)[\s>]`)

// RenderOptions describe one m0/m1 injection pass.
type RenderOptions struct {
	SessionID  string
	ProjectKey string // "" = no project memory
	ProjectDir string // "" = no project docs
	Settings   Settings
	// HistoryBudget is the <session-history> token budget (0 = default).
	HistoryBudget int
	ModelKey      string
	SystemHash    string
	// CacheExpired is true when the provider prompt cache has gone cold.
	CacheExpired bool
	// CacheBusting is true when this pass may change the wire prefix anyway
	// (drops executed, historian published, force band, …).
	CacheBusting bool
	// Force asks for a HARD materialization (historian publish, /ctx-flush,
	// force band).
	Force       bool
	ForceReason string
}

// MaterializeDecision says whether m0 must be rebuilt and why.
type MaterializeDecision struct {
	Value  bool
	Reason string
}

// RenderResult is the outcome of InjectM0M1.
type RenderResult struct {
	M0, M1 string
	// Boundary is the message ID whose prefix (inclusive) is now represented
	// by m0/m1; "" means trim nothing.
	Boundary      string
	Materialized  bool
	SoftRefreshed bool
	Decision      MaterializeDecision
	UpdateCount   int
}

// renderIdentity is everything whose change invalidates the cached m0.
type renderIdentity struct {
	Project  string `json:"p"`
	Docs     string `json:"d,omitempty"`
	History  int    `json:"h"`
	Memory   int    `json:"m"`
	MemoryOn bool   `json:"mo"`
	Temporal bool   `json:"t,omitempty"`
	Model    string `json:"model,omitempty"`
	System   string `json:"sys,omitempty"`
}

func (o RenderOptions) historyBudget() int {
	if o.HistoryBudget > 0 {
		return o.HistoryBudget
	}
	return DefaultHistoryBudgetTokens
}

func (o RenderOptions) memoryBudget() int {
	if o.Settings.MemoryBudget > 0 {
		return o.Settings.MemoryBudget
	}
	return DefaultMemoryBudget
}

func (o RenderOptions) memoryOn() bool { return o.Settings.MemoryEnabled && o.ProjectKey != "" }

func (o RenderOptions) docsDir() string {
	if !o.Settings.InjectDocs {
		return ""
	}
	return o.ProjectDir
}

func (o RenderOptions) identity() renderIdentity {
	return renderIdentity{
		Project:  o.ProjectKey,
		Docs:     o.docsDir(),
		History:  o.historyBudget(),
		Memory:   o.memoryBudget(),
		MemoryOn: o.memoryOn(),
		Temporal: o.Settings.TemporalAwareness,
		Model:    o.ModelKey,
		System:   o.SystemHash,
	}
}

func (r renderIdentity) key() string {
	b, _ := json.Marshal(r)
	return string(b)
}

// ---------------------------------------------------------------------------
// Project docs

// canonicalizeDoc strips a BOM, normalizes CRLF, trims trailing blanks per
// line and trailing newlines.
func canonicalizeDoc(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// readProjectDoc reads a regular, non-symlink file of at most 256 KiB.
func readProjectDoc(path string) (string, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxProjectDocBytes {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxProjectDocBytes+1))
	if err != nil || len(b) > maxProjectDocBytes {
		return "", false
	}
	c := canonicalizeDoc(string(b))
	return c, strings.TrimSpace(c) != ""
}

// RenderProjectDocs renders the <project-docs> block ("" when none exist).
func RenderProjectDocs(dir string) string {
	if dir == "" {
		return ""
	}
	var sections []string
	for _, name := range ProjectDocFiles {
		c, ok := readProjectDoc(filepath.Join(dir, name))
		if !ok {
			continue
		}
		sections = append(sections, `<file name="`+escapeXMLAttr(name)+`">`+"\n"+escapeXMLContent(c)+"\n</file>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "<project-docs>\n" + strings.Join(sections, "\n\n") + "\n</project-docs>"
}

// ---------------------------------------------------------------------------
// m0

type m0Render struct {
	text      string
	memoryIDs []int64
}

func renderM0(o RenderOptions, docs string, comps []Compartment, mems []Memory, profile []string) m0Render {
	var parts []string
	if docs != "" {
		parts = append(parts, docs)
	}
	if o.memoryOn() {
		if p := RenderUserProfile(profile, DefaultUserProfileBudgetTokens); p != "" {
			parts = append(parts, p)
		}
	}
	hist := RenderSessionHistory(DecayCompartmentsFrom(comps, o.Settings.TemporalAwareness), o.historyBudget())
	if hist == "" {
		hist = M0EmptyBody
	}
	parts = append(parts, hist)
	var ids []int64
	if o.memoryOn() {
		sel := SelectMemories(mems, o.memoryBudget())
		if len(sel) > 0 {
			parts = append(parts, "<project-memory>\n"+renderMemoryBody(sel)+"</project-memory>")
			ids = MemoryIDs(sel)
			slices.Sort(ids)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if text == "" {
		text = M0EmptyBody
	}
	return m0Render{text: text, memoryIDs: ids}
}

// ---------------------------------------------------------------------------
// m1

// m0Markers are the snapshot watermarks the m1 delta is computed against.
type m0Markers struct {
	CompartmentSeq  int
	MaxMemoryID     int64
	MaxMutationID   int64
	ProfileVersion  int64
	MaterializedAtM int64 // ms; frozen memory expiry cutoff
}

func markersFromMeta(m SessionMeta) m0Markers {
	return m0Markers{
		CompartmentSeq:  m.CachedM0CompartmentSeq,
		MaxMemoryID:     m.CachedM0MaxMemoryID,
		MaxMutationID:   m.CachedM0MemoryMutation,
		ProfileVersion:  m.CachedM0ProfileVersion,
		MaterializedAtM: m.CachedM0MaterializedAt,
	}
}

type m1Render struct {
	text        string
	updateCount int
	memoryIDs   []int64
}

// renderMemoryUpdatesM1 renders <memory-updates> against the baseline ids
// and returns the replacement memories forced into <new-memories>.
func renderMemoryUpdatesM1(u MemoryUpdates, baseline []int64, eligible map[int64]bool) (block string, count int, forced []int64) {
	base := map[int64]bool{}
	for _, id := range baseline {
		base[id] = true
	}
	forcedSet := map[int64]bool{}
	lines := []string{"These memories changed since the snapshot below — trust these:"}
	for _, m := range u.Mutations {
		id := strconv.FormatInt(m.TargetID, 10)
		if m.Type == MutationSuperseded {
			repl := m.SupersededBy
			if repl > 0 && !base[repl] && eligible[repl] {
				forcedSet[repl] = true
			}
			if !base[m.TargetID] {
				continue
			}
			if repl > 0 && eligible[repl] {
				lines = append(lines, `  <superseded id="`+id+`" by="`+strconv.FormatInt(repl, 10)+`"/>`)
			} else {
				lines = append(lines, `  <removed id="`+id+`"/>`)
			}
			continue
		}
		if !base[m.TargetID] {
			continue
		}
		if !eligible[m.TargetID] {
			lines = append(lines, `  <removed id="`+id+`"/>`)
			continue
		}
		if m.Type == MutationUpdate {
			cat := ""
			if m.Category != "" {
				cat = ` category="` + escapeXMLAttr(m.Category) + `"`
			}
			lines = append(lines, `  <updated id="`+id+`"`+cat+`>`+escapeXMLContent(m.NewContent)+`</updated>`)
			continue
		}
		lines = append(lines, `  <removed id="`+id+`"/>`)
	}
	for id := range forcedSet {
		forced = append(forced, id)
	}
	slices.Sort(forced)
	if len(forced) > maxForcedMemoriesPerDelta {
		forced = forced[:maxForcedMemoriesPerDelta]
	}
	if len(lines) == 1 {
		return "", 0, forced
	}
	return "<memory-updates>\n" + strings.Join(lines, "\n") + "\n</memory-updates>", len(lines) - 1, forced
}

func (s *Store) renderM1(ctx context.Context, q querier, o RenderOptions, mk m0Markers, baseline []int64) (m1Render, error) {
	var blocks []string
	var out m1Render
	var eligibleMems []Memory
	forced := map[int64]bool{}
	if o.memoryOn() {
		cutoff := mk.MaterializedAtM
		if cutoff <= 0 {
			cutoff = nowMS()
		}
		var err error
		eligibleMems, err = projectMemoriesAt(ctx, q, o.ProjectKey, cutoff)
		if err != nil {
			return out, err
		}
		eligible := map[int64]bool{}
		for _, m := range eligibleMems {
			eligible[m.ID] = true
		}
		if len(baseline) > 0 {
			u, err := memoryUpdatesSince(ctx, q, o.ProjectKey, mk.MaxMutationID, baseline...)
			if err != nil {
				return out, err
			}
			block, n, f := renderMemoryUpdatesM1(u, baseline, eligible)
			if block != "" {
				blocks = append(blocks, block)
			}
			out.updateCount = n
			for _, id := range f {
				forced[id] = true
			}
		}
	}

	comps, err := s.compartments(ctx, q, o.SessionID, false)
	if err != nil {
		return out, err
	}
	var fresh []Compartment
	for _, c := range comps {
		if c.Sequence > mk.CompartmentSeq && !isNoContentCompartment(c) {
			fresh = append(fresh, c)
		}
	}
	if len(fresh) > 0 {
		rendered := make([]string, 0, len(fresh))
		for _, c := range DecayCompartmentsFrom(fresh, o.Settings.TemporalAwareness) {
			rendered = append(rendered, RenderCompartmentAtTier(c, 1))
		}
		blocks = append(blocks, "<new-compartments>\n"+strings.Join(rendered, "\n\n")+"\n</new-compartments>")
	}

	if o.memoryOn() {
		var newer, forcedMems []Memory
		for _, m := range eligibleMems {
			switch {
			case forced[m.ID]:
				forcedMems = append(forcedMems, m)
			case m.ID > mk.MaxMemoryID:
				newer = append(newer, m)
			}
		}
		delta := append(SelectMemories(newer, max(1, o.memoryBudget()/4)), forcedMems...)
		if len(delta) > 0 {
			blocks = append(blocks, RenderNewMemories(delta))
			out.memoryIDs = MemoryIDs(delta)
		}

		ver, err := dreamStateInt(ctx, q, userProfileVersionKey)
		if err != nil {
			return out, err
		}
		if ver != mk.ProfileVersion {
			items, err := userProfileItems(ctx, q)
			if err != nil {
				return out, err
			}
			if p := RenderNewUserProfile(items, DefaultUserProfileBudgetTokens, ver, mk.ProfileVersion); p != "" {
				blocks = append(blocks, p)
			}
		}
	}

	if len(blocks) == 0 {
		out.text = M1EmptyPlaceholder
		return out, nil
	}
	out.text = "<session-history-since>\n" + strings.Join(blocks, "\n") + "\n</session-history-since>"
	return out, nil
}

// ---------------------------------------------------------------------------
// Decision

func maxCompartmentSeq(ctx context.Context, q querier, sessionID string) (int, error) {
	var n *int64
	if err := q.QueryRowContext(ctx, `SELECT MAX(sequence) FROM mc_compartments WHERE session_id = ?`, sessionID).Scan(&n); err != nil {
		return 0, err
	}
	if n == nil {
		return 0, nil
	}
	return int(*n), nil
}

// mustMaterialize reports whether the cached m0 has to be rebuilt. New
// compartments, memories, profile edits and doc edits are NOT reasons: they
// surface in m1 so the cached prefix stays byte-stable.
func mustMaterialize(m SessionMeta, o RenderOptions, curMaxSeq int) MaterializeDecision {
	hard := func(r string) MaterializeDecision { return MaterializeDecision{Value: true, Reason: r} }
	if m.CachedM0 == "" {
		return hard("first_render")
	}
	if m.CachedM1 == "" {
		return hard("cached_m1_missing")
	}
	if !o.memoryOn() && (memoryTagRe.MatchString(m.CachedM0) || memoryTagRe.MatchString(m.CachedM1)) {
		return hard("memory_disabled")
	}
	if m.RenderEpoch != m.CachedRenderEpoch {
		return hard("render_epoch")
	}
	var cached renderIdentity
	_ = json.Unmarshal([]byte(m.CachedM0BudgetKey), &cached)
	cur := o.identity()
	if cached.Project != cur.Project {
		return hard("project_changed")
	}
	if cached.Docs != cur.Docs || cached.History != cur.History || cached.Memory != cur.Memory ||
		cached.MemoryOn != cur.MemoryOn || cached.Temporal != cur.Temporal {
		return hard("budget_changed")
	}
	if cur.Model != "" && cur.Model != cached.Model {
		return hard("model_changed")
	}
	if cur.System != "" && cur.System != cached.System {
		return hard("system_changed")
	}
	if o.CacheExpired && m.LastResponseTime > m.CachedM0MaterializedAt {
		return hard("ttl_idle")
	}
	if curMaxSeq < m.CachedM0CompartmentSeq {
		return hard("compartments_rewritten")
	}
	return MaterializeDecision{Reason: "cached"}
}

// MustMaterialize is the read-only decision for the current state (used by
// the transform to decide whether this pass busts the cache anyway).
func (s *Store) MustMaterialize(ctx context.Context, o RenderOptions) (MaterializeDecision, error) {
	if o.Force {
		return MaterializeDecision{Value: true, Reason: forceReason(o)}, nil
	}
	m, err := s.Meta(ctx, o.SessionID)
	if err != nil {
		return MaterializeDecision{}, err
	}
	seq, err := maxCompartmentSeq(ctx, s.db, o.SessionID)
	if err != nil {
		return MaterializeDecision{}, err
	}
	return mustMaterialize(m, o, seq), nil
}

func forceReason(o RenderOptions) string {
	if o.ForceReason != "" {
		return o.ForceReason
	}
	return "forced"
}

// ---------------------------------------------------------------------------
// Materialize / refresh / replay

func lastBoundary(comps []Compartment) string {
	if len(comps) == 0 {
		return ""
	}
	return comps[len(comps)-1].EndMessageID
}

func (s *Store) materialize(ctx context.Context, q querier, o RenderOptions) (RenderResult, error) {
	var res RenderResult
	comps, err := s.compartments(ctx, q, o.SessionID, false)
	if err != nil {
		return res, err
	}
	var mems []Memory
	var profile []string
	mk := m0Markers{MaterializedAtM: nowMS()}
	if len(comps) > 0 {
		mk.CompartmentSeq = comps[len(comps)-1].Sequence
	}
	if o.memoryOn() {
		if mems, err = projectMemoriesAt(ctx, q, o.ProjectKey, mk.MaterializedAtM); err != nil {
			return res, err
		}
		if profile, err = userProfileItems(ctx, q); err != nil {
			return res, err
		}
		if mk.MaxMemoryID, err = maxMemoryID(ctx, q, o.ProjectKey); err != nil {
			return res, err
		}
		if mk.MaxMutationID, err = maxMemoryMutationID(ctx, q, o.ProjectKey); err != nil {
			return res, err
		}
	}
	if mk.ProfileVersion, err = dreamStateInt(ctx, q, userProfileVersionKey); err != nil {
		return res, err
	}
	m0 := renderM0(o, RenderProjectDocs(o.docsDir()), comps, mems, profile)
	m1, err := s.renderM1(ctx, q, o, mk, m0.memoryIDs)
	if err != nil {
		return res, err
	}
	meta, err := s.meta(ctx, q, o.SessionID)
	if err != nil {
		return res, err
	}
	boundary := lastBoundary(comps)
	ids := m0.memoryIDs
	if ids == nil {
		ids = []int64{}
	}
	if err := s.setMeta(ctx, q, o.SessionID, map[string]any{
		"cached_m0":                 m0.text,
		"cached_m1":                 m1.text,
		"cached_m0_compartment_seq": mk.CompartmentSeq,
		"cached_m0_memory_mutation": mk.MaxMutationID,
		"cached_m0_max_memory_id":   mk.MaxMemoryID,
		"cached_m0_profile_version": mk.ProfileVersion,
		"cached_m0_memory_ids":      ids,
		"cached_m0_materialized_at": mk.MaterializedAtM,
		"cached_m0_boundary":        boundary,
		"cached_m0_memory_enabled":  o.memoryOn(),
		"cached_m0_budget_key":      o.identity().key(),
		"cached_render_epoch":       meta.RenderEpoch,
		"materialize_pending":       "",
	}); err != nil {
		return res, err
	}
	return RenderResult{M0: m0.text, M1: m1.text, Boundary: boundary, Materialized: true, UpdateCount: m1.updateCount}, nil
}

func (s *Store) softRefresh(ctx context.Context, q querier, o RenderOptions) (RenderResult, error) {
	meta, err := s.meta(ctx, q, o.SessionID)
	if err != nil {
		return RenderResult{}, err
	}
	if meta.CachedM0 == "" {
		return RenderResult{}, errors.New("magiccontext: no cached m0 to refresh")
	}
	m1, err := s.renderM1(ctx, q, o, markersFromMeta(meta), meta.CachedM0MemoryIDs)
	if err != nil {
		return RenderResult{}, err
	}
	comps, err := s.compartments(ctx, q, o.SessionID, false)
	if err != nil {
		return RenderResult{}, err
	}
	boundary := lastBoundary(comps)
	if err := s.setMeta(ctx, q, o.SessionID, map[string]any{
		"cached_m1":          m1.text,
		"cached_m0_boundary": boundary,
	}); err != nil {
		return RenderResult{}, err
	}
	return RenderResult{M0: meta.CachedM0, M1: m1.text, Boundary: boundary, SoftRefreshed: true, UpdateCount: m1.updateCount}, nil
}

// m1UnderPressure is the backstop that folds an oversized delta into m0.
func m1UnderPressure(r RenderResult, historyBudget int) bool {
	if r.UpdateCount > m1PressureMaxUpdates {
		return true
	}
	m1 := float64(EstimateTokens(r.M1))
	if m1 > m1PressureHistoryRatio*float64(historyBudget) {
		return true
	}
	m0 := EstimateTokens(r.M0)
	return m0 >= m1PressureMinM0Tokens && m1 > m1PressureM0Ratio*float64(m0)
}

// InjectM0M1 decides between a HARD materialization, a soft m1 refresh (on
// cache-busting passes) and a byte-identical replay of the cached pair.
func (s *Store) InjectM0M1(ctx context.Context, o RenderOptions) (RenderResult, error) {
	var res RenderResult
	err := s.Immediate(ctx, func(q querier) error {
		meta, err := s.meta(ctx, q, o.SessionID)
		if err != nil {
			return err
		}
		var dec MaterializeDecision
		if o.Force {
			dec = MaterializeDecision{Value: true, Reason: forceReason(o)}
		} else {
			seq, err := maxCompartmentSeq(ctx, q, o.SessionID)
			if err != nil {
				return err
			}
			dec = mustMaterialize(meta, o, seq)
		}
		switch {
		case dec.Value:
			res, err = s.materialize(ctx, q, o)
		case o.CacheBusting:
			res, err = s.softRefresh(ctx, q, o)
			if err == nil && m1UnderPressure(res, o.historyBudget()) {
				dec = MaterializeDecision{Value: true, Reason: "m1_pressure"}
				res, err = s.materialize(ctx, q, o)
			}
		default:
			res = RenderResult{M0: meta.CachedM0, M1: meta.CachedM1, Boundary: meta.CachedM0Boundary}
		}
		res.Decision = dec
		return err
	})
	if err != nil {
		return RenderResult{}, err
	}
	if res.M0 == "" {
		res.M0 = M0EmptyBody
	}
	if res.M1 == "" {
		res.M1 = M1EmptyPlaceholder
	}
	return res, nil
}

// BumpRenderEpoch forces the next pass to rematerialize (recomp, compartment
// edits, /ctx-flush).
func (s *Store) BumpRenderEpoch(ctx context.Context, q querier, sessionID string) error {
	if q == nil {
		q = s.db
	}
	if _, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO mc_session_meta (session_id) VALUES (?)`, sessionID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `UPDATE mc_session_meta SET render_epoch = render_epoch + 1 WHERE session_id = ?`, sessionID)
	return err
}
