package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

//go:embed web
var webFS embed.FS

// DefaultPort is where the viewer listens unless configured otherwise.
const DefaultPort = 7480

// cdnScripts are the hosts an artifact may load scripts from when
// ServerOptions.AllowCDN is on. It matches the hosted Artifacts contract.
var cdnScripts = []string{
	"https://cdnjs.cloudflare.com",
	"https://cdn.jsdelivr.net",
	"https://unpkg.com",
	"https://cdn.tailwindcss.com",
	"https://code.jquery.com",
}

// ServerOptions configures the viewer.
type ServerOptions struct {
	// Port to listen on (127.0.0.1 only). Zero means DefaultPort.
	Port int
	// AllowCDN lets pages load scripts from the CDN allow-list and fonts
	// from Google Fonts. Off by default: pages then make no outside
	// requests at all.
	AllowCDN bool
}

// Server is the local artifacts viewer.
type Server struct {
	store *Store
	opts  ServerOptions
	srv   *http.Server
	url   string
}

var (
	runningMu  sync.Mutex
	running    = map[string]string{} // store root → base URL
	owned      = map[string]bool{}   // store roots served by this process
	runningSrv []*Server
)

// Ensure makes sure a viewer for store is reachable and returns its base
// URL (http://127.0.0.1:7480). It reuses a viewer another Ta-Da process
// already runs for the same store, and otherwise starts one in this
// process, trying the next few ports if the configured one is taken.
func Ensure(store *Store, opts ServerOptions) (string, error) {
	runningMu.Lock()
	defer runningMu.Unlock()
	if u, ok := running[store.root]; ok {
		// A viewer started by another process may have exited since.
		if owned[store.root] || pingOK(strings.TrimPrefix(u, "http://"), store) {
			return u, nil
		}
		delete(running, store.root)
	}
	port := opts.Port
	if port <= 0 {
		port = DefaultPort
	}
	var lastErr error
	for p := port; p < port+20; p++ {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p))
		ln, err := new(net.ListenConfig).Listen(context.Background(), "tcp", addr)
		if err != nil {
			lastErr = err
			if pingOK(addr, store) {
				u := "http://" + addr
				running[store.root] = u
				return u, nil
			}
			continue
		}
		s := newServer(store, opts, "http://"+addr)
		go func() {
			if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("Artifacts viewer stopped", "error", err)
			}
		}()
		running[store.root] = s.url
		owned[store.root] = true
		runningSrv = append(runningSrv, s)
		slog.Info("Artifacts viewer listening", "url", s.url)
		return s.url, nil
	}
	return "", fmt.Errorf("start artifacts viewer: %w", lastErr)
}

// Serve runs a viewer in the foreground until ctx ends. Unlike Ensure it
// fails when the port is taken.
func Serve(ctx context.Context, store *Store, opts ServerOptions) (string, <-chan error, error) {
	port := opts.Port
	if port <= 0 {
		port = DefaultPort
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		if pingOK(addr, store) {
			return "", nil, fmt.Errorf("a Ta-Da artifacts viewer is already running at http://%s", addr)
		}
		return "", nil, err
	}
	s := newServer(store, opts, "http://"+addr)
	runningMu.Lock()
	running[store.root] = s.url
	owned[store.root] = true
	runningMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(sctx)
	}()
	return s.url, done, nil
}

// storeTag identifies a store without revealing its path.
func storeTag(store *Store) string {
	sum := sha256.Sum256([]byte(store.root))
	return hex.EncodeToString(sum[:8])
}

