package artifacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStore(t.TempDir())
	require.NoError(t, err)
	return st
}

func strPtr(s string) *string { return &s }

const fullPage = "<!doctype html><html><head><title>Q3 Revenue</title></head><body>hi</body></html>"

func TestPublishCreatesAndVersions(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	res, err := st.Publish(ctx, PublishInput{Content: strPtr(fullPage)})
	require.NoError(t, err)
	require.True(t, res.Created)
	require.Equal(t, 1, res.Version.N)
	require.Equal(t, KindPage, res.Meta.Kind)
	require.Equal(t, "Q3 Revenue", res.Meta.Title)
	require.False(t, res.Wrapped)
	id := res.Meta.ID

	css := filepath.Join(t.TempDir(), "style.css")
	require.NoError(t, os.WriteFile(css, []byte("body{}"), 0o600))
	res2, err := st.Publish(ctx, PublishInput{ID: id, Files: map[string]*string{"style.css": &css}, Label: "styles"})
	require.NoError(t, err)
	require.False(t, res2.Created)
	require.Equal(t, 2, res2.Version.N)
	require.Equal(t, "styles", res2.Version.Label)
	require.Len(t, res2.Version.Files, 2)
	require.Equal(t, "index.html", res2.Version.Files[0].Path)

	// The page was carried over from v1.
	page, err := st.ReadFile(id, 2, "index.html")
	require.NoError(t, err)
	require.Equal(t, fullPage, string(page))

	// A nil source removes a file.
	res3, err := st.Publish(ctx, PublishInput{ID: id, Files: map[string]*string{"style.css": nil}})
	require.NoError(t, err)
	require.Len(t, res3.Version.Files, 1)

	m, err := st.Get(id)
	require.NoError(t, err)
	require.Equal(t, 3, m.Latest())
	v, ok := m.Version(0)
	require.True(t, ok)
	require.Equal(t, 3, v.N)

	list, err := st.List(ListOptions{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "/a/"+id, list[0].URL)
}

func TestPublishBaseVersionConflict(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	res, err := st.Publish(ctx, PublishInput{Content: strPtr(fullPage)})
	require.NoError(t, err)
	id := res.Meta.ID

	_, err = st.Publish(ctx, PublishInput{ID: id, Content: strPtr(fullPage), BaseVersion: 1})
	require.NoError(t, err)
	_, err = st.Publish(ctx, PublishInput{ID: id, Content: strPtr(fullPage), BaseVersion: 1})
	require.ErrorContains(t, err, "conflict")
}

func TestPublishKindIsFixed(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	res, err := st.Publish(t.Context(), PublishInput{Content: strPtr(fullPage)})
	require.NoError(t, err)
	_, err = st.Publish(t.Context(), PublishInput{ID: res.Meta.ID, Kind: KindSlides, Content: strPtr(fullPage)})
	require.ErrorContains(t, err, "can't change")
}

func TestPublishFailedCreateLeavesNothing(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	_, err := st.Publish(t.Context(), PublishInput{})
	require.Error(t, err)
	_, err = st.Publish(t.Context(), PublishInput{Content: strPtr(fullPage), Files: map[string]*string{"../x": strPtr("/etc/hosts")}})
	require.Error(t, err)

	entries, err := os.ReadDir(st.Root())
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, e.IsDir(), "left behind %s", e.Name())
	}
}

func TestPublishWrapsFragments(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	res, err := st.Publish(ctx, PublishInput{Content: strPtr("<p>hello</p>"), Title: "Greeting"})
	require.NoError(t, err)
	require.True(t, res.Wrapped)
	require.True(t, res.NoTitle)
	page, err := st.ReadFile(res.Meta.ID, 1, "index.html")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(page), "<!doctype html>"))
	require.Contains(t, string(page), "<title>Greeting</title>")

	res, err = st.Publish(ctx, PublishInput{Content: strPtr("<title>Own Name</title><p>x</p>")})
	require.NoError(t, err)
	require.True(t, res.Wrapped)
	require.False(t, res.NoTitle)
	require.Equal(t, "Own Name", res.Meta.Title)
	page, err = st.ReadFile(res.Meta.ID, 1, "index.html")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(page), "<title>"))
}

func TestPublishDocumentFromMarkdownFile(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	src := filepath.Join(t.TempDir(), "design-notes.md")
	require.NoError(t, os.WriteFile(src, []byte("# Design Notes\n\nBody."), 0o600))

	res, err := st.Publish(t.Context(), PublishInput{SourcePath: src})
	require.NoError(t, err)
	require.Equal(t, KindDocument, res.Meta.Kind)
	require.Equal(t, "Design Notes", res.Meta.Title)
	require.False(t, res.Wrapped)
	require.Equal(t, "index.md", res.Meta.PageName())
}

func TestParseID(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"abc123def":                           "abc123def",
		"http://127.0.0.1:7480/a/abc123def":   "abc123def",
		"/a/abc123def?v=2":                    "abc123def",
		"http://127.0.0.1:7480/p/abc123def/3": "abc123def",
		"http://127.0.0.1:7480/p/0123456789abcdef0123456789abcdef/abc123def/3/index.html": "abc123def",
	} {
		got, err := ParseID(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got)
	}
	for _, in := range []string{"", "../etc", "ABC", "a/b"} {
		_, err := ParseID(in)
		require.Error(t, err, in)
	}
}

func TestCleanPublishedPath(t *testing.T) {
	t.Parallel()
	got, err := cleanPublishedPath("img/./logo.png")
	require.NoError(t, err)
	require.Equal(t, "img/logo.png", got)
	for _, bad := range []string{"/abs.js", "../up.js", "a/../../b", ".env", ""} {
		_, err := cleanPublishedPath(bad)
		require.Error(t, err, bad)
	}
}

func TestTitleFromPage(t *testing.T) {
	t.Parallel()
	require.Equal(t, "A &B", titleFromPage([]byte("<title> A &amp;B </title>"), KindPage))
	require.Equal(t, "Plan", titleFromPage([]byte("intro\n# Plan ##\n"), KindDocument))
	require.Empty(t, titleFromPage([]byte("<p>none</p>"), KindPage))
	require.Equal(t, "Q3 report", titleFromSource("/x/Q3_report.html"))
	require.Empty(t, titleFromSource("/x/index.html"))
}

func TestDeleteRemovesArtifact(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	res, err := st.Publish(t.Context(), PublishInput{Content: strPtr(fullPage)})
	require.NoError(t, err)
	require.NoError(t, st.Delete(res.Meta.ID))
	_, err = st.Get(res.Meta.ID)
	require.ErrorIs(t, err, ErrNotFound)
}
