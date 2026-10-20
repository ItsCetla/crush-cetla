// Package artifacts stores and serves pages the agent publishes: single-file
// or multi-file HTML pages, Markdown documents and slide decks, with
// versions, a small per-artifact database, per-viewer storage and comments.
// Everything lives on disk under one directory shared by every Ta-Da
// process, and a local viewer (see server.go) shows it in the browser.
package artifacts

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/lock"
)

// Kind is the type of an artifact.
type Kind string

const (
	// KindPage is an HTML page shown as written.
	KindPage Kind = "page"
	// KindDocument is Markdown rendered into a reading layout.
	KindDocument Kind = "document"
	// KindSlides is HTML whose <section> elements become slides.
	KindSlides Kind = "slides"
)

// Limits mirror the hosted Artifacts product.
const (
	MaxPageBytes    = 16 << 20
	MaxFileBytes    = 15 << 20
	MaxVersionBytes = 256 << 20
	MaxFiles        = 511
	maxDocBytes     = 1 << 20
	maxStorageBytes = 5 << 20
)

// ErrNotFound is returned when an artifact does not exist.
var ErrNotFound = errors.New("artifact not found")

// FileInfo describes one published file of a version.
type FileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Type string `json:"type"`
}

// Version is one published state of an artifact.
type Version struct {
	N     int        `json:"n"`
	Time  time.Time  `json:"time"`
	Label string     `json:"label,omitempty"`
	Files []FileInfo `json:"files"`
}

// Meta is everything known about an artifact except its file contents.
type Meta struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	Kind        Kind      `json:"kind"`
	Pinned      bool      `json:"pinned"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	Project     string    `json:"project"`
	SessionID   string    `json:"session_id,omitempty"`
	Versions    []Version `json:"versions"`
}

// Latest returns the newest version number.
func (m *Meta) Latest() int {
	if len(m.Versions) == 0 {
		return 0
	}
	return m.Versions[len(m.Versions)-1].N
}

// Version returns version n, or the latest when n <= 0.
func (m *Meta) Version(n int) (Version, bool) {
	if n <= 0 {
		n = m.Latest()
	}
	for _, v := range m.Versions {
		if v.N == n {
			return v, true
		}
	}
	return Version{}, false
}

// Summary is the list view of an artifact.
type Summary struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	Kind        Kind      `json:"kind"`
	Version     int       `json:"version"`
	Pinned      bool      `json:"pinned"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	Project     string    `json:"project"`
	URL         string    `json:"url"`
}

// Summary returns the list view of m.
func (m *Meta) Summary() Summary {
	return Summary{
		ID: m.ID, Title: m.Title, Description: m.Description, Icon: m.Icon,
		Kind: m.Kind, Version: m.Latest(), Pinned: m.Pinned,
		Created: m.Created, Updated: m.Updated, Project: m.Project,
		URL: "/a/" + m.ID,
	}
}

// Store keeps artifacts on disk. It is safe for concurrent use, including
// from several processes: every write takes a per-artifact file lock.
type Store struct {
	root string

	keyMu sync.Mutex
	key   string
}

// DefaultRoot is where artifacts live unless TADA_ARTIFACTS_DIR says
// otherwise: next to Ta-Da's global data (~/.local/share/tada/artifacts).
func DefaultRoot() string {
	if d := os.Getenv("TADA_ARTIFACTS_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("TADA_GLOBAL_DATA"); d != "" {
		return filepath.Join(d, "artifacts")
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "tada", "artifacts")
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" && filepath.Separator == '\\' {
		return filepath.Join(d, "tada", "artifacts")
	}
	return filepath.Join(home.Dir(), ".local", "share", "tada", "artifacts")
}

// NewStore opens (and creates) a store rooted at dir.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create artifacts dir: %w", err)
	}
	return &Store{root: dir}, nil
}

// Root returns the store directory.
func (s *Store) Root() string { return s.root }

var idRe = regexp.MustCompile(`^[a-z0-9]{6,32}$`)

var pageKeyRe = regexp.MustCompile(`^[0-9a-f]{32}/`)

