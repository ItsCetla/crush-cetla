// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/memory (constants, normalize-hash, storage-memory, promotion, memory-selection), storage-memory-mutation-log.ts, hooks/magic-context/inject-compartments.ts.

// Cross-session project memories, ported from cortexkit/magic-context (MIT)
// memory/{constants,normalize-hash,storage-memory,promotion,memory-selection}.ts,
// storage-memory-mutation-log.ts and the memory renderers of
// hooks/magic-context/inject-compartments.ts.

package magiccontext

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Memory statuses.
const (
	MemoryStatusActive    = "active"
	MemoryStatusPermanent = "permanent"
	MemoryStatusArchived  = "archived"
)

// Memory source types.
const (
	MemorySourceHistorian = "historian"
	MemorySourceAgent     = "agent"
	MemorySourceDreamer   = "dreamer"
	MemorySourceUser      = "user"
)

// Mutation log types.
const (
	MutationArchive    = "archive"
	MutationDelete     = "delete"
	MutationUpdate     = "update"
	MutationSuperseded = "superseded"
)

// LegacyMemoryCategories are readable pre-v2 categories.
var LegacyMemoryCategories = []string{
	"USER_DIRECTIVES", "USER_PREFERENCES", "CONFIG_DEFAULTS", "ARCHITECTURE_DECISIONS",
	"ENVIRONMENT", "WORKFLOW_RULES", "KNOWN_ISSUES",
}

// MemoryCategoryPriority is the render order: v2 categories, then legacy.
var MemoryCategoryPriority = append(slices.Clone(V2MemoryCategories), LegacyMemoryCategories...)

// categoryTTL holds the default lifetime of decaying legacy categories.
var categoryTTL = map[string]time.Duration{
	"WORKFLOW_RULES": 90 * 24 * time.Hour,
	"KNOWN_ISSUES":   30 * 24 * time.Hour,
}

// IsMemoryCategory reports whether c is a known (v2 or legacy) category.
func IsMemoryCategory(c string) bool { return slices.Contains(MemoryCategoryPriority, c) }

func isV2Category(c string) bool { return slices.Contains(V2MemoryCategories, c) }

func categoryExpiresAt(category string, now int64) int64 {
	if ttl, ok := categoryTTL[category]; ok {
		return now + ttl.Milliseconds()
	}
	return 0
}

// NormalizeMemoryContent lowercases, collapses whitespace and trims.
func NormalizeMemoryContent(s string) string {
	return strings.TrimSpace(whitespaceRe.ReplaceAllString(strings.ToLower(s), " "))
}

// MemoryHash is the dedup hash: md5 hex of the normalized content.
func MemoryHash(content string) string {
	sum := md5.Sum([]byte(NormalizeMemoryContent(content)))
	return hex.EncodeToString(sum[:])
}

// Memory is one mc_memories row.
type Memory struct {
	ID                 int64
	ProjectPath        string
	Category           string
	Content            string
	NormalizedHash     string
	Importance         int // 0 = unclassified (NULL)
	Scope              string
	SourceSessionID    string
	SourceType         string
	SeenCount          int
	RetrievalCount     int
	FirstSeenAt        int64
	CreatedAt          int64
	UpdatedAt          int64
	LastSeenAt         int64
	LastRetrievedAt    int64
	Status             string
	ExpiresAt          int64
	VerificationStatus string
	VerifiedAt         int64
	ClassifiedAt       int64
	SupersededBy       int64
	MergedFrom         string
}

// Active reports whether the memory is visible (active or permanent and not
// superseded).
func (m Memory) Active() bool {
	return (m.Status == MemoryStatusActive || m.Status == MemoryStatusPermanent) && m.SupersededBy == 0
}

const memoryCols = `id, project_path, category, content, normalized_hash, importance, scope, source_session_id, source_type, seen_count, retrieval_count, first_seen_at, created_at, updated_at, last_seen_at, last_retrieved_at, status, expires_at, verification_status, verified_at, classified_at, superseded_by_memory_id, merged_from`

