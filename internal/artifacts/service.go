package artifacts

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/pkg/browser"
)

//go:embed guide.md.tpl
var guideTemplate string

// Service runs the artifact actions shared by Ta-Da's native tools and
// the MCP server.
type Service struct {
	Store  *Store
	Server ServerOptions
	// OpenOnCreate opens a new artifact in the browser when it's first
	// published.
	OpenOnCreate bool
	// Open opens a URL; nil means the system browser.
	Open func(url string) error
}

// CallEnv is per-call context.
type CallEnv struct {
	// WorkingDir resolves relative file paths.
	WorkingDir string
	// Project is recorded on new artifacts (usually the working dir).
	Project   string
	SessionID string
}

var (
	defaultOnce sync.Once
	defaultSvc  *Service
	defaultErr  error
)

// Default returns the process-wide service over DefaultRoot. Call
// Configure first to change its options.
func Default() (*Service, error) {
	defaultOnce.Do(func() {
		var st *Store
		st, defaultErr = NewStore(DefaultRoot())
		if defaultErr == nil {
			defaultSvc = &Service{Store: st}
		}
	})
	return defaultSvc, defaultErr
}

// BaseURL makes sure the viewer runs and returns its base URL.
func (s *Service) BaseURL() (string, error) { return Ensure(s.Store, s.Server) }

func (s *Service) open(u string) error {
	if s.Open != nil {
		return s.Open(u)
	}
	return OpenBrowser(u)
}

var browserMu sync.Mutex

// OpenBrowser opens u in the system browser without letting the opener
// write to this process's stdout (which may be an MCP stream or a TUI).
func OpenBrowser(u string) error {
	browserMu.Lock()
	defer browserMu.Unlock()
	browser.Stdout, browser.Stderr = io.Discard, io.Discard
	return browser.OpenURL(u)
}

// ArtifactParams are the inputs of the artifact tool.
type ArtifactParams struct {
	Action      string             `json:"action,omitempty" description:"One of publish (default), list, read, delete, open, pin, unpin, guide." jsonschema:"One of publish (default), list, read, delete, open, pin, unpin, guide."`
	ID          string             `json:"id,omitempty" description:"The artifact's id or viewer link. Omit on publish to create a new artifact; pass it to publish a new version of an existing one." jsonschema:"The artifact's id or viewer link. Omit on publish to create a new artifact; pass it to publish a new version of an existing one."`
	FilePath    string             `json:"file_path,omitempty" description:"publish: a local .html file (or .md for a document) holding the page. Preferred over content for anything long." jsonschema:"publish: a local .html file (or .md for a document) holding the page. Preferred over content for anything long."`
	Content     *string            `json:"content,omitempty" description:"publish: the page itself, inline, instead of file_path." jsonschema:"publish: the page itself, inline, instead of file_path."`
	Files       map[string]*string `json:"files,omitempty" description:"publish: supporting files as {\"published/path\": \"local/source/path\"}; null removes one. Files not named are kept from the previous version." jsonschema:"publish: supporting files as {published/path: local/source/path}; null removes one. Files not named are kept from the previous version."`
	Kind        string             `json:"kind,omitempty" description:"publish, new artifacts only: page (default), document (Markdown, rendered for reading) or slides (each <section> is a slide)." jsonschema:"publish, new artifacts only: page (default), document (Markdown rendered for reading) or slides (each <section> is a slide)."`
	Title       string             `json:"title,omitempty" description:"publish: a short name of two to four words." jsonschema:"publish: a short name of two to four words."`
	Description string             `json:"description,omitempty" description:"publish: one sentence for the gallery card." jsonschema:"publish: one sentence for the gallery card."`
	Icon        string             `json:"icon,omitempty" description:"publish: one generic word for the icon, such as chart, calendar, recipe, code, map, game, doc, slides." jsonschema:"publish: one generic word for the icon, such as chart, calendar, recipe, code, map, game, doc, slides."`
	Label       string             `json:"label,omitempty" description:"publish: a short name for this version, such as 'Dark mode'." jsonschema:"publish: a short name for this version."`
	BaseVersion int                `json:"base_version,omitempty" description:"publish: the version your edit is based on; the publish is refused if someone published a newer one meanwhile." jsonschema:"publish: the version your edit is based on; refused if a newer one was published meanwhile."`
	Path        string             `json:"path,omitempty" description:"read: one published file to return instead of the page." jsonschema:"read: one published file to return instead of the page."`
	Version     int                `json:"version,omitempty" description:"read/open: a version number; latest when omitted." jsonschema:"read/open: a version number; latest when omitted."`
	Query       string             `json:"query,omitempty" description:"list: only artifacts whose title or description contains this." jsonschema:"list: only artifacts whose title or description contains this."`
	Limit       int                `json:"limit,omitempty" description:"list: the maximum number to return (default 25)." jsonschema:"list: the maximum number to return (default 25)."`
}