func pingOK(addr string, store *Store) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/ping", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var p struct {
		App   string `json:"app"`
		Store string `json:"store"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&p) != nil {
		return false
	}
	return p.App == "tada-artifacts" && p.Store == storeTag(store)
}

// Handler returns the viewer's HTTP handler, for tests.
func Handler(store *Store, opts ServerOptions, baseURL string) http.Handler {
	return newServer(store, opts, baseURL).srv.Handler
}

func newServer(store *Store, opts ServerOptions, baseURL string) *Server {
	s := &Server{store: store, opts: opts, url: baseURL}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.static("web/gallery.html"))
	mux.HandleFunc("GET /a/{id}", s.static("web/viewer.html"))
	mux.HandleFunc("GET /_tada/{file}", s.asset)
	mux.HandleFunc("GET /p/{id}/{ver}/{path...}", s.file)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("file", "favicon.svg")
		s.asset(w, r)
	})
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResp(w, map[string]string{"app": "tada-artifacts", "store": storeTag(store)})
	})
	mux.HandleFunc("GET /api/artifacts", s.apiList)
	mux.HandleFunc("GET /api/artifacts/{id}", s.apiGet)
	mux.HandleFunc("DELETE /api/artifacts/{id}", s.apiDelete)
	mux.HandleFunc("POST /api/artifacts/{id}/pin", s.apiPin)
	mux.HandleFunc("GET /api/artifacts/{id}/comments", s.apiComments)
	mux.HandleFunc("POST /api/artifacts/{id}/comments", s.apiAddComment)
	mux.HandleFunc("POST /api/artifacts/{id}/comments/{cid}/resolve", s.apiResolve)
	mux.HandleFunc("POST /api/artifacts/{id}/db", s.apiDB)
	mux.HandleFunc("POST /api/artifacts/{id}/storage", s.apiStorage)
	mux.HandleFunc("GET /api/events", s.apiEvents)
	s.srv = &http.Server{
		Handler:           s.guard(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// guard blocks DNS rebinding (Host must be loopback) and cross-site
// writes (every write must carry a custom header, which a page on
// another origin, or a sandboxed artifact, can't send without a CORS
// preflight this server never approves).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		switch host {
		case "127.0.0.1", "localhost", "::1":
		default:
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-Tada-Viewer") != "1" {
				apiError(w, http.StatusForbidden, errors.New("missing X-Tada-Viewer header"))
				return
			}
			if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				apiError(w, http.StatusUnsupportedMediaType, errors.New("send JSON"))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) static(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		b, err := webFS.ReadFile(name)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		_, _ = w.Write(b)
	}
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if strings.Contains(name, "/") || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	b, err := webFS.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType(name))
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// pageCSP sandboxes every artifact response so even a page opened
// top-level runs in an opaque origin, and limits where it may load from.
func (s *Server) pageCSP() string {
	scripts := "'self' 'unsafe-inline' 'unsafe-eval' blob: data:"
	styles := "'self' 'unsafe-inline' blob: data:"
	fonts := "'self' data: blob:"
	img := "'self' data: blob:"
	if s.opts.AllowCDN {
		cdn := strings.Join(cdnScripts, " ")
		scripts += " " + cdn
		styles += " " + cdn + " https://fonts.googleapis.com"
		fonts += " " + cdn + " https://fonts.gstatic.com"
		img = "* data: blob:"
	}
	// 'self' inside a sandboxed document still means the server's origin,
	// but name it as well for browsers that disagree.
	self := s.url
	return strings.Join([]string{
		"sandbox allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads",
		"default-src 'self' " + self + " data: blob:",
		"script-src " + scripts + " " + self,
		"style-src " + styles + " " + self,
		"font-src " + fonts + " " + self,
		"img-src " + img + " " + self,
		"media-src 'self' data: blob: " + self,
		"connect-src 'self' data: blob: " + self,
		"worker-src 'self' blob: " + self,
		"frame-src 'self' blob: data: " + self,
		"base-uri 'self'",
	}, "; ")
}

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.store.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	n := 0
	if ver := r.PathValue("ver"); ver != "latest" {
		if n, err = strconv.Atoi(strings.TrimPrefix(ver, "v")); err != nil || n <= 0 {
			http.NotFound(w, r)
			return
		}
	}
	v, ok := m.Version(n)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p := r.PathValue("path")
	if p == "" {
		p = "index.html"
	}
	w.Header().Set("Content-Security-Policy", s.pageCSP())
	w.Header().Set("Cache-Control", "no-cache")

	if m.Kind == KindDocument && p == "index.html" {
		src, err := s.store.ReadFile(id, v.N, "index.md")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		out, err := s.renderDocument(m, v.N, src)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(out)
		return
	}

	b, err := s.store.ReadFile(id, v.N, p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := contentType(p)
	if strings.HasPrefix(ct, "text/html") {
		b = s.inject(m, v.N, b, p == "index.html")
		ct = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(b)
}

// bootScript is the data the runtime needs, placed before runtime.js.
func (s *Server) bootScript(m *Meta, n int) string {
	st, _ := s.store.Storage(m.ID)
	if st == nil {
		st = map[string]string{}
	}
	// json.Marshal escapes <, > and &, so the data can't close the tag.
	b, _ := json.Marshal(map[string]any{
		"id": m.ID, "version": n, "title": m.Title, "kind": m.Kind,
		"storage": st, "cdn": s.opts.AllowCDN,
	})
	return "<script>window.__TADA__=" + string(b) + ";</script>"
}

func webText(name string) string {
	b, err := webFS.ReadFile("web/" + name)
	if err != nil {
		return ""
	}
	// Inlined text must not end the surrounding element early.
	return strings.NewReplacer("</script", `<\/script`, "</style", `<\/style`).Replace(string(b))
}

var (
	reHead    = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
	reHTMLOpn = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)
	reBodyEnd = regexp.MustCompile(`(?i)</body\s*>`)
)

// inject adds the runtime (and, for slide decks, the deck script and
// styles) to an HTML page.
func (s *Server) inject(m *Meta, n int, page []byte, isMain bool) []byte {
	head := s.bootScript(m, n) + "<script>" + webText("runtime.js") + "</script>"
	slides := isMain && m.Kind == KindSlides
	if slides {
		head += "<style>" + webText("slides.css") + "</style>"
	}
	page = insertAfter(page, head)
	if slides {
		tail := []byte("<script>" + webText("slides.js") + "</script>")
		if locs := reBodyEnd.FindAllIndex(page, -1); len(locs) > 0 {
			at := locs[len(locs)-1][0]
			page = append(page[:at:at], append(tail, page[at:]...)...)
		} else {
			page = append(page, tail...)
		}
	}
	return page
}

func insertAfter(page []byte, add string) []byte {
	for _, re := range []*regexp.Regexp{reHead, reHTMLOpn} {
		if loc := re.FindIndex(page); loc != nil {
			at := loc[1]
			out := make([]byte, 0, len(page)+len(add))
			out = append(out, page[:at]...)
			out = append(out, add...)
			return append(out, page[at:]...)
		}
	}
	return append([]byte(add), page...)
}

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.Footnote, extension.Typographer),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

func (s *Server) renderDocument(m *Meta, n int, src []byte) ([]byte, error) {
	var body bytes.Buffer
	if err := markdown.Convert(src, &body); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>" + htmlEscape(m.Title) + "</title>\n")
	b.WriteString(s.bootScript(m, n))
	b.WriteString("<script>" + webText("runtime.js") + "</script>\n")
	b.WriteString("<style>" + webText("doc.css") + "</style>\n</head>\n<body>\n<main class=\"tada-doc\">\n")
	b.Write(body.Bytes())
	b.WriteString("</main>\n</body>\n</html>\n")
	return b.Bytes(), nil
}

// API.

type artifactJSON struct {
	Summary
	Versions []Version `json:"versions"`
}

func (s *Server) apiList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.store.List(ListOptions{Query: r.URL.Query().Get("q"), Limit: limit})
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	if list == nil {
		list = []Summary{}
	}
	writeJSONResp(w, map[string]any{"artifacts": list})
}

func (s *Server) apiGet(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		apiError(w, statusFor(err), err)
		return
	}
	writeJSONResp(w, map[string]any{"artifact": artifactJSON{Summary: m.Summary(), Versions: m.Versions}})
}

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("id")); err != nil {
		apiError(w, statusFor(err), err)
		return
	}
	writeJSONResp(w, map[string]bool{"ok": true})
}

func (s *Server) apiPin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Pinned bool `json:"pinned"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.store.SetPinned(r.Context(), r.PathValue("id"), in.Pinned); err != nil {
		apiError(w, statusFor(err), err)
		return
	}
	writeJSONResp(w, map[string]bool{"ok": true})
}

