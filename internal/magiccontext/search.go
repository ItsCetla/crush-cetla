// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/search.ts, literal-probes.ts, memory/storage-memory-fts.ts, hooks/magic-context/auto-search-hint.ts.

// Unified archive search (memories, raw message history, git commits),
// ported from cortexkit/magic-context (MIT) search.ts, literal-probes.ts,
// memory/storage-memory-fts.ts and hooks/magic-context/auto-search-hint.ts.

package magiccontext

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Search sources.
const (
	SourceMemory    = "memory"
	SourceMessage   = "message"
	SourceGitCommit = "git_commit"
	SourcePrimer    = "primer"
	SourceNote      = "note"
)

// AllSearchSources lists every ctx_search source.
var AllSearchSources = []string{SourceMemory, SourceMessage, SourceGitCommit, SourcePrimer, SourceNote}

const (
	searchDefaultLimit   = 10
	searchMaxLimit       = 25
	ftsSemanticCandidate = 50
	semanticWeight       = 0.7
	ftsWeight            = 0.3
	singleSourcePenalty  = 0.8
	rrfK                 = 60.0
	verbatimRankBonus    = 1.0 / 60
	idfFalloff           = 100.0
	resultPreviewLimit   = 220
)

var sourceBoost = map[string]float64{
	SourceMemory: 1.3, SourceMessage: 1.275, SourcePrimer: 1.25, SourceGitCommit: 1.2, SourceNote: 1.0,
}

// SearchHit is one result of any source.
type SearchHit struct {
	Source    string
	Score     float64
	MatchType string // hybrid | semantic | fts
	Content   string

	// memory
	MemoryID int64
	Category string
	// message
	Ordinal   int
	MessageID string
	Role      string
	// git_commit
	SHA         string
	ShortSHA    string
	CommittedAt int64 // ms
	// generic (notes/primers plugged in through ExtraSources)
	ID        int64
	Label     string // pre-rendered header tail for custom sources
	CreatedAt int64  // ms
	// primer
	Support int
	// note (Ordinal holds the anchor message when it is in this session)
	Status string
}

// SearchSourceFunc lets other subsystems (notes, primers) contribute hits.
// queryVec is nil when embeddings are off.
type SearchSourceFunc func(ctx context.Context, req SearchRequest, queryVec []float32, limit int) ([]SearchHit, error)

// SearchRequest drives Search.
type SearchRequest struct {
	ProjectKey string
	SessionID  string
	Query      string
	Limit      int      // default 10, clamped to 1..25
	Sources    []string // empty = all
	From, To   int64    // inclusive ms; 0 = open
	// Explicit is true for ctx_search (literal probes, relaxed message
	// fallback, retrieval bumps) and false for the auto hint.
	Explicit bool
	// VisibleMemoryIDs are already in <project-memory> and are suppressed.
	VisibleMemoryIDs []int64
	MemoryEnabled    bool
	Embedder         Embedder
	EmbeddingModel   string
	// ExtraSources maps "primer"/"note" to providers implemented elsewhere.
	ExtraSources map[string]SearchSourceFunc
}

// SearchDiagnostics explain hidden results.
type SearchDiagnostics struct {
	SuppressedMemoryIDs []int64
	NewerMessageMatches int
	NoGitRepository     bool
	FTSUnavailable      bool
}

// SearchResult is the unified, sorted, limited result.
type SearchResult struct {
	Hits        []SearchHit
	Diagnostics SearchDiagnostics
}

// ---------------------------------------------------------------------------
// FTS5 setup (lazy; LIKE fallback when the sqlite build lacks fts5)

type ftsState struct {
	once sync.Once
	ok   bool
	err  error
}

var ftsStates sync.Map // *sql.DB -> *ftsState

var ftsTables = []struct{ fts, base, rowid, cols string }{
	{"mc_memories_fts", "mc_memories", "id", "content, category"},
	{"mc_message_index_fts", "mc_message_index", "rowid", "content, role"},
	{"mc_git_commits_fts", "mc_git_commits", "rowid", "message"},
}

func ftsDDL(fts, base, rowid, cols string) []string {
	var newCols, oldCols []string
	for _, c := range strings.Split(cols, ", ") {
		newCols = append(newCols, "new."+c)
		oldCols = append(oldCols, "old."+c)
	}
	nc, oc := strings.Join(newCols, ", "), strings.Join(oldCols, ", ")
	ins := fmt.Sprintf(`INSERT INTO %s(rowid, %s) VALUES (new.%s, %s);`, fts, cols, rowid, nc)
	del := fmt.Sprintf(`INSERT INTO %s(%s, rowid, %s) VALUES ('delete', old.%s, %s);`, fts, fts, cols, rowid, oc)
	return []string{
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(%s, content='%s', content_rowid='%s', tokenize='porter unicode61')`, fts, cols, base, rowid),
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s_ai AFTER INSERT ON %s BEGIN %s END`, fts, base, ins),
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s_ad AFTER DELETE ON %s BEGIN %s END`, fts, base, del),
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s_au AFTER UPDATE ON %s BEGIN %s %s END`, fts, base, del, ins),
	}
}