// Result is what an action did, for rendering.
type Result struct {
	Text     string `json:"-"`
	Action   string `json:"action"`
	ID       string `json:"id,omitempty"`
	Title    string `json:"title,omitempty"`
	Kind     Kind   `json:"kind,omitempty"`
	Version  int    `json:"version,omitempty"`
	URL      string `json:"url,omitempty"`
	Created  bool   `json:"created,omitempty"`
	Opened   bool   `json:"opened,omitempty"`
	Warnings int    `json:"warnings,omitempty"`
}

// Artifact runs one artifact action.
func (s *Service) Artifact(ctx context.Context, env CallEnv, p ArtifactParams) (*Result, error) {
	action := strings.ToLower(cmp.Or(strings.TrimSpace(p.Action), "publish"))
	switch action {
	case "publish":
		return s.publish(ctx, env, p)
	case "list":
		return s.list(p)
	case "read":
		return s.read(p)
	case "delete":
		id, err := ParseID(p.ID)
		if err != nil {
			return nil, err
		}
		m, err := s.Store.Get(id)
		if err != nil {
			return nil, err
		}
		if err := s.Store.Delete(id); err != nil {
			return nil, err
		}
		return &Result{Action: action, ID: id, Title: m.Title, Text: fmt.Sprintf("Deleted %q (%s). The link no longer works.", m.Title, id)}, nil
	case "open":
		id, err := ParseID(p.ID)
		if err != nil {
			return nil, err
		}
		m, err := s.Store.Get(id)
		if err != nil {
			return nil, err
		}
		u, err := s.viewerURL(id, p.Version)
		if err != nil {
			return nil, err
		}
		if err := s.open(u); err != nil {
			return &Result{Action: action, ID: id, Title: m.Title, URL: u, Text: "Couldn't open a browser (" + err.Error() + "). The link is " + u}, nil
		}
		return &Result{Action: action, ID: id, Title: m.Title, URL: u, Opened: true, Text: "Opened " + u + " in the browser."}, nil
	case "pin", "unpin":
		id, err := ParseID(p.ID)
		if err != nil {
			return nil, err
		}
		if err := s.Store.SetPinned(ctx, id, action == "pin"); err != nil {
			return nil, err
		}
		return &Result{Action: action, ID: id, Text: fmt.Sprintf("%sned %s.", strings.ToUpper(action[:1])+action[1:], id)}, nil
	case "guide":
		return &Result{Action: action, Text: s.Guide()}, nil
	}
	return nil, fmt.Errorf("unknown action %q: use publish, list, read, delete, open, pin, unpin or guide", p.Action)
}

func (s *Service) viewerURL(id string, n int) (string, error) {
	base, err := s.BaseURL()
	if err != nil {
		return "", err
	}
	u := base + ViewerPath(id)
	if n > 0 {
		u += fmt.Sprintf("?v=%d", n)
	}
	return u, nil
}

func resolvePath(env CallEnv, p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return filepath.Join(env.WorkingDir, p)
}

