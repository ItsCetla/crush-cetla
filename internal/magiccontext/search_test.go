// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for features/magic-context/search.ts.

// Tests for unified search (port of cortexkit/magic-context, MIT).

package magiccontext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func p2InsertSession(t *testing.T, s *Store, id string) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO sessions (id, title, updated_at, created_at) VALUES (?, 't', 0, 0)`, id)
	require.NoError(t, err)
}

func TestSearchQueryBuilding(t *testing.T) {
	require.Equal(t, `"foo" "ba""r"`, sanitizeFTSQuery(`foo ba"r`))
	require.Equal(t, `"where" OR "the" OR "opencode" OR "source" OR "v2"`, relaxedFTSQuery("where is the opencode source v2 v2"))
	require.Equal(t, []int64{7234}, parseIDQuery("#7234"))
	require.Equal(t, []int64{12, 34}, parseIDQuery("12, 34"))
	require.Nil(t, parseIDQuery("12 abc"))
	probes := extractLiteralProbes("why does `foo bar` fail with ERR_BAD_THING in config.yaml and getUserName?")
	require.Contains(t, probes, "foo bar")
	require.Contains(t, probes, "ERR_BAD_THING")
	require.Contains(t, probes, "config.yaml")
	require.LessOrEqual(t, len(probes), 5)
}

func TestSearchMemoriesAndMessages(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	require.True(t, s.EnsureSearchIndex(ctx))
	proj := "git:abc"
	a, _, err := s.AddMemory(ctx, MemoryInput{ProjectKey: proj, Category: "CONFIG_VALUES", Content: "OpenCode source lives at ~/Work/OSS/opencode"})
	require.NoError(t, err)
	b, _, err := s.AddMemory(ctx, MemoryInput{ProjectKey: proj, Category: "CONSTRAINTS", Content: "The opencode build needs bun"})
	require.NoError(t, err)
	_, _, err = s.AddMemory(ctx, MemoryInput{ProjectKey: "git:other", Category: "CONSTRAINTS", Content: "opencode elsewhere"})
	require.NoError(t, err)

	p2InsertSession(t, s, "ses1")
	now := time.Now().UnixMilli()
	require.NoError(t, s.UpsertMessageIndex(ctx, "ses1", []IndexedMessage{
		{Ordinal: 1, MessageID: "m1", Role: "user", Content: "we decided sqlite over postgres for the dreamer lease", CreatedAt: now},
		{Ordinal: 2, MessageID: "m2", Role: "assistant", Content: "unrelated chatter", CreatedAt: now},
		{Ordinal: 9, MessageID: "m9", Role: "user", Content: "sqlite again but in the live tail", CreatedAt: now},
	}))
	_, err = s.InsertCompartments(ctx, s.db, "ses1", false, []Compartment{{Sequence: 1, StartOrdinal: 1, EndOrdinal: 5, Title: "c", Content: "x"}})
	require.NoError(t, err)

	res, err := s.Search(ctx, SearchRequest{ProjectKey: proj, SessionID: "ses1", Query: "opencode source", MemoryEnabled: true, Explicit: true, Sources: []string{SourceMemory}})
	require.NoError(t, err)
	require.NotEmpty(t, res.Hits)
	require.Equal(t, a.ID, res.Hits[0].MemoryID)
	for _, h := range res.Hits {
		require.NotEqual(t, "opencode elsewhere", h.Content)
	}
	m, _, _ := s.GetMemory(ctx, a.ID)
	require.Equal(t, 1, m.RetrievalCount)

	// Visible ids are suppressed and reported.
	res, err = s.Search(ctx, SearchRequest{ProjectKey: proj, Query: "opencode", MemoryEnabled: true, VisibleMemoryIDs: []int64{a.ID, b.ID}, Sources: []string{SourceMemory}})
	require.NoError(t, err)
	require.Empty(t, res.Hits)
	require.Equal(t, []int64{a.ID, b.ID}, res.Diagnostics.SuppressedMemoryIDs)
	out := FormatSearchResults("opencode", res)
	require.True(t, strings.HasPrefix(out, `No hidden results found for "opencode".`))
	require.Contains(t, out, "2 matches found, all already visible")

	// Messages: only ordinals at/below the compartment boundary; newer counted.
	res, err = s.Search(ctx, SearchRequest{ProjectKey: proj, SessionID: "ses1", Query: "sqlite", Explicit: true, Sources: []string{SourceMessage}})
	require.NoError(t, err)
	require.Len(t, res.Hits, 1)
	require.Equal(t, 1, res.Hits[0].Ordinal)
	require.Equal(t, 1, res.Diagnostics.NewerMessageMatches)
	out = FormatSearchResults("sqlite", res)
	require.Contains(t, out, "[1] [message] score=1.00 ordinal=1 range=1-4 role=user")
	require.Contains(t, out, "1 raw-message match is newer than the last compartment boundary")
	require.Contains(t, out, "ctx_expand(start, end)")

	// Relaxed fallback for explicit queries with a non-matching word.
	res, err = s.Search(ctx, SearchRequest{SessionID: "ses1", Query: "postgres zebra", Explicit: true, Sources: []string{SourceMessage}})
	require.NoError(t, err)
	require.Len(t, res.Hits, 1)

	// Non-git project reports no git repository.
	res, err = s.Search(ctx, SearchRequest{ProjectKey: "dir:123", Query: "anything", Sources: []string{SourceGitCommit}})
	require.NoError(t, err)
	require.True(t, res.Diagnostics.NoGitRepository)

	// Date range excludes everything in the past.
	res, err = s.Search(ctx, SearchRequest{ProjectKey: proj, SessionID: "ses1", Query: "opencode", MemoryEnabled: true, From: now + 86400000})
	require.NoError(t, err)
	require.Empty(t, res.Hits)
}