func scanMemory(r interface{ Scan(...any) error }) (Memory, error) {
	var m Memory
	var imp sql.NullInt64
	err := r.Scan(&m.ID, &m.ProjectPath, &m.Category, &m.Content, &m.NormalizedHash, &imp, &m.Scope, &m.SourceSessionID,
		&m.SourceType, &m.SeenCount, &m.RetrievalCount, &m.FirstSeenAt, &m.CreatedAt, &m.UpdatedAt, &m.LastSeenAt,
		&m.LastRetrievedAt, &m.Status, &m.ExpiresAt, &m.VerificationStatus, &m.VerifiedAt, &m.ClassifiedAt,
		&m.SupersededBy, &m.MergedFrom)
	if imp.Valid {
		m.Importance = int(imp.Int64)
	}
	return m, err
}

func queryMemories(ctx context.Context, q querier, query string, args ...any) ([]Memory, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func memoryByID(ctx context.Context, q querier, id int64) (Memory, bool, error) {
	m, err := scanMemory(q.QueryRowContext(ctx, `SELECT `+memoryCols+` FROM mc_memories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, false, nil
	}
	return m, err == nil, err
}

func memoryByHash(ctx context.Context, q querier, projectKey, category, hash string) (Memory, bool, error) {
	m, err := scanMemory(q.QueryRowContext(ctx, `SELECT `+memoryCols+` FROM mc_memories WHERE project_path = ? AND category = ? AND normalized_hash = ?`, projectKey, category, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, false, nil
	}
	return m, err == nil, err
}

// GetMemory loads one memory by id in any status.
func (s *Store) GetMemory(ctx context.Context, id int64) (Memory, bool, error) {
	return memoryByID(ctx, s.db, id)
}

// ProjectMemories lists the active and permanent, unexpired, unsuperseded
// memories of a project ordered by category, updated_at DESC, id.
func (s *Store) ProjectMemories(ctx context.Context, projectKey string) ([]Memory, error) {
	return projectMemoriesAt(ctx, s.db, projectKey, nowMS())
}

// projectMemoriesAt is ProjectMemories with a frozen expiry cutoff (ms); the
// m1 render passes the m0 materialization time so replays stay byte-stable.
func projectMemoriesAt(ctx context.Context, q querier, projectKey string, cutoffMS int64) ([]Memory, error) {
	return queryMemories(ctx, q, `SELECT `+memoryCols+` FROM mc_memories
WHERE project_path = ? AND status IN ('active','permanent') AND superseded_by_memory_id = 0 AND (expires_at = 0 OR expires_at > ?)
ORDER BY category ASC, updated_at DESC, id ASC`, projectKey, cutoffMS)
}

// MemoriesAfter lists the visible memories with id > afterID (the
// <new-memories> delta against a cached m0 snapshot).
func (s *Store) MemoriesAfter(ctx context.Context, projectKey string, afterID int64) ([]Memory, error) {
	return queryMemories(ctx, s.db, `SELECT `+memoryCols+` FROM mc_memories
WHERE project_path = ? AND id > ? AND status IN ('active','permanent') AND superseded_by_memory_id = 0 AND (expires_at = 0 OR expires_at > ?)
ORDER BY id ASC`, projectKey, afterID, nowMS())
}

// MaxMemoryID returns the highest memory id of the project (0 when none).
func (s *Store) MaxMemoryID(ctx context.Context, projectKey string) (int64, error) {
	return maxMemoryID(ctx, s.db, projectKey)
}

func maxMemoryID(ctx context.Context, q querier, projectKey string) (int64, error) {
	var n sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT MAX(id) FROM mc_memories WHERE project_path = ?`, projectKey).Scan(&n)
	return n.Int64, err
}

// MaxMemoryMutationID returns the newest mutation log id (0 when none).
func (s *Store) MaxMemoryMutationID(ctx context.Context, projectKey string) (int64, error) {
	return maxMemoryMutationID(ctx, s.db, projectKey)
}

func maxMemoryMutationID(ctx context.Context, q querier, projectKey string) (int64, error) {
	var n sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT MAX(id) FROM mc_memory_mutation_log WHERE project_path = ?`, projectKey).Scan(&n)
	return n.Int64, err
}

// MemoryInput describes a new memory.
type MemoryInput struct {
	ProjectKey      string
	Category        string
	Content         string
	SourceSessionID string
	SourceType      string // default historian
	Importance      int    // 0 = NULL
	ExpiresAt       int64  // 0 = category default
}

// addMemory inserts a memory or, when (project, category, hash) already
// exists, bumps its seen count. created reports which happened.
func addMemory(ctx context.Context, q querier, in MemoryInput) (Memory, bool, error) {
	now := nowMS()
	hash := MemoryHash(in.Content)
	if m, ok, err := memoryByHash(ctx, q, in.ProjectKey, in.Category, hash); err != nil {
		return Memory{}, false, err
	} else if ok {
		if _, err := q.ExecContext(ctx, `UPDATE mc_memories SET seen_count = seen_count + 1, last_seen_at = ? WHERE id = ?`, now, m.ID); err != nil {
			return Memory{}, false, err
		}
		m.SeenCount++
		m.LastSeenAt = now
		return m, false, nil
	}
	src := in.SourceType
	if src == "" {
		src = MemorySourceHistorian
	}
	exp := in.ExpiresAt
	if exp == 0 {
		exp = categoryExpiresAt(in.Category, now)
	}
	var imp any
	if in.Importance > 0 {
		imp = in.Importance
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO mc_memories (project_path, category, content, normalized_hash, importance, source_session_id, source_type, first_seen_at, created_at, updated_at, last_seen_at, expires_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_path, category, normalized_hash) DO NOTHING`,
		in.ProjectKey, in.Category, in.Content, hash, imp, in.SourceSessionID, src, now, now, now, now, exp); err != nil {
		return Memory{}, false, err
	}
	m, ok, err := memoryByHash(ctx, q, in.ProjectKey, in.Category, hash)
	if err != nil {
		return Memory{}, false, err
	}
	if !ok {
		return Memory{}, false, fmt.Errorf("magiccontext: memory insert vanished")
	}
	return m, true, nil
}

// AddMemory inserts (or dedups) one memory in its own transaction.
func (s *Store) AddMemory(ctx context.Context, in MemoryInput) (m Memory, created bool, err error) {
	err = s.Immediate(ctx, func(q querier) error {
		m, created, err = addMemory(ctx, q, in)
		return err
	})
	return m, created, err
}

// PromoteFacts promotes historian session facts into project memories
// (memory.auto_promote). Run it inside the publish transaction: storage errors
// propagate, malformed or unpromotable facts are skipped. Duplicates bump the
// seen count. Embedding is left to the caller (see EmbedMemories).
func (s *Store) PromoteFacts(ctx context.Context, q querier, projectKey, sessionID string, facts []ParsedFact) error {
	_, err := promoteFacts(ctx, q, projectKey, sessionID, facts)
	return err
}

func promoteFacts(ctx context.Context, q querier, projectKey, sessionID string, facts []ParsedFact) ([]Memory, error) {
	if projectKey == "" {
		return nil, nil
	}
	var created []Memory
	for _, f := range facts {
		if strings.TrimSpace(f.Content) == "" || !IsMemoryCategory(f.Category) {
			continue
		}
		m, isNew, err := addMemory(ctx, q, MemoryInput{
			ProjectKey: projectKey, Category: f.Category, Content: f.Content,
			SourceSessionID: sessionID, SourceType: MemorySourceHistorian,
		})
		if err != nil {
			return nil, err
		}
		if isNew {
			created = append(created, m)
		}
	}
	return created, nil
}

// logMutation appends to mc_memory_mutation_log.
func logMutation(ctx context.Context, q querier, projectKey, typ string, target, supersededBy int64, category, content string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO mc_memory_mutation_log (project_path, mutation_type, target_memory_id, superseded_by_id, category, new_content, queued_at) VALUES (?,?,?,?,?,?,?)`,
		projectKey, typ, target, supersededBy, category, content, nowMS())
	return err
}

// rewriteMemory replaces content/category, invalidating embeddings,
// verifications and classification, and logs an 'update'.
func rewriteMemory(ctx context.Context, q querier, m Memory, content, category string) error {
	now := nowMS()
	if _, err := q.ExecContext(ctx, `UPDATE mc_memories SET content = ?, category = ?, normalized_hash = ?, updated_at = ?, classified_at = 0 WHERE id = ?`,
		content, category, MemoryHash(content), now, m.ID); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM mc_memory_embeddings WHERE memory_id = ?`, m.ID); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM mc_memory_verifications WHERE memory_id = ?`, m.ID); err != nil {
		return err
	}
	return logMutation(ctx, q, m.ProjectPath, MutationUpdate, m.ID, 0, category, content)
}

func archiveMemory(ctx context.Context, q querier, m Memory) error {
	if _, err := q.ExecContext(ctx, `UPDATE mc_memories SET status = 'archived', updated_at = ? WHERE id = ?`, nowMS(), m.ID); err != nil {
		return err
	}
	return logMutation(ctx, q, m.ProjectPath, MutationArchive, m.ID, 0, "", "")
}

func supersedeMemory(ctx context.Context, q querier, m Memory, by int64) error {
	if _, err := q.ExecContext(ctx, `UPDATE mc_memories SET superseded_by_memory_id = ?, updated_at = ? WHERE id = ?`, by, nowMS(), m.ID); err != nil {
		return err
	}
	return logMutation(ctx, q, m.ProjectPath, MutationSuperseded, m.ID, by, "", "")
}

// UpdateMemory rewrites one memory (used by the dreamer and ctx_memory).
func (s *Store) UpdateMemory(ctx context.Context, id int64, content, category string) error {
	return s.Immediate(ctx, func(q querier) error {
		m, ok, err := memoryByID(ctx, q, id)
		if err != nil {
			return err
		}
		if !ok {
			return errMemoryNotFound(id)
		}
		if category == "" {
			category = m.Category
		}
		return rewriteMemory(ctx, q, m, content, category)
	})
}

// ArchiveMemories archives memories and logs each archive.
func (s *Store) ArchiveMemories(ctx context.Context, ids []int64) error {
	return s.Immediate(ctx, func(q querier) error {
		for _, id := range ids {
			m, ok, err := memoryByID(ctx, q, id)
			if err != nil {
				return err
			}
			if !ok {
				return errMemoryNotFound(id)
			}
			if err := archiveMemory(ctx, q, m); err != nil {
				return err
			}
		}
		return nil
	})
}

// SupersedeMemory marks id as replaced by byID.
func (s *Store) SupersedeMemory(ctx context.Context, id, byID int64) error {
	return s.Immediate(ctx, func(q querier) error {
		m, ok, err := memoryByID(ctx, q, id)
		if err != nil {
			return err
		}
		if !ok {
			return errMemoryNotFound(id)
		}
		return supersedeMemory(ctx, q, m, byID)
	})
}

func errMemoryNotFound(id int64) error {
	return fmt.Errorf("memory with ID %d was not found", id)
}

// mergeMemoryStats folds the sources' counters into the canonical row.
func mergeMemoryStats(ctx context.Context, q querier, canonical int64, sources []Memory) error {
	seen, retrieved := 0, 0
	ids := map[int64]bool{}
	permanent := false
	for _, m := range sources {
		seen += m.SeenCount
		retrieved += m.RetrievalCount
		ids[m.ID] = true
		if m.MergedFrom != "" {
			var prior []int64
			if json.Unmarshal([]byte(m.MergedFrom), &prior) == nil {
				for _, p := range prior {
					ids[p] = true
				}
			}
		}
		if m.Status == MemoryStatusPermanent {
			permanent = true
		}
	}
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	slices.Sort(list)
	b, _ := json.Marshal(list)
	status := "status"
	if permanent {
		status = "'permanent'"
	}
	_, err := q.ExecContext(ctx, `UPDATE mc_memories SET seen_count = ?, retrieval_count = ?, merged_from = ?, status = `+status+`, updated_at = ? WHERE id = ?`,
		max(seen, 1), retrieved, string(b), nowMS(), canonical)
	return err
}

// bumpRetrieval records explicit-search retrievals.
func bumpRetrieval(ctx context.Context, q querier, ids []int64) error {
	now := nowMS()
	for _, id := range ids {
		if _, err := q.ExecContext(ctx, `UPDATE mc_memories SET retrieval_count = retrieval_count + 1, last_retrieved_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Embeddings

// saveMemoryEmbeddingIfHashMatches stores v only when the row still has the
// content the vector was computed from.
func saveMemoryEmbeddingIfHashMatches(ctx context.Context, q querier, id int64, modelID, hash string, v []float32) error {
	_, err := q.ExecContext(ctx, `INSERT INTO mc_memory_embeddings (memory_id, model_id, embedding)
SELECT id, ?, ? FROM mc_memories WHERE id = ? AND normalized_hash = ?
ON CONFLICT(memory_id, model_id) DO UPDATE SET embedding = excluded.embedding`, modelID, encodeEmbedding(v), id, hash)
	return err
}

// EmbedMemories embeds the given memories and stores the vectors (guarded
// by content hash). Best effort: callers run it after their transaction
// commits, typically in a goroutine. A nil embedder is a no-op.
func (s *Store) EmbedMemories(ctx context.Context, embed Embedder, modelID string, mems []Memory) error {
	if embed == nil || len(mems) == 0 {
		return nil
	}
	texts := make([]string, len(mems))
	for i, m := range mems {
		texts[i] = m.Content
	}
	vecs, err := embed(ctx, texts)
	if err != nil {
		return err
	}
	return s.Immediate(ctx, func(q querier) error {
		for i, m := range mems {
			if i >= len(vecs) || len(vecs[i]) == 0 {
				continue
			}
			if err := saveMemoryEmbeddingIfHashMatches(ctx, q, m.ID, modelID, m.NormalizedHash, vecs[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// PromoteFactsAndEmbed is PromoteFacts plus best-effort background embedding
// of newly created memories once the caller's transaction has committed:
// call the returned func after commit (it is never nil).
func (s *Store) PromoteFactsAndEmbed(ctx context.Context, q querier, projectKey, sessionID string, facts []ParsedFact, embed Embedder, modelID string) (afterCommit func(), err error) {
	created, err := promoteFacts(ctx, q, projectKey, sessionID, facts)
	if err != nil || embed == nil || len(created) == 0 {
		return func() {}, err
	}
	return func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			_ = s.EmbedMemories(ctx, embed, modelID, created)
		}()
	}, nil
}

// ---------------------------------------------------------------------------
// Selection and rendering

// SelectMemories picks memories greedily under budgetTokens: permanent
// first, then importance DESC (unclassified lowest), then the most recent of
// last_seen_at/verified_at, then id. budgetTokens <= 0 keeps everything.
func SelectMemories(mems []Memory, budgetTokens int) []Memory {
	sorted := slices.Clone(mems)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		ap, bp := a.Status == MemoryStatusPermanent, b.Status == MemoryStatusPermanent
		if ap != bp {
			return ap
		}
		if a.Importance != b.Importance {
			return a.Importance > b.Importance
		}
		ar, br := max(a.LastSeenAt, a.VerifiedAt), max(b.LastSeenAt, b.VerifiedAt)
		if ar != br {
			return ar > br
		}
		return a.ID < b.ID
	})
	if budgetTokens <= 0 {
		return sorted
	}
	var out []Memory
	used := 0
	for _, m := range sorted {
		cost := EstimateTokens(memoryLine(m)) + 1
		if used+cost > budgetTokens {
			continue
		}
		used += cost
		out = append(out, m)
	}
	return out
}

func memoryLine(m Memory) string {
	return "#" + strconv.FormatInt(m.ID, 10) + ": " + escapeXMLContent(m.Content)
}

func categoryRank(c string) int {
	if i := slices.Index(MemoryCategoryPriority, c); i >= 0 {
		return i
	}
	return len(MemoryCategoryPriority)
}

func renderMemoryBody(mems []Memory) string {
	sorted := slices.Clone(mems)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if ra, rb := categoryRank(a.Category), categoryRank(b.Category); ra != rb {
			return ra < rb
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.ID < b.ID
	})
	var sb strings.Builder
	cur := ""
	for i, m := range sorted {
		if i == 0 || m.Category != cur {
			if i > 0 {
				sb.WriteString("</" + cur + ">\n")
			}
			cur = m.Category
			sb.WriteString("<" + cur + ">\n")
		}
		sb.WriteString(memoryLine(m) + "\n")
	}
	if len(sorted) > 0 {
		sb.WriteString("</" + cur + ">\n")
	}
	return sb.String()
}

// RenderProjectMemory selects memories under budgetTokens and renders the
// <project-memory> block ("" when nothing is selected).
func RenderProjectMemory(mems []Memory, budgetTokens int) string {
	sel := SelectMemories(mems, budgetTokens)
	if len(sel) == 0 {
		return ""
	}
	return "<project-memory>\n" + renderMemoryBody(sel) + "</project-memory>"
}

// RenderNewMemories renders the m1 <new-memories> delta ("" when empty).
func RenderNewMemories(mems []Memory) string {
	if len(mems) == 0 {
		return ""
	}
	return "<new-memories>\n" + renderMemoryBody(mems) + "</new-memories>"
}

// MemoryIDs returns the ids of mems (e.g. the visible set for ctx_search).
func MemoryIDs(mems []Memory) []int64 {
	out := make([]int64, len(mems))
	for i, m := range mems {
		out[i] = m.ID
	}
	return out
}

// ---------------------------------------------------------------------------
// Mutation deltas (m1 <memory-updates>)

// MemoryMutation is one coalesced mutation of a rendered memory.
type MemoryMutation struct {
	ID           int64
	Type         string
	TargetID     int64
	SupersededBy int64 // terminal replacement for superseded
	Category     string
	NewContent   string
}

// MemoryUpdates is the delta since a cached snapshot.
type MemoryUpdates struct {
	Mutations     []MemoryMutation
	MaxMutationID int64
}

func isTerminalMutation(t string) bool {
	return t == MutationArchive || t == MutationDelete || t == MutationSuperseded
}

// MemoryUpdatesSince returns the coalesced mutations logged after
// sinceMutationID (one per target; archive/delete/superseded win over later
// updates, superseded chains resolve to their terminal replacement). When
// renderedIDs is given only those targets are reported, like upstream's
// snapshot-scoped lookup. MaxMutationID is the newest log id overall.
func (s *Store) MemoryUpdatesSince(ctx context.Context, projectKey string, sinceMutationID int64, renderedIDs ...int64) (MemoryUpdates, error) {
	return memoryUpdatesSince(ctx, s.db, projectKey, sinceMutationID, renderedIDs...)
}

func memoryUpdatesSince(ctx context.Context, q querier, projectKey string, sinceMutationID int64, renderedIDs ...int64) (MemoryUpdates, error) {
	var out MemoryUpdates
	maxID, err := maxMemoryMutationID(ctx, q, projectKey)
	if err != nil {
		return out, err
	}
	out.MaxMutationID = maxID
	rows, err := q.QueryContext(ctx, `SELECT id, mutation_type, target_memory_id, superseded_by_id, category, new_content
FROM mc_memory_mutation_log WHERE project_path = ? AND id > ? ORDER BY id ASC`, projectKey, sinceMutationID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	byTarget := map[int64]MemoryMutation{}
	for rows.Next() {
		var m MemoryMutation
		if err := rows.Scan(&m.ID, &m.Type, &m.TargetID, &m.SupersededBy, &m.Category, &m.NewContent); err != nil {
			return out, err
		}
		cur, ok := byTarget[m.TargetID]
		if ok && isTerminalMutation(cur.Type) && !isTerminalMutation(m.Type) {
			continue
		}
		byTarget[m.TargetID] = m
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	var want map[int64]bool
	if len(renderedIDs) > 0 {
		want = map[int64]bool{}
		for _, id := range renderedIDs {
			want[id] = true
		}
	}
	for target, m := range byTarget {
		if want != nil && !want[target] {
			continue
		}
		if m.Type == MutationSuperseded {
			seen := map[int64]bool{target: true}
			term := m.SupersededBy
			for depth := 0; depth < 16; depth++ {
				next, ok := byTarget[term]
				if !ok || next.Type != MutationSuperseded || seen[next.SupersededBy] {
					break
				}
				seen[term] = true
				term = next.SupersededBy
			}
			m.SupersededBy = term
		}
		out.Mutations = append(out.Mutations, m)
	}
	sort.Slice(out.Mutations, func(i, j int) bool { return out.Mutations[i].ID < out.Mutations[j].ID })
	return out, nil
}

// RenderMemoryUpdates renders the <memory-updates> block ("" when empty).
func RenderMemoryUpdates(u MemoryUpdates) string {
	if len(u.Mutations) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<memory-updates>\nThese memories changed since the snapshot below — trust these:\n")
	for _, m := range u.Mutations {
		id := strconv.FormatInt(m.TargetID, 10)
		switch m.Type {
		case MutationUpdate:
			cat := ""
			if m.Category != "" {
				cat = ` category="` + escapeXMLAttr(m.Category) + `"`
			}
			sb.WriteString(`  <updated id="` + id + `"` + cat + `>` + escapeXMLContent(m.NewContent) + "</updated>\n")
		case MutationSuperseded:
			sb.WriteString(`  <superseded id="` + id + `" by="` + strconv.FormatInt(m.SupersededBy, 10) + `"/>` + "\n")
		default:
			sb.WriteString(`  <removed id="` + id + `"/>` + "\n")
		}
	}
	sb.WriteString("</memory-updates>")
	return sb.String()
}

// ---------------------------------------------------------------------------
// Tabular listing (ctx_memory get)

func isoMS(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// FormatMemoryList renders memories as the ctx_memory get/list table.
func FormatMemoryList(mems []Memory) string {
	if len(mems) == 0 {
		return "No active memories found."
	}
	allActive := true
	for _, m := range mems {
		if m.Status != MemoryStatusActive {
			allActive = false
		}
	}
	noun := "memories"
	if len(mems) == 1 {
		noun = "memory"
	}
	adj := ""
	if allActive {
		adj = "active "
	}
	header := []string{"ID", "CATEGORY", "STATUS", "VERIFY", "UPDATED", "CONTENT"}
	rows := make([][]string, 0, len(mems))
	for _, m := range mems {
		rows = append(rows, []string{
			strconv.FormatInt(m.ID, 10), m.Category, m.Status, m.VerificationStatus, isoMS(m.UpdatedAt),
			strings.TrimSpace(whitespaceRe.ReplaceAllString(m.Content, " ")),
		})
	}
	widths := make([]int, len(header)-1)
	for i := range widths {
		widths[i] = len(header[i])
		for _, r := range rows {
			widths[i] = max(widths[i], len(r[i]))
		}
	}
	pad := func(cells []string) string {
		out := make([]string, len(cells))
		for i, c := range cells {
			if i < len(widths) {
				c += strings.Repeat(" ", widths[i]-len(c))
			}
			out[i] = c
		}
		return strings.Join(out, " | ")
	}
	dashes := make([]string, len(header))
	for i := range dashes {
		if i < len(widths) {
			dashes[i] = strings.Repeat("-", widths[i])
		} else {
			dashes[i] = "-------"
		}
	}
	lines := []string{fmt.Sprintf("Found %d %s%s:", len(mems), adj, noun), "", pad(header), strings.Join(dashes, "-+-")}
	for _, r := range rows {
		lines = append(lines, pad(r))
	}
	return strings.Join(lines, "\n")
}
