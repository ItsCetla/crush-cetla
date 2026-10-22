package export

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
)

// Document is the export as every renderer sees it. Its strings are
// already scrubbed when the export redacts.
type Document struct {
	Title            string       `json:"title"`
	SessionID        string       `json:"session_id"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
	ExportedAt       time.Time    `json:"exported_at"`
	Project          string       `json:"project,omitempty"`
	Models           []string     `json:"models"`
	PromptTokens     int64        `json:"prompt_tokens"`
	CompletionTokens int64        `json:"completion_tokens"`
	EstimatedUsage   bool         `json:"estimated_usage,omitempty"`
	Cost             float64      `json:"cost"`
	Files            []FileChange `json:"files_changed"`
	Turns            []Turn       `json:"turns"`
	Hosts            []Host       `json:"hosts,omitempty"`
	Redacted         bool         `json:"redacted"`
}

// FileChange is one file the session changed, first version to last.
type FileChange struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
	Diff      string `json:"diff,omitempty"`
}

// Role is who a turn is from.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Turn is one message in the conversation.
type Turn struct {
	Role    Role      `json:"role"`
	Model   string    `json:"model,omitempty"`
	Time    time.Time `json:"time"`
	Summary bool      `json:"summary,omitempty"`
	Blocks  []Block   `json:"blocks"`
}

// BlockKind is what a block holds.
type BlockKind string

const (
	BlockText       BlockKind = "text"
	BlockReasoning  BlockKind = "reasoning"
	BlockTool       BlockKind = "tool"
	BlockShell      BlockKind = "shell"
	BlockAttachment BlockKind = "attachment"
	BlockNotice     BlockKind = "notice"
)

// Block is one piece of a turn.
type Block struct {
	Kind  BlockKind  `json:"kind"`
	Text  string     `json:"text,omitempty"`
	Tool  *ToolCall  `json:"tool,omitempty"`
	Shell *ShellCall `json:"shell,omitempty"`
}

// ToolCall is a tool the assistant called, with its result.
type ToolCall struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	Input   string `json:"input,omitempty"`
	Result  string `json:"result,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	// NoResult is set when the call never got a result, for example
	// because the turn was cancelled.
	NoResult  bool   `json:"no_result,omitempty"`
	DiffPath  string `json:"diff_path,omitempty"`
	Diff      string `json:"diff,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Removals  int    `json:"removals,omitempty"`
}

// ShellCall is a command run from the prompt in bang mode.
type ShellCall struct {
	Command  string `json:"command"`
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// Host is a network host the session talked to.
type Host struct {
	Host     string   `json:"host"`
	Purposes []string `json:"purposes,omitempty"`
	Local    bool     `json:"local,omitempty"`
	Requests int64    `json:"requests"`
	BytesOut int64    `json:"bytes_out"`
	BytesIn  int64    `json:"bytes_in"`
}

// Build turns d into the document the renderers draw.
func Build(d Data, o Options) Document {
	b := newBuilder(d, o)
	s := d.Session
	exported := d.ExportedAt
	if exported.IsZero() {
		exported = time.Now()
	}
	doc := Document{
		Title:            b.clean(s.Title),
		SessionID:        s.ID,
		CreatedAt:        b.time(s.CreatedAt),
		UpdatedAt:        b.time(s.UpdatedAt),
		ExportedAt:       exported.In(b.loc),
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.CompletionTokens,
		EstimatedUsage:   s.EstimatedUsage,
		Cost:             s.Cost,
		Redacted:         o.Redact,
		Models:           []string{},
		Files:            []FileChange{},
		Turns:            []Turn{},
	}
	if doc.Title == "" {
		doc.Title = "Untitled session"
	}
	if d.WorkingDir != "" {
		doc.Project = filepath.Base(d.WorkingDir)
	}

	results := make(map[string]message.ToolResult)
	for _, m := range d.Messages {
		for _, tr := range m.ToolResults() {
			results[tr.ToolCallID] = tr
		}
	}
	for _, m := range d.Messages {
		if m.Role == message.Assistant && m.Model != "" {
			model := m.Model
			if m.PrismModelName != "" {
				model = m.PrismModelName
			}
			if !slices.Contains(doc.Models, model) {
				doc.Models = append(doc.Models, model)
			}
		}
		if t, ok := b.turn(m, results); ok {
			doc.Turns = append(doc.Turns, t)
		}
	}

	doc.Files = b.files(d.Files)
	if len(doc.Files) == 0 {
		doc.Files = filesFromTools(doc.Turns)
	}
	for _, h := range d.Hosts {
		host := Host{
			Host:     h.Host,
			Local:    h.Local,
			Requests: h.Requests,
			BytesOut: h.BytesOut,
			BytesIn:  h.BytesIn,
		}
		for _, p := range h.Purposes {
			host.Purposes = append(host.Purposes, p.Label())
		}
		doc.Hosts = append(doc.Hosts, host)
	}
	return doc
}

type builder struct {
	wd    string
	loc   *time.Location
	scrub []func(string) string
}

func newBuilder(d Data, o Options) *builder {
	b := &builder{wd: filepath.Clean(d.WorkingDir), loc: o.Location}
	if d.WorkingDir == "" {
		b.wd = ""
	}
	if b.loc == nil {
		b.loc = time.Local
	}
	if o.Redact {
		b.scrub = append(b.scrub, RedactSecrets)
		if home := homeReplacer(o.HomeDir); home != nil {
			b.scrub = append(b.scrub, home)
		}
	}
	return b
}

// clean scrubs s when the export redacts.
func (b *builder) clean(s string) string {
	for _, f := range b.scrub {
		s = f(s)
	}
	return s
}

func (b *builder) time(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	// Some timestamps are stored in milliseconds.
	if unix > 1e12 {
		return time.UnixMilli(unix).In(b.loc)
	}
	return time.Unix(unix, 0).In(b.loc)
}

// rel shows p relative to the working directory when it is inside it.
func (b *builder) rel(p string) string {
	if b.wd != "" && filepath.IsAbs(p) {
		if r, err := filepath.Rel(b.wd, p); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(r)
		}
	}
	return b.clean(p)
}

// relText shows absolute paths under the working directory in free text
// relative to it.
func (b *builder) relText(s string) string {
	if b.wd == "" || b.wd == "/" || b.wd == "." {
		return s
	}
	return strings.ReplaceAll(s, b.wd+string(filepath.Separator), "")
}

func (b *builder) turn(m message.Message, results map[string]message.ToolResult) (Turn, bool) {
	t := Turn{Time: b.time(m.CreatedAt), Summary: m.IsSummaryMessage}
	switch m.Role {
	case message.User:
		t.Role = RoleUser
	case message.Assistant:
		t.Role = RoleAssistant
		t.Model = m.Model
		if m.PrismModelName != "" {
			t.Model = m.PrismModelName
		}
	default:
		// Tool results are shown with their calls; system messages are
		// not part of the conversation people had.
		return t, false
	}

	for _, part := range m.Parts {
		switch p := part.(type) {
		case message.TextContent:
			if p.Hidden || strings.TrimSpace(p.Text) == "" {
				continue
			}
			t.Blocks = append(t.Blocks, Block{Kind: BlockText, Text: b.clean(p.Text)})
		case message.ReasoningContent:
			if strings.TrimSpace(p.Thinking) == "" {
				continue
			}
			t.Blocks = append(t.Blocks, Block{Kind: BlockReasoning, Text: b.clean(p.Thinking)})
		case message.ToolCall:
			tc := b.toolCall(p, results)
			t.Blocks = append(t.Blocks, Block{Kind: BlockTool, Tool: &tc})
		case message.ShellCommand:
			t.Blocks = append(t.Blocks, Block{Kind: BlockShell, Shell: &ShellCall{
				Command:  b.clean(p.Command),
				Output:   b.clean(p.Output),
				ExitCode: p.ExitCode,
			}})
		case message.ImageURLContent:
			t.Blocks = append(t.Blocks, Block{Kind: BlockAttachment, Text: "Image: " + b.clean(p.URL)})
		case message.BinaryContent:
			name := filepath.Base(p.Path)
			if p.Path == "" {
				name = "attachment"
			}
			text := name
			if p.MIMEType != "" {
				text += " (" + p.MIMEType + ")"
			}
			t.Blocks = append(t.Blocks, Block{Kind: BlockAttachment, Text: b.clean(text)})
		case message.Finish:
			if notice := finishNotice(p); notice != "" {
				t.Blocks = append(t.Blocks, Block{Kind: BlockNotice, Text: b.clean(notice)})
			}
		}
	}
	return t, len(t.Blocks) > 0
}

func finishNotice(f message.Finish) string {
	var label string
	switch f.Reason {
	case message.FinishReasonCanceled:
		label = "Cancelled"
	case message.FinishReasonError:
		label = "Error"
	case message.FinishReasonMaxTokens:
		label = "Stopped at the output token limit"
	case message.FinishReasonContentFilter:
		label = "Refused"
	default:
		return ""
	}
	detail := strings.TrimSpace(strings.Join(nonEmpty(f.Message, f.Details), ": "))
	if detail == "" {
		return label
	}
	return label + ": " + detail
}

func nonEmpty(ss ...string) []string {
	out := ss[:0]
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func (b *builder) toolCall(c message.ToolCall, results map[string]message.ToolResult) ToolCall {
	tc := ToolCall{
		Name:    c.Name,
		Summary: b.clean(b.relText(toolSummary(c.Input))),
		Input:   b.clean(prettyJSON(c.Input)),
	}
	r, ok := results[c.ID]
	if !ok {
		tc.NoResult = true
	} else {
		tc.IsError = r.IsError
		switch {
		case r.Content != "":
			tc.Result = b.clean(r.Content)
		case strings.HasPrefix(r.MIMEType, "image/"):
			tc.Result = "[image: " + r.MIMEType + "]"
		case r.Data != "":
			tc.Result = "[binary result]"
		}
	}
	if path, d, add, rem := b.toolDiff(c, r); d != "" {
		tc.DiffPath, tc.Diff, tc.Additions, tc.Removals = path, b.clean(d), add, rem
	}
	return tc
}

// toolDiff works out the change a file-editing tool made, from the
// result's metadata when the tool recorded one and from its input
// otherwise.
func (b *builder) toolDiff(c message.ToolCall, r message.ToolResult) (path, unified string, add, rem int) {
	var in struct {
		FilePath  string `json:"file_path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
		Content   string `json:"content"`
	}
	_ = json.Unmarshal([]byte(c.Input), &in)
	var meta struct {
		FilePath   string `json:"file_path"`
		OldContent string `json:"old_content"`
		NewContent string `json:"new_content"`
		Diff       string `json:"diff"`
	}
	if r.Metadata != "" {
		_ = json.Unmarshal([]byte(r.Metadata), &meta)
	}
	file := cmpOr(meta.FilePath, in.FilePath)
	if file == "" || r.IsError {
		return "", "", 0, 0
	}
	path = b.rel(file)
	switch {
	case meta.OldContent != "" || meta.NewContent != "":
		unified, add, rem = diff.GenerateDiff(meta.OldContent, meta.NewContent, path)
	case meta.Diff != "":
		unified = meta.Diff
		add, rem = countDiff(unified)
	case in.OldString != "" || in.NewString != "":
		unified, add, rem = diff.GenerateDiff(in.OldString, in.NewString, path)
	case isWriteTool(c.Name) && in.Content != "":
		unified, add, rem = diff.GenerateDiff("", in.Content, path)
	}
	if add == 0 && rem == 0 {
		return "", "", 0, 0
	}
	return path, unified, add, rem
}