func (s *Server) apiComments(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Comments(r.PathValue("id"))
	if err != nil {
		apiError(w, statusFor(err), err)
		return
	}
	writeJSONResp(w, map[string]any{"comments": list})
}

func (s *Server) apiAddComment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body    string `json:"body"`
		Parent  string `json:"parent"`
		Anchor  string `json:"anchor"`
		ToAgent bool   `json:"to_agent"`
		Version int    `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := s.store.AddComment(r.Context(), r.PathValue("id"), Comment{
		Body: in.Body, Parent: in.Parent, Anchor: strings.TrimSpace(in.Anchor),
		ToAgent: in.ToAgent, Version: in.Version, Author: AuthorYou,
	})
	if err != nil {
		apiError(w, statusFor(err), err)
		return
	}
	writeJSONResp(w, map[string]any{"comment": c})
}

func (s *Server) apiResolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Resolved bool `json:"resolved"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.store.ResolveComment(r.Context(), r.PathValue("id"), r.PathValue("cid"), in.Resolved); err != nil {
		apiError(w, statusFor(err), err)
		return
	}
	writeJSONResp(w, map[string]bool{"ok": true})
}

func (s *Server) apiDB(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Op         string         `json:"op"`
		Collection string         `json:"collection"`
		DocID      string         `json:"doc_id"`
		Data       map[string]any `json:"data"`
		Query      map[string]any `json:"query"`
		OrderBy    string         `json:"order_by"`
		Desc       bool           `json:"desc"`
		Limit      int            `json:"limit"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	switch in.Op {
	case "get":
		d, err := s.store.GetDoc(id, in.Collection, in.DocID)
		if err != nil {
			apiError(w, statusFor(err), err)
			return
		}
		writeJSONResp(w, map[string]any{"doc": d})
	case "list", "query":
		q := QueryOptions{OrderBy: in.OrderBy, Desc: in.Desc, Limit: in.Limit}
		if in.Op == "query" {
			q.Where = in.Query
		}
		docs, err := s.store.ListDocs(id, in.Collection, q)
		if err != nil {
			apiError(w, statusFor(err), err)
			return
		}
		writeJSONResp(w, map[string]any{"docs": docs})
	case "set", "update", "delete":
		docs, err := s.store.WriteDocs(r.Context(), id, []DocWrite{{Op: in.Op, Collection: in.Collection, DocID: in.DocID, Data: in.Data}})
		if err != nil {
			apiError(w, statusFor(err), err)
			return
		}
		if in.Op == "delete" {
			writeJSONResp(w, map[string]bool{"ok": true})
			return
		}
		writeJSONResp(w, map[string]any{"doc": docs[0]})
	default:
		apiError(w, http.StatusBadRequest, fmt.Errorf("unknown op %q", in.Op))
	}
}

func (s *Server) apiStorage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key   string  `json:"key"`
		Value *string `json:"value"`
		Clear bool    `json:"clear"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.store.SetStorage(r.Context(), r.PathValue("id"), in.Key, in.Value, in.Clear); err != nil {
		apiError(w, statusFor(err), err)
		return
	}
	writeJSONResp(w, map[string]bool{"ok": true})
}

