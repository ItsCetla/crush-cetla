// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/primers (primer-clustering.ts), storage-primers.ts.

// Primers: recurring "how does X work" questions clustered across sessions
// into project primers. Primers are never injected into context; they only
// surface through ctx_search source "primer". Ported from
// cortexkit/magic-context (MIT): features/magic-context/primers
// (primer-clustering.ts).
package magiccontext

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Primer clustering constants.
const (
	PrimerSimilarityThreshold = 0.85
	PrimerHysteresis          = 0.02
	PrimerPromotionThreshold  = 2 // distinct support days
	PrimerMinSpanDays         = 7
	PrimerCandidateTTL        = 90 * 24 * time.Hour
	PrimerCandidateMaxAge     = 180 * 24 * time.Hour
	defaultPrimerQuestion     = "How does this project subsystem work?"
	primerDayMS               = int64(24 * time.Hour / time.Millisecond)
)

// PrimerCandidate is one mc_primer_candidates row.
type PrimerCandidate struct {
	ID                     int64
	ProjectPath            string
	SessionID              string
	Question               string
	NormalizedQuestion     string
	SourceCompartmentStart int
	SourceCompartmentEnd   int
	SourceMessageTime      int64
	Embedding              []float32
	EmbeddingModelID       string
	CreatedAt              int64
}

// Primer is one mc_primers row.
type Primer struct {
	ID                 int64
	ProjectPath        string
	Question           string
	Embedding          []float32
	EmbeddingModelID   string
	Answer             string
	Status             string
	TotalSupport       int
	LastObservedAt     int64
	AnswerRefreshedAt  int64
	SourceCandidateIDs []int64
	CreatedAt          int64
	UpdatedAt          int64
}

// PrimerCandidateInput is the historian hand-off for one primer question.
type PrimerCandidateInput struct {
	ProjectPath            string
	SessionID              string
	Question               string
	SourceCompartmentStart int
	SourceCompartmentEnd   int
	SourceMessageTime      int64
	Embedding              []float32
	EmbeddingModelID       string
}

var (
	primerSpaceRe    = regexp.MustCompile(`\s+`)
	primerTrailingRe = regexp.MustCompile(`[?.!]+$`)
)

// NormalizePrimerQuestion is the text-match key of a question.
func NormalizePrimerQuestion(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	q = strings.NewReplacer("“", `"`, "”", `"`, "’", "'").Replace(q)
	q = primerSpaceRe.ReplaceAllString(q, " ")
	q = primerTrailingRe.ReplaceAllString(q, "")
	return strings.TrimSpace(q)
}

// PrimerCandidateFromHistorian builds the hand-off from a historian pass:
// only the first candidate is used, anchored at its origin compartment
// (1-based into newCompartments). Untagged or out-of-range candidates fall
// back to the whole chunk span. ok is false when there is nothing to store.
func PrimerCandidateFromHistorian(projectPath, sessionID string, cands []ParsedPrimerCandidate, newCompartments []Compartment, now time.Time) (PrimerCandidateInput, bool) {
	if len(cands) == 0 || len(newCompartments) == 0 || projectPath == "" {
		return PrimerCandidateInput{}, false
	}
	pc := cands[0]
	q := strings.TrimSpace(pc.Question)
	if q == "" {
		return PrimerCandidateInput{}, false
	}
	startC, endC := newCompartments[0], newCompartments[len(newCompartments)-1]
	if idx := pc.OriginCompartmentIndex; idx >= 1 && idx <= len(newCompartments) {
		startC, endC = newCompartments[idx-1], newCompartments[idx-1]
	}
	t := startC.StartTime * 1000
	if t <= 0 {
		t = now.UnixMilli()
	}
	return PrimerCandidateInput{
		ProjectPath: projectPath, SessionID: sessionID, Question: q,
		SourceCompartmentStart: startC.StartOrdinal, SourceCompartmentEnd: endC.EndOrdinal, SourceMessageTime: t,
	}, true
}