// SourcePaths lists the local files a publish would read, resolved, so a
// caller can check permissions first.
func SourcePaths(env CallEnv, p ArtifactParams) []string {
	var out []string
	if p.FilePath != "" {
		out = append(out, resolvePath(env, p.FilePath))
	}
	for _, src := range p.Files {
		if src != nil {
			out = append(out, resolvePath(env, *src))
		}
	}
	return out
}

func (s *Service) publish(ctx context.Context, env CallEnv, p ArtifactParams) (*Result, error) {
	if p.Content != nil && p.FilePath != "" {
		return nil, errors.New("pass file_path or content, not both")
	}
	files := make(map[string]*string, len(p.Files))
	for k, v := range p.Files {
		if v != nil {
			r := resolvePath(env, *v)
			v = &r
		}
		files[k] = v
	}
	in := PublishInput{
		Title: p.Title, Description: p.Description, Icon: p.Icon, Kind: Kind(strings.ToLower(p.Kind)),
		Label: p.Label, Content: p.Content, SourcePath: resolvePath(env, p.FilePath), Files: files,
		BaseVersion: p.BaseVersion, Project: env.Project, SessionID: env.SessionID,
	}
	if strings.TrimSpace(p.ID) != "" {
		id, err := ParseID(p.ID)
		if err != nil {
			return nil, err
		}
		in.ID = id
	}
	res, err := s.Store.Publish(ctx, in)
	if err != nil {
		return nil, err
	}
	m := res.Meta
	u, err := s.viewerURL(m.ID, 0)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	verb := "Updated"
	if res.Created {
		verb = "Published"
	}
	fmt.Fprintf(&b, "%s %q (%s) as v%d.\nLink: %s\n", verb, m.Title, m.Kind, res.Version.N, u)
	fmt.Fprintf(&b, "id: %s\n", m.ID)
	if len(res.Version.Files) > 1 {
		b.WriteString("Files:")
		for _, f := range res.Version.Files {
			fmt.Fprintf(&b, " %s (%s)", f.Path, humanSize(f.Size))
		}
		b.WriteString("\n")
	}
	if res.Wrapped {
		b.WriteString("Note: the page had no doctype, so it was given a document skeleton (doctype, charset, viewport, title).\n")
	}
	page, _ := s.Store.ReadFile(m.ID, res.Version.N, m.PageName())
	warns := s.lint(m.Kind, page)
	if res.NoTitle {
		// The skeleton supplied a placeholder <title>, so lint can't see
		// that the page never had one.
		warns = append(warns, "the page has no <title>; give it a name of two to four words.")
	}
	for _, w := range warns {
		b.WriteString("Warning: " + w + "\n")
	}
	if n := s.openAgentComments(m.ID); n > 0 {
		b.WriteString(commentsNote(n, " on this artifact"))
	}
	r := &Result{
		Action: "publish", ID: m.ID, Title: m.Title, Kind: m.Kind, Version: res.Version.N,
		URL: u, Created: res.Created, Warnings: len(warns),
	}
	if res.Created && s.OpenOnCreate {
		if err := s.open(u); err == nil {
			r.Opened = true
			b.WriteString("Opened it in the browser.\n")
		}
	}
	r.Text = strings.TrimSpace(b.String())
	return r, nil
}

// commentsNote tells the agent how many comments are waiting for it.
func commentsNote(n int, where string) string {
	if n == 1 {
		return "There is 1 unresolved comment for you" + where + "; read it with artifact_comments.\n"
	}
	return fmt.Sprintf("There are %d unresolved comments for you%s; read them with artifact_comments.\n", n, where)
}

func (s *Service) openAgentComments(id string) int {
	list, err := s.Store.Comments(id)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range list {
		if c.ToAgent && !c.Resolved && c.Author == AuthorYou {
			n++
		}
	}
	return n
}

var (
	reTitle     = regexp.MustCompile(`(?is)<title[^>]*>\s*\S.*?</title>`)
	reExtScript = regexp.MustCompile(`(?i)<script[^>]+src=["']?(https?:)?//([^/"' >]+)`)
	reExtLink   = regexp.MustCompile(`(?i)<link[^>]+href=["']?(https?:)?//([^/"' >]+)`)
	reSection   = regexp.MustCompile(`(?i)<section[\s>]`)
)

