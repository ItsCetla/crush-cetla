// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for inject-compartments.ts m0/m1 behaviour.

package magiccontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func renderOpts(sid, project string) RenderOptions {
	return RenderOptions{
		SessionID:  sid,
		ProjectKey: project,
		Settings:   Settings{Enabled: true, MemoryEnabled: true},
		ModelKey:   "prov/model",
		SystemHash: "sys1",
	}
}

func addComp(t *testing.T, s *Store, sid string, seq int, title, endMsg string) {
	t.Helper()
	_, err := s.InsertCompartments(context.Background(), s.db, sid, false, []Compartment{{
		Sequence: seq, StartOrdinal: seq*10 - 9, EndOrdinal: seq * 10,
		StartMessageID: "m" + title, EndMessageID: endMsg, Title: title,
		Content: "Did the " + title + " work in detail.",
	}})
	require.NoError(t, err)
}

func TestInjectFirstRenderAndReplay(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t, "s1")
	addComp(t, s, "s1", 1, "alpha", "msg-10")
	addTestMemories(t, s, "proj", 2)
	o := renderOpts("s1", "proj")

	r, err := s.InjectM0M1(ctx, o)
	require.NoError(t, err)
	require.True(t, r.Materialized)
	require.Equal(t, "first_render", r.Decision.Reason)
	require.Contains(t, r.M0, "alpha")
	require.Contains(t, r.M0, "<project-memory>")
	require.Equal(t, M1EmptyPlaceholder, r.M1)
	require.Equal(t, "msg-10", r.Boundary)

	// New compartment + memory: replay stays byte-identical on a quiet pass.
	addComp(t, s, "s1", 2, "beta", "msg-20")
	addTestMemories(t, s, "proj", 1)
	r2, err := s.InjectM0M1(ctx, o)
	require.NoError(t, err)
	require.False(t, r2.Materialized)
	require.False(t, r2.SoftRefreshed)
	require.Equal(t, r.M0, r2.M0)
	require.Equal(t, r.M1, r2.M1)
	require.Equal(t, "msg-10", r2.Boundary)

	// A cache-busting pass soft-refreshes m1 only.
	o.CacheBusting = true
	r3, err := s.InjectM0M1(ctx, o)
	require.NoError(t, err)
	require.True(t, r3.SoftRefreshed)
	require.Equal(t, r.M0, r3.M0)
	require.Contains(t, r3.M1, "<new-compartments>")
	require.Contains(t, r3.M1, "beta")
	require.Contains(t, r3.M1, "<new-memories>")
	require.Equal(t, "msg-20", r3.Boundary)

	// Replay now returns the refreshed pair.
	o.CacheBusting = false
	r4, err := s.InjectM0M1(ctx, o)
	require.NoError(t, err)
	require.Equal(t, r3.M1, r4.M1)
	require.Equal(t, "msg-20", r4.Boundary)

	// Force folds everything into m0.
	o.Force, o.ForceReason = true, "historian_publish"
	r5, err := s.InjectM0M1(ctx, o)
	require.NoError(t, err)
	require.True(t, r5.Materialized)
	require.Equal(t, "historian_publish", r5.Decision.Reason)
	require.Contains(t, r5.M0, "beta")
	require.Equal(t, M1EmptyPlaceholder, r5.M1)
}

func TestInjectMemoryUpdates(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t, "s1")
	mems := addTestMemories(t, s, "proj", 3)
	o := renderOpts("s1", "proj")
	_, err := s.InjectM0M1(ctx, o)
	require.NoError(t, err)

	require.NoError(t, s.UpdateMemory(ctx, mems[0].ID, "Rewritten fact", ""))
	require.NoError(t, s.ArchiveMemories(ctx, []int64{mems[1].ID}))
	o.CacheBusting = true
	r, err := s.InjectM0M1(ctx, o)
	require.NoError(t, err)
	require.True(t, r.SoftRefreshed)
	require.Contains(t, r.M1, "<memory-updates>")
	require.Contains(t, r.M1, "Rewritten fact")
	require.Contains(t, r.M1, `<removed id="`)
	require.Equal(t, 2, r.UpdateCount)
}

func TestMustMaterializeReasons(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t, "s1")
	addComp(t, s, "s1", 1, "alpha", "msg-10")
	o := renderOpts("s1", "proj")
	_, err := s.InjectM0M1(ctx, o)
	require.NoError(t, err)

	check := func(o RenderOptions, want string) {
		t.Helper()
		d, err := s.MustMaterialize(ctx, o)
		require.NoError(t, err)
		require.Equal(t, want, d.Reason)
	}
	check(o, "cached")
	o2 := o
	o2.ModelKey = "other/model"
	check(o2, "model_changed")
	o2 = o
	o2.SystemHash = "sys2"
	check(o2, "system_changed")
	o2 = o
	o2.HistoryBudget = 1234
	check(o2, "budget_changed")
	o2 = o
	o2.ProjectKey = "other"
	check(o2, "project_changed")
	o2 = o
	o2.Settings.MemoryEnabled = false
	check(o2, "budget_changed")

	require.NoError(t, s.BumpRenderEpoch(ctx, nil, "s1"))
	check(o, "render_epoch")
	_, err = s.InjectM0M1(ctx, o)
	require.NoError(t, err)
	check(o, "cached")

	require.NoError(t, s.DeleteCompartments(ctx, s.db, "s1", false))
	check(o, "compartments_rewritten")
}

func TestMemoryDisabledInvalidatesMemoryBlocks(t *testing.T) {
	ctx := context.Background()
	s := p3Store(t, "s1")
	addTestMemories(t, s, "proj", 1)
	o := renderOpts("s1", "proj")
	r, err := s.InjectM0M1(ctx, o)
	require.NoError(t, err)
	require.Contains(t, r.M0, "<project-memory>")

	o.Settings.MemoryEnabled = false
	r, err = s.InjectM0M1(ctx, o)
	require.NoError(t, err)
	require.Equal(t, "memory_disabled", r.Decision.Reason)
	require.NotContains(t, r.M0, "<project-memory>")
	require.Equal(t, M0EmptyBody, r.M0)
}

func TestProjectDocs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ARCHITECTURE.md"), []byte("\ufeff# Arch  \r\nuses <xml> & stuff\r\n\r\n"), 0o644))
	got := RenderProjectDocs(dir)
	require.Equal(t, "<project-docs>\n<file name=\"ARCHITECTURE.md\">\n# Arch\nuses &lt;xml&gt; &amp; stuff\n</file>\n</project-docs>", got)

	// Symlinks and oversized files are refused.
	secret := filepath.Join(t.TempDir(), "secret.md")
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(secret, filepath.Join(dir, "STRUCTURE.md")))
	require.NotContains(t, RenderProjectDocs(dir), "secret")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ARCHITECTURE.md"), []byte(strings.Repeat("a", maxProjectDocBytes+1)), 0o644))
	require.Equal(t, "", RenderProjectDocs(dir))
}

func TestM1Pressure(t *testing.T) {
	require.True(t, m1UnderPressure(RenderResult{UpdateCount: 41}, 60000))
	require.False(t, m1UnderPressure(RenderResult{M0: "x", M1: "y"}, 60000))
	require.True(t, m1UnderPressure(RenderResult{M0: strings.Repeat("word ", 2000), M1: strings.Repeat("word ", 400)}, 60000))
}
