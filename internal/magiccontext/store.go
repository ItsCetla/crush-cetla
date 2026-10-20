// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/storage-meta.ts, storage-tags.ts, storage-ops.ts, compartment-storage.ts.

package magiccontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Store is the raw-SQL persistence layer for every mc_ table. It works on
// the shared app database; the tables are created by the goose migration
// and stay empty unless Magic Context is enabled.
type Store struct {
	db *sql.DB
	// writeMu serializes immediate transactions inside this process. SQLite
	// also locks, but serializing here avoids busy waits between the
	// historian, the transform and tool calls.
	writeMu sync.Mutex
}

// NewStore wraps the app database.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// DB exposes the handle for subsystems that keep their own queries.
func (s *Store) DB() *sql.DB { return s.db }

// querier is satisfied by *sql.DB, *sql.Conn and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func nowMS() int64 { return time.Now().UnixMilli() }

// Immediate runs fn inside BEGIN IMMEDIATE on a dedicated connection so
// readers never observe a half-applied queue, publish or flush.
func (s *Store) Immediate(ctx context.Context, fn func(q querier) error) (err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
			panic(p)
		}
		if err != nil {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
			return
		}
		_, err = conn.ExecContext(ctx, "COMMIT")
		if err != nil {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	return fn(conn)
}

// ---------------------------------------------------------------------------
// Ordinals

// EnsureOrdinals returns the stable 1-based ordinal of every id, assigning the
// next free ordinals (in the given order) to ids seen for the first time.
func (s *Store) EnsureOrdinals(ctx context.Context, sessionID string, ids []string) (map[string]int, error) {
	out, err := s.loadOrdinals(ctx, s.db, sessionID)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	err = s.Immediate(ctx, func(q querier) error {
		cur, err := s.loadOrdinals(ctx, q, sessionID)
		if err != nil {
			return err
		}
		next := 0
		for _, o := range cur {
			next = max(next, o)
		}
		for _, id := range missing {
			if _, ok := cur[id]; ok {
				continue
			}
			next++
			if _, err := q.ExecContext(ctx, `INSERT INTO mc_ordinals (session_id, message_id, ordinal) VALUES (?, ?, ?)`, sessionID, id, next); err != nil {
				return err
			}
			cur[id] = next
		}
		out = cur
		return nil
	})
	return out, err
}