// lint returns problems a page would show in the viewer.
func (s *Service) lint(kind Kind, page []byte) []string {
	var out []string
	if kind == KindDocument {
		return nil
	}
	if !reTitle.Match(page) {
		out = append(out, "the page has no <title>; give it a name of two to four words.")
	}
	if kind == KindSlides && !reSection.Match(page) {
		out = append(out, "a slides artifact needs one <section> per slide.")
	}
	allowed := map[string]bool{}
	if s.Server.AllowCDN {
		for _, c := range cdnScripts {
			allowed[strings.TrimPrefix(c, "https://")] = true
		}
	}
	var blocked []string
	for _, m := range reExtScript.FindAllSubmatch(page, -1) {
		if h := strings.ToLower(string(m[2])); !allowed[h] {
			blocked = append(blocked, h)
		}
	}
	if !s.Server.AllowCDN {
		for _, m := range reExtLink.FindAllSubmatch(page, -1) {
			blocked = append(blocked, strings.ToLower(string(m[2])))
		}
	}
	slices.Sort(blocked)
	blocked = slices.Compact(blocked)
	if len(blocked) > 0 {
		if s.Server.AllowCDN {
			out = append(out, "scripts from "+strings.Join(blocked, ", ")+" are blocked; only cdnjs.cloudflare.com, cdn.jsdelivr.net, unpkg.com, cdn.tailwindcss.com and code.jquery.com are allowed.")
		} else {
			out = append(out, "external resources from "+strings.Join(blocked, ", ")+" are blocked: this viewer allows no outside requests. Put the code and styles inline.")
		}
	}
	return out
}

func (s *Service) list(p ArtifactParams) (*Result, error) {
	limit := cmp.Or(p.Limit, 25)
	list, err := s.Store.List(ListOptions{Query: p.Query, Limit: limit})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return &Result{Action: "list", Text: "No artifacts yet."}, nil
	}
	base, _ := s.BaseURL()
	var b strings.Builder
	for _, a := range list {
		pin := ""
		if a.Pinned {
			pin = " · pinned"
		}
		fmt.Fprintf(&b, "- %s · %s · v%d · %s%s · %s\n", a.ID, a.Title, a.Version, a.Kind, pin, relTime(a.Updated))
		if a.Description != "" {
			fmt.Fprintf(&b, "  %s\n", a.Description)
		}
		if base != "" {
			fmt.Fprintf(&b, "  %s%s\n", base, a.URL)
		}
	}
	return &Result{Action: "list", Text: strings.TrimSpace(b.String())}, nil
}

const maxReadBytes = 400 << 10

func (s *Service) read(p ArtifactParams) (*Result, error) {
	id, err := ParseID(p.ID)
	if err != nil {
		return nil, err
	}
	m, err := s.Store.Get(id)
	if err != nil {
		return nil, err
	}
	v, ok := m.Version(p.Version)
	if !ok {
		return nil, fmt.Errorf("%s has no v%d (latest is v%d)", id, p.Version, m.Latest())
	}
	name := cmp.Or(p.Path, m.PageName())
	data, err := s.Store.ReadFile(id, v.N, name)
	if err != nil {
		return nil, fmt.Errorf("%s v%d has no file %q", id, v.N, name)
	}
	u, _ := s.viewerURL(id, 0)
	var b strings.Builder
	fmt.Fprintf(&b, "%q (%s) · id %s · v%d of %d · %s\n", m.Title, m.Kind, m.ID, v.N, m.Latest(), u)
	if m.Description != "" {
		b.WriteString(m.Description + "\n")
	}
	b.WriteString("Files:")
	for _, f := range v.Files {
		fmt.Fprintf(&b, " %s (%s)", f.Path, humanSize(f.Size))
	}
	b.WriteString("\n")
	if cols, _ := s.Store.Collections(id); len(cols) > 0 {
		b.WriteString("Database:")
		for _, k := range slices.Sorted(maps.Keys(cols)) {
			fmt.Fprintf(&b, " %s (%d docs)", k, cols[k])
		}
		b.WriteString("\n")
	}
	if n := s.openAgentComments(id); n > 0 {
		b.WriteString(commentsNote(n, ""))
	}
	switch {
	case !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0:
		fmt.Fprintf(&b, "\n%s is binary (%s); not shown.", name, humanSize(int64(len(data))))
	case len(data) > maxReadBytes:
		fmt.Fprintf(&b, "\n--- %s (first %s of %s) ---\n%s", name, humanSize(maxReadBytes), humanSize(int64(len(data))), data[:maxReadBytes])
	default:
		fmt.Fprintf(&b, "\n--- %s ---\n%s", name, data)
	}
	return &Result{Action: "read", ID: id, Title: m.Title, Kind: m.Kind, Version: v.N, URL: u, Text: b.String()}, nil
}

