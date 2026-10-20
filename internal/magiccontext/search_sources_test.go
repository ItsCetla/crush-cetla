// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for features/magic-context/search.ts (searchNotes, searchPrimers).

package magiccontext

import (
	"context"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func TestCtxSearchNotesAndPrimers(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	proj, sid := "git:abc", "s1"
	n1, err := s.AddSessionNote(ctx, sid, "Remember the release checklist lives in docs/release.md")
	require.NoError(t, err)
	_, err = s.AddSessionNote(ctx, "other", "release checklist for another session")
	require.NoError(t, err)
	sn, err := s.AddSmartNote(ctx, sid, proj, "Ping when the release branch is cut", "release branch exists")
	require.NoError(t, err)
	now := time.Now().UnixMilli()
	_, err = s.db.ExecContext(ctx, `INSERT INTO mc_primers (project_path, question, status, total_support, last_observed_at, source_candidate_ids, created_at, updated_at) VALUES (?,?, 'active', 3, ?, '[]', ?, ?)`,
		proj, "How is the release checklist organised?", now, now, now)
	require.NoError(t, err)

	d := SearchToolDeps{
		Store: s, Settings: Settings{MemoryEnabled: true},
		ProjectKey: func(context.Context) string { return proj },
		SessionID:  func(context.Context) string { return sid },
	}
	out, err := runCtxSearch(ctx, d, CtxSearchParams{Query: "release checklist", Sources: []string{"note", "primer"}})
	require.NoError(t, err)
	require.Contains(t, out, "[primer] score=")
	require.Contains(t, out, "support=3 match=fts\nQ: How is the release checklist organised?")
	require.Contains(t, out, "id=#1 status=active")
	require.Contains(t, out, "docs/release.md")
	require.NotContains(t, out, "another session")
	_ = n1

	out, err = runCtxSearch(ctx, d, CtxSearchParams{Query: "release branch", Sources: []string{"note"}})
	require.NoError(t, err)
	require.Contains(t, out, "[note]")
	require.Contains(t, out, "Ping when the release branch is cut")
	require.Contains(t, out, "status="+sn.Status)

	// Primers are a memory feature; notes are not.
	d.Settings.MemoryEnabled = false
	out, err = runCtxSearch(ctx, d, CtxSearchParams{Query: "release checklist", Sources: []string{"note", "primer"}})
	require.NoError(t, err)
	require.NotContains(t, out, "[primer]")
	require.Contains(t, out, "[note]")
}

func TestRankNotesForNeedle(t *testing.T) {
	notes := []Note{
		{ID: 1, Content: "alpha beta gamma delta", CreatedAt: 1},
		{ID: 2, Content: "alpha beta", CreatedAt: 2},
		{ID: 3, Content: "unrelated", CreatedAt: 3},
	}
	r := rankNotesForNeedle(notes, "alpha beta")
	require.Len(t, r, 2)
	require.Equal(t, int64(2), r[0].note.ID) // exact phrase and denser
	require.InDelta(t, 2+1+0.5, r[0].score, 1e-9)
	require.Empty(t, rankNotesForNeedle(notes, "  "))
	require.InDelta(t, singleSourcePenalty, normalizeNoteKeywordScore(10), 1e-9)
}

func TestSyncMessageIndex(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id, title, updated_at, created_at) VALUES ('s1', 't', 0, 0)`)
	require.NoError(t, err)
	e := &Engine{Store: s, Settings: Settings{Enabled: true}}
	raw := []RawMessage{
		{Ordinal: 1, ID: "u1", Role: message.User, CreatedAt: 100, Texts: []string{"fix the  sqlite\nlease <system-reminder>noise</system-reminder>"}},
		{Ordinal: 2, ID: "a1", Role: message.Assistant, CreatedAt: 101, Texts: []string{"done", "and tested"}},
		{Ordinal: 3, ID: "t1", Role: message.Tool, CreatedAt: 102},
		{Ordinal: 4, ID: "u2", Role: message.User, CreatedAt: 103, Hidden: true, Texts: nil},
	}
	require.NoError(t, e.syncMessageIndex(ctx, "s1", raw))
	got := indexRows(t, s, "s1")
	require.Equal(t, map[int]string{1: "fix the sqlite lease", 2: "done / and tested"}, got)
	var ca int64
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT created_at FROM mc_message_index WHERE session_id='s1' AND ordinal=1`).Scan(&ca))
	require.Equal(t, int64(100_000), ca)

	// Edit, plus a deleted tail message; ordinal 1 dropped from the live view stays archived.
	raw[1].Texts = []string{"done, edited"}
	require.NoError(t, s.UpsertMessageIndex(ctx, "s1", []IndexedMessage{{Ordinal: 9, MessageID: "gone", Role: "user", Content: "deleted later"}}))
	require.NoError(t, e.syncMessageIndex(ctx, "s1", raw[1:]))
	require.Equal(t, map[int]string{1: "fix the sqlite lease", 2: "done, edited"}, indexRows(t, s, "s1"))
}

func indexRows(t *testing.T, s *Store, sid string) map[int]string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT ordinal, content FROM mc_message_index WHERE session_id = ?`, sid)
	require.NoError(t, err)
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var o int
		var c string
		require.NoError(t, rows.Scan(&o, &c))
		out[o] = c
	}
	return out
}
