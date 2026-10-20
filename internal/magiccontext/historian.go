// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/compartment-runner.ts, compartment-runner-historian.ts, compartment-runner-incremental.ts, compartment-prompt.ts.

package magiccontext

// Historian runner. Ported from magic-context
// compartment-runner-incremental.ts, compartment-runner-historian.ts and
// historian-prompt-fit.ts (MIT, Copyright (c) 2025 Ufuk Altinok).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/message"
)

const (
	historianMaxRetries       = 2
	historianAlertCooldown    = 60 * time.Second
	historianFitSlack         = 64
	historianMinFitChunk      = 1000
	historianMaxOutputTokens  = 16000
	historianLeaseTTL         = 2 * time.Minute
	historianTruncationMarker = "\n[… tokens truncated by Magic Context to fit the historian window …]"

	// EpisodeFilteredNoise marks a placeholder compartment covering a run of
	// raw messages that carried no narrative (tool noise, hidden turns).
	EpisodeFilteredNoise = "filtered-noise"
)

var (
	errHistorianWindowTooSmall = errors.New("historian window too small")
	errStoredCompartments      = errors.New("stored compartments invalid")
	errLeaseLost               = errors.New("compartment lease no longer held")
)

// Historian compresses old raw history into compartments.
type Historian struct {
	Store    *Store
	Settings Settings
	// Generate runs the historian model; Fallback (optional) is tried when
	// the primary fails or keeps producing invalid output.
	Generate Generator
	Fallback Generator
	// ContextWindow is the historian model's context (0 = default).
	ContextWindow int
	ProjectKey    string
	Embed         Embedder
	EmbedModelID  string
	// Notify shows a user-facing notice for sessionID (rate-limited).
	Notify func(sessionID, text string)
	// OnPublished runs after a successful publish commits.
	OnPublished func(ctx context.Context, sessionID string, res HistorianResult)
	Logf        func(format string, args ...any)
	// Sleep is the retry backoff wait (tests override it).
	Sleep func(ctx context.Context, d time.Duration) error

	mu        sync.Mutex
	lastAlert map[string]time.Time
}

// HistorianRun is one historian invocation.
type HistorianRun struct {
	SessionID string
	Raw       []RawMessage
	Boundary  TailBoundary
	Emergency bool
	// ForceKeepLast keeps the trailing compartment when the chunk reaches
	// the end of eligible history (wrapup).
	ForceKeepLast bool
	// Recomp writes to the staging table with the structural prompt.
	Recomp bool
	// ChunkBudget caps the chunk size in tokens (0 = derived).
	ChunkBudget int
	// Holder is a lease the caller already holds ("" = acquire one).
	Holder string
}

// HistorianResult describes one run.
type HistorianResult struct {
	Published        []Compartment
	LastEnd          int
	HasMore          bool
	DiscardedLast    bool
	Skipped          bool
	Reason           string
	Facts            []ParsedFact
	UserObservations []string
	PrimerCandidates []ParsedPrimerCandidate
}

// PendingMaterialization is stored in materialize_pending after a publish;
// the transform consumes it to move the compaction marker.
type PendingMaterialization struct {
	End          int    `json:"end"`
	Seq          int    `json:"seq"`
	EndMessageID string `json:"end_message_id"`
	PublishedAt  int64  `json:"published_at"`
}

func (h *Historian) logf(format string, args ...any) {
	if h.Logf != nil {
		h.Logf(format, args...)
	}
}

func (h *Historian) contextWindow() int {
	if h.ContextWindow > 0 {
		return h.ContextWindow
	}
	return DefaultHistorianContext
}

func (h *Historian) outputReserve() int {
	return min(historianMaxOutputTokens, max(1024, h.contextWindow()/4))
}

func newLeaseHolder() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "historian-" + hex.EncodeToString(b[:])
}

