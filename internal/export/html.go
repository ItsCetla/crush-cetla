package export

import (
	"bytes"
	_ "embed"
	"fmt"
	"html"
	"html/template"
	"io"
	"strings"

	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

//go:embed page.html.tmpl
var pageSource string

var pageTemplate = template.Must(template.New("page").Parse(pageSource))

// markdown renders message text. Raw HTML in it is shown as text and
// images are shown as their alt text, so the page never loads anything.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(
		renderer.WithNodeRenderers(util.Prioritized(safeRenderer{}, 100)),
	),
)

type htmlPage struct {
	Title  string
	Meta   []htmlMeta
	Files  []htmlFile
	Turns  []htmlTurn
	Hosts  []htmlHost
	Footer string
}

type htmlMeta struct{ Label, Value string }

type htmlFile struct {
	Path      string
	Additions int
	Removals  int
	Diff      htmlDiff
}

type htmlDiff struct {
	Lines []htmlDiffLine
	More  string
}

type htmlDiffLine struct{ Class, Text string }

type htmlTurn struct {
	Class  string
	Label  string
	Clock  string
	Model  string
	Blocks []htmlBlock
}

type htmlBlock struct {
	Kind  BlockKind
	Text  string
	HTML  template.HTML
	Tool  *htmlTool
	Shell *htmlShell
}

type htmlTool struct {
	Name       string
	Summary    string
	Input      string
	HasDiff    bool
	DiffPath   string
	Diff       htmlDiff
	Additions  int
	Removals   int
	Result     string
	ResultMore string
	IsError    bool
	NoResult   bool
}

type htmlShell struct {
	Command  string
	Output   string
	More     string
	ExitCode int
}

type htmlHost struct {
	Host     string
	Local    bool
	Purposes string
	Requests string
	Sent     string
	Received string
}

func renderHTML(w io.Writer, d Document) error {
	page := htmlPage{Title: d.Title, Footer: footerLine(d)}
	if date := formatDate(d.CreatedAt); date != "" {
		page.Meta = append(page.Meta, htmlMeta{"Date", date})
	}
	page.Meta = append(page.Meta, htmlMeta{plural(len(d.Models), "Model", "Models"), d.modelsLine()})
	if d.Project != "" {
		page.Meta = append(page.Meta, htmlMeta{"Project", d.Project})
	}
	page.Meta = append(page.Meta,
		htmlMeta{"Tokens", d.tokensLine()},
		htmlMeta{"Cost", formatCost(d.Cost)},
		htmlMeta{"Files changed", d.filesLine()},
	)

	for _, f := range d.Files {
		page.Files = append(page.Files, htmlFile{
			Path:      f.Path,
			Additions: f.Additions,
			Removals:  f.Removals,
			Diff:      diffView(f.Diff),
		})
	}

	for _, t := range d.Turns {
		ht := htmlTurn{
			Class: string(t.Role),
			Label: turnLabel(t),
			Clock: formatClock(t.Time),
			Model: t.Model,
		}
		if t.Summary {
			ht.Class += " summary"
		}
		for _, b := range t.Blocks {
			hb := htmlBlock{Kind: b.Kind, Text: b.Text}
			switch b.Kind {
			case BlockText, BlockReasoning:
				out, err := renderMarkdownHTML(b.Text)
				if err != nil {
					return err
				}
				hb.HTML = out
			case BlockTool:
				hb.Tool = toolView(b.Tool)
			case BlockShell:
				out, more := truncate(b.Shell.Output, maxResultLines, maxResultBytes)
				hb.Shell = &htmlShell{
					Command:  b.Shell.Command,
					Output:   out,
					More:     more,
					ExitCode: b.Shell.ExitCode,
				}
			}
			ht.Blocks = append(ht.Blocks, hb)
		}
		page.Turns = append(page.Turns, ht)
	}

	for _, h := range d.Hosts {
		page.Hosts = append(page.Hosts, htmlHost{
			Host:     h.Host,
			Local:    h.Local,
			Purposes: h.purposes(),
			Requests: commas(h.Requests),
			Sent:     netlog.Bytes(h.BytesOut),
			Received: netlog.Bytes(h.BytesIn),
		})
	}

	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, page); err != nil {
		return fmt.Errorf("render html export: %w", err)
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func toolView(c *ToolCall) *htmlTool {
	t := &htmlTool{
		Name:      c.Name,
		Summary:   c.Summary,
		Input:     c.Input,
		HasDiff:   c.Diff != "",
		DiffPath:  c.DiffPath,
		Additions: c.Additions,
		Removals:  c.Removals,
		IsError:   c.IsError,
		NoResult:  c.NoResult,
	}
	if t.HasDiff {
		t.Diff = diffView(c.Diff)
	}
	if c.Result != "" {
		t.Result, t.ResultMore = truncate(c.Result, maxResultLines, maxResultBytes)
	}
	return t
}

// diffView splits a unified diff into classed lines, leaving out the
// file name headers the page already shows.
func diffView(unified string) htmlDiff {
	body, more := truncate(unified, maxDiffLines, maxDiffLines*200)
	var v htmlDiff
	v.More = more
	for line := range strings.SplitSeq(body, "\n") {
		var class string
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			continue
		case strings.HasPrefix(line, "@@"):
			class = "h"
		case strings.HasPrefix(line, "+"):
			class = "a"
		case strings.HasPrefix(line, "-"):
			class = "d"
		}
		if line == "" {
			// An empty block would collapse to no height.
			line = " "
		}
		v.Lines = append(v.Lines, htmlDiffLine{Class: class, Text: line})
	}
	return v
}

func renderMarkdownHTML(s string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(s), &buf); err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}
	// The output is goldmark's HTML with raw HTML escaped, so it is safe
	// to place in the page as is.
	return template.HTML(buf.String()), nil //nolint:gosec
}

// safeRenderer replaces goldmark's renderers for images and raw HTML so
// an export never fetches a remote resource or runs markup from a
// message.
type safeRenderer struct{}

func (safeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindImage, renderImage)
	reg.Register(ast.KindRawHTML, renderRawHTML)
	reg.Register(ast.KindHTMLBlock, renderHTMLBlock)
}

func renderImage(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString(`<span class="img">[image: `)
	} else {
		_, _ = w.WriteString(`]</span>`)
	}
	return ast.WalkContinue, nil
}

func renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*ast.RawHTML)
	for i := range n.Segments.Len() {
		seg := n.Segments.At(i)
		_, _ = w.WriteString(html.EscapeString(string(seg.Value(source))))
	}
	return ast.WalkSkipChildren, nil
}

func renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.HTMLBlock)
	_, _ = w.WriteString("<pre><code>")
	lines := n.Lines()
	for i := range lines.Len() {
		seg := lines.At(i)
		_, _ = w.WriteString(html.EscapeString(string(seg.Value(source))))
	}
	if n.HasClosure() {
		_, _ = w.WriteString(html.EscapeString(string(n.ClosureLine.Value(source))))
	}
	_, _ = w.WriteString("</code></pre>\n")
	return ast.WalkContinue, nil
}