// ParseID accepts a bare id or any viewer URL that contains one
// (http://127.0.0.1:7480/a/<id>, /p/<page key>/<id>/3/index.html, ...).
func ParseID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if idRe.MatchString(s) {
		return s, nil
	}
	if i := strings.Index(s, "/a/"); i >= 0 {
		return ParseID(firstSegment(s[i+3:]))
	}
	if i := strings.Index(s, "/p/"); i >= 0 {
		rest := s[i+3:]
		// Page links put a 32-character key before the id. New ids are
		// 10 characters, so the two can't be confused.
		if loc := pageKeyRe.FindStringIndex(rest); loc != nil {
			rest = rest[loc[1]:]
		}
		return ParseID(firstSegment(rest))
	}
	return "", fmt.Errorf("%q is not an artifact id or link", s)
}

func firstSegment(s string) string {
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		return s[:i]
	}
	return s
}

func newID() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func (s *Store) dir(id string) string { return filepath.Join(s.root, id) }

func (s *Store) versionDir(id string, n int) string {
	return filepath.Join(s.dir(id), "v", fmt.Sprint(n))
}

func (s *Store) lock(ctx context.Context, id string) (func(), error) {
	if err := os.MkdirAll(s.dir(id), 0o700); err != nil {
		return nil, err
	}
	return lock.File(ctx, filepath.Join(s.dir(id), ".lock"))
}

// Get loads one artifact's metadata.
func (s *Store) Get(id string) (*Meta, error) {
	if !idRe.MatchString(id) {
		return nil, ErrNotFound
	}
	var m Meta
	if err := readJSON(filepath.Join(s.dir(id), "meta.json"), &m); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// ListOptions filters List.
type ListOptions struct {
	Query   string
	Project string
	Pinned  bool
	Limit   int
}

// List returns artifacts, most recently updated first (pinned ones first
// when opts.Pinned is set, only pinned ones).
func (s *Store) List(opts ListOptions) ([]Summary, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(opts.Query))
	var out []Summary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := s.Get(e.Name())
		if err != nil || len(m.Versions) == 0 {
			continue
		}
		if opts.Pinned && !m.Pinned {
			continue
		}
		if opts.Project != "" && m.Project != opts.Project {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(m.Title+" "+m.Description+" "+m.ID), q) {
			continue
		}
		out = append(out, m.Summary())
	}
	slices.SortFunc(out, func(a, b Summary) int { return b.Updated.Compare(a.Updated) })
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

// PublishInput describes one publish. Exactly one of Content and
// SourcePath sets the page; Files adds, replaces (non-nil source) or
// removes (nil source) supporting files. Leaving the page unset on an
// update keeps the previous page.
type PublishInput struct {
	ID          string
	Title       string
	Description string
	Icon        string
	Kind        Kind
	Label       string
	Content     *string
	SourcePath  string
	Files       map[string]*string
	BaseVersion int
	Project     string
	SessionID   string
}

// PublishResult reports what a publish did.
type PublishResult struct {
	Meta    *Meta
	Created bool
	Version Version
	Wrapped bool
	// NoTitle is set when a wrapped page had no <title> of its own, so
	// the one in its skeleton is only a placeholder.
	NoTitle bool
}

// Publish creates an artifact (no ID) or adds a new version to one.
func (s *Store) Publish(ctx context.Context, in PublishInput) (*PublishResult, error) {
	if in.ID != "" {
		return s.publish(ctx, in, in.ID, false)
	}
	id := newID()
	res, err := s.publish(ctx, in, id, true)
	if err != nil {
		// Don't leave an empty artifact directory behind.
		_ = os.RemoveAll(s.dir(id))
	}
	return res, err
}

