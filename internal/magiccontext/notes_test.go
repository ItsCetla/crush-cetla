// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for storage-notes.ts, tools/ctx-note, the note nudge and dreamer/cron.ts.

// Tests for notes, the ctx_note tool, the note nudge and the dream cron
// (port of cortexkit/magic-context, MIT).

package magiccontext

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func p3Store(t *testing.T, sessions ...string) *Store {
	t.Helper()
	s := p2TestStore(t)
	for _, sid := range sessions {
		_, err := s.db.ExecContext(context.Background(), `INSERT INTO sessions (id, title, updated_at, created_at) VALUES (?, 't', 0, 0)`, sid)
		require.NoError(t, err)
	}
	return s
}

func p3NoteDeps(s *Store, set Settings) NoteToolDeps {
	return NoteToolDeps{
		Store:       s,
		Settings:    set,
		SessionID:   func(context.Context) string { return "ses1" },
		ProjectPath: func(context.Context) string { return "git:proj" },
	}
}

func TestCtxNoteWriteReadDismiss(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t, "ses1")
	deps := p3NoteDeps(s, Settings{})

	out := RunCtxNote(ctx, deps, CtxNoteParams{Content: "Revisit flaky upload test\nretry path looks racy"})
	require.True(t, strings.HasPrefix(out, "Saved session note #1."), out)
	require.Contains(t, out, "1 active")

	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "write", Content: "   "})
	require.Equal(t, "Error: 'content' is required when action is 'write'.", out)

	out = RunCtxNote(ctx, deps, CtxNoteParams{Content: "Second note"})
	require.Contains(t, out, "#2")

	out = RunCtxNote(ctx, deps, CtxNoteParams{})
	require.Contains(t, out, "Revisit flaky upload test")
	require.Contains(t, out, "Second note")
	require.True(t, strings.HasSuffix(out, noteDismissFooter))
	m, err := s.Meta(ctx, "ses1")
	require.NoError(t, err)
	require.Positive(t, m.LastNoteReadAt)

	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "read", NoteIDs: []float64{1, 99}})
	require.Contains(t, out, "## Notes by ID")
	require.Contains(t, out, "retry path looks racy")
	require.Contains(t, out, "- Note #99: not_found")

	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "update", NoteIDs: []float64{1, 2}, Content: "x"})
	require.Contains(t, out, "exactly one positive integer id")
	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "update", NoteIDs: []float64{2}, Content: "Second note, edited"})
	require.Equal(t, "Updated note #2:\nContent: Second note, edited", out)

	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "dismiss", NoteIDs: []float64{1}})
	require.Equal(t, "Note #1 dismissed.", out)
	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "dismiss", NoteIDs: []float64{1}})
	require.Contains(t, out, "not found in your session/project or already dismissed")
	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "dismiss", NoteIDs: []float64{2, 1, 77}})
	require.Equal(t, "Dismissed 1 of 3 notes.\n- Note #2: dismissed\n- Note #1: already_dismissed\n- Note #77: not_found", out)

	require.Equal(t, EmptyNoteReadReply, RunCtxNote(ctx, deps, CtxNoteParams{Action: "read"}))
	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "read", Filter: "dismissed"})
	require.Contains(t, out, "Revisit flaky upload test")

	// Another session cannot see or dismiss ses1's notes.
	other := deps
	other.SessionID = func(context.Context) string { return "ses2" }
	out = RunCtxNote(ctx, other, CtxNoteParams{Action: "dismiss", NoteIDs: []float64{2, 3}})
	require.Contains(t, out, "- Note #2: not_found")
}

func TestCtxNoteSmartNotes(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t, "ses1")
	cond := "PR #42 is merged"

	out := RunCtxNote(ctx, p3NoteDeps(s, Settings{}), CtxNoteParams{Content: "Bump dep", SurfaceCondition: cond})
	require.Equal(t, SmartNotesDisabledReply, out)

	out = RunCtxNote(ctx, p3NoteDeps(s, Settings{SmartNotes: true}), CtxNoteParams{Content: "Bump dep", SurfaceCondition: cond})
	require.Contains(t, out, "require dreamer")

	deps := p3NoteDeps(s, Settings{SmartNotes: true, Dreamer: true})
	noProj := deps
	noProj.ProjectPath = func(context.Context) string { return "" }
	out = RunCtxNote(ctx, noProj, CtxNoteParams{Content: "Bump dep", SurfaceCondition: cond})
	require.Contains(t, out, "Could not resolve project identity for smart note")

	out = RunCtxNote(ctx, deps, CtxNoteParams{Content: "Bump dep", SurfaceCondition: cond})
	require.Equal(t, "Created smart note #1. Dreamer will evaluate the condition during nightly runs:\n- Content: Bump dep\n- Condition: PR #42 is merged", out)
	n, ok, err := s.GetNote(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, NoteTypeSmart, n.Type)
	require.Equal(t, NoteStatusPending, n.Status)

	// Default glance includes pending smart notes.
	require.Contains(t, RunCtxNote(ctx, deps, CtxNoteParams{}), "Bump dep")

	RunCtxNote(ctx, deps, CtxNoteParams{Content: "plain"})
	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "update", NoteIDs: []float64{2}, SurfaceCondition: "x"})
	require.Equal(t, "Error: "+sessionNoteConditionError, out)

	out = RunCtxNote(ctx, deps, CtxNoteParams{Action: "update", NoteIDs: []float64{1}, SurfaceCondition: "v2 released"})
	require.Contains(t, out, "Condition: v2 released")
}