// notify sends text unless a notice went out for the session recently.
func (h *Historian) notify(sessionID, text string) {
	if h.Notify == nil {
		return
	}
	h.mu.Lock()
	if h.lastAlert == nil {
		h.lastAlert = map[string]time.Time{}
	}
	now := time.Now()
	if t, ok := h.lastAlert[sessionID]; ok && now.Sub(t) < historianAlertCooldown {
		h.mu.Unlock()
		return
	}
	h.lastAlert[sessionID] = now
	h.mu.Unlock()
	h.Notify(sessionID, text)
}

// Run executes one historian pass under the session lease, recording and
// surfacing failures.
func (h *Historian) Run(ctx context.Context, run HistorianRun) (HistorianResult, error) {
	if h.Generate == nil {
		return HistorianResult{}, ErrNoGenerator
	}
	holder := run.Holder
	if holder == "" {
		holder = newLeaseHolder()
		ok, err := h.Store.AcquireLease(ctx, run.SessionID, holder, historianLeaseTTL)
		if err != nil {
			return HistorianResult{}, err
		}
		if !ok {
			return HistorianResult{Skipped: true, Reason: "lease held"}, nil
		}
		defer func() { _ = h.Store.ReleaseLease(context.WithoutCancel(ctx), run.SessionID, holder) }()
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(historianLeaseTTL / 3)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ctx.Done():
					return
				case <-t.C:
					_ = h.Store.RenewLease(ctx, run.SessionID, holder, historianLeaseTTL)
				}
			}
		}()
		run.Holder = holder
	}

	res, err := h.runPass(ctx, run)
	if err == nil || run.Recomp || ctx.Err() != nil {
		return res, err
	}
	h.recordFailure(ctx, run.SessionID, err)
	return res, err
}

func (h *Historian) recordFailure(ctx context.Context, sessionID string, err error) {
	ctx = context.WithoutCancel(ctx)
	m, _ := h.Store.Meta(ctx, sessionID)
	count := m.HistorianFailureCount + 1
	_ = h.Store.SetMeta(ctx, sessionID, map[string]any{
		"historian_failure_count": count,
		"historian_last_error":    err.Error(),
	})
	h.logf("magic-context: historian failure session=%s count=%d: %v", sessionID, count, err)
	switch {
	case errors.Is(err, errHistorianWindowTooSmall):
		h.notify(sessionID, BuildHistorianWindowTooSmallNotice())
	case errors.Is(err, errStoredCompartments):
		h.notify(sessionID, BuildStoredCompartmentsInvalidNotice())
	case errors.Is(err, errLeaseLost):
	default:
		h.notify(sessionID, BuildHistorianFailureNotice(count))
	}
}

// NonNarrativeRanges returns maximal runs of raw ordinals that carry no
// narrative; stored compartments may leave gaps inside them.
func NonNarrativeRanges(raw []RawMessage) []OrdRange {
	var out []OrdRange
	for _, m := range raw {
		if !nonNarrative(m) {
			continue
		}
		if n := len(out); n > 0 && out[n-1].End == m.Ordinal-1 {
			out[n-1].End = m.Ordinal
			continue
		}
		out = append(out, OrdRange{m.Ordinal, m.Ordinal})
	}
	return out
}

func nonNarrative(m RawMessage) bool {
	switch m.Role {
	case message.Tool:
		return true
	case message.User:
		return !m.MeaningfulUser()
	case message.Assistant:
		for _, t := range m.Texts {
			if strings.TrimSpace(t) != "" {
				return false
			}
		}
		return len(m.ToolCalls) == 0
	}
	return false
}

