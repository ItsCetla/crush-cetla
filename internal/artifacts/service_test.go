package artifacts

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// freePort returns a port that was free a moment ago. Ensure moves on to
// the next one if another test grabbed it in between.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type openRecorder struct {
	mu   sync.Mutex
	urls []string
}

func (o *openRecorder) open(u string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.urls = append(o.urls, u)
	return nil
}

func newTestService(t *testing.T, allowCDN bool) (*Service, *openRecorder) {
	t.Helper()
	rec := &openRecorder{}
	return &Service{
		Store:        newTestStore(t),
		Server:       ServerOptions{Port: freePort(t), AllowCDN: allowCDN},
		OpenOnCreate: true,
		Open:         rec.open,
	}, rec
}

func TestServicePublishReadAndOpen(t *testing.T) {
	t.Parallel()
	svc, rec := newTestService(t, false)
	ctx := t.Context()
	env := CallEnv{WorkingDir: t.TempDir()}

	r, err := svc.Artifact(ctx, env, ArtifactParams{Content: strPtr(fullPage), Description: "Revenue by month."})
	require.NoError(t, err)
	require.True(t, r.Created)
	require.True(t, r.Opened)
	require.Zero(t, r.Warnings)
	require.Contains(t, r.Text, "Published \"Q3 Revenue\"")
	require.True(t, strings.HasSuffix(r.URL, "/a/"+r.ID))
	require.Equal(t, []string{r.URL}, rec.urls)

	// Only a new artifact opens the browser.
	r2, err := svc.Artifact(ctx, env, ArtifactParams{ID: r.URL, Content: strPtr(fullPage), BaseVersion: 1})
	require.NoError(t, err)
	require.False(t, r2.Opened)
	require.Equal(t, 2, r2.Version)
	require.Len(t, rec.urls, 1)

	_, err = svc.Artifact(ctx, env, ArtifactParams{ID: r.ID, Content: strPtr(fullPage), BaseVersion: 1})
	require.ErrorContains(t, err, "conflict")

	read, err := svc.Artifact(ctx, env, ArtifactParams{Action: "read", ID: r.ID})
	require.NoError(t, err)
	require.Contains(t, read.Text, "v2 of 2")
	require.Contains(t, read.Text, "Revenue by month.")
	require.Contains(t, read.Text, "--- index.html ---\n"+fullPage)

	_, err = svc.Artifact(ctx, env, ArtifactParams{Action: "read", ID: r.ID, Version: 9})
	require.Error(t, err)

	list, err := svc.Artifact(ctx, env, ArtifactParams{Action: "list"})
	require.NoError(t, err)
	require.Contains(t, list.Text, r.ID)

	_, err = svc.Artifact(ctx, env, ArtifactParams{Action: "explode"})
	require.ErrorContains(t, err, "unknown action")
}

func TestServicePublishWarnings(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t, false)
	ctx := t.Context()

	r, err := svc.Artifact(ctx, CallEnv{}, ArtifactParams{Content: strPtr("<p>no title</p>")})
	require.NoError(t, err)
	require.Contains(t, r.Text, "no doctype")
	require.Contains(t, r.Text, "Warning: the page has no <title>")
	require.Equal(t, 1, r.Warnings)

	r, err = svc.Artifact(ctx, CallEnv{}, ArtifactParams{Kind: "slides", Content: strPtr(fullPage)})
	require.NoError(t, err)
	require.Contains(t, r.Text, "<section>")

	_, err = svc.Artifact(ctx, CallEnv{}, ArtifactParams{Content: strPtr(fullPage), FilePath: "x.html"})
	require.ErrorContains(t, err, "not both")
}

func TestServiceLint(t *testing.T) {
	t.Parallel()
	page := []byte(`<!doctype html><title>Chart</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.1"></script>
<script src="https://evil.example/x.js"></script>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter">`)

	offline := &Service{}
	warns := offline.lint(KindPage, page)
	require.Len(t, warns, 1)
	require.Contains(t, warns[0], "no outside requests")
	require.Contains(t, warns[0], "cdn.jsdelivr.net")
	require.Contains(t, warns[0], "evil.example")
	require.Contains(t, warns[0], "fonts.googleapis.com")

	online := &Service{Server: ServerOptions{AllowCDN: true}}
	warns = online.lint(KindPage, page)
	require.Len(t, warns, 1)
	require.Contains(t, warns[0], "evil.example")
	require.True(t, strings.HasPrefix(warns[0], "scripts from evil.example are blocked"), warns[0])

	require.Nil(t, offline.lint(KindDocument, []byte("# Hi")))
}