// EnsureSearchIndex lazily creates the fts5 tables and triggers once per
// database. It reports false (and search falls back to LIKE) when the
// sqlite build has no fts5 module.
func (s *Store) EnsureSearchIndex(ctx context.Context) bool {
	v, _ := ftsStates.LoadOrStore(s.db, &ftsState{})
	st := v.(*ftsState)
	st.once.Do(func() {
		if _, err := s.db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS temp.mc_fts_probe USING fts5(x)`); err != nil {
			st.err = err
			return
		}
		_, _ = s.db.ExecContext(ctx, `DROP TABLE IF EXISTS temp.mc_fts_probe`)
		st.err = s.Immediate(ctx, func(q querier) error {
			for _, t := range ftsTables {
				var n int
				if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, t.fts).Scan(&n); err != nil {
					return err
				}
				for _, stmt := range ftsDDL(t.fts, t.base, t.rowid, t.cols) {
					if _, err := q.ExecContext(ctx, stmt); err != nil {
						return err
					}
				}
				if n == 0 {
					if _, err := q.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s(%s) VALUES ('rebuild')`, t.fts, t.fts)); err != nil {
						return err
					}
				}
			}
			return nil
		})
		st.ok = st.err == nil
	})
	return st.ok
}

// ---------------------------------------------------------------------------
// Query building

// sanitizeFTSQuery quotes every whitespace token (implicit AND).
func sanitizeFTSQuery(q string) string {
	q = strings.ReplaceAll(q, "\x00", " ")
	var parts []string
	for _, tok := range strings.Fields(q) {
		parts = append(parts, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " ")
}

var (
	wordTokenRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)
	digitRe     = regexp.MustCompile(`\p{N}`)
)

func relaxedTokens(q string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range wordTokenRe.FindAllString(q, -1) {
		key := strings.ToLower(tok)
		if seen[key] || (utf8.RuneCountInString(tok) <= 2 && !digitRe.MatchString(tok)) {
			continue
		}
		seen[key] = true
		out = append(out, tok)
		if len(out) == 16 {
			break
		}
	}
	return out
}

// relaxedFTSQuery ORs the meaningful word tokens.
func relaxedFTSQuery(q string) string {
	toks := relaxedTokens(q)
	for i, t := range toks {
		toks[i] = `"` + t + `"`
	}
	return strings.Join(toks, " OR ")
}

func likePattern(tok string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(tok) + "%"
}

// likeClause builds `(col LIKE ? ESCAPE '\' AND/OR ...)` for tokens.
func likeClause(col string, toks []string, joiner string) (string, []any) {
	if len(toks) == 0 {
		return "", nil
	}
	parts := make([]string, len(toks))
	args := make([]any, len(toks))
	for i, t := range toks {
		parts[i] = col + ` LIKE ? ESCAPE '\'`
		args[i] = likePattern(t)
	}
	return "(" + strings.Join(parts, " "+joiner+" ") + ")", args
}

func dateBounds(req SearchRequest) (int64, int64) {
	from, to := req.From, req.To
	if to == 0 {
		to = math.MaxInt64
	}
	return from, to
}

// ---------------------------------------------------------------------------
// Literal probes

var (
	probeQuotedRe = regexp.MustCompile("[\"`]([^\"`]{3,80})[\"`]")
	probeSlashRe  = regexp.MustCompile(`(?i)/[a-z][a-z0-9]*(?:-[a-z0-9]+)+`)
	probeErrorRe  = regexp.MustCompile(`\b(?:TS\d{4,}|ERR_[A-Z][A-Z0-9_]*)\b`)
	probeDottedRe = regexp.MustCompile(`(?i)[a-z0-9][a-z0-9_-]*(?:\.[a-z0-9_-]+)+`)
	probeKebabRe  = regexp.MustCompile(`(?i)[a-z][a-z0-9]*(?:[-_][a-z0-9]+)+`)
	probeCamelRe  = regexp.MustCompile(`\b[a-zA-Z][a-z0-9]*(?:[A-Z][a-z0-9]*)+\b`)
	probeShaRe    = regexp.MustCompile(`(?i)\b[0-9a-f]{7,40}\b`)
	hasDigitRe    = regexp.MustCompile(`[0-9]`)
)

// extractLiteralProbes pulls exact-match literals out of a query.
func extractLiteralProbes(q string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if len(out) >= 5 || len(p) < 3 || seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	for _, m := range probeQuotedRe.FindAllStringSubmatch(q, -1) {
		add(m[1])
	}
	for _, re := range []*regexp.Regexp{probeSlashRe, probeErrorRe, probeDottedRe, probeKebabRe} {
		for _, m := range re.FindAllString(q, -1) {
			add(m)
		}
	}
	for _, m := range probeCamelRe.FindAllString(q, -1) {
		if strings.ToLower(m) != m { // a real camelCase / PascalCase word
			add(m)
		}
	}
	for _, m := range probeShaRe.FindAllString(q, -1) {
		if hasDigitRe.MatchString(m) {
			add(m)
		}
	}
	return out
}

func linearDecayScore(rank, n int) float64 {
	if n <= 0 {
		return 0
	}
	return math.Max(0, 1-float64(rank)/float64(n))
}

// ---------------------------------------------------------------------------
// Search