func (h *Historian) runPass(ctx context.Context, run HistorianRun) (HistorianResult, error) {
	sid := run.SessionID
	prior, err := h.Store.compartments(ctx, h.Store.db, sid, run.Recomp)
	if err != nil {
		return HistorianResult{}, err
	}
	if msg := ValidateStoredCompartments(prior, NonNarrativeRanges(run.Raw)); msg != "" {
		return HistorianResult{}, fmt.Errorf("%w: %s", errStoredCompartments, msg)
	}
	lastEnd, seqOffset := 0, 0
	if n := len(prior); n > 0 {
		lastEnd = prior[n-1].EndOrdinal
		seqOffset = prior[n-1].Sequence + 1
	}
	offset := lastEnd + 1
	eligibleEnd := run.Boundary.EligibleEnd
	if eligibleEnd <= offset {
		return HistorianResult{Skipped: true, Reason: "nothing eligible", LastEnd: lastEnd}, nil
	}

	set := h.Settings
	memoryEnabled := set.MemoryEnabled && !run.Recomp && h.ProjectKey != ""
	system := historianSystemPrompt
	if run.Recomp {
		system = historianStructuralSystemPrompt
	}
	system = withContentLanguageDirective(system, set.Language, true)

	var mems []Memory
	if memoryEnabled {
		all, err := h.Store.ProjectMemories(ctx, h.ProjectKey)
		if err != nil {
			return HistorianResult{}, err
		}
		mems = SelectMemories(all, 0)
	}
	requested := HistorianChunkTokens(h.contextWindow())
	if run.ChunkBudget > 0 {
		requested = min(requested, run.ChunkBudget)
	}
	fit, err := h.fitPrompt(fitArgs{
		system: system, requested: requested, sessionID: sid, chunkStart: offset,
		lastOrdinal: eligibleEnd - 1, history: prior, memories: mems,
		memoryDisabled: !memoryEnabled, userObsDisabled: !set.Dreamer, extractionFree: run.Recomp,
	})
	if err != nil {
		return HistorianResult{}, err
	}

	chunk := ReadChunk(run.Raw, offset, eligibleEnd, fit.chunkTokens)
	if strings.TrimSpace(chunk.Text) == "" || chunk.MessageCount == 0 {
		return h.publishFilteredNoise(ctx, run, chunk, offset, eligibleEnd, seqOffset)
	}
	if msg := validateChunkCoverage(chunk); msg != "" {
		return HistorianResult{}, errors.New(msg)
	}

	inputText := chunk.Text
	prompt := func(text string) string {
		return buildCompartmentAgentPrompt(compartmentPromptInputs{
			seedExamples: fit.seedExamples, sessionReferences: fit.sessionReferences,
			projectMemory:  fit.projectMemory,
			inputSource:    fmt.Sprintf("Messages %d-%d:\n\n%s", chunk.StartIndex, chunk.EndIndex, text),
			memoryDisabled: !memoryEnabled, userObservationsDisabled: !set.Dreamer,
			extractionFree: run.Recomp,
		})
	}
	limit := h.contextWindow() - h.outputReserve()
	if EstimateTokens(system)+EstimateTokens(prompt(inputText)) > limit {
		inputText = truncateHistorianInput(inputText, func(t string) bool {
			return EstimateTokens(system)+EstimateTokens(prompt(t)) <= limit
		})
	}
	user := prompt(inputText)

	pass, err := h.validatedPass(ctx, system, user, chunk, seqOffset)
	if err != nil {
		return HistorianResult{}, err
	}

	emitted := pass.Compartments
	persisted := emitted
	forceKeep := run.ForceKeepLast && !chunk.HasMore
	// Recomp keeps every validated compartment (upstream recomp runner).
	if !run.Emergency && !forceKeep && !run.Recomp && shouldDiscardLastCompartment(emitted, chunk) {
		persisted = emitted[:len(emitted)-1]
	}
	lastNewEnd := 0
	if n := len(persisted); n > 0 {
		lastNewEnd = persisted[n-1].EndOrdinal
	}
	if lastNewEnd+1 <= offset {
		return HistorianResult{}, fmt.Errorf("historian made no forward progress beyond raw message %d", offset-1)
	}
	discarded := len(persisted) < len(emitted)
	skipUnanchored := discarded || forceKeep

	var events []ParsedEvent
	for _, e := range pass.Events {
		switch {
		case e.AtCompartment == 0:
			if forceKeep {
				continue
			}
		case e.AtCompartment > len(persisted):
			continue
		case forceKeep && e.AtCompartment >= len(emitted):
			continue
		}
		events = append(events, e)
	}
	res := HistorianResult{DiscardedLast: discarded, LastEnd: lastNewEnd}
	if !skipUnanchored && !run.Recomp {
		res.Facts = pass.Facts
		res.UserObservations = pass.UserObservations
		res.PrimerCandidates = pass.PrimerCandidates
	}
	res.HasMore = chunk.HasMore || lastNewEnd+1 < eligibleEnd
	return h.publish(ctx, run, persisted, events, res, offset)
}

