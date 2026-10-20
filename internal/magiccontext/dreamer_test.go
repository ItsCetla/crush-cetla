// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for features/magic-context/dreamer, user-memory and primers.

// Tests for the dreamer, user profile and primers (port of
// cortexkit/magic-context, MIT). All model calls use fake Generators.

package magiccontext

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func fakeGen(reply string, calls *[]string) Generator {
	return func(_ context.Context, system, user string, _ int) (string, error) {
		if calls != nil {
			*calls = append(*calls, user)
		}
		return reply, nil
	}
}

func TestReviewUserMemories(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t)
	n, err := s.InsertUserMemoryCandidates(ctx, nil, "session-abcdefghijklmnop", 1, 5, []string{"Asks for terse answers", "Wants tests run", " ", "Asks for short replies"})
	require.NoError(t, err)
	require.Equal(t, 3, n)
	_, err = s.db.ExecContext(ctx, `INSERT INTO mc_user_memories (content, status, promoted_at, source_candidate_ids, created_at, updated_at) VALUES ('Old habit', 'active', 1, '[]', 1, 1)`)
	require.NoError(t, err)

	// Below threshold: no model call.
	var calls []string
	res, err := ReviewUserMemories(ctx, s, fakeGen("", &calls), 5, time.Now())
	require.NoError(t, err)
	require.Empty(t, calls)
	require.Zero(t, res.CandidatesConsumed)

	reply := "Here you go:\n```json\n" + `{"promote":[{"content":"Prefers concise answers","candidate_ids":[1,3]}],` +
		`"update_existing":[],"dismiss_existing":[{"memory_id":1,"reason":"stale"}],"consume_candidate_ids":[2]}` + "\n```"
	v0, err := s.UserProfileVersion(ctx)
	require.NoError(t, err)
	res, err = ReviewUserMemories(ctx, s, fakeGen(reply, &calls), 0, time.Now())
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Contains(t, calls[0], `- Candidate #1 [session session-abcd]: "Asks for terse answers"`)
	require.Contains(t, calls[0], `- Memory #1: "Old habit"`)
	require.Equal(t, UserMemoryReviewResult{Promoted: 1, Dismissed: 1, CandidatesConsumed: 3}, res)

	left, err := s.UserMemoryCandidates(ctx)
	require.NoError(t, err)
	require.Empty(t, left)
	items, err := s.UserProfileItems(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"Prefers concise answers"}, items)
	v1, err := s.UserProfileVersion(ctx)
	require.NoError(t, err)
	require.Greater(t, v1, v0)
	require.Contains(t, RenderUserProfile(items, DefaultUserProfileBudgetTokens), "Prefers concise answers")
	require.Empty(t, RenderNewUserProfile(items, DefaultUserProfileBudgetTokens, v1, v1))

	_, err = parseUserMemoryVerdict("")
	require.EqualError(t, err, "User memory review returned no output.")
	_, err = parseUserMemoryVerdict("nothing here")
	require.EqualError(t, err, "User memory review returned no JSON.")
	_, err = parseUserMemoryVerdict("{nope}")
	require.EqualError(t, err, "User memory review returned invalid JSON.")
}

var testMemorySeq int

func addTestMemories(t *testing.T, s *Store, project string, n int) []Memory {
	t.Helper()
	var out []Memory
	for range n {
		testMemorySeq++
		m, _, err := s.AddMemory(context.Background(), MemoryInput{ProjectKey: project, Category: "ARCHITECTURE", Content: fmt.Sprintf("Fact number %d about the system", testMemorySeq)})
		require.NoError(t, err)
		out = append(out, m)
	}
	return out
}

func TestClassifyMemories(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t)
	mems := addTestMemories(t, s, "git:p", 9)

	n, err := ClassifyMemories(ctx, s, nil, "git:p")
	require.NoError(t, err)
	require.Zero(t, n, "pool under 10 is a no-op even without a generator")

	mems = append(mems, addTestMemories(t, s, "git:p", 1)...)
	var b strings.Builder
	b.WriteString("<classify>\n")
	for i, m := range mems {
		scope := "project"
		if i == 0 {
			scope = "universe"
		}
		fmt.Fprintf(&b, `<memory id="%d" importance="%d" scope="%s" shareable="false"/>`+"\n", m.ID, 10*i+500, scope)
	}
	b.WriteString("</classify>")
	var calls []string
	n, err = ClassifyMemories(ctx, s, fakeGen(b.String(), &calls), "git:p")
	require.NoError(t, err)
	require.Equal(t, 10, n)
	require.Contains(t, calls[0], "## Task: Classify Project Memories\n\n**Project:** git:p")
	require.Contains(t, calls[0], "(current: importance=50 scope=project shareable=false)")
	got, err := s.ProjectMemories(ctx, "git:p")
	require.NoError(t, err)
	for _, m := range got {
		require.Positive(t, m.ClassifiedAt)
		require.Equal(t, 100, m.Importance, "clamped")
		if m.ID == mems[0].ID {
			require.Equal(t, "universe", m.Scope)
		}
	}

	_, err = parseClassifyManifest(`<classify><memory id="1" importance="5"/>`)
	require.ErrorContains(t, err, "close tag")
	_, err = parseClassifyManifest(`<classify><memory importance="5"/></classify>`)
	require.EqualError(t, err, "classify manifest entry missing numeric id")
	_, err = parseClassifyManifest(`<classify><memory id="3" scope="galaxy"/></classify>`)
	require.EqualError(t, err, "classify manifest invalid scope galaxy")
	_, err = parseClassifyManifest(`<classify><memory id="3"/></classify>`)
	require.EqualError(t, err, "classify manifest entry 3 missing classification fields")
	_, err = parseClassifyManifest(`<classify><memory id="3" importance="1"/><memory id="3" importance="2"/></classify>`)
	require.ErrorContains(t, err, "duplicate")
}