func TestSearchSemanticWithFakeEmbedder(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	proj := "git:abc"
	vec := func(text string) []float32 {
		if strings.Contains(strings.ToLower(text), "database") || strings.Contains(strings.ToLower(text), "sqlite") {
			return []float32{1, 0}
		}
		return []float32{0, 1}
	}
	embed := Embedder(func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i, t := range texts {
			out[i] = vec(t)
		}
		return out, nil
	})
	m, _, err := s.AddMemory(ctx, MemoryInput{ProjectKey: proj, Category: "ARCHITECTURE", Content: "We store state in SQLite"})
	require.NoError(t, err)
	_, _, err = s.AddMemory(ctx, MemoryInput{ProjectKey: proj, Category: "ARCHITECTURE", Content: "UI uses bubbletea"})
	require.NoError(t, err)
	mems, _ := s.ProjectMemories(ctx, proj)
	require.NoError(t, s.EmbedMemories(ctx, embed, "fake", mems))

	res, err := s.Search(ctx, SearchRequest{ProjectKey: proj, Query: "which database engine?", MemoryEnabled: true, Embedder: embed, EmbeddingModel: "fake", Sources: []string{SourceMemory}})
	require.NoError(t, err)
	require.NotEmpty(t, res.Hits)
	require.Equal(t, m.ID, res.Hits[0].MemoryID)
	require.Equal(t, "semantic", res.Hits[0].MatchType)
	require.InDelta(t, 0.8, res.Hits[0].Score, 1e-6)
}

func TestHTTPEmbedderLocal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/embeddings", r.URL.Path)
		require.Equal(t, "Bearer k", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"index": 1, "embedding": []float32{0, 1}},
			{"index": 0, "embedding": []float32{1, 0}},
		}})
	}))
	defer srv.Close()
	require.Nil(t, NewHTTPEmbedder("", "m", ""))
	vecs, err := NewHTTPEmbedder(srv.URL, "m", "k")(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, [][]float32{{1, 0}, {0, 1}}, vecs)
}

func TestCtxSearchTool(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	proj := "git:abc"
	m, _, err := s.AddMemory(ctx, MemoryInput{ProjectKey: proj, Category: "NAMING", Content: "Packages are lowercase"})
	require.NoError(t, err)
	d := SearchToolDeps{
		Store: s, Settings: Settings{MemoryEnabled: true},
		ProjectKey: func(context.Context) string { return proj },
		SessionID:  func(context.Context) string { return "" },
	}
	out, err := runCtxSearch(ctx, d, CtxSearchParams{})
	require.NoError(t, err)
	require.Equal(t, "Error: 'query' is required.", out)
	out, _ = runCtxSearch(ctx, d, CtxSearchParams{Query: "x", From: "nope"})
	require.Equal(t, "Error: Invalid 'from' date; use YYYY-MM-DD or a full ISO datetime.", out)
	out, _ = runCtxSearch(ctx, d, CtxSearchParams{Query: "x", From: "2026-02-01", To: "2026-01-01"})
	require.Equal(t, "Error: Invalid date range; 'from' must be on or before 'to'.", out)

	out, _ = runCtxSearch(ctx, d, CtxSearchParams{Query: "#1"})
	require.Contains(t, out, "Found 1 result for \"#1\":")
	require.Contains(t, out, "[1] [memory] score=1.00 id=1 category=NAMING match=fts\nPackages are lowercase")
	_ = m

	out, _ = runCtxSearch(ctx, d, CtxSearchParams{Query: "lowercase packages", Sources: []string{"memory"}})
	require.Contains(t, out, "id=1 category=NAMING")
	out, _ = runCtxSearch(ctx, d, CtxSearchParams{Query: "zzzqqq", Sources: []string{"memory"}})
	require.Equal(t, `No results found for "zzzqqq" across notes, memories, primers, git commits, or message history.`, out)
}