func (s *Store) loadOrdinals(ctx context.Context, q querier, sessionID string) (map[string]int, error) {
	rows, err := q.QueryContext(ctx, `SELECT message_id, ordinal FROM mc_ordinals WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var o int
		if err := rows.Scan(&id, &o); err != nil {
			return nil, err
		}
		out[id] = o
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Tags

// Tag types.
const (
	TagMessage = "message"
	TagTool    = "tool"
	TagFile    = "file"
)

// Tag statuses.
const (
	TagActive    = "active"
	TagDropped   = "dropped"
	TagCompacted = "compacted"
)

// Tag is one taggable unit (§N§).
type Tag struct {
	ID            int64
	Number        int
	MessageID     string
	PartKey       string
	Type          string
	ToolName      string
	ToolCallID    string
	Status        string
	ByteSize      int
	InputByteSize int
	DropMode      string
	CreatedAt     int64
}

const tagCols = `id, tag_number, message_id, part_key, type, tool_name, tool_call_id, status, byte_size, input_byte_size, drop_mode, created_at`

func scanTag(r interface{ Scan(...any) error }) (Tag, error) {
	var t Tag
	err := r.Scan(&t.ID, &t.Number, &t.MessageID, &t.PartKey, &t.Type, &t.ToolName, &t.ToolCallID, &t.Status, &t.ByteSize, &t.InputByteSize, &t.DropMode, &t.CreatedAt)
	return t, err
}

// LoadTags returns every tag of the session ordered by tag number.
func (s *Store) LoadTags(ctx context.Context, sessionID string) ([]Tag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tagCols+` FROM mc_tags WHERE session_id = ? ORDER BY tag_number`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TagByNumber returns one tag.
func (s *Store) TagByNumber(ctx context.Context, sessionID string, n int) (Tag, bool, error) {
	t, err := scanTag(s.db.QueryRowContext(ctx, `SELECT `+tagCols+` FROM mc_tags WHERE session_id = ? AND tag_number = ?`, sessionID, n))
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, false, nil
	}
	return t, err == nil, err
}

// InsertTags assigns the next tag numbers to the new tags (in order) and
// returns them with ID and Number filled in.
func (s *Store) InsertTags(ctx context.Context, sessionID string, tags []Tag) ([]Tag, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	err := s.Immediate(ctx, func(q querier) error {
		var next int
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(tag_number), 0) FROM mc_tags WHERE session_id = ?`, sessionID).Scan(&next); err != nil {
			return err
		}
		now := nowMS()
		for i := range tags {
			t := &tags[i]
			var existing Tag
			existing, err := scanTag(q.QueryRowContext(ctx, `SELECT `+tagCols+` FROM mc_tags WHERE session_id = ? AND part_key = ?`, sessionID, t.PartKey))
			if err == nil {
				*t = existing
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			next++
			if t.Status == "" {
				t.Status = TagActive
			}
			res, err := q.ExecContext(ctx, `INSERT INTO mc_tags (session_id, tag_number, message_id, part_key, type, tool_name, tool_call_id, status, byte_size, input_byte_size, drop_mode, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
				sessionID, next, t.MessageID, t.PartKey, t.Type, t.ToolName, t.ToolCallID, t.Status, t.ByteSize, t.InputByteSize, t.DropMode, now)
			if err != nil {
				return err
			}
			t.ID, _ = res.LastInsertId()
			t.Number = next
			t.CreatedAt = now
		}
		return nil
	})
	return tags, err
}

// UpdateTagSizes refreshes the measured sizes of a tag.
func (s *Store) UpdateTagSizes(ctx context.Context, id int64, size, inputSize int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mc_tags SET byte_size = ?, input_byte_size = ? WHERE id = ?`, size, inputSize, id)
	return err
}

// MarkTagsDropped sets status=dropped and the persisted drop mode, and removes
// any pending op for those tags.
func (s *Store) MarkTagsDropped(ctx context.Context, q querier, sessionID string, drops map[int64]string) error {
	for id, mode := range drops {
		if _, err := q.ExecContext(ctx, `UPDATE mc_tags SET status = 'dropped', drop_mode = CASE WHEN drop_mode = '' THEN ? ELSE drop_mode END WHERE id = ? AND session_id = ?`, mode, id, sessionID); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM mc_pending_ops WHERE session_id = ? AND tag_id = ?`, sessionID, id); err != nil {
			return err
		}
	}
	return nil
}

// MarkTagsCompacted marks the tags of compartmentalized messages.
func (s *Store) MarkTagsCompacted(ctx context.Context, q querier, sessionID string, messageIDs []string) error {
	for _, id := range messageIDs {
		if _, err := q.ExecContext(ctx, `UPDATE mc_tags SET status = 'compacted' WHERE session_id = ? AND message_id = ? AND status = 'active'`, sessionID, id); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM mc_pending_ops WHERE session_id = ? AND tag_id IN (SELECT id FROM mc_tags WHERE session_id = ? AND message_id = ?)`, sessionID, sessionID, id); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Pending ops

// PendingOp is a queued ctx_reduce drop.
type PendingOp struct {
	TagID     int64
	Operation string
	QueuedAt  int64
}

// QueuePendingOp queues one op (idempotent).
func (s *Store) QueuePendingOp(ctx context.Context, q querier, sessionID string, tagID int64, op string, at int64) error {
	_, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO mc_pending_ops (session_id, tag_id, operation, queued_at) VALUES (?,?,?,?)`, sessionID, tagID, op, at)
	return err
}

// PendingOps lists the queued ops of the session, oldest first.
func (s *Store) PendingOps(ctx context.Context, sessionID string) ([]PendingOp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag_id, operation, queued_at FROM mc_pending_ops WHERE session_id = ? ORDER BY queued_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingOp
	for rows.Next() {
		var p PendingOp
		if err := rows.Scan(&p.TagID, &p.Operation, &p.QueuedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Compartments

// Compartment is one historian-written block of compressed history.
type Compartment struct {
	ID             int64
	Sequence       int
	StartOrdinal   int
	EndOrdinal     int
	StartMessageID string
	EndMessageID   string
	Title          string
	Content        string
	P1, P2, P3, P4 string
	Importance     *int
	EpisodeType    string
	StartTime      int64
	EndTime        int64
	CreatedAt      int64
}

const compartmentCols = `id, sequence, start_ordinal, end_ordinal, start_message_id, end_message_id, title, content, p1, p2, p3, p4, importance, episode_type, start_time, end_time, created_at`

func compartmentTable(recomp bool) string {
	if recomp {
		return "mc_recomp_compartments"
	}
	return "mc_compartments"
}

// Compartments lists the session's compartments in sequence order.
func (s *Store) Compartments(ctx context.Context, sessionID string) ([]Compartment, error) {
	return s.compartments(ctx, s.db, sessionID, false)
}

// RecompCompartments lists staged recomp compartments.
func (s *Store) RecompCompartments(ctx context.Context, sessionID string) ([]Compartment, error) {
	return s.compartments(ctx, s.db, sessionID, true)
}

func (s *Store) compartments(ctx context.Context, q querier, sessionID string, recomp bool) ([]Compartment, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+compartmentCols+` FROM `+compartmentTable(recomp)+` WHERE session_id = ? ORDER BY sequence`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Compartment
	for rows.Next() {
		var c Compartment
		var imp sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Sequence, &c.StartOrdinal, &c.EndOrdinal, &c.StartMessageID, &c.EndMessageID, &c.Title, &c.Content, &c.P1, &c.P2, &c.P3, &c.P4, &imp, &c.EpisodeType, &c.StartTime, &c.EndTime, &c.CreatedAt); err != nil {
			return nil, err
		}
		if imp.Valid {
			v := int(imp.Int64)
			c.Importance = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// InsertCompartments appends compartments and returns them with ids.
func (s *Store) InsertCompartments(ctx context.Context, q querier, sessionID string, recomp bool, cs []Compartment) ([]Compartment, error) {
	now := nowMS()
	for i := range cs {
		c := &cs[i]
		if c.CreatedAt == 0 {
			c.CreatedAt = now
		}
		var imp any
		if c.Importance != nil {
			imp = *c.Importance
		}
		res, err := q.ExecContext(ctx, `INSERT INTO `+compartmentTable(recomp)+` (session_id, sequence, start_ordinal, end_ordinal, start_message_id, end_message_id, title, content, p1, p2, p3, p4, importance, episode_type, start_time, end_time, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			sessionID, c.Sequence, c.StartOrdinal, c.EndOrdinal, c.StartMessageID, c.EndMessageID, c.Title, c.Content, c.P1, c.P2, c.P3, c.P4, imp, c.EpisodeType, c.StartTime, c.EndTime, c.CreatedAt)
		if err != nil {
			return nil, err
		}
		c.ID, _ = res.LastInsertId()
	}
	return cs, nil
}

// DeleteCompartments clears one table for the session.
func (s *Store) DeleteCompartments(ctx context.Context, q querier, sessionID string, recomp bool) error {
	_, err := q.ExecContext(ctx, `DELETE FROM `+compartmentTable(recomp)+` WHERE session_id = ?`, sessionID)
	return err
}

// ---------------------------------------------------------------------------
// Session facts and events

// SessionFact is a categorized fact extracted by the historian.
type SessionFact struct {
	ID        int64
	Category  string
	Content   string
	CreatedAt int64
}

// InsertSessionFacts stores facts (deduplicated by category+content).
func (s *Store) InsertSessionFacts(ctx context.Context, q querier, sessionID string, facts []ParsedFact) error {
	now := nowMS()
	for _, f := range facts {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM mc_session_facts WHERE session_id = ? AND category = ? AND content = ?`, sessionID, f.Category, f.Content).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO mc_session_facts (session_id, category, content, created_at, updated_at) VALUES (?,?,?,?,?)`, sessionID, f.Category, f.Content, now, now); err != nil {
			return err
		}
	}
	return nil
}

// SessionFacts lists the session's facts.
func (s *Store) SessionFacts(ctx context.Context, sessionID string) ([]SessionFact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, category, content, created_at FROM mc_session_facts WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionFact
	for rows.Next() {
		var f SessionFact
		if err := rows.Scan(&f.ID, &f.Category, &f.Content, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// InsertEvent stores one historian event.
func (s *Store) InsertEvent(ctx context.Context, q querier, sessionID string, compartmentID int64, kind string, fields map[string]string) error {
	b, _ := json.Marshal(fields)
	var cid any
	if compartmentID > 0 {
		cid = compartmentID
	}
	_, err := q.ExecContext(ctx, `INSERT INTO mc_events (session_id, compartment_id, kind, fields_json, created_at) VALUES (?,?,?,?,?)`, sessionID, cid, kind, string(b), nowMS())
	return err
}

// ---------------------------------------------------------------------------
// Session meta

// SessionMeta is the per-session Magic Context state row.
type SessionMeta struct {
	LastResponseTime       int64
	LastInputTokens        int64
	LastContextPercentage  float64
	LastNudgeTokens        int64
	LastNudgeBand          string
	LastNudgeTurn          int64
	CompartmentInProgress  bool
	HistorianFailureCount  int
	HistorianLastError     string
	LeaseHolder            string
	LeaseUntil             int64
	M0Floor                int
	CachedM0               string
	CachedM1               string
	CachedM0CompartmentSeq int
	CachedM0MemoryMutation int64
	CachedM0MaxMemoryID    int64
	CachedM0ProfileVersion int64
	CachedM0Budget         int
	RenderEpoch            int64
	CachedRenderEpoch      int64
	MaterializePending     string
	SystemPromptHash       string
	LastModelKey           string
	EmergencyLatched       bool
	PriorBoundaryOrdinal   int
	StrippedReduceIDs      []string
	NoteNudgeTrigger       string
	NoteNudgeAnchorID      string
	NoteNudgeText          string
	NoteNudgeLastAt        int64
	LastNoteReadAt         int64
	SearchHintAnchorID     string
	SearchHintText         string
	UserTurns              int64
	CachedM0MemoryIDs      []int64
	CachedM0MaterializedAt int64
	CachedM0Boundary       string
	CachedM0MemoryEnabled  bool
	CachedM0BudgetKey      string
	// RecompPartialRange is "start-end" while a partial recomp is staged.
	RecompPartialRange string
	// RecompPassCount counts the historian passes saved to staging.
	RecompPassCount int
}

const metaCols = `last_response_time, last_input_tokens, last_context_percentage, last_nudge_tokens, last_nudge_band, last_nudge_turn, compartment_in_progress, historian_failure_count, historian_last_error, lease_holder, lease_until, m0_floor, cached_m0, cached_m1, cached_m0_compartment_seq, cached_m0_memory_mutation, cached_m0_max_memory_id, cached_m0_profile_version, cached_m0_budget, render_epoch, cached_render_epoch, materialize_pending, system_prompt_hash, last_model_key, emergency_latched, prior_boundary_ordinal, stripped_reduce_ids, note_nudge_trigger, note_nudge_anchor_id, note_nudge_text, note_nudge_last_at, last_note_read_at, search_hint_anchor_id, search_hint_text, user_turns, cached_m0_memory_ids, cached_m0_materialized_at, cached_m0_boundary, cached_m0_memory_enabled, cached_m0_budget_key, recomp_partial_range, recomp_pass_count`

// Meta loads the session meta row (zero value when absent).
func (s *Store) Meta(ctx context.Context, sessionID string) (SessionMeta, error) {
	return s.meta(ctx, s.db, sessionID)
}

func (s *Store) meta(ctx context.Context, q querier, sessionID string) (SessionMeta, error) {
	var m SessionMeta
	var inProg, latched, memEnabled int
	var stripped, memIDs string
	err := q.QueryRowContext(ctx, `SELECT `+metaCols+` FROM mc_session_meta WHERE session_id = ?`, sessionID).Scan(
		&m.LastResponseTime, &m.LastInputTokens, &m.LastContextPercentage, &m.LastNudgeTokens, &m.LastNudgeBand, &m.LastNudgeTurn,
		&inProg, &m.HistorianFailureCount, &m.HistorianLastError, &m.LeaseHolder, &m.LeaseUntil, &m.M0Floor, &m.CachedM0, &m.CachedM1,
		&m.CachedM0CompartmentSeq, &m.CachedM0MemoryMutation, &m.CachedM0MaxMemoryID, &m.CachedM0ProfileVersion, &m.CachedM0Budget,
		&m.RenderEpoch, &m.CachedRenderEpoch, &m.MaterializePending, &m.SystemPromptHash, &m.LastModelKey, &latched, &m.PriorBoundaryOrdinal,
		&stripped, &m.NoteNudgeTrigger, &m.NoteNudgeAnchorID, &m.NoteNudgeText, &m.NoteNudgeLastAt, &m.LastNoteReadAt,
		&m.SearchHintAnchorID, &m.SearchHintText, &m.UserTurns,
		&memIDs, &m.CachedM0MaterializedAt, &m.CachedM0Boundary, &memEnabled, &m.CachedM0BudgetKey, &m.RecompPartialRange, &m.RecompPassCount)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionMeta{}, nil
	}
	if err != nil {
		return m, err
	}
	m.CompartmentInProgress = inProg != 0
	m.EmergencyLatched = latched != 0
	m.CachedM0MemoryEnabled = memEnabled != 0
	_ = json.Unmarshal([]byte(stripped), &m.StrippedReduceIDs)
	_ = json.Unmarshal([]byte(memIDs), &m.CachedM0MemoryIDs)
	return m, nil
}

var metaColumnSet = func() map[string]bool {
	out := map[string]bool{}
	for c := range strings.SplitSeq(metaCols, ",") {
		out[strings.TrimSpace(c)] = true
	}
	return out
}()

// SetMeta upserts the named columns. Values are stored as given; bools are
// converted to 0/1 and string slices to JSON.
func (s *Store) SetMeta(ctx context.Context, sessionID string, fields map[string]any) error {
	return s.setMeta(ctx, s.db, sessionID, fields)
}

func (s *Store) setMeta(ctx context.Context, q querier, sessionID string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	if _, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO mc_session_meta (session_id) VALUES (?)`, sessionID); err != nil {
		return err
	}
	var sets []string
	var args []any
	for k, v := range fields {
		if !metaColumnSet[k] {
			return fmt.Errorf("magiccontext: unknown meta column %q", k)
		}
		switch x := v.(type) {
		case bool:
			if x {
				v = 1
			} else {
				v = 0
			}
		case []string:
			if x == nil {
				x = []string{}
			}
			b, _ := json.Marshal(x)
			v = string(b)
		case []int64:
			if x == nil {
				x = []int64{}
			}
			b, _ := json.Marshal(x)
			v = string(b)
		}
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	args = append(args, sessionID)
	_, err := q.ExecContext(ctx, `UPDATE mc_session_meta SET `+strings.Join(sets, ", ")+` WHERE session_id = ?`, args...)
	return err
}

// ---------------------------------------------------------------------------
// Compartment lease

// AcquireLease takes the per-session historian lease when it is free or
// expired. It returns false when another holder owns a live lease.
func (s *Store) AcquireLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	ok := false
	err := s.Immediate(ctx, func(q querier) error {
		m, err := s.meta(ctx, q, sessionID)
		if err != nil {
			return err
		}
		now := nowMS()
		if m.LeaseHolder != "" && m.LeaseHolder != holder && m.LeaseUntil > now {
			return nil
		}
		ok = true
		return s.setMeta(ctx, q, sessionID, map[string]any{
			"lease_holder": holder, "lease_until": now + ttl.Milliseconds(), "compartment_in_progress": true,
		})
	})
	return ok, err
}

// RenewLease extends a held lease.
func (s *Store) RenewLease(ctx context.Context, sessionID, holder string, ttl time.Duration) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mc_session_meta SET lease_until = ? WHERE session_id = ? AND lease_holder = ?`, nowMS()+ttl.Milliseconds(), sessionID, holder)
	return err
}

// LeaseHeld reports whether holder still owns the lease.
func (s *Store) LeaseHeld(ctx context.Context, q querier, sessionID, holder string) (bool, error) {
	m, err := s.meta(ctx, q, sessionID)
	if err != nil {
		return false, err
	}
	return m.LeaseHolder == holder && m.LeaseUntil > nowMS(), nil
}

// ReleaseLease frees the lease when holder still owns it.
func (s *Store) ReleaseLease(ctx context.Context, sessionID, holder string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mc_session_meta SET lease_holder = '', lease_until = 0, compartment_in_progress = 0 WHERE session_id = ? AND lease_holder = ?`, sessionID, holder)
	return err
}

// ---------------------------------------------------------------------------
// Message index (for ctx_search over raw history)

// IndexedMessage is one searchable message row.
type IndexedMessage struct {
	Ordinal   int
	MessageID string
	Role      string
	Content   string
	CreatedAt int64
}

// UpsertMessageIndex records message text for search.
func (s *Store) UpsertMessageIndex(ctx context.Context, sessionID string, rows []IndexedMessage) error {
	if len(rows) == 0 {
		return nil
	}
	return s.Immediate(ctx, func(q querier) error {
		for _, r := range rows {
			if _, err := q.ExecContext(ctx, `INSERT INTO mc_message_index (session_id, ordinal, message_id, role, content, created_at) VALUES (?,?,?,?,?,?)
ON CONFLICT(session_id, ordinal) DO UPDATE SET message_id = excluded.message_id, role = excluded.role, content = excluded.content, created_at = excluded.created_at`,
				sessionID, r.Ordinal, r.MessageID, r.Role, r.Content, r.CreatedAt); err != nil {
				return err
			}
		}
		return nil
	})
}

// IndexedOrdinals returns which ordinals already have index rows with the
// given content length (cheap change detection).
func (s *Store) IndexedLengths(ctx context.Context, sessionID string) (map[int]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ordinal, length(content) FROM mc_message_index WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var o, n int
		if err := rows.Scan(&o, &n); err != nil {
			return nil, err
		}
		out[o] = n
	}
	return out, rows.Err()
}