// apiEvents streams changes as Server-Sent Events. It polls the disk
// because publishes can come from any Ta-Da process.
func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	only := r.URL.Query().Get("id")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = io.WriteString(w, "retry: 2000\n\n")
	fl.Flush()

	w2 := newWatcher(s.store, only)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			fl.Flush()
		case <-tick.C:
			for _, ev := range w2.poll() {
				b, _ := json.Marshal(ev.data)
				if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, b); err != nil {
					return
				}
			}
			fl.Flush()
		}
	}
}

type event struct {
	name string
	data map[string]any
}

type artState struct {
	metaStamp, commentStamp, dataStamp time.Time
	version                            int
	pinned                             bool
	collections                        map[string]string
}

// watcher remembers what it last saw so poll can report only changes.
type watcher struct {
	store *Store
	only  string
	seen  map[string]*artState
}

func newWatcher(store *Store, only string) *watcher {
	w := &watcher{store: store, only: only, seen: map[string]*artState{}}
	w.poll()
	return w
}

func (w *watcher) ids() []string {
	if w.only != "" {
		return []string{w.only}
	}
	entries, _ := os.ReadDir(w.store.root)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && idRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

func (w *watcher) poll() []event {
	var evs []event
	present := map[string]bool{}
	for _, id := range w.ids() {
		ms, cs, ds := w.store.Stamp(id)
		if ms.IsZero() {
			continue
		}
		present[id] = true
		st := w.seen[id]
		first := st == nil
		if first {
			st = &artState{}
			w.seen[id] = st
		}
		if !ms.Equal(st.metaStamp) {
			st.metaStamp = ms
			if m, err := w.store.Get(id); err == nil {
				if !first && m.Latest() != st.version {
					evs = append(evs, event{"version", map[string]any{"id": id, "version": m.Latest()}})
				} else if !first && m.Pinned != st.pinned {
					evs = append(evs, event{"meta", map[string]any{"id": id}})
				}
				st.version, st.pinned = m.Latest(), m.Pinned
			}
		}
		if w.only == "" {
			continue
		}
		if !cs.Equal(st.commentStamp) {
			if !first {
				evs = append(evs, event{"comment", map[string]any{"id": id}})
			}
			st.commentStamp = cs
		}
		if !ds.Equal(st.dataStamp) {
			st.dataStamp = ds
			cols := w.collectionHashes(id)
			if !first {
				for name, h := range cols {
					if st.collections[name] != h {
						evs = append(evs, event{"db", map[string]any{"id": id, "collection": name}})
					}
				}
				for name := range st.collections {
					if _, ok := cols[name]; !ok {
						evs = append(evs, event{"db", map[string]any{"id": id, "collection": name}})
					}
				}
			}
			st.collections = cols
		}
	}
	for id := range w.seen {
		if !present[id] {
			delete(w.seen, id)
			evs = append(evs, event{"deleted", map[string]any{"id": id}})
		}
	}
	return evs
}

func (w *watcher) collectionHashes(id string) map[string]string {
	db, err := w.store.loadDB(id)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(db))
	for name, docs := range db {
		b, _ := json.Marshal(docs)
		sum := sha256.Sum256(b)
		out[name] = hex.EncodeToString(sum[:8])
	}
	return out
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		apiError(w, http.StatusBadRequest, fmt.Errorf("bad JSON: %w", err))
		return false
	}
	return true
}

func statusFor(err error) int {
	if errors.Is(err, ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

func apiError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// ViewerPath is the path of an artifact's viewer page.
func ViewerPath(id string) string { return "/a/" + id }

// PagePath is the path of a version's page (0 = latest).
func PagePath(id string, n int) string {
	v := "latest"
	if n > 0 {
		v = strconv.Itoa(n)
	}
	return path.Join("/p", id, v, "index.html")
}