func TestCtxNoteDescriptions(t *testing.T) {
	plain := withoutSmartNoteText(ctxNoteDescription)
	require.NotContains(t, plain, "surface_condition")
	require.Contains(t, plain, "newest first")
	require.Contains(t, plain, "- dismiss: retire 1–50 notes")
	require.NotNil(t, NewCtxNoteTool(NoteToolDeps{Settings: Settings{SmartNotes: true, Dreamer: true}}))
	require.NotNil(t, NewCtxNoteTool(NoteToolDeps{}))
}

func TestNoteNudgeFlow(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t, "ses1")
	_, err := s.AddSessionNote(ctx, "ses1", "Follow up on cache eviction\ndetails")
	require.NoError(t, err)
	now := time.Now()

	txt, err := s.PeekNoteNudge(ctx, NoteNudgePeek{SessionID: "ses1", CurrentUserMessageID: "u1", Now: now})
	require.NoError(t, err)
	require.Empty(t, txt, "no trigger armed")

	require.NoError(t, s.TriggerNoteNudge(ctx, "ses1", "historian"))
	// First peek pins the trigger to the current user message.
	txt, err = s.PeekNoteNudge(ctx, NoteNudgePeek{SessionID: "ses1", CurrentUserMessageID: "u1", Now: now})
	require.NoError(t, err)
	require.Empty(t, txt)
	// Same message: still deferred.
	txt, err = s.PeekNoteNudge(ctx, NoteNudgePeek{SessionID: "ses1", CurrentUserMessageID: "u1", Now: now})
	require.NoError(t, err)
	require.Empty(t, txt)
	// Next user message: delivered.
	txt, err = s.PeekNoteNudge(ctx, NoteNudgePeek{SessionID: "ses1", CurrentUserMessageID: "u2", Now: now})
	require.NoError(t, err)
	require.Contains(t, txt, "0 notes ready, 1 active")
	require.Contains(t, txt, "Follow up on cache eviction")
	require.Contains(t, RenderNoteNudge(txt), `<instruction name="deferred_notes">`)

	require.NoError(t, s.MarkNoteNudgeDelivered(ctx, "ses1", txt, "u2", now))
	m, err := s.Meta(ctx, "ses1")
	require.NoError(t, err)
	anchors := NoteNudgeAnchors(m)
	require.Len(t, anchors, 1)
	require.Equal(t, "u2", anchors[0].MessageID)

	// Cooldown suppresses an immediate re-trigger.
	require.NoError(t, s.TriggerNoteNudge(ctx, "ses1", "notes"))
	_, _ = s.PeekNoteNudge(ctx, NoteNudgePeek{SessionID: "ses1", CurrentUserMessageID: "u3", Now: now})
	txt, err = s.PeekNoteNudge(ctx, NoteNudgePeek{SessionID: "ses1", CurrentUserMessageID: "u4", Now: now.Add(time.Minute)})
	require.NoError(t, err)
	require.Empty(t, txt)
}

func TestDreamCron(t *testing.T) {
	c, err := ParseCron("0 3 * * *")
	require.NoError(t, err)
	base := time.Date(2026, 3, 10, 2, 30, 0, 0, time.Local)
	next, ok := c.Next(base, 0)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 3, 10, 3, 0, 0, 0, time.Local), next)
	next, ok = c.Next(next, 0)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 3, 11, 3, 0, 0, 0, time.Local), next)

	w, err := ParseCron("0 4 * * 0") // Sundays
	require.NoError(t, err)
	next, ok = w.Next(base, 0)
	require.True(t, ok)
	require.Equal(t, time.Sunday, next.Weekday())
	require.Equal(t, 4, next.Hour())

	_, err = ParseCron("bad")
	require.Error(t, err)
	require.Zero(t, NextDueAtMs("", base.UnixMilli()))
	require.Zero(t, NextDueAtMs("0 0 31 2 *", base.UnixMilli()))
}