// Search runs the unified search across the selected sources.
func (s *Store) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	var res SearchResult
	if req.Limit <= 0 {
		req.Limit = searchDefaultLimit
	}
	req.Limit = min(req.Limit, searchMaxLimit)
	q := strings.TrimSpace(req.Query)
	if q == "" {
		return res, nil
	}
	req.Query = q
	hasFTS := s.EnsureSearchIndex(ctx)
	res.Diagnostics.FTSUnavailable = !hasFTS
	want := func(src string) bool { return len(req.Sources) == 0 || slices.Contains(req.Sources, src) }
	tierLimit := max(req.Limit*3, 10)

	var queryVec []float32
	if req.Embedder != nil && req.EmbeddingModel != "" {
		ectx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if vecs, err := req.Embedder(ectx, []string{q}); err == nil && len(vecs) == 1 {
			queryVec = vecs[0]
		}
		cancel()
	}

	var all []SearchHit
	if want(SourceMemory) && req.MemoryEnabled && req.ProjectKey != "" {
		hits, suppressed, err := s.searchMemories(ctx, req, hasFTS, queryVec, tierLimit)
		if err != nil {
			return res, err
		}
		res.Diagnostics.SuppressedMemoryIDs = suppressed
		all = append(all, hits...)
	}
	if want(SourceMessage) && req.SessionID != "" {
		hits, newer, err := s.searchMessages(ctx, req, hasFTS, tierLimit)
		if err != nil {
			return res, err
		}
		res.Diagnostics.NewerMessageMatches = newer
		all = append(all, hits...)
	}
	if want(SourceGitCommit) && req.ProjectKey != "" {
		if !strings.HasPrefix(req.ProjectKey, "git:") {
			res.Diagnostics.NoGitRepository = true
		} else {
			hits, err := s.searchGitCommits(ctx, req, hasFTS, queryVec, tierLimit)
			if err != nil {
				return res, err
			}
			all = append(all, hits...)
		}
	}
	for _, src := range []string{SourcePrimer, SourceNote} {
		fn := req.ExtraSources[src]
		// Primers are a memory feature; notes always run.
		if fn == nil || !want(src) || (src == SourcePrimer && !req.MemoryEnabled) {
			continue
		}
		hits, err := fn(ctx, req, queryVec, tierLimit)
		if err != nil {
			return res, err
		}
		all = append(all, hits...)
	}
	sortUnified(all)
	if len(all) > req.Limit {
		all = all[:req.Limit]
	}
	res.Hits = all
	if req.Explicit {
		var ids []int64
		for _, h := range all {
			if h.Source == SourceMemory {
				ids = append(ids, h.MemoryID)
			}
		}
		if len(ids) > 0 {
			if err := s.Immediate(ctx, func(q querier) error { return bumpRetrieval(ctx, q, ids) }); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

func sortUnified(all []SearchHit) {
	order := map[string]int{SourceMemory: 0, SourceMessage: 1, SourceGitCommit: 2, SourcePrimer: 3, SourceNote: 4}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		sa, sb := a.Score*sourceBoost[a.Source], b.Score*sourceBoost[b.Source]
		if sa != sb {
			return sa > sb
		}
		if a.Source != b.Source {
			return order[a.Source] < order[b.Source]
		}
		switch a.Source {
		case SourceMemory:
			return a.MemoryID < b.MemoryID
		case SourceMessage:
			return a.Ordinal < b.Ordinal
		case SourceGitCommit:
			return a.CommittedAt > b.CommittedAt
		case SourceNote:
			return a.CreatedAt > b.CreatedAt
		default:
			return a.ID < b.ID
		}
	})
}

