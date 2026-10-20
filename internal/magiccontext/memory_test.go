// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for features/magic-context/memory and tools/ctx-memory.

// Tests for the memory store and ctx_memory tool (port of cortexkit/magic-context, MIT).

package magiccontext

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

func p2TestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Connect(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Release(dir)
	})
	return NewStore(conn)
}

func p2MemDeps(s *Store, project string) MemoryToolDeps {
	return MemoryToolDeps{
		Store:      s,
		Settings:   Settings{MemoryEnabled: true},
		ProjectKey: func(context.Context) string { return project },
		SessionID:  func(context.Context) string { return "ses1" },
	}
}

func TestMemoryAddDedupAndHash(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	m, created, err := s.AddMemory(ctx, MemoryInput{ProjectKey: "git:abc", Category: "CONSTRAINTS", Content: "Builds need  RGBA PNGs."})
	require.NoError(t, err)
	require.True(t, created)
	m2, created, err := s.AddMemory(ctx, MemoryInput{ProjectKey: "git:abc", Category: "CONSTRAINTS", Content: "builds need rgba pngs."})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, m.ID, m2.ID)
	require.Equal(t, 2, m2.SeenCount)

	mems, err := s.ProjectMemories(ctx, "git:abc")
	require.NoError(t, err)
	require.Len(t, mems, 1)
	other, err := s.ProjectMemories(ctx, "git:other")
	require.NoError(t, err)
	require.Empty(t, other)
}

func TestCtxMemoryWriteUpdateArchive(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	d := p2MemDeps(s, "git:abc")

	require.Equal(t, "Error: 'content' is required when action is 'write'.", runCtxMemory(ctx, d, CtxMemoryParams{Action: "write"}))
	require.Equal(t, "Error: 'category' is required when action is 'write'.", runCtxMemory(ctx, d, CtxMemoryParams{Action: "write", Content: "x"}))
	require.Equal(t, "Error: Unknown memory category 'NOPE'.", runCtxMemory(ctx, d, CtxMemoryParams{Action: "write", Content: "x", Category: "NOPE"}))
	require.Equal(t, "Saved memory [ID: 1] in CONFIG_VALUES.", runCtxMemory(ctx, d, CtxMemoryParams{Action: "write", Content: "Port is 8080", Category: "CONFIG_VALUES"}))
	require.Equal(t, "Memory already exists [ID: 1] in CONFIG_VALUES (seen count incremented).", runCtxMemory(ctx, d, CtxMemoryParams{Action: "write", Content: "port is 8080", Category: "CONFIG_VALUES"}))
	require.Equal(t, "Saved memory [ID: 2] in CONFIG_VALUES.", runCtxMemory(ctx, d, CtxMemoryParams{Action: "write", Content: "Host is localhost", Category: "CONFIG_VALUES"}))

	require.Equal(t, "Error: Memory content already exists as ID 1; merge or archive duplicates instead.",
		runCtxMemory(ctx, d, CtxMemoryParams{Action: "update", IDs: []float64{2}, Content: "Port is 8080"}))
	require.Equal(t, "Updated memory [ID: 2] in ARCHITECTURE.",
		runCtxMemory(ctx, d, CtxMemoryParams{Action: "update", IDs: []float64{2}, Content: "Host is 0.0.0.0", Category: "ARCHITECTURE"}))
	m, ok, err := s.GetMemory(ctx, 2)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "Host is 0.0.0.0", m.Content)
	require.Equal(t, "ARCHITECTURE", m.Category)

	require.Equal(t, "Error: Memory with ID 99 was not found.", runCtxMemory(ctx, d, CtxMemoryParams{Action: "update", IDs: []float64{99}, Content: "x"}))
	require.Equal(t, "Archived memory [ID: 2] (obsolete).", runCtxMemory(ctx, d, CtxMemoryParams{Action: "archive", IDs: []float64{2, 2}, Reason: "obsolete"}))
	require.Equal(t, "Error: Memory with ID 2 is archived or superseded; restore it before updating.",
		runCtxMemory(ctx, d, CtxMemoryParams{Action: "update", IDs: []float64{2}, Content: "y"}))

	got := runCtxMemory(ctx, d, CtxMemoryParams{Action: "get", IDs: []float64{1, 2, 77}})
	require.Contains(t, got, "Port is 8080")
	require.Contains(t, got, "archived")
	require.Contains(t, got, "id 77: not found or not visible from this project")

	// Other projects can't see or mutate these memories.
	foreign := p2MemDeps(s, "git:zzz")
	require.Equal(t, "Error: Memory with ID 1 was not found.", runCtxMemory(ctx, foreign, CtxMemoryParams{Action: "update", IDs: []float64{1}, Content: "x"}))
	require.Contains(t, runCtxMemory(ctx, foreign, CtxMemoryParams{Action: "get", IDs: []float64{1}}), "not visible")

	d.AllowedActions = []string{"get"}
	require.Equal(t, "Error: Action 'write' is not allowed in this context.", runCtxMemory(ctx, d, CtxMemoryParams{Action: "write"}))
	d.AllowedActions = nil
	d.Settings.MemoryEnabled = false
	require.Equal(t, "Cross-session memory is disabled for this project.", runCtxMemory(ctx, d, CtxMemoryParams{Action: "get", IDs: []float64{1}}))
}

func TestCtxMemoryMerge(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	d := p2MemDeps(s, "git:abc")
	for _, c := range []string{"Use tabs", "Indent with tabs"} {
		runCtxMemory(ctx, d, CtxMemoryParams{Action: "write", Content: c, Category: "NAMING"})
	}
	runCtxMemory(ctx, d, CtxMemoryParams{Action: "write", Content: "Other", Category: "CONSTRAINTS"})

	require.Equal(t, "Error: 'ids' must include at least two distinct memory IDs when action is 'merge'.",
		runCtxMemory(ctx, d, CtxMemoryParams{Action: "merge", IDs: []float64{1, 1}, Content: "x"}))
	require.True(t, strings.HasPrefix(runCtxMemory(ctx, d, CtxMemoryParams{Action: "merge", IDs: []float64{1, 3}, Content: "x"}),
		"Error: Cannot merge memories from different categories (NAMING, CONSTRAINTS)."))

	// Canonical is an existing source when content matches one of them.
	require.Equal(t, "Merged memories [1, 2] into canonical memory [ID: 2] in NAMING; superseded [1].",
		runCtxMemory(ctx, d, CtxMemoryParams{Action: "merge", IDs: []float64{1, 2}, Content: "indent with tabs"}))
	m1, _, _ := s.GetMemory(ctx, 1)
	require.Equal(t, int64(2), m1.SupersededBy)
	m2, _, _ := s.GetMemory(ctx, 2)
	require.Equal(t, 2, m2.SeenCount)
	require.Equal(t, "[1,2]", m2.MergedFrom)

	upd, err := s.MemoryUpdatesSince(ctx, "git:abc", 0, 1, 2)
	require.NoError(t, err)
	require.NotEmpty(t, upd.Mutations)
}

func TestRenderProjectMemoryBudget(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	for _, c := range []string{"Alpha rule", "Beta rule", "Gamma rule"} {
		_, _, err := s.AddMemory(ctx, MemoryInput{ProjectKey: "git:abc", Category: "PROJECT_RULES", Content: c})
		require.NoError(t, err)
	}
	mems, err := s.ProjectMemories(ctx, "git:abc")
	require.NoError(t, err)
	out := RenderProjectMemory(mems, 10000)
	require.Contains(t, out, "Alpha rule")
	require.Contains(t, out, "Gamma rule")
}
