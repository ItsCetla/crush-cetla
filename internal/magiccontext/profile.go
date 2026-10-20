// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/user-memory.

// Global user profile: historian observation candidates, promoted user
// memories and the <user-profile> render. Ported from cortexkit/magic-context
// (MIT): features/magic-context/user-memory.
package magiccontext

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultUserProfileBudgetTokens caps the rendered profile.
const DefaultUserProfileBudgetTokens = 4000

// userProfileVersionKey is the mc_dream_state key bumped on every profile
// change so m0 caches can detect a stale <user-profile>.
const userProfileVersionKey = "user_profile_version"

// UserMemoryCandidate is one historian user observation awaiting review.
type UserMemoryCandidate struct {
	ID                     int64
	Content                string
	SessionID              string
	SourceCompartmentStart int
	SourceCompartmentEnd   int
	CreatedAt              int64
}

// UserMemory is one promoted profile entry.
type UserMemory struct {
	ID                 int64
	Content            string
	Status             string
	PromotedAt         int64
	SourceCandidateIDs string
	CreatedAt          int64
	UpdatedAt          int64
}

// CompartmentSpan returns the start ordinal of the first and the end ordinal
// of the last compartment (the historian hand-off source range).
func CompartmentSpan(newCompartments []Compartment) (start, end int) {
	if len(newCompartments) == 0 {
		return 0, 0
	}
	return newCompartments[0].StartOrdinal, newCompartments[len(newCompartments)-1].EndOrdinal
}

// InsertUserMemoryCandidates stores historian user observations. Call it in
// the historian publish transaction with CompartmentSpan(newCompartments).
func (s *Store) InsertUserMemoryCandidates(ctx context.Context, q querier, sessionID string, start, end int, observations []string) (int, error) {
	if q == nil {
		q = s.db
	}
	now := nowMS()
	n := 0
	for _, o := range observations {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO mc_user_memory_candidates (content, session_id, source_compartment_start, source_compartment_end, created_at) VALUES (?,?,?,?,?)`,
			o, sessionID, start, end, now); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// UserMemoryCandidates lists the pending candidates oldest first.
func (s *Store) UserMemoryCandidates(ctx context.Context) ([]UserMemoryCandidate, error) {
	return dreamUserCandidates(ctx, s.db)
}

func dreamUserCandidates(ctx context.Context, q querier) ([]UserMemoryCandidate, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, content, session_id, source_compartment_start, source_compartment_end, created_at FROM mc_user_memory_candidates ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserMemoryCandidate
	for rows.Next() {
		var c UserMemoryCandidate
		if err := rows.Scan(&c.ID, &c.Content, &c.SessionID, &c.SourceCompartmentStart, &c.SourceCompartmentEnd, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ActiveUserMemories lists the profile ordered by promoted_at, id.
func (s *Store) ActiveUserMemories(ctx context.Context) ([]UserMemory, error) {
	return dreamActiveUserMemories(ctx, s.db)
}

func dreamActiveUserMemories(ctx context.Context, q querier) ([]UserMemory, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, content, status, promoted_at, source_candidate_ids, created_at, updated_at FROM mc_user_memories WHERE status = 'active' ORDER BY promoted_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserMemory
	for rows.Next() {
		var m UserMemory
		if err := rows.Scan(&m.ID, &m.Content, &m.Status, &m.PromotedAt, &m.SourceCandidateIDs, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UserProfileItems returns the active profile contents in render order.
func (s *Store) UserProfileItems(ctx context.Context) ([]string, error) {
	return userProfileItems(ctx, s.db)
}

func userProfileItems(ctx context.Context, q querier) ([]string, error) {
	mems, err := dreamActiveUserMemories(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(mems))
	for i, m := range mems {
		out[i] = m.Content
	}
	return out, nil
}

// UserProfileVersion is the monotonically bumped profile version (0 when
// never changed). Compare with SessionMeta.CachedM0ProfileVersion.
func (s *Store) UserProfileVersion(ctx context.Context) (int64, error) {
	return dreamStateInt(ctx, s.db, userProfileVersionKey)
}

func dreamStateInt(ctx context.Context, q querier, key string) (int64, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM mc_dream_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n, nil
}

func dreamBumpProfileVersion(ctx context.Context, q querier) error {
	_, err := q.ExecContext(ctx, `INSERT INTO mc_dream_state (key, value) VALUES (?, '1')
ON CONFLICT(key) DO UPDATE SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)`, userProfileVersionKey)
	return err
}

// renderUserProfileContent drops a leading "User " and capitalizes the rest.
func renderUserProfileContent(c string) string {
	c = strings.TrimSpace(c)
	if rest, ok := strings.CutPrefix(c, "User "); ok {
		c = strings.TrimSpace(rest)
	}
	if r, size := utf8.DecodeRuneInString(c); size > 0 {
		c = string(unicode.ToUpper(r)) + c[size:]
	}
	return c
}

// TrimUserProfileToBudget keeps the items that fit (items that don't are
// skipped, later ones may still fit).
func TrimUserProfileToBudget(items []string, budgetTokens int) []string {
	if budgetTokens <= 0 {
		budgetTokens = DefaultUserProfileBudgetTokens
	}
	var out []string
	used := 0
	for _, it := range items {
		cost := EstimateTokens("- "+it) + 4
		if used+cost > budgetTokens {
			continue
		}
		used += cost
		out = append(out, it)
	}
	return out
}

func renderProfileBlock(tag string, items []string, budgetTokens int) string {
	kept := TrimUserProfileToBudget(items, budgetTokens)
	var b strings.Builder
	n := 0
	for _, it := range kept {
		c := renderUserProfileContent(it)
		if c == "" {
			continue
		}
		if n == 0 {
			b.WriteString("<" + tag + ">\n")
		}
		b.WriteString("- " + escapeXMLContent(c) + "\n")
		n++
	}
	if n == 0 {
		return ""
	}
	b.WriteString("</" + tag + ">")
	return b.String()
}

// RenderUserProfile renders "<user-profile>\n- item\n…\n</user-profile>" for
// the m0 system block ("" when empty). budgetTokens <= 0 uses the default.
func RenderUserProfile(items []string, budgetTokens int) string {
	return renderProfileBlock("user-profile", items, budgetTokens)
}

// RenderNewUserProfile renders the m1 <new-user-profile> delta when the
// profile changed since the cached m0 (currentVersion != cachedVersion),
// with a quarter of the profile budget. It returns "" otherwise.
func RenderNewUserProfile(items []string, budgetTokens int, currentVersion, cachedVersion int64) string {
	if currentVersion == cachedVersion {
		return ""
	}
	if budgetTokens <= 0 {
		budgetTokens = DefaultUserProfileBudgetTokens
	}
	return renderProfileBlock("new-user-profile", items, max(1, budgetTokens/4))
}