func (s *Store) publish(ctx context.Context, in PublishInput, id string, created bool) (*PublishResult, error) {
	if !created {
		var err error
		if id, err = ParseID(id); err != nil {
			return nil, err
		}
	}

	unlock, err := s.lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()

	var m *Meta
	if created {
		kind := cmp.Or(in.Kind, KindPage)
		if in.SourcePath != "" && strings.EqualFold(filepath.Ext(in.SourcePath), ".md") && in.Kind == "" {
			kind = KindDocument
		}
		if !validKind(kind) {
			return nil, fmt.Errorf("unknown kind %q: use page, document or slides", kind)
		}
		if in.Content == nil && in.SourcePath == "" {
			return nil, errors.New("a new artifact needs a page: pass file_path or content")
		}
		now := time.Now().UTC()
		m = &Meta{
			ID: id, Kind: kind, Created: now, Project: in.Project, SessionID: in.SessionID,
			Title: cmp.Or(strings.TrimSpace(in.Title), titleFromSource(in.SourcePath), "Untitled"),
		}
	} else {
		if m, err = s.Get(id); err != nil {
			return nil, err
		}
		if in.Kind != "" && in.Kind != m.Kind {
			return nil, fmt.Errorf("artifact %s is a %s; its kind can't change", id, m.Kind)
		}
		if in.BaseVersion > 0 && in.BaseVersion != m.Latest() {
			return nil, fmt.Errorf("conflict: artifact %s is at v%d, not v%d. Read it again and merge your change onto v%d", id, m.Latest(), in.BaseVersion, m.Latest())
		}
		if t := strings.TrimSpace(in.Title); t != "" {
			m.Title = t
		}
	}
	if in.Description != "" {
		m.Description = strings.TrimSpace(in.Description)
	}
	if in.Icon != "" {
		m.Icon = strings.ToLower(strings.TrimSpace(in.Icon))
	}

	// Start from the previous version's files.
	files := map[string][]byte{}
	if prev := m.Latest(); prev > 0 {
		pv, _ := m.Version(prev)
		for _, f := range pv.Files {
			b, err := os.ReadFile(filepath.Join(s.versionDir(id, prev), filepath.FromSlash(f.Path)))
			if err != nil {
				return nil, fmt.Errorf("read previous version: %w", err)
			}
			files[f.Path] = b
		}
	}

	pageName := m.pageName()
	var wrapped, noTitle bool
	switch {
	case in.Content != nil:
		files[pageName] = []byte(*in.Content)
	case in.SourcePath != "":
		b, err := readLimited(in.SourcePath, MaxPageBytes)
		if err != nil {
			return nil, err
		}
		files[pageName] = b
	}
	if page, ok := files[pageName]; ok && created && strings.TrimSpace(in.Title) == "" {
		if t := titleFromPage(page, m.Kind); t != "" {
			m.Title = t
		}
	}
	if page, ok := files[pageName]; ok && m.Kind != KindDocument {
		if w, did := wrapSkeleton(page, m.Title); did {
			files[pageName], wrapped = w, true
			noTitle = !rePageTitle.Match(page)
		}
	}
	if _, ok := files[pageName]; !ok {
		return nil, fmt.Errorf("artifact has no %s", pageName)
	}
	if len(files[pageName]) > MaxPageBytes {
		return nil, fmt.Errorf("page is %d bytes; the limit is 16MB", len(files[pageName]))
	}

	for pub, src := range in.Files {
		clean, err := cleanPublishedPath(pub)
		if err != nil {
			return nil, err
		}
		if clean == pageName {
			return nil, fmt.Errorf("%s is the page itself; pass it as file_path or content", pageName)
		}
		if src == nil {
			delete(files, clean)
			continue
		}
		b, err := readLimited(*src, MaxFileBytes)
		if err != nil {
			return nil, err
		}
		files[clean] = b
	}
	if len(files) > MaxFiles {
		return nil, fmt.Errorf("%d files; a version may hold at most %d", len(files), MaxFiles)
	}

	// Write the new version.
	n := m.Latest() + 1
	vdir := s.versionDir(id, n)
	_ = os.RemoveAll(vdir)
	var total int64
	var infos []FileInfo
	for p, b := range files {
		total += int64(len(b))
		if total > MaxVersionBytes {
			return nil, errors.New("version is larger than 256MB")
		}
		dst := filepath.Join(vdir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return nil, err
		}
		infos = append(infos, FileInfo{Path: p, Size: int64(len(b)), Type: contentType(p)})
	}
	slices.SortFunc(infos, func(a, b FileInfo) int {
		if a.Path == pageName {
			return -1
		}
		if b.Path == pageName {
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	now := time.Now().UTC()
	v := Version{N: n, Time: now, Label: strings.TrimSpace(in.Label), Files: infos}
	m.Versions = append(m.Versions, v)
	m.Updated = now
	if err := writeJSON(filepath.Join(s.dir(id), "meta.json"), m); err != nil {
		return nil, err
	}
	return &PublishResult{Meta: m, Created: created, Version: v, Wrapped: wrapped, NoTitle: noTitle}, nil
}

// pageName is the file that holds the page for m's kind.
func (m *Meta) pageName() string {
	if m.Kind == KindDocument {
		return "index.md"
	}
	return "index.html"
}

// PageName is the file that holds the page for m's kind.
func (m *Meta) PageName() string { return m.pageName() }

// ReadFile returns one file of version n (latest when n <= 0).
func (s *Store) ReadFile(id string, n int, p string) ([]byte, error) {
	m, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	v, ok := m.Version(n)
	if !ok {
		return nil, fmt.Errorf("artifact %s has no v%d", id, n)
	}
	clean, err := cleanPublishedPath(p)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(v.Files, func(f FileInfo) bool { return f.Path == clean }) {
		return nil, fs.ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.versionDir(id, v.N), filepath.FromSlash(clean)))
}

// SetPinned pins or unpins an artifact.
func (s *Store) SetPinned(ctx context.Context, id string, pinned bool) error {
	return s.update(ctx, id, func(m *Meta) { m.Pinned = pinned })
}

func (s *Store) update(ctx context.Context, id string, fn func(*Meta)) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := s.Get(id)
	if err != nil {
		return err
	}
	fn(m)
	return writeJSON(filepath.Join(s.dir(id), "meta.json"), m)
}

// Delete removes an artifact and everything it holds.
func (s *Store) Delete(id string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	// Move aside first so readers never see half a directory.
	tmp := filepath.Join(s.root, ".trash-"+id+"-"+newID())
	if err := os.Rename(s.dir(id), tmp); err != nil {
		return err
	}
	return os.RemoveAll(tmp)
}

// Stamp returns a value that changes whenever anything about an artifact
// changes. The viewer uses it to push live updates.
func (s *Store) Stamp(id string) (meta, comments, data time.Time) {
	st := func(name string) time.Time {
		fi, err := os.Stat(filepath.Join(s.dir(id), name))
		if err != nil {
			return time.Time{}
		}
		return fi.ModTime()
	}
	return st("meta.json"), st("comments.json"), st("data.json")
}

func validKind(k Kind) bool {
	return k == KindPage || k == KindDocument || k == KindSlides
}

var (
	rePageTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reMDHeading = regexp.MustCompile(`(?m)^#[ \t]+(.+?)[ \t#]*$`)
)

// titleFromPage reads a title from the page itself: its <title>, or the
// first top-level heading of a Markdown document.
func titleFromPage(page []byte, kind Kind) string {
	var t string
	if kind == KindDocument {
		if m := reMDHeading.FindSubmatch(page); m != nil {
			t = string(m[1])
		}
	} else if m := rePageTitle.FindSubmatch(page); m != nil {
		t = html.UnescapeString(string(m[1]))
	}
	t = strings.Join(strings.Fields(t), " ")
	if r := []rune(t); len(r) > 80 {
		t = strings.TrimSpace(string(r[:80]))
	}
	return t
}

func titleFromSource(p string) string {
	if p == "" {
		return ""
	}
	base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	if base == "index" || base == "" {
		return ""
	}
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	return strings.ToUpper(base[:1]) + base[1:]
}

func cleanPublishedPath(p string) (string, error) {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	clean := path.Clean("/" + p)[1:]
	if clean == "" || clean == "." || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return "", fmt.Errorf("published path %q must be relative, with no leading slash or \"..\" segment", p)
	}
	if strings.HasPrefix(path.Base(clean), ".") {
		return "", fmt.Errorf("published path %q can't be a dotfile", p)
	}
	return clean, nil
}

