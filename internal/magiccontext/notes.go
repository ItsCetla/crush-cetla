// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/storage-notes.ts, tools/ctx-note/render.ts, note-nudge hook.

// Session notes, smart-note storage, note rendering and the deferred note
// nudge. Ported from cortexkit/magic-context (MIT): features/magic-context
// notes storage, tools/ctx-note/render.ts and the note-nudge hook.
package magiccontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Note types.
const (
	NoteTypeSession = "session"
	NoteTypeSmart   = "smart"
)

// Note statuses.
const (
	NoteStatusActive    = "active"
	NoteStatusPending   = "pending"
	NoteStatusReady     = "ready"
	NoteStatusDismissed = "dismissed"
)

// Smart-note check statuses.
const (
	NoteCheckUncompiled = "uncompiled"
	NoteCheckFallback   = "fallback"
	NoteCheckParked     = "parked"
)

// Note rendering/nudge constants.
const (
	NoteGlanceTitleMax   = 80
	NoteNudgeTitleMax    = 60
	NoteStaleAfter       = 30 * 24 * time.Hour
	NoteNudgeCooldown    = 15 * time.Minute
	DefaultNoteReadLimit = 25
	MaxNoteIDs           = 50
)

// EmptyNoteReadReply is the read body when nothing matches.
const EmptyNoteReadReply = "## Notes\n\nNo session notes or smart notes."

// AllNoteStatuses is the "all" read filter.
var AllNoteStatuses = []string{NoteStatusActive, NoteStatusPending, NoteStatusReady, NoteStatusDismissed}

// Note is one mc_notes row.
type Note struct {
	ID                int64
	Type              string
	Status            string
	Content           string
	SessionID         string
	ProjectPath       string
	SurfaceCondition  string
	CreatedAt         int64
	UpdatedAt         int64
	LastCheckedAt     int64
	ReadyAt           int64
	ReadyReason       string
	CheckStatus       string
	CheckNextDueAt    int64
	CheckFailureCount int
	AnchorOrdinal     int64
}

// TouchedAt is updated_at, or created_at for never-updated rows.
func (n Note) TouchedAt() int64 {
	if n.UpdatedAt > 0 {
		return n.UpdatedAt
	}
	return n.CreatedAt
}

const noteCols = `id, type, status, content, session_id, project_path, surface_condition, created_at, updated_at, last_checked_at, ready_at, ready_reason, check_status, check_next_due_at, check_failure_count, anchor_ordinal`

func scanNote(r interface{ Scan(...any) error }) (Note, error) {
	var n Note
	err := r.Scan(&n.ID, &n.Type, &n.Status, &n.Content, &n.SessionID, &n.ProjectPath, &n.SurfaceCondition, &n.CreatedAt, &n.UpdatedAt,
		&n.LastCheckedAt, &n.ReadyAt, &n.ReadyReason, &n.CheckStatus, &n.CheckNextDueAt, &n.CheckFailureCount, &n.AnchorOrdinal)
	return n, err
}