func TestServiceGuideFollowsCDNSetting(t *testing.T) {
	t.Parallel()
	off := (&Service{}).Guide()
	require.Contains(t, off, "No outside requests")
	require.NotContains(t, off, "cdnjs.cloudflare.com")
	on := (&Service{Server: ServerOptions{AllowCDN: true}}).Guide()
	require.Contains(t, on, "cdnjs.cloudflare.com")
	require.NotContains(t, on, "No outside requests")
}

func TestCommentsNote(t *testing.T) {
	t.Parallel()
	require.Equal(t, "There is 1 unresolved comment for you on this artifact; read it with artifact_comments.\n", commentsNote(1, " on this artifact"))
	require.Equal(t, "There are 3 unresolved comments for you; read them with artifact_comments.\n", commentsNote(3, ""))
}

func TestServiceDataAndComments(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t, false)
	svc.OpenOnCreate = false
	ctx := t.Context()
	r, err := svc.Artifact(ctx, CallEnv{}, ArtifactParams{Content: strPtr(fullPage)})
	require.NoError(t, err)

	out, err := svc.Data(ctx, DataParams{ID: r.ID, Action: "set", Collection: "rows", DocID: "a", Data: map[string]any{"n": 1}})
	require.NoError(t, err)
	require.Contains(t, out, `"a"`)
	out, err = svc.Data(ctx, DataParams{ID: r.ID, Action: "collections"})
	require.NoError(t, err)
	require.Contains(t, out, "rows")

	_, err = svc.Store.AddComment(ctx, r.ID, Comment{Body: "Bigger font", ToAgent: true})
	require.NoError(t, err)
	read, err := svc.Artifact(ctx, CallEnv{}, ArtifactParams{Action: "read", ID: r.ID})
	require.NoError(t, err)
	require.Contains(t, read.Text, "There is 1 unresolved comment for you; read it")
	require.Contains(t, read.Text, "Database: rows (1 docs)")

	list, err := svc.Comments(ctx, CommentsParams{ID: r.ID})
	require.NoError(t, err)
	require.Contains(t, list, "Bigger font")
	comments, err := svc.Store.Comments(r.ID)
	require.NoError(t, err)
	_, err = svc.Comments(ctx, CommentsParams{ID: r.ID, Action: "reply", CommentID: comments[0].ID, Body: "Done."})
	require.NoError(t, err)
	_, err = svc.Comments(ctx, CommentsParams{ID: r.ID, Action: "resolve", CommentID: comments[0].ID})
	require.NoError(t, err)
	require.Zero(t, svc.openAgentComments(r.ID))
}

func TestServerGuard(t *testing.T) {
	t.Parallel()
	st, id := newTestArtifact(t)
	h := Handler(st, ServerOptions{}, "http://127.0.0.1:7480")

	do := func(method, host, target string, hdr map[string]string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
		req.Host = host
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusOK, do("GET", "127.0.0.1:7480", "/api/artifacts", nil, "").Code)
	require.Equal(t, http.StatusOK, do("GET", "localhost:7480", "/api/ping", nil, "").Code)
	// DNS rebinding: a foreign Host is refused.
	require.Equal(t, http.StatusForbidden, do("GET", "evil.example:7480", "/api/artifacts", nil, "").Code)

	// Writes need the viewer header and JSON.
	pin := "/api/artifacts/" + id + "/pin"
	require.Equal(t, http.StatusForbidden, do("POST", "127.0.0.1:7480", pin, map[string]string{"Content-Type": "application/json"}, `{"pinned":true}`).Code)
	require.Equal(t, http.StatusUnsupportedMediaType, do("POST", "127.0.0.1:7480", pin, map[string]string{"X-Tada-Viewer": "1", "Content-Type": "text/plain"}, `{"pinned":true}`).Code)
	require.Equal(t, http.StatusOK, do("POST", "127.0.0.1:7480", pin, map[string]string{"X-Tada-Viewer": "1", "Content-Type": "application/json"}, `{"pinned":true}`).Code)
	m, err := st.Get(id)
	require.NoError(t, err)
	require.True(t, m.Pinned)
}

func TestServerPageIsSandboxed(t *testing.T) {
	t.Parallel()
	st, id := newTestArtifact(t)
	for _, allow := range []bool{false, true} {
		h := Handler(st, ServerOptions{AllowCDN: allow}, "http://127.0.0.1:7480")
		req := httptest.NewRequestWithContext(t.Context(), "GET", PagePath(id, 1), nil)
		req.Host = "127.0.0.1:7480"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		csp := w.Header().Get("Content-Security-Policy")
		require.True(t, strings.HasPrefix(csp, "sandbox allow-scripts"), csp)
		require.Equal(t, allow, strings.Contains(csp, "cdnjs.cloudflare.com"))
		require.Equal(t, allow, strings.Contains(csp, "fonts.gstatic.com"))
		require.Contains(t, w.Body.String(), "Q3 Revenue")
	}
}