func TestRunDreamExpiryAndScheduling(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t)
	m, _, err := s.AddMemory(ctx, MemoryInput{ProjectKey: "git:p", Category: "ARCHITECTURE", Content: "Temporary fact", ExpiresAt: 1000})
	require.NoError(t, err)

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.Local)
	clock := now
	deps := DreamDeps{
		Store: s, ProjectPath: "git:p", HolderID: "h1",
		Settings: Settings{Dreamer: true, MemoryEnabled: true},
		Now:      func() time.Time { return clock },
	}

	// First sight schedules without running.
	due, err := DreamDue(ctx, deps)
	require.NoError(t, err)
	require.Empty(t, due)

	clock = now.Add(36 * time.Hour)
	due, err = DreamDue(ctx, deps)
	require.NoError(t, err)
	require.Contains(t, due, DreamTaskClassifyMemories)
	require.Contains(t, due, DreamTaskPromotePrimers)
	require.Contains(t, due, DreamTaskReviewUserMemories)
	require.NotContains(t, due, DreamTaskEvaluateSmartNotes, "smart notes disabled")
	require.NotContains(t, due, DreamTaskMaintainDocs, "disabled by default")

	rep, err := RunDueDreams(ctx, deps)
	require.NoError(t, err)
	require.Positive(t, rep.RunID)
	require.Equal(t, 1, rep.MemoryChanges["expired"])
	got, _, err := s.GetMemory(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, MemoryStatusArchived, got.Status)
	status := map[string]string{}
	for _, tr := range rep.Tasks {
		status[tr.Task] = tr.Status
	}
	require.Equal(t, DreamStatusOK, status[DreamTaskClassifyMemories], "empty pool is a no-op")
	require.Equal(t, DreamStatusOK, status[DreamTaskReviewUserMemories])
	require.NotContains(t, status, DreamTaskVerify, "tool-loop tasks are never due")
	rep, err = RunDream(ctx, deps, []string{DreamTaskVerify})
	require.NoError(t, err)
	require.Equal(t, DreamStatusSkipped, rep.Tasks[0].Status)

	// Everything advanced to a future occurrence.
	due, err = DreamDue(ctx, deps)
	require.NoError(t, err)
	require.Empty(t, due)

	// A failing task retries after 15 minutes.
	addTestMemories(t, s, "git:p", 10)
	rep, err = RunDream(ctx, deps, []string{DreamTaskClassifyMemories})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Failed)
	require.Contains(t, rep.Tasks[0].Err, ErrNoGenerator.Error())
	var next int64
	var retry int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT next_due_at, retry_count FROM mc_task_schedule_state WHERE project_path = ? AND task = ?`, "git:p", DreamTaskClassifyMemories).Scan(&next, &retry))
	require.Equal(t, 1, retry)
	require.Equal(t, clock.Add(15*time.Minute).UnixMilli(), next)

	// Schedule override: "" disables.
	empty := ""
	deps.Settings.DreamerTasks = map[string]config.MCDreamerTaskOpts{DreamTaskClassifyMemories: {Schedule: &empty}}
	require.Empty(t, DreamTaskSchedule(deps.Settings, DreamTaskClassifyMemories))

	// Lease held by someone else skips the task.
	ok, err := s.dreamAcquireLease(ctx, dreamLeaseKey(DreamTaskPromotePrimers, "git:p"), "other", clock)
	require.NoError(t, err)
	require.True(t, ok)
	rep, err = RunDream(ctx, deps, []string{DreamTaskPromotePrimers})
	require.NoError(t, err)
	require.Equal(t, DreamStatusSkipped, rep.Tasks[0].Status)
}

func TestPromoteAndSearchPrimers(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t)
	now := time.Now()
	in := func(sid string, at time.Time, start int) PrimerCandidateInput {
		return PrimerCandidateInput{ProjectPath: "git:p", SessionID: sid, Question: "How does the dreamer lease work?", SourceCompartmentStart: start, SourceCompartmentEnd: start + 1, SourceMessageTime: at.UnixMilli()}
	}
	require.NoError(t, s.InsertPrimerCandidates(ctx, nil, []PrimerCandidateInput{in("a", now.Add(-10*24*time.Hour), 1)}))
	res, err := PromotePrimers(ctx, s, "git:p", nil, "", now)
	require.NoError(t, err)
	require.Zero(t, res.Promoted, "one support day is not enough")

	require.NoError(t, s.InsertPrimerCandidates(ctx, nil, []PrimerCandidateInput{in("b", now.Add(-24*time.Hour), 3)}))
	res, err = PromotePrimers(ctx, s, "git:p", nil, "", now)
	require.NoError(t, err)
	require.Equal(t, 1, res.Promoted)
	require.Equal(t, 2, res.Candidates)

	prims, err := s.ActivePrimers(ctx, "git:p")
	require.NoError(t, err)
	require.Len(t, prims, 1)
	require.Equal(t, 2, prims[0].TotalSupport)

	// A rerun updates rather than duplicates.
	res, err = PromotePrimers(ctx, s, "git:p", nil, "", now)
	require.NoError(t, err)
	require.Zero(t, res.Promoted)
	require.Equal(t, 1, res.Updated)

	hits, err := s.SearchPrimers(ctx, "git:p", "dreamer lease", nil, "", 5)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Contains(t, FormatPrimerHit(hits[0]), "[primer #")
	hits, err = s.SearchPrimers(ctx, "git:other", "dreamer lease", nil, "", 5)
	require.NoError(t, err)
	require.Empty(t, hits)
}