func queryNotes(ctx context.Context, q querier, query string, args ...any) ([]Note, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func noteByID(ctx context.Context, q querier, id int64) (Note, bool, error) {
	n, err := scanNote(q.QueryRowContext(ctx, `SELECT `+noteCols+` FROM mc_notes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, false, nil
	}
	return n, err == nil, err
}

// GetNote loads one note by id regardless of scope.
func (s *Store) GetNote(ctx context.Context, id int64) (Note, bool, error) {
	return noteByID(ctx, s.db, id)
}

// noteAnchorOrdinal is the session's conversation tail (0 when unindexed).
func noteAnchorOrdinal(ctx context.Context, q querier, sessionID string) int64 {
	var n sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT MAX(ordinal) FROM mc_message_index WHERE session_id = ?`, sessionID).Scan(&n); err != nil {
		return 0
	}
	return n.Int64
}

// AddSessionNote saves an active session note anchored at the current tail.
func (s *Store) AddSessionNote(ctx context.Context, sessionID, content string) (Note, error) {
	return s.addNote(ctx, NoteTypeSession, NoteStatusActive, sessionID, "", content, "")
}

// AddSmartNote saves a pending smart note owned by the project (and
// remembering the session that wrote it).
func (s *Store) AddSmartNote(ctx context.Context, sessionID, projectPath, content, condition string) (Note, error) {
	return s.addNote(ctx, NoteTypeSmart, NoteStatusPending, sessionID, projectPath, content, condition)
}

func (s *Store) addNote(ctx context.Context, typ, status, sessionID, projectPath, content, condition string) (Note, error) {
	var out Note
	err := s.Immediate(ctx, func(q querier) error {
		now := nowMS()
		res, err := q.ExecContext(ctx, `INSERT INTO mc_notes (type, status, content, session_id, project_path, surface_condition, created_at, updated_at, anchor_ordinal) VALUES (?,?,?,?,?,?,?,?,?)`,
			typ, status, content, sessionID, projectPath, condition, now, now, noteAnchorOrdinal(ctx, q, sessionID))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, _, err = noteByID(ctx, q, id)
		return err
	})
	return out, err
}

// NoteQuery selects notes. Session notes match SessionID (type session);
// smart notes match ProjectPath (type smart). Empty Statuses = all.
type NoteQuery struct {
	SessionID   string
	ProjectPath string
	Type        string // "" = both
	Statuses    []string
}

// GetNotes lists notes ordered by created_at, id.
func (s *Store) GetNotes(ctx context.Context, nq NoteQuery) ([]Note, error) {
	return getNotes(ctx, s.db, nq)
}

func getNotes(ctx context.Context, q querier, nq NoteQuery) ([]Note, error) {
	var scopes []string
	var args []any
	if nq.Type != NoteTypeSmart && nq.SessionID != "" {
		scopes = append(scopes, `(type = 'session' AND session_id = ?)`)
		args = append(args, nq.SessionID)
	}
	if nq.Type != NoteTypeSession && nq.ProjectPath != "" {
		scopes = append(scopes, `(type = 'smart' AND project_path = ?)`)
		args = append(args, nq.ProjectPath)
	}
	if len(scopes) == 0 {
		return nil, nil
	}
	where := "(" + strings.Join(scopes, " OR ") + ")"
	if len(nq.Statuses) > 0 {
		where += ` AND status IN (` + strings.TrimSuffix(strings.Repeat("?,", len(nq.Statuses)), ",") + `)`
		for _, st := range nq.Statuses {
			args = append(args, st)
		}
	}
	return queryNotes(ctx, q, `SELECT `+noteCols+` FROM mc_notes WHERE `+where+` ORDER BY created_at ASC, id ASC`, args...)
}

// DefaultNoteGlance is the unfiltered read: active session notes plus every
// current (ready or pending) smart note of the project.
func (s *Store) DefaultNoteGlance(ctx context.Context, sessionID, projectPath string) ([]Note, error) {
	sess, err := s.GetNotes(ctx, NoteQuery{SessionID: sessionID, Type: NoteTypeSession, Statuses: []string{NoteStatusActive}})
	if err != nil {
		return nil, err
	}
	smart, err := s.GetNotes(ctx, NoteQuery{ProjectPath: projectPath, Type: NoteTypeSmart, Statuses: []string{NoteStatusReady, NoteStatusPending}})
	if err != nil {
		return nil, err
	}
	return append(sess, smart...), nil
}

func noteInScope(n Note, sessionID, projectPath string) bool {
	if n.Type == NoteTypeSmart {
		return projectPath != "" && n.ProjectPath == projectPath
	}
	return sessionID != "" && n.SessionID == sessionID
}

// healNote repairs a session note left in a smart-only state.
func healNote(ctx context.Context, q querier, n Note) (Note, error) {
	if n.Type == NoteTypeSession && n.Status == NoteStatusPending {
		if _, err := q.ExecContext(ctx, `UPDATE mc_notes SET status = 'active', surface_condition = '' WHERE id = ?`, n.ID); err != nil {
			return n, err
		}
		n.Status, n.SurfaceCondition = NoteStatusActive, ""
	}
	return n, nil
}

// NoteInScope loads a note visible to the session/project (healed).
func (s *Store) NoteInScope(ctx context.Context, id int64, sessionID, projectPath string) (Note, bool, error) {
	n, ok, err := noteByID(ctx, s.db, id)
	if err != nil || !ok || !noteInScope(n, sessionID, projectPath) {
		return Note{}, false, err
	}
	n, err = healNote(ctx, s.db, n)
	return n, err == nil, err
}

// UpdateNote changes content and/or the smart condition of a note in scope.
// A changed condition re-arms the smart check. It returns false when the
// note is out of scope or the fields don't fit its type.
func (s *Store) UpdateNote(ctx context.Context, id int64, sessionID, projectPath string, content, condition *string) (Note, bool, error) {
	var out Note
	ok := false
	err := s.Immediate(ctx, func(q querier) error {
		n, found, err := noteByID(ctx, q, id)
		if err != nil || !found || !noteInScope(n, sessionID, projectPath) || n.Status == NoteStatusDismissed {
			return err
		}
		if condition != nil && n.Type != NoteTypeSmart {
			return nil
		}
		sets := []string{"updated_at = ?"}
		args := []any{nowMS()}
		if content != nil {
			sets = append(sets, "content = ?")
			args = append(args, *content)
		}
		if condition != nil && *condition != n.SurfaceCondition {
			sets = append(sets, "surface_condition = ?", "status = 'pending'", "last_checked_at = 0", "ready_at = 0", "ready_reason = ''",
				"check_status = 'uncompiled'", "check_failure_count = 0", "check_next_due_at = 0")
			args = append(args, *condition)
		}
		args = append(args, id)
		if _, err := q.ExecContext(ctx, `UPDATE mc_notes SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return err
		}
		ok = true
		out, _, err = noteByID(ctx, q, id)
		return err
	})
	return out, ok, err
}

// Note dismiss outcomes.
const (
	NoteDismissed        = "dismissed"
	NoteNotFound         = "not_found"
	NoteNotOwned         = "not_owned"
	NoteAlreadyDismissed = "already_dismissed"
)

// NoteDismissOutcome is the per-id dismiss result.
type NoteDismissOutcome struct {
	ID      int64
	Outcome string
}

// DismissNotes retires notes in one transaction.
func (s *Store) DismissNotes(ctx context.Context, ids []int64, sessionID, projectPath string) ([]NoteDismissOutcome, error) {
	var out []NoteDismissOutcome
	err := s.Immediate(ctx, func(q querier) error {
		out = out[:0]
		now := nowMS()
		for _, id := range ids {
			n, found, err := noteByID(ctx, q, id)
			if err != nil {
				return err
			}
			switch {
			case !found:
				out = append(out, NoteDismissOutcome{id, NoteNotFound})
			case !noteInScope(n, sessionID, projectPath):
				out = append(out, NoteDismissOutcome{id, NoteNotOwned})
			case n.Status == NoteStatusDismissed:
				out = append(out, NoteDismissOutcome{id, NoteAlreadyDismissed})
			default:
				if _, err := q.ExecContext(ctx, `UPDATE mc_notes SET status = 'dismissed', updated_at = ? WHERE id = ?`, now, id); err != nil {
					return err
				}
				out = append(out, NoteDismissOutcome{id, NoteDismissed})
			}
		}
		return nil
	})
	return out, err
}

// MarkNoteReady surfaces a smart note.
func (s *Store) MarkNoteReady(ctx context.Context, id int64, reason string) error {
	now := nowMS()
	_, err := s.db.ExecContext(ctx, `UPDATE mc_notes SET status = 'ready', ready_at = ?, ready_reason = ?, updated_at = ?, last_checked_at = ? WHERE id = ? AND type = 'smart'`,
		now, reason, now, now, id)
	return err
}

// MarkNoteChecked records an unmet check and the next due time.
func (s *Store) MarkNoteChecked(ctx context.Context, id, nextDueAt int64) error {
	now := nowMS()
	_, err := s.db.ExecContext(ctx, `UPDATE mc_notes SET last_checked_at = ?, updated_at = ?, check_next_due_at = ?, check_failure_count = 0 WHERE id = ? AND type = 'smart'`,
		now, now, nextDueAt, id)
	return err
}

// SessionNoteTray is " K active, oldest AGE." for the write reply ("" when
// no active session notes).
func (s *Store) SessionNoteTray(ctx context.Context, sessionID string, now time.Time) (string, error) {
	notes, err := s.GetNotes(ctx, NoteQuery{SessionID: sessionID, Type: NoteTypeSession, Statuses: []string{NoteStatusActive}})
	if err != nil || len(notes) == 0 {
		return "", err
	}
	oldest := notes[0].TouchedAt()
	for _, n := range notes[1:] {
		oldest = min(oldest, n.TouchedAt())
	}
	return fmt.Sprintf(" %d active, oldest %s.", len(notes), FormatNoteAge(oldest, now)), nil
}

// ---------------------------------------------------------------------------
// Rendering (render.ts)

// FormatNoteAge renders a coarse age: 5m, 3h, 2d, 4w.
func FormatNoteAge(ms int64, now time.Time) string {
	d := max(time.Duration(now.UnixMilli()-ms)*time.Millisecond, 0)
	switch {
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	default:
		return strconv.Itoa(int(d/(7*24*time.Hour))) + "w"
	}
}

// ClipNoteTitle returns the trimmed first line, clipped to max runes + "…".
func ClipNoteTitle(content string, maxRunes int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return line
}

func sortNotesForGlance(notes []Note) []Note {
	rank := func(n Note) int {
		switch n.Status {
		case NoteStatusReady:
			return 0
		case NoteStatusPending:
			return 1
		}
		return 2
	}
	out := append([]Note(nil), notes...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		if a.UpdatedAt != b.UpdatedAt {
			return a.UpdatedAt > b.UpdatedAt
		}
		return a.ID > b.ID
	})
	return out
}

func noteGlanceRow(n Note, now time.Time) string {
	parts := []string{"#" + strconv.FormatInt(n.ID, 10), FormatNoteAge(n.TouchedAt(), now), ClipNoteTitle(n.Content, NoteGlanceTitleMax)}
	if n.Status != NoteStatusActive {
		parts = append(parts, n.Status)
	}
	if n.Type == NoteTypeSmart && n.Status == NoteStatusPending && n.CheckStatus == NoteCheckParked {
		r := n.ReadyReason
		if r == "" {
			r = "Condition can't be checked"
		}
		parts = append(parts, "parked: "+r)
	}
	if now.UnixMilli()-n.TouchedAt() >= NoteStaleAfter.Milliseconds() {
		parts = append(parts, "stale")
	}
	return strings.Join(parts, " · ")
}

// RenderNoteGlance renders the one-row-per-note read page.
func RenderNoteGlance(notes []Note, now time.Time, limit, offset int) string {
	if limit <= 0 {
		limit = DefaultNoteReadLimit
	}
	offset = max(offset, 0)
	sorted := sortNotesForGlance(notes)
	total := len(sorted)
	if offset >= total {
		return EmptyNoteReadReply
	}
	page := sorted[offset:min(total, offset+limit)]
	rows := make([]string, len(page))
	for i, n := range page {
		rows[i] = noteGlanceRow(n, now)
	}
	out := "## Notes\n\n" + strings.Join(rows, "\n")
	if remaining := total - offset - len(page); remaining > 0 {
		out += fmt.Sprintf("\n\nShowing %d of %d — %d older: ctx_note(action=\"read\", offset=%d)", len(page), total, remaining, offset+len(page))
	}
	return out
}

// RenderNoteBody renders the full body of one note.
func RenderNoteBody(n Note, now time.Time) string {
	out := fmt.Sprintf("- **#%d** · %s · %s: %s", n.ID, FormatNoteAge(n.TouchedAt(), now), n.Status, n.Content)
	if n.AnchorOrdinal > 0 {
		out += fmt.Sprintf(" ↳ @msg %d", n.AnchorOrdinal)
	}
	if n.Type == NoteTypeSmart {
		if n.Status == NoteStatusReady {
			r := n.ReadyReason
			if r == "" {
				r = n.SurfaceCondition
			}
			if r == "" {
				r = "Condition satisfied"
			}
			out += "\n  Condition met: " + r
		} else {
			c := n.SurfaceCondition
			if c == "" {
				c = "No condition recorded"
			}
			out += "\n  Condition: " + c
			if n.Status == NoteStatusPending && strings.HasPrefix(n.ReadyReason, "Condition can't be checked:") {
				out += "\n  " + n.ReadyReason
			}
		}
	}
	return out
}

// RenderNotesByID renders the by-id read (missing ids as not_found).
func RenderNotesByID(ids []int64, found map[int64]Note, now time.Time) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		if n, ok := found[id]; ok {
			parts[i] = RenderNoteBody(n, now)
		} else {
			parts[i] = fmt.Sprintf("- Note #%d: not_found", id)
		}
	}
	return "## Notes by ID\n\n" + strings.Join(parts, "\n\n")
}

// ---------------------------------------------------------------------------
// Deferred note nudge

// NoteNudgeAnchor is a delivered nudge pinned to a user message.
type NoteNudgeAnchor struct {
	MessageID string `json:"m"`
	Text      string `json:"t"`
}

const noteTriggerSep = "\x1f"

func splitNoteTrigger(v string) (trigger, msgID string) {
	trigger, msgID, _ = strings.Cut(v, noteTriggerSep)
	return trigger, msgID
}

func decodeStringList(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	if strings.HasPrefix(v, "[") && json.Unmarshal([]byte(v), &out) == nil {
		return out
	}
	return []string{v}
}

func encodeStringList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// NoteNudgeAnchors decodes the delivered anchors from session meta.
func NoteNudgeAnchors(m SessionMeta) []NoteNudgeAnchor {
	ids := decodeStringList(m.NoteNudgeAnchorID)
	texts := decodeStringList(m.NoteNudgeText)
	out := make([]NoteNudgeAnchor, 0, len(ids))
	for i, id := range ids {
		a := NoteNudgeAnchor{MessageID: id}
		if i < len(texts) {
			a.Text = texts[i]
		}
		out = append(out, a)
	}
	return out
}

// RenderNoteNudge wraps nudge text for appending to a user message.
func RenderNoteNudge(text string) string {
	if text == "" {
		return ""
	}
	return "\n\n<instruction name=\"deferred_notes\">" + text + "</instruction>"
}

// FormatNoteNudge renders "R notes ready, A active (oldest AGE): #id title".
func FormatNoteNudge(ready, active int, oldestActive int64, shown *Note, now time.Time) string {
	out := fmt.Sprintf("%d notes ready, %d active", ready, active)
	if active > 0 {
		out += " (oldest " + FormatNoteAge(oldestActive, now) + ")"
	}
	if shown != nil {
		out += fmt.Sprintf(": #%d %s", shown.ID, ClipNoteTitle(shown.Content, NoteNudgeTitleMax))
	}
	return out
}

// TriggerNoteNudge arms the deferred nudge (e.g. after a historian run,
// a note write or a smart note surfacing).
func (s *Store) TriggerNoteNudge(ctx context.Context, sessionID, trigger string) error {
	if trigger == "" {
		trigger = "notes"
	}
	return s.SetMeta(ctx, sessionID, map[string]any{"note_nudge_trigger": trigger})
}

// NoteNudgePeek is the input to PeekNoteNudge.
type NoteNudgePeek struct {
	SessionID            string
	ProjectPath          string
	CurrentUserMessageID string
	// NoteReadStillVisible is true while the last ctx_note read result is
	// still in the visible context.
	NoteReadStillVisible bool
	Now                  time.Time
}

var (
	uncheckableNoteRe   = regexp.MustCompile(`^Smart note #\d+ cannot be checked\.\nCondition: `)
	uncheckableNudgeRe  = regexp.MustCompile(`^Smart note check unavailable: [\s\S]*Read ctx_note #(\d+) for the condition`)
	uncheckablePrefixFn = func(id int64) string { return fmt.Sprintf("Smart note #%d cannot be checked.", id) }
)

// PeekNoteNudge returns the nudge text to deliver on the current user
// message ("" when nothing is due). It may clear or defer the trigger.
func (s *Store) PeekNoteNudge(ctx context.Context, in NoteNudgePeek) (string, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	m, err := s.Meta(ctx, in.SessionID)
	if err != nil {
		return "", err
	}
	trigger, msgID := splitNoteTrigger(m.NoteNudgeTrigger)
	if trigger == "" {
		return "", nil
	}
	if msgID == "" {
		if in.CurrentUserMessageID != "" {
			err = s.SetMeta(ctx, in.SessionID, map[string]any{"note_nudge_trigger": trigger + noteTriggerSep + in.CurrentUserMessageID})
		}
		return "", err
	}
	if in.CurrentUserMessageID == msgID {
		return "", nil
	}
	clear := func() (string, error) {
		return "", s.SetMeta(ctx, in.SessionID, map[string]any{"note_nudge_trigger": ""})
	}
	if m.NoteNudgeLastAt > 0 && now.UnixMilli()-m.NoteNudgeLastAt < NoteNudgeCooldown.Milliseconds() {
		return clear()
	}
	active, err := s.GetNotes(ctx, NoteQuery{SessionID: in.SessionID, Type: NoteTypeSession, Statuses: []string{NoteStatusActive}})
	if err != nil {
		return "", err
	}
	ready, err := s.GetNotes(ctx, NoteQuery{ProjectPath: in.ProjectPath, Type: NoteTypeSmart, Statuses: []string{NoteStatusReady}})
	if err != nil {
		return "", err
	}
	if len(active) == 0 && len(ready) == 0 {
		return clear()
	}
	var maxTouch int64
	oldest := int64(0)
	for i, n := range active {
		maxTouch = max(maxTouch, n.UpdatedAt, n.ReadyAt)
		if i == 0 || n.TouchedAt() < oldest {
			oldest = n.TouchedAt()
		}
	}
	for _, n := range ready {
		maxTouch = max(maxTouch, n.UpdatedAt, n.ReadyAt)
	}
	if m.LastNoteReadAt > 0 && in.NoteReadStillVisible && maxTouch > 0 && m.LastNoteReadAt > maxTouch {
		return clear()
	}
	for _, n := range active {
		if uncheckableNoteRe.MatchString(n.Content) {
			reason := "check unavailable"
			if _, after, ok := strings.Cut(n.Content, "\nReason: "); ok {
				r, _, _ := strings.Cut(after, "\n")
				if r = strings.TrimSpace(r); r != "" {
					reason = r
				}
			}
			return fmt.Sprintf("Smart note check unavailable: %s. Read ctx_note #%d for the condition and repair instructions; this is NOT evidence that the condition is met.", reason, n.ID), nil
		}
	}
	pool := ready
	if len(pool) == 0 {
		pool = active
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
	counter := len(NoteNudgeAnchors(m))
	h := fnv.New32a()
	_, _ = h.Write([]byte(in.SessionID + "#" + strconv.Itoa(counter)))
	shown := pool[int(h.Sum32()%uint32(len(pool)))]
	return FormatNoteNudge(len(ready), len(active), oldest, &shown, now), nil
}

// MarkNoteNudgeDelivered records a delivered nudge on msgID (or, with no
// msgID, drops the trigger and the cooldown).
func (s *Store) MarkNoteNudgeDelivered(ctx context.Context, sessionID, text, msgID string, now time.Time) error {
	return s.Immediate(ctx, func(q querier) error {
		if msgID == "" {
			return s.setMeta(ctx, q, sessionID, map[string]any{"note_nudge_trigger": "", "note_nudge_last_at": int64(0)})
		}
		m, err := s.meta(ctx, q, sessionID)
		if err != nil {
			return err
		}
		anchors := NoteNudgeAnchors(m)
		ids := make([]string, 0, len(anchors)+1)
		texts := make([]string, 0, len(anchors)+1)
		for _, a := range anchors {
			ids = append(ids, a.MessageID)
			texts = append(texts, a.Text)
		}
		ids = append(ids, msgID)
		texts = append(texts, text)
		if err := s.setMeta(ctx, q, sessionID, map[string]any{
			"note_nudge_anchor_id": encodeStringList(ids), "note_nudge_text": encodeStringList(texts),
			"note_nudge_last_at": now.UnixMilli(), "note_nudge_trigger": "",
		}); err != nil {
			return err
		}
		if sm := uncheckableNudgeRe.FindStringSubmatch(text); sm != nil {
			id, _ := strconv.ParseInt(sm[1], 10, 64)
			_, err = q.ExecContext(ctx, `UPDATE mc_notes SET status = 'dismissed', updated_at = ? WHERE id = ? AND session_id = ? AND type = 'session' AND content LIKE 'Smart note #% cannot be checked.%'`,
				now.UnixMilli(), id, sessionID)
		}
		return err
	})
}