func fuseScores(sem, fts float64, hasSem, hasFTS bool) (float64, string) {
	switch {
	case hasSem && hasFTS:
		return semanticWeight*sem + ftsWeight*fts, "hybrid"
	case hasSem:
		return singleSourcePenalty * sem, "semantic"
	default:
		return singleSourcePenalty * fts, "fts"
	}
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

const memoryVisibleWhere = `m.project_path = ? AND m.status IN ('active','permanent') AND m.superseded_by_memory_id = 0 AND (m.expires_at = 0 OR m.expires_at > ?) AND m.created_at BETWEEN ? AND ?`

func (s *Store) searchMemories(ctx context.Context, req SearchRequest, hasFTS bool, queryVec []float32, limit int) ([]SearchHit, []int64, error) {
	from, to := dateBounds(req)
	now := nowMS()
	base := []any{req.ProjectKey, now, from, to}
	cols := strings.ReplaceAll("m."+memoryCols, ", ", ", m.")

	run := func(match string) ([]Memory, error) {
		if hasFTS {
			return queryMemories(ctx, s.db, `SELECT `+cols+` FROM mc_memories_fts JOIN mc_memories m ON m.id = mc_memories_fts.rowid
WHERE `+memoryVisibleWhere+` AND mc_memories_fts MATCH ? ORDER BY bm25(mc_memories_fts), m.updated_at DESC, m.id ASC LIMIT ?`,
				append(slices.Clone(base), match, ftsSemanticCandidate)...)
		}
		return nil, nil
	}
	var ftsMems []Memory
	var err error
	if hasFTS {
		if ftsMems, err = run(sanitizeFTSQuery(req.Query)); err != nil {
			return nil, nil, err
		}
		if len(ftsMems) == 0 {
			if rq := relaxedFTSQuery(req.Query); rq != "" {
				if ftsMems, err = run(rq); err != nil {
					return nil, nil, err
				}
			}
		}
	} else {
		for _, joiner := range []string{"AND", "OR"} {
			toks := strings.Fields(req.Query)
			if joiner == "OR" {
				toks = relaxedTokens(req.Query)
			}
			clause, args := likeClause("m.content", toks, joiner)
			if clause == "" {
				continue
			}
			ftsMems, err = queryMemories(ctx, s.db, `SELECT `+cols+` FROM mc_memories m WHERE `+memoryVisibleWhere+` AND `+clause+` ORDER BY m.updated_at DESC, m.id ASC LIMIT ?`,
				append(append(slices.Clone(base), args...), ftsSemanticCandidate)...)
			if err != nil {
				return nil, nil, err
			}
			if len(ftsMems) > 0 {
				break
			}
		}
	}

	type cand struct {
		m              Memory
		fts, sem       float64
		hasFTS, hasSem bool
	}
	cands := map[int64]*cand{}
	for i, m := range ftsMems {
		cands[m.ID] = &cand{m: m, fts: 1 / float64(i+1), hasFTS: true}
	}
	if queryVec != nil {
		rows, err := s.db.QueryContext(ctx, `SELECT `+cols+`, e.embedding FROM mc_memory_embeddings e JOIN mc_memories m ON m.id = e.memory_id
WHERE e.model_id = ? AND `+memoryVisibleWhere, append([]any{req.EmbeddingModel}, base...)...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var blob []byte
			var m Memory
			var imp sql.NullInt64
			if err := rows.Scan(&m.ID, &m.ProjectPath, &m.Category, &m.Content, &m.NormalizedHash, &imp, &m.Scope, &m.SourceSessionID,
				&m.SourceType, &m.SeenCount, &m.RetrievalCount, &m.FirstSeenAt, &m.CreatedAt, &m.UpdatedAt, &m.LastSeenAt,
				&m.LastRetrievedAt, &m.Status, &m.ExpiresAt, &m.VerificationStatus, &m.VerifiedAt, &m.ClassifiedAt,
				&m.SupersededBy, &m.MergedFrom, &blob); err != nil {
				rows.Close()
				return nil, nil, err
			}
			m.Importance = int(imp.Int64)
			sim := clamp01(cosineSimilarity(queryVec, decodeEmbedding(blob)))
			c := cands[m.ID]
			if c == nil {
				c = &cand{m: m}
				cands[m.ID] = c
			}
			c.sem, c.hasSem = sim, true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	visible := map[int64]bool{}
	for _, id := range req.VisibleMemoryIDs {
		visible[id] = true
	}
	var hits []SearchHit
	var suppressed []int64
	for _, c := range cands {
		score, mt := fuseScores(c.sem, c.fts, c.hasSem, c.hasFTS)
		if score <= 0 {
			continue
		}
		if visible[c.m.ID] {
			suppressed = append(suppressed, c.m.ID)
			continue
		}
		hits = append(hits, SearchHit{Source: SourceMemory, Score: score, MatchType: mt, Content: c.m.Content, MemoryID: c.m.ID, Category: c.m.Category, CreatedAt: c.m.CreatedAt})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].MemoryID < hits[j].MemoryID
	})
	slices.Sort(suppressed)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, suppressed, nil
}

// MessageCutoff is the last compartment's end ordinal (0 = none): raw
// messages after it are still in the live context and are not searched.
func (s *Store) MessageCutoff(ctx context.Context, sessionID string) (int, error) {
	cs, err := s.Compartments(ctx, sessionID)
	if err != nil || len(cs) == 0 {
		return 0, err
	}
	end := 0
	for _, c := range cs {
		end = max(end, c.EndOrdinal)
	}
	return end, nil
}

type msgRow struct {
	ordinal   int
	messageID string
	role      string
	content   string
	createdAt int64
}

func (s *Store) messageMatches(ctx context.Context, req SearchRequest, hasFTS bool, match string, toks []string, joiner string, cutoff, limit int, newer bool) ([]msgRow, error) {
	from, to := dateBounds(req)
	cmp := "<="
	if newer {
		cmp = ">"
	}
	var query string
	var args []any
	if hasFTS {
		query = `SELECT i.ordinal, i.message_id, i.role, i.content, i.created_at FROM mc_message_index_fts f JOIN mc_message_index i ON i.rowid = f.rowid
WHERE i.session_id = ? AND i.ordinal ` + cmp + ` ? AND i.created_at BETWEEN ? AND ? AND mc_message_index_fts MATCH ? ORDER BY bm25(mc_message_index_fts), i.ordinal ASC LIMIT ?`
		args = []any{req.SessionID, cutoff, from, to, match, limit}
	} else {
		clause, largs := likeClause("i.content", toks, joiner)
		if clause == "" {
			return nil, nil
		}
		query = `SELECT i.ordinal, i.message_id, i.role, i.content, i.created_at FROM mc_message_index i
WHERE i.session_id = ? AND i.ordinal ` + cmp + ` ? AND i.created_at BETWEEN ? AND ? AND ` + clause + ` ORDER BY i.ordinal DESC LIMIT ?`
		args = append(append([]any{req.SessionID, cutoff, from, to}, largs...), limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []msgRow
	for rows.Next() {
		var r msgRow
		if err := rows.Scan(&r.ordinal, &r.messageID, &r.role, &r.content, &r.createdAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) searchMessages(ctx context.Context, req SearchRequest, hasFTS bool, limit int) ([]SearchHit, int, error) {
	cutoff, err := s.MessageCutoff(ctx, req.SessionID)
	if err != nil {
		return nil, 0, err
	}
	fetch := limit * 3
	strictToks := strings.Fields(req.Query)
	base, err := s.messageMatches(ctx, req, hasFTS, sanitizeFTSQuery(req.Query), strictToks, "AND", cutoff, fetch, false)
	if err != nil {
		return nil, 0, err
	}
	if len(base) == 0 && req.Explicit {
		if rq := relaxedFTSQuery(req.Query); rq != "" {
			if base, err = s.messageMatches(ctx, req, hasFTS, rq, relaxedTokens(req.Query), "OR", cutoff, fetch, false); err != nil {
				return nil, 0, err
			}
		}
	}
	newerCount := 0
	if req.Explicit {
		newer, err := s.messageMatches(ctx, req, hasFTS, sanitizeFTSQuery(req.Query), strictToks, "AND", cutoff, 1000, true)
		if err != nil {
			return nil, 0, err
		}
		newerCount = len(newer)
	}

	type acc struct {
		r     msgRow
		score float64
	}
	byOrd := map[int]*acc{}
	for i, r := range base {
		byOrd[r.ordinal] = &acc{r: r, score: 1 / (rrfK + float64(i+1))}
	}
	if req.Explicit {
		probes := extractLiteralProbes(req.Query)
		if len(probes) > 0 {
			var total int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mc_message_index WHERE session_id = ? AND ordinal <= ?`, req.SessionID, cutoff).Scan(&total); err != nil {
				return nil, 0, err
			}
			weights := map[string]float64{}
			for _, p := range probes {
				rows, err := s.messageMatches(ctx, req, hasFTS, sanitizeFTSQuery(p), []string{p}, "AND", cutoff, max(fetch, 200), false)
				if err != nil {
					return nil, 0, err
				}
				w := 1.0
				if total > 0 {
					w = 1 / (1 + idfFalloff*float64(len(rows))/float64(total))
				}
				weights[p] = w
				for i, r := range rows {
					if i >= fetch {
						break
					}
					a := byOrd[r.ordinal]
					if a == nil {
						a = &acc{r: r}
						byOrd[r.ordinal] = a
					}
					a.score += w / (rrfK + float64(i+1))
				}
			}
			for _, a := range byOrd {
				best := 0.0
				lc := strings.ToLower(a.r.content)
				for _, p := range probes {
					if strings.Contains(lc, strings.ToLower(p)) {
						best = math.Max(best, weights[p])
					}
				}
				a.score += best * verbatimRankBonus
			}
		}
	}
	list := make([]*acc, 0, len(byOrd))
	for _, a := range byOrd {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].r.ordinal < list[j].r.ordinal
	})
	n := len(list)
	var hits []SearchHit
	for i, a := range list {
		score := linearDecayScore(i, n)
		if score <= 0 {
			continue
		}
		hits = append(hits, SearchHit{Source: SourceMessage, Score: score, MatchType: "fts", Content: a.r.content, Ordinal: a.r.ordinal, MessageID: a.r.messageID, Role: a.r.role, CreatedAt: a.r.createdAt})
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, newerCount, nil
}

type gitRow struct {
	sha, short, message string
	committedAt         int64
}

func scanGitRows(rows *sql.Rows, err error) ([]gitRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gitRow
	for rows.Next() {
		var r gitRow
		if err := rows.Scan(&r.sha, &r.short, &r.message, &r.committedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) searchGitCommits(ctx context.Context, req SearchRequest, hasFTS bool, queryVec []float32, limit int) ([]SearchHit, error) {
	from, to := dateBounds(req)
	fetch := max(limit*3, 30)
	var ftsRows []gitRow
	var err error
	if hasFTS {
		for _, match := range []string{sanitizeFTSQuery(req.Query), relaxedFTSQuery(req.Query)} {
			if match == "" {
				continue
			}
			ftsRows, err = scanGitRows(s.db.QueryContext(ctx, `SELECT g.sha, g.short_sha, g.message, g.committed_at FROM mc_git_commits_fts f JOIN mc_git_commits g ON g.rowid = f.rowid
WHERE g.project_path = ? AND g.committed_at BETWEEN ? AND ? AND mc_git_commits_fts MATCH ? ORDER BY bm25(mc_git_commits_fts), g.committed_at DESC LIMIT ?`,
				req.ProjectKey, from, to, match, fetch))
			if err != nil {
				return nil, err
			}
			if len(ftsRows) > 0 {
				break
			}
		}
	} else {
		for _, joiner := range []string{"AND", "OR"} {
			toks := strings.Fields(req.Query)
			if joiner == "OR" {
				toks = relaxedTokens(req.Query)
			}
			clause, args := likeClause("g.message", toks, joiner)
			if clause == "" {
				continue
			}
			ftsRows, err = scanGitRows(s.db.QueryContext(ctx, `SELECT g.sha, g.short_sha, g.message, g.committed_at FROM mc_git_commits g
WHERE g.project_path = ? AND g.committed_at BETWEEN ? AND ? AND `+clause+` ORDER BY g.committed_at DESC LIMIT ?`,
				append(append([]any{req.ProjectKey, from, to}, args...), fetch)...))
			if err != nil {
				return nil, err
			}
			if len(ftsRows) > 0 {
				break
			}
		}
	}
	type cand struct {
		r              gitRow
		fts, sem       float64
		hasFTS, hasSem bool
	}
	cands := map[string]*cand{}
	for i, r := range ftsRows {
		cands[r.sha] = &cand{r: r, fts: 1 / float64(i+1), hasFTS: true}
	}
	if queryVec != nil {
		rows, err := s.db.QueryContext(ctx, `SELECT g.sha, g.short_sha, g.message, g.committed_at, e.embedding FROM mc_git_commit_embeddings e JOIN mc_git_commits g ON g.sha = e.sha
WHERE e.model_id = ? AND g.project_path = ? AND g.committed_at BETWEEN ? AND ?`, req.EmbeddingModel, req.ProjectKey, from, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r gitRow
			var blob []byte
			if err := rows.Scan(&r.sha, &r.short, &r.message, &r.committedAt, &blob); err != nil {
				rows.Close()
				return nil, err
			}
			c := cands[r.sha]
			if c == nil {
				c = &cand{r: r}
				cands[r.sha] = c
			}
			c.sem, c.hasSem = clamp01(cosineSimilarity(queryVec, decodeEmbedding(blob))), true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	var hits []SearchHit
	for _, c := range cands {
		score, mt := fuseScores(c.sem, c.fts, c.hasSem, c.hasFTS)
		if score <= 0 {
			continue
		}
		hits = append(hits, SearchHit{Source: SourceGitCommit, Score: score, MatchType: mt, Content: c.r.message, SHA: c.r.sha, ShortSHA: c.r.short, CommittedAt: c.r.committedAt})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].CommittedAt > hits[j].CommittedAt
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// MemoriesByIDs resolves ID-shaped queries: active, permanent and archived
// memories of the project, in the given order.
func (s *Store) MemoriesByIDs(ctx context.Context, projectKey string, ids []int64) ([]Memory, error) {
	var out []Memory
	for _, id := range ids {
		m, ok, err := memoryByID(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		if !ok || m.ProjectPath != projectKey || !slices.Contains([]string{MemoryStatusActive, MemoryStatusPermanent, MemoryStatusArchived}, m.Status) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

var (
	idSplitRe  = regexp.MustCompile(`[\s,]+`)
	idShapeRe  = regexp.MustCompile(`^#?\d+$`)
	collapseWS = regexp.MustCompile(`\s+`)
)

// parseIDQuery returns the ids of an ID-shaped query (`#7234`, `12, 34`).
func parseIDQuery(q string) []int64 {
	var toks []string
	for _, t := range idSplitRe.Split(strings.TrimSpace(q), -1) {
		if t != "" {
			toks = append(toks, t)
		}
	}
	if len(toks) == 0 || len(toks) > 5 {
		return nil
	}
	var ids []int64
	for _, t := range toks {
		if !idShapeRe.MatchString(t) {
			return nil
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(t, "#"), 10, 64)
		if err != nil || n <= 0 {
			return nil
		}
		ids = append(ids, n)
	}
	return ids
}

// ---------------------------------------------------------------------------
// Formatting

func previewText(s string) string {
	s = strings.TrimSpace(collapseWS.ReplaceAllString(s, " "))
	if utf8.RuneCountInString(s) <= resultPreviewLimit {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:resultPreviewLimit-1]), " \t\n") + "…"
}

func formatAge(ms int64, now time.Time) string {
	diff := now.UnixMilli() - ms
	if diff < 0 {
		return "future"
	}
	days := diff / 86400000
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "1d ago"
	case days < 30:
		return fmt.Sprintf("%dd ago", days)
	case days < 365:
		if m := days / 30; m <= 1 {
			return "1mo ago"
		} else {
			return fmt.Sprintf("%dmo ago", m)
		}
	default:
		if y := days / 365; y == 1 {
			return "1y ago"
		} else {
			return fmt.Sprintf("%dy ago", y)
		}
	}
}

func formatHit(i int, h SearchHit, now time.Time) string {
	score := strconv.FormatFloat(h.Score, 'f', 2, 64)
	switch h.Source {
	case SourceMemory:
		return fmt.Sprintf("[%d] [memory] score=%s id=%d category=%s match=%s\n%s", i, score, h.MemoryID, h.Category, h.MatchType, previewText(h.Content))
	case SourceGitCommit:
		return fmt.Sprintf("[%d] [git_commit] score=%s sha=%s %s match=%s\n%s", i, score, h.ShortSHA, formatAge(h.CommittedAt, now), h.MatchType, previewText(h.Content))
	case SourceMessage:
		return fmt.Sprintf("[%d] [message] score=%s ordinal=%d range=%d-%d role=%s\n%s", i, score, h.Ordinal, max(1, h.Ordinal-3), h.Ordinal+3, h.Role, previewText(h.Content))
	case SourcePrimer:
		return fmt.Sprintf("[%d] [primer] score=%s id=%d support=%d match=%s\n%s", i, score, h.ID, h.Support, h.MatchType, previewText(h.Content))
	case SourceNote:
		anchor := ""
		if h.Ordinal > 0 {
			anchor = fmt.Sprintf(" @msg %d", h.Ordinal)
		}
		return fmt.Sprintf("[%d] [note] score=%s id=#%d status=%s %s%s\n%s", i, score, h.ID, h.Status, formatAge(h.CreatedAt, now), anchor, previewText(h.Content))
	default:
		label := h.Label
		if label != "" {
			label = " " + label
		}
		return fmt.Sprintf("[%d] [%s] score=%s%s\n%s", i, h.Source, score, label, previewText(h.Content))
	}
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ", ")
}

func diagnosticLines(res SearchResult) []string {
	var lines []string
	d := res.Diagnostics
	if n := len(d.SuppressedMemoryIDs); n > 0 {
		hasMem := false
		for _, h := range res.Hits {
			if h.Source == SourceMemory {
				hasMem = true
			}
		}
		if hasMem {
			match, verb := "matches", "they are"
			if n == 1 {
				match, verb = "match", "it is"
			}
			lines = append(lines, fmt.Sprintf("Memories: %d additional %s suppressed because %s already visible in your project-memory block (ids %s).", n, match, verb, joinIDs(d.SuppressedMemoryIDs)))
		} else {
			match := "matches"
			if n == 1 {
				match = "match"
			}
			lines = append(lines, fmt.Sprintf("Memories: %d %s found, all already visible in your project-memory block (ids %s).", n, match, joinIDs(d.SuppressedMemoryIDs)))
		}
	}
	if n := d.NewerMessageMatches; n > 0 {
		phrase := "raw-message matches are"
		if n == 1 {
			phrase = "raw-message match is"
		}
		lines = append(lines, fmt.Sprintf("Message history: %d %s newer than the last compartment boundary (already in your context).", n, phrase))
	}
	if d.NoGitRepository {
		lines = append(lines, "Git commits: no git repository — commit search unavailable for this project.")
	}
	return lines
}

const expandFooter = "Use ctx_expand(start, end) with the range from any message result above to read the conversation context. If the host no longer holds those messages, ctx_expand returns indexed text from the search archive only, with no surrounding messages or tool output."

// FormatSearchResults renders the ctx_search reply.
func FormatSearchResults(query string, res SearchResult) string {
	diag := diagnosticLines(res)
	if len(res.Hits) == 0 {
		if len(diag) > 0 {
			return fmt.Sprintf("No hidden results found for %q.\n\n%s", query, strings.Join(diag, "\n"))
		}
		return fmt.Sprintf("No results found for %q across notes, memories, primers, git commits, or message history.", query)
	}
	now := time.Now()
	noun := "results"
	if len(res.Hits) == 1 {
		noun = "result"
	}
	blocks := make([]string, len(res.Hits))
	hasMsg := false
	for i, h := range res.Hits {
		blocks[i] = formatHit(i+1, h, now)
		if h.Source == SourceMessage {
			hasMsg = true
		}
	}
	out := fmt.Sprintf("Found %d %s for %q:\n\n", len(res.Hits), noun, query) + strings.Join(blocks, "\n\n")
	if len(diag) > 0 {
		out += "\n\n" + strings.Join(diag, "\n")
	}
	if hasMsg {
		out += "\n\n" + expandFooter
	}
	return out
}

// ---------------------------------------------------------------------------
// Auto search hint

const (
	hintMaxFragments = 3
	hintFragmentCap  = 80
	hintMaxChars     = 800
	hintFooter       = "If the fragments above seem relevant to the current request, you may run ctx_search to retrieve full context. Otherwise ignore."
)

// AutoSearchHintRequest drives AutoSearchHint.
type AutoSearchHintRequest struct {
	ProjectKey       string
	SessionID        string
	Prompt           string
	Settings         Settings
	Embedder         Embedder
	VisibleMemoryIDs []int64
}

// AutoSearchHint returns the <ctx-search-hint> block to append to a new user
// message, or "" when it is suppressed (auto search off, prompt too short or
// already hinted, or the top score below the threshold). It never counts
// retrievals. Callers persist the decision per message for cache stability.
func (s *Store) AutoSearchHint(ctx context.Context, r AutoSearchHintRequest) (string, error) {
	set := r.Settings
	if !set.AutoSearch {
		return "", nil
	}
	p := r.Prompt
	if strings.Contains(p, "<ctx-search-hint>") || strings.Contains(p, "<ctx-search-auto>") {
		return "", nil
	}
	minChars := set.AutoSearchMinChars
	if minChars <= 0 {
		minChars = 20
	}
	if utf8.RuneCountInString(strings.TrimSpace(p)) < minChars {
		return "", nil
	}
	sources := []string{SourceMessage}
	if set.MemoryEnabled {
		sources = append(sources, SourceMemory)
	}
	if set.GitIndex {
		sources = append(sources, SourceGitCommit)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := s.Search(ctx, SearchRequest{
		ProjectKey: r.ProjectKey, SessionID: r.SessionID, Query: p, Limit: hintMaxFragments,
		Sources: sources, MemoryEnabled: set.MemoryEnabled, VisibleMemoryIDs: r.VisibleMemoryIDs,
		Embedder: r.Embedder, EmbeddingModel: set.EmbeddingModel,
	})
	if err != nil {
		return "", err
	}
	threshold := set.AutoSearchThreshold
	if threshold <= 0 {
		threshold = 0.6
	}
	if len(res.Hits) == 0 || res.Hits[0].Score < threshold {
		return "", nil
	}
	return buildSearchHint(res.Hits, time.Now()), nil
}

func truncateFragment(s string, limit int) string {
	s = strings.TrimSpace(collapseWS.ReplaceAllString(s, " "))
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return strings.TrimRight(string(r[:limit-1]), " \t\n") + "…"
}

func buildSearchHint(hits []SearchHit, now time.Time) string {
	var lines []string
	for _, h := range hits {
		if len(lines) == hintMaxFragments {
			break
		}
		var frag string
		if h.Source == SourceGitCommit {
			subject, _, _ := strings.Cut(h.Content, "\n")
			frag = fmt.Sprintf("commit %s %s: %s", h.ShortSHA, formatAge(h.CommittedAt, now), truncateFragment(subject, max(10, hintFragmentCap-20)))
		} else {
			frag = truncateFragment(compressFragment(h.Content), hintFragmentCap)
		}
		if strings.TrimSpace(frag) == "" {
			continue
		}
		lines = append(lines, "- "+frag)
	}
	if len(lines) == 0 {
		return ""
	}
	header := fmt.Sprintf("Your memory may contain %d related fragments:", len(lines))
	if len(lines) == 1 {
		header = "Your memory may contain 1 related fragment:"
	}
	body := header + "\n" + strings.Join(lines, "\n") + "\n" + hintFooter
	wrap := func(b string) string { return "<ctx-search-hint>\n" + b + "\n</ctx-search-hint>" }
	out := wrap(body)
	if over := utf8.RuneCountInString(out) - hintMaxChars; over > 0 {
		r := []rune(body)
		cut := max(0, len(r)-over-1)
		out = wrap(strings.TrimRight(string(r[:cut]), " \t\n") + "…")
	}
	return out
}

// ---------------------------------------------------------------------------
// compressFragment approximates upstream's caveman "ultra" compressor (ASCII
// rules) for hint fragments: filler/hedging/pleasantry drops, phrase
// shortenings, auxiliary and article drops, symbol connectives and
// abbreviations of terms used 3+ times. Paths, identifiers and code spans are
// protected.

var (
	protectRe = regexp.MustCompile("`[^`]*`|\\S*[/\\\\._]\\S*|\\b(?:msg|ses|toolu)_[A-Za-z0-9]+|\\b[0-9a-f]{7,40}\\b")
	dropRe    = regexp.MustCompile(`(?i)\s*\b(?:just|really|basically|actually|essentially|simply|clearly|obviously|quite|very|somewhat|rather|fairly|sort of|kind of|a bit|i think|i believe|i feel|probably|perhaps|maybe|it seems|it appears|arguably|i suppose|i guess|please|thanks|thank you|kindly|if possible)\b`)
	shortenRe = []struct {
		re  *regexp.Regexp
		rep string
	}{
		{regexp.MustCompile(`(?i)\bin order to\b`), "to"},
		{regexp.MustCompile(`(?i)\bdue to the fact that\b`), "because"},
		{regexp.MustCompile(`(?i)\bat this point in time\b`), "now"},
		{regexp.MustCompile(`(?i)\bat the moment\b`), "now"},
		{regexp.MustCompile(`(?i)\bin the event that\b`), "if"},
		{regexp.MustCompile(`(?i)\bfor the purpose of\b`), "for"},
		{regexp.MustCompile(`(?i)\bwith regard to\b`), "about"},
		{regexp.MustCompile(`(?i)\bin spite of the fact that\b`), "though"},
		{regexp.MustCompile(`(?i)\bon the grounds that\b`), "because"},
		{regexp.MustCompile(`(?i)\bfor the reason that\b`), "because"},
	}
	auxRe        = regexp.MustCompile(`(?i)\s+\b(?:has been|had been|have been|will be|would be|could be|should be|might be|may be|being|been|were|was|are|is|am|be)\b\s+(\w+(?:ed|en|ing|ized|ised)\b)`)
	articleRe    = regexp.MustCompile(`(?i)\b(?:the|a|an)\b\s+`)
	connectiveRe = []struct {
		re  *regexp.Regexp
		rep string
	}{
		{regexp.MustCompile(`(?i)\b(?:and then|then after|afterwards)\b`), "→"},
		{regexp.MustCompile(`(?i)\bbecause of\b`), "//"},
		{regexp.MustCompile(`(?i)\btherefore\b`), "→"},
		{regexp.MustCompile(`(?i)\bbecause\b`), "//"},
		{regexp.MustCompile(`(?i)\bhowever\b`), "but"},
		{regexp.MustCompile(`(?i)\bfurthermore\b`), "+"},
		{regexp.MustCompile(`(?i)\badditionally\b`), "+"},
		{regexp.MustCompile(`(?i)\bas well as\b`), "+"},
		{regexp.MustCompile(`(?i) and `), " + "},
		{regexp.MustCompile(`(?i) or `), " | "},
	}
	abbreviations = [][2]string{
		{"historian", "hist"},
		{"compartment", "cmpt"},
		{"compartments", "cmpts"},
		{"compressor", "cmp"},
		{"compression", "cmp"},
		{"context", "ctx"},
		{"message", "msg"},
		{"messages", "msgs"},
		{"session", "ses"},
		{"configuration", "cfg"},
		{"config", "cfg"},
		{"implementation", "impl"},
		{"implemented", "impl"},
		{"repository", "repo"},
		{"database", "db"},
		{"directory", "dir"},
	}
	hintMultiSpaceRe = regexp.MustCompile(` {2,}`)
)

func compressFragment(text string) string {
	if text == "" {
		return text
	}
	var kept []string
	work := protectRe.ReplaceAllStringFunc(text, func(m string) string {
		kept = append(kept, m)
		return fmt.Sprintf("\x00%d\x00", len(kept)-1)
	})
	work = dropRe.ReplaceAllString(work, "")
	for _, r := range shortenRe {
		work = r.re.ReplaceAllString(work, r.rep)
	}
	work = auxRe.ReplaceAllString(work, " $1")
	work = articleRe.ReplaceAllString(work, "")
	for _, r := range connectiveRe {
		work = r.re.ReplaceAllString(work, r.rep)
	}
	for _, ab := range abbreviations {
		re := regexp.MustCompile(`(?i)\b` + ab[0] + `\b`)
		if len(re.FindAllStringIndex(work, -1)) < 3 {
			continue
		}
		work = re.ReplaceAllStringFunc(work, func(m string) string {
			if m[0] >= 'A' && m[0] <= 'Z' {
				return strings.ToUpper(ab[1][:1]) + ab[1][1:]
			}
			return ab[1]
		})
	}
	work = strings.TrimSpace(hintMultiSpaceRe.ReplaceAllString(work, " "))
	for i := len(kept) - 1; i >= 0; i-- {
		work = strings.ReplaceAll(work, fmt.Sprintf("\x00%d\x00", i), kept[i])
	}
	return work
}