// InsertPrimerCandidates upserts candidates (one per project/session/range).
// Call it in the historian publish transaction (q may be nil).
func (s *Store) InsertPrimerCandidates(ctx context.Context, q querier, in []PrimerCandidateInput) error {
	if q == nil {
		q = s.db
	}
	now := nowMS()
	for _, c := range in {
		var emb any
		if len(c.Embedding) > 0 {
			emb = encodeEmbedding(c.Embedding)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO mc_primer_candidates (project_path, session_id, question, normalized_question, source_compartment_start, source_compartment_end, source_message_time, question_embedding, question_embedding_model_id, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(project_path, session_id, source_compartment_start, source_compartment_end) DO UPDATE SET
 question = excluded.question, normalized_question = excluded.normalized_question,
 source_message_time = excluded.source_message_time,
 question_embedding = COALESCE(excluded.question_embedding, mc_primer_candidates.question_embedding),
 question_embedding_model_id = CASE WHEN excluded.question_embedding IS NOT NULL THEN excluded.question_embedding_model_id ELSE mc_primer_candidates.question_embedding_model_id END,
 created_at = MIN(mc_primer_candidates.created_at, excluded.created_at)`,
			c.ProjectPath, c.SessionID, c.Question, NormalizePrimerQuestion(c.Question), c.SourceCompartmentStart, c.SourceCompartmentEnd,
			c.SourceMessageTime, emb, c.EmbeddingModelID, now); err != nil {
			return err
		}
	}
	return nil
}

func dreamPrimerCandidates(ctx context.Context, q querier, project string) ([]PrimerCandidate, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, project_path, session_id, question, normalized_question, source_compartment_start, source_compartment_end, source_message_time, question_embedding, question_embedding_model_id, created_at FROM mc_primer_candidates WHERE project_path = ? ORDER BY id ASC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PrimerCandidate
	for rows.Next() {
		var c PrimerCandidate
		var emb []byte
		if err := rows.Scan(&c.ID, &c.ProjectPath, &c.SessionID, &c.Question, &c.NormalizedQuestion, &c.SourceCompartmentStart, &c.SourceCompartmentEnd,
			&c.SourceMessageTime, &emb, &c.EmbeddingModelID, &c.CreatedAt); err != nil {
			return nil, err
		}
		if len(emb) > 0 {
			c.Embedding = decodeEmbedding(emb)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dreamPrimers(ctx context.Context, q querier, project string, activeOnly bool) ([]Primer, error) {
	where := `project_path = ?`
	if activeOnly {
		where += ` AND status = 'active'`
	}
	rows, err := q.QueryContext(ctx, `SELECT id, project_path, question, question_embedding, question_embedding_model_id, answer, status, total_support, last_observed_at, answer_refreshed_at, source_candidate_ids, created_at, updated_at FROM mc_primers WHERE `+where+` ORDER BY id ASC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Primer
	for rows.Next() {
		var p Primer
		var emb []byte
		var src string
		if err := rows.Scan(&p.ID, &p.ProjectPath, &p.Question, &emb, &p.EmbeddingModelID, &p.Answer, &p.Status, &p.TotalSupport, &p.LastObservedAt,
			&p.AnswerRefreshedAt, &src, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if len(emb) > 0 {
			p.Embedding = decodeEmbedding(emb)
		}
		_ = json.Unmarshal([]byte(src), &p.SourceCandidateIDs)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActivePrimers lists a project's active primers (for ctx_search source
// "primer"; primers are never injected).
func (s *Store) ActivePrimers(ctx context.Context, project string) ([]Primer, error) {
	return dreamPrimers(ctx, s.db, project, true)
}

// SearchPrimers scores active primers against a query: cosine similarity
// when a query embedding in the same model space is available, else a
// token-overlap ratio on the normalized question+answer. Results with
// score > 0 are returned best first, at most limit (<=0 = all).
func (s *Store) SearchPrimers(ctx context.Context, project, query string, queryEmbedding []float32, modelID string, limit int) ([]PrimerHit, error) {
	prims, err := s.ActivePrimers(ctx, project)
	if err != nil {
		return nil, err
	}
	qn := NormalizePrimerQuestion(query)
	qTok := strings.Fields(qn)
	var hits []PrimerHit
	for _, p := range prims {
		var score float64
		if len(queryEmbedding) > 0 && len(p.Embedding) > 0 && modelID != "" && p.EmbeddingModelID == modelID {
			score = cosineSimilarity(queryEmbedding, p.Embedding)
		} else if len(qTok) > 0 {
			hay := " " + NormalizePrimerQuestion(p.Question+" "+p.Answer) + " "
			n := 0
			for _, t := range qTok {
				if strings.Contains(hay, " "+t+" ") || strings.Contains(hay, t) {
					n++
				}
			}
			score = float64(n) / float64(len(qTok))
		}
		if score > 0 {
			hits = append(hits, PrimerHit{Primer: p, Score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// PrimerHit is one ctx_search primer result.
type PrimerHit struct {
	Primer Primer
	Score  float64
}

// FormatPrimerHit renders a primer search hit line.
func FormatPrimerHit(h PrimerHit) string {
	out := fmt.Sprintf("[primer #%d] %s (support %d)", h.Primer.ID, h.Primer.Question, h.Primer.TotalSupport)
	if a := strings.TrimSpace(h.Primer.Answer); a != "" {
		out += "\n" + a
	}
	return out
}

func primerOccurrenceKey(c PrimerCandidate) string {
	return fmt.Sprintf("%s\x1f%s\x1f%d\x1f%d", c.ProjectPath, c.SessionID, c.SourceCompartmentStart, c.SourceCompartmentEnd)
}

type primerCluster struct {
	primer     *Primer
	candidates []PrimerCandidate
	centroid   []float32
	modelID    string
}

func (cl *primerCluster) textKey() string {
	if len(cl.candidates) > 0 {
		return cl.candidates[0].NormalizedQuestion
	}
	if cl.primer != nil {
		return NormalizePrimerQuestion(cl.primer.Question)
	}
	return ""
}

func (cl *primerCluster) recomputeCentroid() {
	var sum []float32
	n := 0
	for _, c := range cl.candidates {
		if len(c.Embedding) == 0 || c.EmbeddingModelID == "" || (cl.modelID != "" && c.EmbeddingModelID != cl.modelID) {
			continue
		}
		if sum == nil {
			sum = make([]float32, len(c.Embedding))
			cl.modelID = c.EmbeddingModelID
		}
		if len(c.Embedding) != len(sum) {
			continue
		}
		for i, v := range c.Embedding {
			sum[i] += v
		}
		n++
	}
	if n > 0 {
		for i := range sum {
			sum[i] /= float32(n)
		}
		cl.centroid = sum
		return
	}
	if cl.primer != nil && len(cl.primer.Embedding) > 0 {
		cl.centroid, cl.modelID = cl.primer.Embedding, cl.primer.EmbeddingModelID
	}
}

func primerScore(cl *primerCluster, c PrimerCandidate) float64 {
	if len(c.Embedding) > 0 && len(cl.centroid) > 0 && c.EmbeddingModelID != "" && c.EmbeddingModelID == cl.modelID {
		return cosineSimilarity(c.Embedding, cl.centroid)
	}
	if c.NormalizedQuestion != "" && c.NormalizedQuestion == cl.textKey() {
		return 1
	}
	return 0
}

// clusterPrimerCandidates assigns candidates to existing primers or new
// clusters, deterministically.
func clusterPrimerCandidates(prims []Primer, cands []PrimerCandidate) []*primerCluster {
	var clusters []*primerCluster
	for i := range prims {
		p := &prims[i]
		clusters = append(clusters, &primerCluster{primer: p, centroid: p.Embedding, modelID: p.EmbeddingModelID})
	}
	sorted := append([]PrimerCandidate(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return fmt.Sprintf("%s\x1f%020d", primerOccurrenceKey(sorted[i]), sorted[i].ID) < fmt.Sprintf("%s\x1f%020d", primerOccurrenceKey(sorted[j]), sorted[j].ID)
	})
	for _, c := range sorted {
		var best *primerCluster
		bestScore := -1.0
		for _, cl := range clusters {
			th := PrimerSimilarityThreshold
			if cl.primer != nil && containsID(cl.primer.SourceCandidateIDs, c.ID) {
				th -= PrimerHysteresis
			}
			sc := primerScore(cl, c)
			if sc >= th && sc > bestScore {
				best, bestScore = cl, sc
			}
		}
		if best == nil {
			best = &primerCluster{}
			clusters = append(clusters, best)
		}
		best.candidates = append(best.candidates, c)
		best.recomputeCentroid()
	}
	return clusters
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

type primerSummary struct {
	support   int
	spanDays  int
	firstAt   int64
	lastAt    int64
	sourceIDs []int64
	earliestQ string
}

func summarizePrimerCluster(cl *primerCluster) primerSummary {
	byKey := map[string]PrimerCandidate{}
	for _, c := range cl.candidates {
		k := primerOccurrenceKey(c)
		if prev, ok := byKey[k]; !ok || primerTime(c) < primerTime(prev) {
			byKey[k] = c
		}
	}
	days := map[int64]PrimerCandidate{}
	for _, c := range byKey {
		d := primerTime(c) / primerDayMS
		if prev, ok := days[d]; !ok || primerTime(c) < primerTime(prev) {
			days[d] = c
		}
	}
	var sum primerSummary
	ids := map[int64]bool{}
	if cl.primer != nil {
		for _, id := range cl.primer.SourceCandidateIDs {
			ids[id] = true
		}
	}
	first := true
	for _, c := range cl.candidates {
		ids[c.ID] = true
	}
	for _, c := range days {
		t := primerTime(c)
		if first || t < sum.firstAt {
			sum.firstAt, sum.earliestQ = t, c.Question
		}
		if first || t > sum.lastAt {
			sum.lastAt = t
		}
		first = false
	}
	sum.support = len(days)
	if sum.support > 0 {
		sum.spanDays = int((sum.lastAt - sum.firstAt) / primerDayMS)
	}
	for id := range ids {
		sum.sourceIDs = append(sum.sourceIDs, id)
	}
	sort.Slice(sum.sourceIDs, func(i, j int) bool { return sum.sourceIDs[i] < sum.sourceIDs[j] })
	return sum
}

func primerTime(c PrimerCandidate) int64 {
	if c.SourceMessageTime > 0 {
		return c.SourceMessageTime
	}
	return c.CreatedAt
}

// PrimerPromotionResult summarizes a promote-primers pass.
type PrimerPromotionResult struct {
	Promoted   int
	Updated    int
	Candidates int
	Pruned     int
}

// PromotePrimers prunes stale candidates, optionally embeds missing ones,
// clusters them and creates/updates primers for clusters with at least
// PrimerPromotionThreshold support days over PrimerMinSpanDays. embed may
// be nil (text matching only); embedding errors fall back to text.
func PromotePrimers(ctx context.Context, s *Store, project string, embed Embedder, modelID string, now time.Time) (PrimerPromotionResult, error) {
	var res PrimerPromotionResult
	nowMs := now.UnixMilli()
	prims, err := dreamPrimers(ctx, s.db, project, true)
	if err != nil {
		return res, err
	}
	protected := map[int64]bool{}
	for _, p := range prims {
		for _, id := range p.SourceCandidateIDs {
			protected[id] = true
		}
	}
	cands, err := dreamPrimerCandidates(ctx, s.db, project)
	if err != nil {
		return res, err
	}
	var keep []PrimerCandidate
	var prune []int64
	for _, c := range cands {
		age := nowMs - primerTime(c)
		if age > PrimerCandidateMaxAge.Milliseconds() || (age > PrimerCandidateTTL.Milliseconds() && !protected[c.ID]) {
			prune = append(prune, c.ID)
			continue
		}
		keep = append(keep, c)
	}
	if embed != nil && modelID != "" {
		var idx []int
		var texts []string
		for i, c := range keep {
			if len(c.Embedding) == 0 || c.EmbeddingModelID != modelID {
				idx = append(idx, i)
				texts = append(texts, c.Question)
			}
		}
		if len(texts) > 0 {
			if vecs, err := embed(ctx, texts); err == nil && len(vecs) == len(texts) {
				for k, i := range idx {
					if len(vecs[k]) == 0 {
						continue
					}
					keep[i].Embedding, keep[i].EmbeddingModelID = vecs[k], modelID
					if _, err := s.db.ExecContext(ctx, `UPDATE mc_primer_candidates SET question_embedding = ?, question_embedding_model_id = ? WHERE id = ?`,
						encodeEmbedding(vecs[k]), modelID, keep[i].ID); err != nil {
						return res, err
					}
				}
			}
		}
	}
	res.Candidates = len(keep)
	clusters := clusterPrimerCandidates(prims, keep)
	err = s.Immediate(ctx, func(q querier) error {
		for _, id := range prune {
			if _, err := q.ExecContext(ctx, `DELETE FROM mc_primer_candidates WHERE id = ?`, id); err != nil {
				return err
			}
		}
		res.Pruned = len(prune)
		for _, cl := range clusters {
			if len(cl.candidates) == 0 {
				continue
			}
			sum := summarizePrimerCluster(cl)
			src, _ := json.Marshal(sum.sourceIDs)
			var emb any
			embModel := ""
			if len(cl.centroid) > 0 {
				emb, embModel = encodeEmbedding(cl.centroid), cl.modelID
			}
			if cl.primer != nil {
				if _, err := q.ExecContext(ctx, `UPDATE mc_primers SET total_support = ?, question_embedding = COALESCE(?, question_embedding), question_embedding_model_id = CASE WHEN ? IS NOT NULL THEN ? ELSE question_embedding_model_id END, last_observed_at = MAX(last_observed_at, ?), source_candidate_ids = ?, updated_at = ? WHERE id = ?`,
					sum.support, emb, emb, embModel, sum.lastAt, string(src), nowMs, cl.primer.ID); err != nil {
					return err
				}
				res.Updated++
				continue
			}
			if sum.support < PrimerPromotionThreshold || sum.spanDays < PrimerMinSpanDays {
				continue
			}
			question := strings.TrimSpace(sum.earliestQ)
			question = strings.TrimRight(question, "?.! ")
			if question == "" {
				question = defaultPrimerQuestion
			} else {
				question += "?"
			}
			if _, err := q.ExecContext(ctx, `INSERT INTO mc_primers (project_path, question, question_embedding, question_embedding_model_id, status, total_support, last_observed_at, source_candidate_ids, created_at, updated_at) VALUES (?,?,?,?, 'active', ?,?,?,?,?)`,
				project, question, emb, embModel, sum.support, sum.lastAt, string(src), nowMs, nowMs); err != nil {
				return err
			}
			res.Promoted++
		}
		return nil
	})
	return res, err
}