func isWriteTool(name string) bool {
	return strings.EqualFold(name, "write")
}

func countDiff(unified string) (add, rem int) {
	for line := range strings.SplitSeq(unified, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			add++
		case strings.HasPrefix(line, "-"):
			rem++
		}
	}
	return add, rem
}

func cmpOr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// summaryKeys are the input fields that best say what a tool call did,
// in order of preference.
var summaryKeys = []string{
	"command", "file_path", "path", "pattern", "url", "query", "symbol",
	"prompt", "description", "name",
}

func toolSummary(input string) string {
	var fields map[string]any
	if json.Unmarshal([]byte(input), &fields) != nil {
		return ""
	}
	for _, k := range summaryKeys {
		if v, ok := fields[k].(string); ok && strings.TrimSpace(v) != "" {
			return oneLine(v, 120)
		}
	}
	return ""
}

// oneLine collapses s to its first line, cut to at most n runes.
func oneLine(s string, n int) string {
	s = strings.TrimSpace(s)
	more := false
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
		more = true
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
		more = true
	}
	if more {
		s += "…"
	}
	return s
}

func prettyJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return ""
	}
	var buf bytes.Buffer
	if json.Indent(&buf, []byte(s), "", "  ") != nil {
		return s
	}
	return buf.String()
}