func TestAutoSearchHint(t *testing.T) {
	ctx := context.Background()
	s := p2TestStore(t)
	proj := "git:abc"
	_, _, err := s.AddMemory(ctx, MemoryInput{ProjectKey: proj, Category: "CONSTRAINTS", Content: "The dreamer lease is really held in the database for 10 minutes"})
	require.NoError(t, err)
	set := Settings{MemoryEnabled: true, AutoSearch: true, AutoSearchMinChars: 20, AutoSearchThreshold: 0.6}
	hint, err := s.AutoSearchHint(ctx, AutoSearchHintRequest{ProjectKey: proj, Prompt: "how does the dreamer lease work", Settings: set})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hint, "<ctx-search-hint>\nYour memory may contain 1 related fragment:\n- "))
	require.NotContains(t, hint, "really")
	require.True(t, strings.HasSuffix(hint, "Otherwise ignore.\n</ctx-search-hint>"))
	require.LessOrEqual(t, len([]rune(hint)), 800)

	for _, p := range []string{"short", "how does the dreamer lease work <ctx-search-hint>"} {
		hint, err = s.AutoSearchHint(ctx, AutoSearchHintRequest{ProjectKey: proj, Prompt: p, Settings: set})
		require.NoError(t, err)
		require.Empty(t, hint)
	}
	m, _, _ := s.GetMemory(ctx, 1)
	require.Zero(t, m.RetrievalCount)
}

func TestCompressFragment(t *testing.T) {
	require.Equal(t, "dreamer lease → db // timeout", compressFragment("The dreamer lease and then the db because the timeout"))
	require.Equal(t, "see config.yaml", compressFragment("please see config.yaml"))
	require.Equal(t, "commit abc1234 today: fix", buildSearchHintLine(SearchHit{Source: SourceGitCommit, ShortSHA: "abc1234", CommittedAt: time.Now().UnixMilli(), Content: "fix\n\nbody"}))
}

func buildSearchHintLine(h SearchHit) string {
	out := buildSearchHint([]SearchHit{h}, time.Now())
	_, after, _ := strings.Cut(out, "- ")
	line, _, _ := strings.Cut(after, "\n")
	return line
}

func TestIndexGitCommitsAndSearch(t *testing.T) {
	if _, err := runGit(t.TempDir(), 5*time.Second, "--version"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "Add dreamer lease timeout", "-m", "Lease is ten minutes."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "Tweak ui colors"},
	} {
		_, err := runGit(repo, 10*time.Second, args...)
		require.NoError(t, err)
	}
	key := ProjectKey(repo)
	require.True(t, strings.HasPrefix(key, "git:"), key)

	s := p2TestStore(t)
	n, err := s.IndexGitCommits(ctx, Settings{GitIndex: true, GitMaxCommits: 10}, repo, key, nil)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	n, err = s.IndexGitCommits(ctx, Settings{GitIndex: true, GitMaxCommits: 1}, repo, key, nil)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM mc_git_commits WHERE project_path = ?`, key).Scan(&count))
	require.Equal(t, 1, count)

	_, err = s.IndexGitCommits(ctx, Settings{GitIndex: true, GitMaxCommits: 10}, repo, key, nil)
	require.NoError(t, err)
	res, err := s.Search(ctx, SearchRequest{ProjectKey: key, Query: "lease timeout", Sources: []string{SourceGitCommit}})
	require.NoError(t, err)
	require.Len(t, res.Hits, 1)
	require.Contains(t, res.Hits[0].Content, "Lease is ten minutes.")
	out := FormatSearchResults("lease timeout", res)
	require.Contains(t, out, "[git_commit] score=0.80 sha="+res.Hits[0].ShortSHA+" today match=fts")
}
