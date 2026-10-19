// Package richtext splits assistant markdown into prose, tables and charts and
// renders tables and ```chart blocks as GUI-style terminal widgets.
package richtext

import (
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Kind identifies what a Segment contains.
type Kind int

const (
	Prose Kind = iota
	Table
	Chart
)

func (k Kind) String() string {
	switch k {
	case Table:
		return "table"
	case Chart:
		return "chart"
	default:
		return "prose"
	}
}

// Segment is one top-level piece of a markdown document. Text is the exact
// original source for that piece; concatenating every Segment.Text of a
// Split result reproduces the input.
//
// Table and Chart segments span whole lines: from the start of their first
// line through the end of their last line, including that line's newline
// (if any). Blank lines between blocks always belong to the surrounding
// Prose segments.
type Segment struct {
	Kind Kind
	Text string
}

// ChartBody returns the JSON between the opening and closing fence lines of a
// Chart segment, without the final line break. It returns "" for other kinds.
func (s Segment) ChartBody() string {
	if s.Kind != Chart {
		return ""
	}
	lines := strings.SplitAfter(s.Text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 2 {
		return ""
	}
	body := strings.Join(lines[1:len(lines)-1], "")
	return strings.TrimSuffix(strings.TrimSuffix(body, "\n"), "\r")
}

var parser = goldmark.New(goldmark.WithExtensions(extension.Table)).Parser()

// span is a half-open byte range of the source.
type span struct {
	kind       Kind
	start, end int
}

// Split cuts markdown into top-level Prose, Table and Chart segments.
func Split(markdown string) []Segment {
	if markdown == "" {
		return nil
	}
	src := []byte(markdown)
	idx := newLineIndex(src)
	doc := parser.Parse(text.NewReader(src))

	var spans []span
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		var sp span
		var ok bool
		switch n := n.(type) {
		case *extast.Table:
			sp, ok = tableSpan(n, idx)
		case *ast.FencedCodeBlock:
			sp, ok = chartSpan(n, src, idx)
		}
		if ok {
			spans = append(spans, sp)
		}
	}
	return assemble(markdown, spans)
}

func assemble(markdown string, spans []span) []Segment {
	var segs []Segment
	add := func(k Kind, s string) {
		if s == "" {
			return
		}
		if k == Prose && len(segs) > 0 && segs[len(segs)-1].Kind == Prose {
			segs[len(segs)-1].Text += s
			return
		}
		segs = append(segs, Segment{Kind: k, Text: s})
	}
	pos := 0
	for _, sp := range spans {
		if sp.start < pos {
			continue
		}
		add(Prose, markdown[pos:sp.start])
		add(sp.kind, markdown[sp.start:sp.end])
		pos = sp.end
	}
	add(Prose, markdown[pos:])
	return segs
}

// tableSpan finds the line range of a top-level table from its rows' cells.
func tableSpan(t *extast.Table, idx lineIndex) (span, bool) {
	first, last := -1, -1
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		line := rowLine(row, idx)
		if line < 0 {
			continue
		}
		if first < 0 {
			first = line
		}
		last = line
	}
	if first < 0 {
		return span{}, false
	}
	if rowLine(t.FirstChild(), idx) != first {
		return span{}, false
	}
	return span{kind: Table, start: idx.start(first), end: idx.end(last)}, true
}

func rowLine(row ast.Node, idx lineIndex) int {
	for c := row.FirstChild(); c != nil; c = c.NextSibling() {
		if c.Lines().Len() > 0 {
			return idx.lineOf(c.Lines().At(0).Start)
		}
	}
	return -1
}

// chartSpan returns the full fence range of a closed ```chart block.
func chartSpan(n *ast.FencedCodeBlock, src []byte, idx lineIndex) (span, bool) {
	if n.Info == nil || strings.TrimSpace(string(n.Info.Segment.Value(src))) != "chart" {
		return span{}, false
	}
	open := idx.lineOf(n.Info.Segment.Start)
	fenceChar, fenceLen := fenceOf(idx.text(src, open))
	if fenceLen == 0 {
		return span{}, false
	}
	closeLine := open + 1
	if l := n.Lines(); l.Len() > 0 {
		closeLine = idx.lineOf(l.At(l.Len()-1).Start) + 1
	}
	if closeLine >= idx.count() || !isClosingFence(idx.text(src, closeLine), fenceChar, fenceLen) {
		return span{}, false
	}
	return span{kind: Chart, start: idx.start(open), end: idx.end(closeLine)}, true
}

func fenceOf(line string) (byte, int) {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 || s == "" || (s[0] != '`' && s[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return s[0], n
}

func isClosingFence(line string, ch byte, minLen int) bool {
	c, n := fenceOf(line)
	if c != ch || n < minLen {
		return false
	}
	rest := strings.TrimLeft(line, " ")[n:]
	return strings.Trim(rest, " \t\r\n") == ""
}

// lineIndex maps byte offsets to lines. Each line includes its terminator.
type lineIndex struct {
	starts []int
	size   int
}

func newLineIndex(src []byte) lineIndex {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' && i+1 < len(src) {
			starts = append(starts, i+1)
		}
	}
	return lineIndex{starts: starts, size: len(src)}
}

func (l lineIndex) count() int { return len(l.starts) }

func (l lineIndex) lineOf(off int) int {
	return sort.Search(len(l.starts), func(i int) bool { return l.starts[i] > off }) - 1
}

func (l lineIndex) start(line int) int { return l.starts[line] }

func (l lineIndex) end(line int) int {
	if line+1 < len(l.starts) {
		return l.starts[line+1]
	}
	return l.size
}

func (l lineIndex) text(src []byte, line int) string {
	return string(src[l.start(line):l.end(line)])
}