func readLimited(p string, limit int64) ([]byte, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory", p)
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s is %d bytes; the limit is %d", p, fi.Size(), limit)
	}
	return os.ReadFile(p)
}

func contentType(p string) string {
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		return t
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".mjs":
		return "text/javascript; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	}
	return "application/octet-stream"
}

var (
	reDoctype = regexp.MustCompile(`(?i)^\s*(<!--.*?-->\s*)*<!doctype`)
	reHTMLTag = regexp.MustCompile(`(?i)<html[\s>]`)
)

// wrapSkeleton gives a fragment a document skeleton (doctype, charset,
// viewport, title) so it never renders in quirks mode.
func wrapSkeleton(page []byte, title string) ([]byte, bool) {
	if reDoctype.Match(page) {
		return page, false
	}
	if reHTMLTag.Match(page) {
		return append([]byte("<!doctype html>\n"), page...), true
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	if !rePageTitle.Match(page) {
		b.WriteString("<title>" + htmlEscape(title) + "</title>\n")
	}
	b.WriteString("</head>\n<body>\n")
	b.Write(page)
	b.WriteString("\n</body>\n</html>\n")
	return []byte(b.String()), true
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func readJSON(p string, v any) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSON writes atomically (temp file + rename).
func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp-" + newID()
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

func homeDir() string { return home.Dir() }