// Guide returns the design guide for writing artifacts.
func (s *Service) Guide() string {
	t, err := template.New("guide").Parse(guideTemplate)
	if err != nil {
		return guideTemplate
	}
	var b bytes.Buffer
	_ = t.Execute(&b, map[string]any{"AllowCDN": s.Server.AllowCDN})
	return b.String()
}

// DataParams are the inputs of the artifact_data tool.
type DataParams struct {
	ID         string         `json:"id" description:"The artifact's id or viewer link." jsonschema:"The artifact's id or viewer link."`
	Action     string         `json:"action" description:"One of get, list, query, set, update, delete, batch, collections." jsonschema:"One of get, list, query, set, update, delete, batch, collections."`
	Collection string         `json:"collection,omitempty" description:"The collection name." jsonschema:"The collection name."`
	DocID      string         `json:"doc_id,omitempty" description:"get/set/update/delete: the document id. set without one creates an id." jsonschema:"get/set/update/delete: the document id. set without one creates an id."`
	Data       map[string]any `json:"data,omitempty" description:"set: the whole document; update: fields to merge (null drops a field)." jsonschema:"set: the whole document; update: fields to merge (null drops a field)."`
	Query      map[string]any `json:"query,omitempty" description:"query: top-level fields that must equal these values." jsonschema:"query: top-level fields that must equal these values."`
	OrderBy    string         `json:"order_by,omitempty" description:"list/query: sort by this field (default: creation time)." jsonschema:"list/query: sort by this field (default: creation time)."`
	Desc       bool           `json:"desc,omitempty" description:"list/query: sort descending." jsonschema:"list/query: sort descending."`
	Limit      int            `json:"limit,omitempty" description:"list/query: the maximum number of documents (default 100)." jsonschema:"list/query: the maximum number of documents (default 100)."`
	Writes     []DocWrite     `json:"writes,omitempty" description:"batch: writes applied together, each {op: set|update|delete, collection, doc_id, data}." jsonschema:"batch: writes applied together, each {op: set|update|delete, collection, doc_id, data}."`
}

// Data runs one database action. The page sees changes live.
func (s *Service) Data(ctx context.Context, p DataParams) (string, error) {
	id, err := ParseID(p.ID)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(p.Action) {
	case "get":
		d, err := s.Store.GetDoc(id, p.Collection, p.DocID)
		if err != nil {
			return "", err
		}
		if d == nil {
			return fmt.Sprintf("No document %s/%s.", p.Collection, p.DocID), nil
		}
		return jsonText(d), nil
	case "list", "query":
		q := QueryOptions{OrderBy: p.OrderBy, Desc: p.Desc, Limit: cmp.Or(p.Limit, 100)}
		if strings.EqualFold(p.Action, "query") {
			q.Where = p.Query
		}
		docs, err := s.Store.ListDocs(id, p.Collection, q)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d documents\n%s", len(docs), jsonText(docs)), nil
	case "collections":
		cols, err := s.Store.Collections(id)
		if err != nil {
			return "", err
		}
		if len(cols) == 0 {
			return "The database is empty.", nil
		}
		return jsonText(cols), nil
	case "set", "update", "delete":
		docs, err := s.Store.WriteDocs(ctx, id, []DocWrite{{Op: strings.ToLower(p.Action), Collection: p.Collection, DocID: p.DocID, Data: p.Data}})
		if err != nil {
			return "", err
		}
		if docs[0] == nil {
			return fmt.Sprintf("Deleted %s/%s.", p.Collection, p.DocID), nil
		}
		return "Saved.\n" + jsonText(docs[0]), nil
	case "batch":
		docs, err := s.Store.WriteDocs(ctx, id, p.Writes)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Applied %d writes.", len(docs)), nil
	}
	return "", fmt.Errorf("unknown action %q: use get, list, query, set, update, delete, batch or collections", p.Action)
}

