package artifacts

import (
	"encoding/json"
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
	// The browser gets the viewer key; the tool result never does.
	key, err := svc.Store.ViewerKey()
	require.NoError(t, err)
	require.Equal(t, []string{r.URL + "#k=" + key}, rec.urls)
	require.NotContains(t, r.URL, key)
	require.NotContains(t, r.Text, key)

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
	h, err := Handler(st, ServerOptions{}, "http://127.0.0.1:7480")
	require.NoError(t, err)
	key, err := st.ViewerKey()
	require.NoError(t, err)

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
	signed := map[string]string{"X-Tada-Key": key}

	require.Equal(t, http.StatusOK, do("GET", "127.0.0.1:7480", "/api/artifacts", signed, "").Code)
	require.Equal(t, http.StatusOK, do("GET", "localhost:7480", "/api/ping", nil, "").Code)
	// DNS rebinding: a foreign Host is refused.
	require.Equal(t, http.StatusForbidden, do("GET", "evil.example:7480", "/api/artifacts", signed, "").Code)

	// Writes need the key, the viewer header and JSON.
	pin := "/api/artifacts/" + id + "/pin"
	write := func(hdr map[string]string) int {
		hdr["X-Tada-Key"] = key
		return do("POST", "127.0.0.1:7480", pin, hdr, `{"pinned":true}`).Code
	}
	require.Equal(t, http.StatusForbidden, write(map[string]string{"Content-Type": "application/json"}))
	require.Equal(t, http.StatusUnsupportedMediaType, write(map[string]string{"X-Tada-Viewer": "1", "Content-Type": "text/plain"}))
	require.Equal(t, http.StatusOK, write(map[string]string{"X-Tada-Viewer": "1", "Content-Type": "application/json"}))
	m, err := st.Get(id)
	require.NoError(t, err)
	require.True(t, m.Pinned)
}

func TestServerNeedsViewerKey(t *testing.T) {
	t.Parallel()
	st, id := newTestArtifact(t)
	h, err := Handler(st, ServerOptions{}, "http://127.0.0.1:7480")
	require.NoError(t, err)
	key, err := st.ViewerKey()
	require.NoError(t, err)

	get := func(target, k string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), "GET", target, nil)
		req.Host = "127.0.0.1:7480"
		if k != "" {
			req.Header.Set("X-Tada-Key", k)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	wrong := strings.Repeat("0", 64)
	for _, p := range []string{
		"/api/artifacts",
		"/api/artifacts/" + id,
		"/api/artifacts/" + id + "/comments",
		"/api/events?id=" + id,
		"/api/events?id=" + id + "&k=" + wrong,
	} {
		require.Equal(t, http.StatusUnauthorized, get(p, "").Code, p)
		require.Equal(t, http.StatusUnauthorized, get(p, wrong).Code, p)
	}
	// Writes are refused before they reach the store.
	req := httptest.NewRequestWithContext(t.Context(), "DELETE", "/api/artifacts/"+id, nil)
	req.Host = "127.0.0.1:7480"
	req.Header.Set("X-Tada-Viewer", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	_, err = st.Get(id)
	require.NoError(t, err)

	// The shells hold no data and stay public.
	require.Equal(t, http.StatusOK, get("/", "").Code)
	require.Equal(t, http.StatusOK, get("/a/"+id, "").Code)
	require.Equal(t, http.StatusOK, get("/_tada/viewer.js", "").Code)

	// With the key, the API hands out the page base, and only that base
	// serves the artifact's pages.
	w = get("/api/artifacts/"+id, key)
	require.Equal(t, http.StatusOK, w.Code)
	var out struct {
		Artifact struct {
			PageBase string `json:"page_base"`
		} `json:"artifact"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, "/p/"+pageKey(key, id)+"/"+id, out.Artifact.PageBase)
	require.NotContains(t, out.Artifact.PageBase, key)
	require.Equal(t, http.StatusOK, get(out.Artifact.PageBase+"/1/index.html", "").Code)
	require.Equal(t, http.StatusNotFound, get("/p/"+strings.Repeat("a", 32)+"/"+id+"/1/index.html", "").Code)
	require.NotEqual(t, http.StatusOK, get("/p/"+id+"/1/index.html", "").Code)

	other, err := NewStore(t.TempDir())
	require.NoError(t, err)
	otherKey, err := other.ViewerKey()
	require.NoError(t, err)
	require.NotEqual(t, key, otherKey)
	require.Equal(t, http.StatusNotFound, get("/p/"+pageKey(otherKey, id)+"/"+id+"/1/index.html", "").Code)
}

func TestServerPageIsSandboxed(t *testing.T) {
	t.Parallel()
	st, id := newTestArtifact(t)
	for _, allow := range []bool{false, true} {
		h, err := Handler(st, ServerOptions{AllowCDN: allow}, "http://127.0.0.1:7480")
		require.NoError(t, err)
		page, err := st.PagePath(id, 1)
		require.NoError(t, err)
		req := httptest.NewRequestWithContext(t.Context(), "GET", page, nil)
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