// files lists the files the session changed, from their first recorded
// version to their last.
func (b *builder) files(hist []history.File) []FileChange {
	byPath := make(map[string][]history.File)
	for _, f := range hist {
		byPath[f.Path] = append(byPath[f.Path], f)
	}
	out := []FileChange{}
	for p, versions := range byPath {
		first, last := versions[0], versions[0]
		for _, v := range versions {
			if v.Version < first.Version {
				first = v
			}
			if v.Version > last.Version {
				last = v
			}
		}
		path := b.rel(p)
		unified, add, rem := diff.GenerateDiff(first.Content, last.Content, path)
		if add == 0 && rem == 0 {
			continue
		}
		out = append(out, FileChange{Path: path, Additions: add, Removals: rem, Diff: b.clean(unified)})
	}
	slices.SortFunc(out, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// filesFromTools totals the edits tool calls made, for sessions without
// file history.
func filesFromTools(turns []Turn) []FileChange {
	idx := make(map[string]int)
	out := []FileChange{}
	for _, t := range turns {
		for _, bl := range t.Blocks {
			if bl.Tool == nil || bl.Tool.DiffPath == "" {
				continue
			}
			i, ok := idx[bl.Tool.DiffPath]
			if !ok {
				i = len(out)
				idx[bl.Tool.DiffPath] = i
				out = append(out, FileChange{Path: bl.Tool.DiffPath})
			}
			out[i].Additions += bl.Tool.Additions
			out[i].Removals += bl.Tool.Removals
		}
	}
	slices.SortFunc(out, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	return out
}