// publishFilteredNoise covers an all-noise span with one placeholder
// compartment so later runs can advance past it.
func (h *Historian) publishFilteredNoise(ctx context.Context, run HistorianRun, chunk Chunk, offset, eligibleEnd, seq int) (HistorianResult, error) {
	end := eligibleEnd - 1
	for o := offset; o <= end; o++ {
		if _, ok := chunk.IDs[o]; !ok {
			return HistorianResult{Skipped: true, Reason: "no narrative in chunk", LastEnd: offset - 1}, nil
		}
	}
	one := 1
	c := Compartment{
		Sequence: seq, StartOrdinal: offset, EndOrdinal: end,
		StartMessageID: chunk.IDs[offset], EndMessageID: chunk.IDs[end],
		Importance: &one, EpisodeType: EpisodeFilteredNoise,
	}
	return h.publish(ctx, run, []Compartment{c}, nil, HistorianResult{LastEnd: end}, offset)
}

func (h *Historian) publish(ctx context.Context, run HistorianRun, cs []Compartment, events []ParsedEvent, res HistorianResult, offset int) (HistorianResult, error) {
	sid := run.SessionID
	byOrd := make(map[int]RawMessage, len(run.Raw))
	for _, m := range run.Raw {
		byOrd[m.Ordinal] = m
	}
	for i := range cs {
		c := &cs[i]
		c.StartTime = byOrd[c.StartOrdinal].CreatedAt
		c.EndTime = byOrd[c.EndOrdinal].CreatedAt
		if c.Content == "" {
			c.Content = c.P1
		}
	}
	lastEnd := cs[len(cs)-1].EndOrdinal
	var compacted []string
	for o := offset; o <= lastEnd; o++ {
		if m, ok := byOrd[o]; ok {
			compacted = append(compacted, m.ID)
		}
	}
	set := h.Settings
	promote := !run.Recomp && set.MemoryEnabled && set.AutoPromote && h.ProjectKey != "" && len(res.Facts) > 0
	var afterCommit []func()
	err := h.Store.Immediate(ctx, func(q querier) error {
		held, err := h.Store.LeaseHeld(ctx, q, sid, run.Holder)
		if err != nil {
			return err
		}
		if !held {
			return errLeaseLost
		}
		inserted, err := h.Store.InsertCompartments(ctx, q, sid, run.Recomp, cs)
		if err != nil {
			return err
		}
		if promote {
			after, err := h.Store.PromoteFactsAndEmbed(ctx, q, h.ProjectKey, sid, res.Facts, h.Embed, h.EmbedModelID)
			if err != nil {
				return err
			}
			if after != nil {
				afterCommit = append(afterCommit, after)
			}
		}
		if !run.Recomp && len(res.Facts) > 0 {
			if err := h.Store.InsertSessionFacts(ctx, q, sid, res.Facts); err != nil {
				return err
			}
		}
		// Staged recomp rows live in mc_recomp_compartments, which the
		// events FK cannot reference; upstream keeps no recomp events either.
		for _, e := range events {
			if run.Recomp {
				break
			}
			var id int64
			if e.AtCompartment >= 1 && e.AtCompartment <= len(inserted) {
				id = inserted[e.AtCompartment-1].ID
			}
			if err := h.Store.InsertEvent(ctx, q, sid, id, e.Kind, e.Fields); err != nil {
				return err
			}
		}
		if run.Recomp {
			return nil
		}
		if err := h.Store.MarkTagsCompacted(ctx, q, sid, compacted); err != nil {
			return err
		}
		// Side-table hand-offs are best effort, as upstream: a failed write is
		// logged and never fails the publish.
		if len(res.UserObservations) > 0 && userObservationCollection(set) {
			start, end := CompartmentSpan(inserted)
			if n, err := h.Store.InsertUserMemoryCandidates(ctx, q, sid, start, end, res.UserObservations); err != nil {
				h.logf("magic-context: storing user memory candidates failed session=%s: %v", sid, err)
			} else if n > 0 {
				h.logf("magic-context: stored %d user memory candidate(s) session=%s", n, sid)
			}
		}
		if pc, ok := PrimerCandidateFromHistorian(h.ProjectKey, sid, res.PrimerCandidates, inserted, time.Now()); ok {
			if err := h.Store.InsertPrimerCandidates(ctx, q, []PrimerCandidateInput{pc}); err != nil {
				h.logf("magic-context: storing primer candidate failed session=%s: %v", sid, err)
			}
		}
		last := inserted[len(inserted)-1]
		pending, _ := json.Marshal(PendingMaterialization{End: last.EndOrdinal, Seq: last.Sequence, EndMessageID: last.EndMessageID, PublishedAt: nowMS()})
		return h.Store.setMeta(ctx, q, sid, map[string]any{
			"historian_failure_count": 0,
			"historian_last_error":    "",
			"emergency_latched":       false,
			"materialize_pending":     string(pending),
		})
	})
	if err != nil {
		return HistorianResult{}, err
	}
	for _, f := range afterCommit {
		f()
	}
	res.Published = cs
	h.logf("magic-context: historian published session=%s compartments=%d range=%d-%d recomp=%v", sid, len(cs), offset, lastEnd, run.Recomp)
	if !run.Recomp {
		if err := h.Store.TriggerNoteNudge(ctx, sid, "historian_complete"); err != nil {
			h.logf("magic-context: historian note trigger failed session=%s: %v", sid, err)
		}
	}
	if h.OnPublished != nil && !run.Recomp {
		h.OnPublished(ctx, sid, res)
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Generation with retries, repair, fallback and the optional editor pass

func (h *Historian) sleep(ctx context.Context, d time.Duration) error {
	if h.Sleep != nil {
		return h.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func historianBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return 2*time.Second + time.Duration(mrand.IntN(1000))*time.Millisecond
	}
	return 6*time.Second + time.Duration(mrand.IntN(2000))*time.Millisecond
}

func (h *Historian) generate(ctx context.Context, gen Generator, system, user string) (string, error) {
	timeout := h.Settings.HistorianTimeout
	if timeout <= 0 {
		timeout = DefaultHistorianTimeout
	}
	var lastErr error
	for attempt := 0; attempt <= historianMaxRetries; attempt++ {
		if attempt > 0 {
			if !IsTransientHistorianError(lastErr) {
				break
			}
			if err := h.sleep(ctx, historianBackoff(attempt)); err != nil {
				return "", err
			}
		}
		tctx, cancel := context.WithTimeout(ctx, timeout)
		out, err := gen(tctx, system, user, h.outputReserve())
		cancel()
		if err == nil && strings.TrimSpace(out) == "" {
			err = errors.New("historian returned empty output")
		}
		if err == nil {
			return out, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		lastErr = err
	}
	return "", lastErr
}

func (h *Historian) validatedPass(ctx context.Context, system, user string, chunk Chunk, seqOffset int) (validatedPass, error) {
	gens := []Generator{h.Generate}
	if h.Fallback != nil {
		gens = append(gens, h.Fallback)
	}
	var lastErr error
	for _, gen := range gens {
		out, err := h.generate(ctx, gen, system, user)
		if err != nil {
			if ctx.Err() != nil {
				return validatedPass{}, ctx.Err()
			}
			lastErr = err
			continue
		}
		pass, verr := validateHistorianOutput(out, chunk, seqOffset)
		if verr != nil {
			repaired, err := h.generate(ctx, gen, system, buildHistorianRepairPrompt(user, out, verr.Error(), h.Settings.Language))
			if err != nil {
				if ctx.Err() != nil {
					return validatedPass{}, ctx.Err()
				}
				lastErr = err
				continue
			}
			if pass, verr = validateHistorianOutput(repaired, chunk, seqOffset); verr != nil {
				lastErr = verr
				continue
			}
			out = repaired
		}
		if h.Settings.TwoPass {
			edited, err := h.generate(ctx, gen, withContentLanguageDirective(historianEditorSystemPrompt, h.Settings.Language, true), buildHistorianEditorPrompt(out))
			if err == nil {
				if ep, eerr := validateHistorianOutput(edited, chunk, seqOffset); eerr == nil {
					pass = ep
				} else {
					h.logf("magic-context: historian editor output invalid, keeping draft: %v", eerr)
				}
			}
		}
		return pass, nil
	}
	return validatedPass{}, lastErr
}

// truncateHistorianInput keeps the longest prefix of text (plus a marker)
// that fits.
func truncateHistorianInput(text string, fits func(string) bool) string {
	runes := []rune(text)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(string(runes[:mid]) + historianTruncationMarker) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(runes[:lo]) + historianTruncationMarker
}

// ---------------------------------------------------------------------------
// Prompt fit

type fitArgs struct {
	system          string
	requested       int
	sessionID       string
	chunkStart      int
	lastOrdinal     int
	history         []Compartment
	memories        []Memory
	memoryDisabled  bool
	userObsDisabled bool
	extractionFree  bool
}

type historianFit struct {
	chunkTokens       int
	seedExamples      string
	sessionReferences string
	projectMemory     string
	trimmed           bool
}

// fitPrompt trims optional prompt blocks (session references, then project
// memory, then seeds) until the requested chunk fits in the historian
// window, shrinking the chunk as a last resort.
func (h *Historian) fitPrompt(a fitArgs) (historianFit, error) {
	seeds := selectSeeds(a.sessionID, a.chunkStart, seedFloor)
	refs := selectSessionReferences(a.history, seeds, a.sessionID, a.chunkStart)
	nRefs := min(len(refs), sessionReferencesLimit)
	limit := h.contextWindow() - h.outputReserve()
	systemTokens := EstimateTokens(a.system)
	header := fmt.Sprintf("Messages %d-%d:\n\n", a.chunkStart, a.lastOrdinal)

	render := func(r, m, s int) historianFit {
		f := historianFit{
			seedExamples:      renderSeedExamplesBlock(seeds[:s]),
			sessionReferences: renderSessionReferencesBlock(refs, float64(r)),
			trimmed:           r < nRefs || m < len(a.memories) || s < len(seeds),
		}
		if m > 0 {
			f.projectMemory = RenderProjectMemory(a.memories[:m], 0)
		}
		return f
	}
	room := func(f historianFit) int {
		fixed := systemTokens + EstimateTokens(buildCompartmentAgentPrompt(compartmentPromptInputs{
			seedExamples: f.seedExamples, sessionReferences: f.sessionReferences,
			projectMemory: f.projectMemory, inputSource: header,
			memoryDisabled: a.memoryDisabled, userObservationsDisabled: a.userObsDisabled,
			extractionFree: a.extractionFree,
		}))
		return limit - fixed - historianFitSlack
	}
	accept := func(f historianFit, chunk int) (historianFit, error) {
		f.chunkTokens = chunk
		return f, nil
	}
	req := a.requested
	for r := nRefs; r >= 0; r-- {
		if f := render(r, len(a.memories), len(seeds)); room(f) >= req {
			return accept(f, req)
		}
	}
	if len(a.memories) > 0 && room(render(0, 0, len(seeds))) >= req {
		lo, hi := 0, len(a.memories)
		for hi-lo > 1 {
			mid := (lo + hi) / 2
			if room(render(0, mid, len(seeds))) >= req {
				lo = mid
			} else {
				hi = mid
			}
		}
		return accept(render(0, lo, len(seeds)), req)
	}
	for s := len(seeds); s >= 0; s-- {
		if f := render(0, 0, s); room(f) >= req {
			return accept(f, req)
		}
	}
	floor := min(req, historianMinFitChunk)
	f := render(0, 0, 0)
	if r := room(f); r >= floor && r > 0 {
		return accept(f, min(req, r))
	}
	return historianFit{}, fmt.Errorf("%w: limit=%d min_chunk_tokens=%d", errHistorianWindowTooSmall, limit, floor)
}