// CommentsParams are the inputs of the artifact_comments tool.
type CommentsParams struct {
	ID          string `json:"id" description:"The artifact's id or viewer link." jsonschema:"The artifact's id or viewer link."`
	Action      string `json:"action,omitempty" description:"One of list (default), reply, resolve, unresolve." jsonschema:"One of list (default), reply, resolve, unresolve."`
	CommentID   string `json:"comment_id,omitempty" description:"reply/resolve/unresolve: the comment or thread. reply without one starts a new thread." jsonschema:"reply/resolve/unresolve: the comment or thread. reply without one starts a new thread."`
	Body        string `json:"body,omitempty" description:"reply: what to say." jsonschema:"reply: what to say."`
	IncludeDone bool   `json:"include_resolved,omitempty" description:"list: include resolved threads." jsonschema:"list: include resolved threads."`
}

// Comments runs one comments action.
func (s *Service) Comments(ctx context.Context, p CommentsParams) (string, error) {
	id, err := ParseID(p.ID)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(cmp.Or(p.Action, "list")) {
	case "list":
		list, err := s.Store.Comments(id)
		if err != nil {
			return "", err
		}
		return formatThreads(list, p.IncludeDone), nil
	case "reply":
		c, err := s.Store.AddComment(ctx, id, Comment{Parent: p.CommentID, Body: p.Body, Author: AuthorAgent})
		if err != nil {
			return "", err
		}
		return "Posted " + c.ID + ".", nil
	case "resolve", "unresolve":
		if p.CommentID == "" {
			return "", errors.New("comment_id is required")
		}
		if err := s.Store.ResolveComment(ctx, id, p.CommentID, strings.EqualFold(p.Action, "resolve")); err != nil {
			return "", err
		}
		return "Done.", nil
	}
	return "", fmt.Errorf("unknown action %q: use list, reply, resolve or unresolve", p.Action)
}

func formatThreads(list []Comment, includeDone bool) string {
	var b strings.Builder
	shown := 0
	for _, root := range list {
		if root.Parent != "" || (root.Resolved && !includeDone) {
			continue
		}
		shown++
		state := ""
		if root.Resolved {
			state = " [resolved]"
		}
		if root.ToAgent {
			state += " [sent to you]"
		}
		fmt.Fprintf(&b, "Thread %s · v%d%s\n", root.ID, root.Version, state)
		if root.Anchor != "" {
			fmt.Fprintf(&b, "  on: %q\n", root.Anchor)
		}
		writeComment(&b, root)
		for _, c := range list {
			if c.Parent == root.ID {
				writeComment(&b, c)
			}
		}
		b.WriteString("\n")
	}
	if shown == 0 {
		if includeDone {
			return "No comments."
		}
		return "No open comments."
	}
	return strings.TrimSpace(b.String())
}

func writeComment(b *strings.Builder, c Comment) {
	fmt.Fprintf(b, "  %s (%s, %s): %s\n", c.Author, c.ID, relTime(c.Created), strings.ReplaceAll(c.Body, "\n", "\n    "))
}

func jsonText(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Local().Format("Jan 2, 2006")
}
