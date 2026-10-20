// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): packages/plugin/src/features/magic-context/search.ts (searchNotes, searchPrimers, formatResult)

package magiccontext

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

const maxNoteKeywordScore = 3.5

// noteSearchableStatuses are the note statuses ctx_search can return.
var noteSearchableStatuses = []string{"active", "pending", "ready"}

// DefaultSearchSources returns the built-in primer and note providers.
func (s *Store) DefaultSearchSources() map[string]SearchSourceFunc {
	return map[string]SearchSourceFunc{
		SourcePrimer: s.searchPrimerSource,
		SourceNote:   s.searchNoteSource,
	}
}

func inDateRange(ts int64, req SearchRequest) bool {
	if req.From > 0 && ts < req.From {
		return false
	}
	if req.To > 0 && ts > req.To {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Notes

var (
	keywordTokenRe = regexp.MustCompile(`[a-z0-9/._:-]+`)
	hasAlnumRe     = regexp.MustCompile(`[a-z0-9]`)
)

func tokenizeKeywordNeedle(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range keywordTokenRe.FindAllString(strings.ToLower(text), -1) {
		if len(m) <= 1 || !hasAlnumRe.MatchString(m) || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func noteSearchText(n Note) string {
	if r := strings.TrimSpace(n.ReadyReason); r != "" {
		return n.Content + "\nReason: " + r
	}
	return n.Content
}

type rankedNote struct {
	note  Note
	score float64
	text  string
}

func sortRankedNotes(r []rankedNote) {
	sort.SliceStable(r, func(i, j int) bool {
		a, b := r[i], r[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.note.CreatedAt != b.note.CreatedAt {
			return a.note.CreatedAt > b.note.CreatedAt
		}
		return a.note.ID < b.note.ID
	})
}

func rankNotesForNeedle(notes []Note, needle string) []rankedNote {
	nn := strings.ToLower(strings.TrimSpace(needle))
	if nn == "" {
		return nil
	}
	needleTokens := tokenizeKeywordNeedle(nn)
	var out []rankedNote
	for _, n := range notes {
		text := noteSearchText(n)
		lower := strings.ToLower(text)
		noteTokens := map[string]bool{}
		for _, t := range tokenizeKeywordNeedle(lower) {
			noteTokens[t] = true
		}
		exact := strings.Contains(lower, nn)
		matched := 0
		for _, t := range needleTokens {
			if noteTokens[t] {
				matched++
			}
		}
		if !exact && matched == 0 {
			continue
		}
		// needleTokens are already unique, so matched == matched unique.
		var coverage, density float64
		if len(needleTokens) > 0 {
			coverage = float64(matched) / float64(len(needleTokens))
		}
		if len(noteTokens) > 0 {
			density = float64(matched) / float64(len(noteTokens))
		}
		score := coverage * density
		if exact && (len(needleTokens) > 1 || strings.TrimSpace(lower) == nn) {
			score += 2
		}
		if len(needleTokens) > 1 && matched == len(needleTokens) {
			score += 0.5 * density
		}
		out = append(out, rankedNote{note: n, score: score, text: text})
	}
	sortRankedNotes(out)
	return out
}

func normalizeNoteKeywordScore(score float64) float64 {
	return clamp01(score/maxNoteKeywordScore) * singleSourcePenalty
}

func (s *Store) searchNoteSource(ctx context.Context, req SearchRequest, _ []float32, limit int) ([]SearchHit, error) {
	if limit <= 0 {
		return nil, nil
	}
	sessionNotes, err := s.GetNotes(ctx, NoteQuery{SessionID: req.SessionID, Type: NoteTypeSession, Statuses: noteSearchableStatuses})
	if err != nil {
		return nil, err
	}
	var smart []Note
	if req.ProjectKey != "" {
		if smart, err = s.GetNotes(ctx, NoteQuery{ProjectPath: req.ProjectKey, Type: NoteTypeSmart, Statuses: noteSearchableStatuses}); err != nil {
			return nil, err
		}
	}
	var notes []Note
	for _, n := range append(sessionNotes, smart...) {
		if inDateRange(n.CreatedAt, req) {
			notes = append(notes, n)
		}
	}
	if len(notes) == 0 {
		return nil, nil
	}
	ranked := rankNotesForNeedle(notes, req.Query)
	var probes []string
	if req.Explicit {
		probes = extractLiteralProbes(req.Query)
	}
	if len(probes) > 0 {
		type fusedNote struct {
			entry rankedNote
			score float64
		}
		fused := map[int64]*fusedNote{}
		add := func(rows []rankedNote, w float64) {
			for _, r := range rows {
				rel := r.score * w
				if f, ok := fused[r.note.ID]; ok {
					if rel > f.score {
						f.entry, f.score = r, rel
					}
				} else {
					fused[r.note.ID] = &fusedNote{entry: r, score: rel}
				}
			}
		}
		add(ranked, 1)
		for _, p := range probes {
			rows := rankNotesForNeedle(notes, p)
			if len(rows) == 0 {
				continue
			}
			add(rows, 1/(1+idfFalloff*float64(len(rows))/float64(len(notes))))
		}
		ranked = ranked[:0]
		for _, f := range fused {
			e := f.entry
			e.score = f.score
			ranked = append(ranked, e)
		}
		sortRankedNotes(ranked)
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	hits := make([]SearchHit, 0, len(ranked))
	for _, r := range ranked {
		h := SearchHit{
			Source:    SourceNote,
			Score:     normalizeNoteKeywordScore(r.score),
			MatchType: "keyword",
			Content:   r.text,
			ID:        r.note.ID,
			Status:    r.note.Status,
			CreatedAt: r.note.CreatedAt,
		}
		if r.note.AnchorOrdinal > 0 && r.note.SessionID == req.SessionID {
			h.Ordinal = int(r.note.AnchorOrdinal)
		}
		hits = append(hits, h)
	}
	return hits, nil
}

// ---------------------------------------------------------------------------
// Primers

func primerText(p Primer) string {
	if a := strings.TrimSpace(p.Answer); a != "" {
		return "Q: " + p.Question + "\nA: " + a
	}
	return "Q: " + p.Question
}

// primerKeywordRank orders primers by keyword relevance; it stands in for
// upstream's primers_fts bm25 lane, whose score depends only on rank order.
func primerKeywordRank(prims []Primer, query string) []int64 {
	qTok := tokenizeKeywordNeedle(query)
	if len(qTok) == 0 {
		return nil
	}
	type scored struct {
		id    int64
		score float64
	}
	var rows []scored
	for _, p := range prims {
		toks := tokenizeKeywordNeedle(p.Question + " " + p.Answer)
		set := map[string]int{}
		for _, t := range toks {
			set[t]++
		}
		matched := 0
		for _, t := range qTok {
			if set[t] > 0 {
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		cov := float64(matched) / float64(len(qTok))
		dens := float64(matched) / float64(max(1, len(set)))
		rows = append(rows, scored{p.ID, cov + 0.1*dens})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		return rows[i].id < rows[j].id
	})
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	return ids
}

func (s *Store) searchPrimerSource(ctx context.Context, req SearchRequest, queryVec []float32, limit int) ([]SearchHit, error) {
	if limit <= 0 || req.ProjectKey == "" {
		return nil, nil
	}
	all, err := s.ActivePrimers(ctx, req.ProjectKey)
	if err != nil {
		return nil, err
	}
	var prims []Primer
	for _, p := range all {
		if inDateRange(p.CreatedAt, req) {
			prims = append(prims, p)
		}
	}
	if len(prims) == 0 {
		return nil, nil
	}
	ftsRank := map[int64]float64{}
	ids := primerKeywordRank(prims, req.Query)
	if len(ids) > limit*3 {
		ids = ids[:limit*3]
	}
	for i, id := range ids {
		ftsRank[id] = linearDecayScore(i, len(ids))
	}
	model := req.EmbeddingModel
	var hits []SearchHit
	for _, p := range prims {
		var sem float64
		if len(queryVec) > 0 && len(p.Embedding) > 0 && model != "" && model != "off" && p.EmbeddingModelID == model {
			sem = clamp01(cosineSimilarity(queryVec, p.Embedding))
		}
		fts := ftsRank[p.ID]
		if sem <= 0 && fts <= 0 {
			continue
		}
		h := SearchHit{Source: SourcePrimer, Content: primerText(p), ID: p.ID, Support: p.TotalSupport, CreatedAt: p.CreatedAt}
		switch {
		case sem > 0 && fts > 0:
			h.Score, h.MatchType = sem*semanticWeight+fts*ftsWeight, "hybrid"
		case sem > 0:
			h.Score, h.MatchType = sem, "semantic"
		default:
			h.Score, h.MatchType = fts, "fts"
		}
		hits = append(hits, h)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Support != b.Support {
			return a.Support > b.Support
		}
		return a.ID < b.ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
