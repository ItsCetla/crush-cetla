package richtext

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// inline is a run of cell text with uniform emphasis.
type inline struct {
	text               string
	bold, italic, code bool
}

type inlines []inline

func (in inlines) plain() string {
	var b strings.Builder
	for _, s := range in {
		b.WriteString(s.text)
	}
	return b.String()
}

func (in inlines) width() int { return ansi.StringWidth(in.plain()) }

// truncate shortens the runs to at most w cells, ending in "…" when cut.
func (in inlines) truncate(w int) inlines {
	if in.width() <= w {
		return in
	}
	if w <= 0 {
		return nil
	}
	var out inlines
	left := w - 1
	for _, s := range in {
		sw := ansi.StringWidth(s.text)
		if sw <= left {
			out = append(out, s)
			left -= sw
			continue
		}
		s.text = ansi.Truncate(s.text, left, "") + "…"
		out = append(out, s)
		return out
	}
	return out
}

// render paints the runs on top of base.
func (in inlines) render(th Theme, base look) string {
	var b strings.Builder
	for _, s := range in {
		l := base
		l.bold = l.bold || s.bold
		l.italic = l.italic || s.italic
		if s.code {
			l.fg = th.Dim
		}
		b.WriteString(th.paint(s.text, l))
	}
	return b.String()
}

// inlineParser turns a small subset of inline markdown (code spans, strong,
// emphasis, links, escapes) into styled runs with the markers removed.
type inlineParser struct {
	src          string
	out          inlines
	cur          strings.Builder
	bold, italic bool
}

func parseInline(s string) inlines {
	p := &inlineParser{src: s}
	p.run()
	return p.out
}

func (p *inlineParser) flush() {
	if p.cur.Len() == 0 {
		return
	}
	p.emit(inline{text: p.cur.String(), bold: p.bold, italic: p.italic})
	p.cur.Reset()
}

func (p *inlineParser) emit(s inline) {
	if n := len(p.out); n > 0 {
		last := &p.out[n-1]
		if last.bold == s.bold && last.italic == s.italic && last.code == s.code {
			last.text += s.text
			return
		}
	}
	p.out = append(p.out, s)
}

func (p *inlineParser) run() {
	s := p.src
	for i := 0; i < len(s); {
		if n := p.step(i); n > 0 {
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		p.cur.WriteRune(r)
		i += size
	}
	p.flush()
}

// step handles a construct at i and returns the bytes consumed, or 0.
func (p *inlineParser) step(i int) int {
	s := p.src
	switch c := s[i]; {
	case c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]):
		p.cur.WriteByte(s[i+1])
		return 2
	case c == '`':
		return p.codeSpan(i)
	case c == '[':
		return p.link(i)
	case (c == '*' || c == '_') && strings.HasPrefix(s[i:], strings.Repeat(string(c), 2)):
		return p.toggle(i, 2, &p.bold)
	case c == '*' || c == '_':
		return p.toggle(i, 1, &p.italic)
	}
	return 0
}

func (p *inlineParser) codeSpan(i int) int {
	s := p.src
	n := backtickRun(s, i)
	j := closingRun(s[i+n:], n)
	if j < 0 {
		p.cur.WriteString(s[i : i+n])
		return n
	}
	body := strings.ReplaceAll(s[i+n:i+n+j], `\|`, "|")
	if len(body) > 1 && body[0] == ' ' && body[len(body)-1] == ' ' && strings.Trim(body, " ") != "" {
		body = body[1 : len(body)-1]
	}
	p.flush()
	p.emit(inline{text: body, bold: p.bold, italic: p.italic, code: true})
	return n + j + n
}

// backtickRun returns the length of the backtick run starting at i.
func backtickRun(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// closingRun finds a backtick run of exactly n in s and returns its offset,
// or -1.
func closingRun(s string, n int) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		m := backtickRun(s, i)
		if m == n {
			return i
		}
		i += m
	}
	return -1
}

func (p *inlineParser) link(i int) int {
	s := p.src
	end := strings.Index(s[i:], "](")
	if end < 0 {
		return 0
	}
	closeIdx := strings.IndexByte(s[i+end:], ')')
	if closeIdx < 0 {
		return 0
	}
	label := s[i+1 : i+end]
	for _, r := range parseInline(label) {
		p.flush()
		r.bold = r.bold || p.bold
		r.italic = r.italic || p.italic
		p.emit(r)
	}
	return end + closeIdx + 1
}

// toggle opens or closes emphasis for a delimiter run of length n at i.
func (p *inlineParser) toggle(i, n int, state *bool) int {
	s := p.src
	delim := s[i : i+n]
	if s[i] == '_' && !p.underscoreOK(i, n, *state) {
		return 0
	}
	if *state && !closes(s, i) {
		return 0
	}
	if !*state && (!opens(s, i+n) || !hasCloser(s[i+n:], delim)) {
		return 0
	}
	p.flush()
	*state = !*state
	return n
}

// opens reports whether a delimiter ending at j can open: the next rune is
// not whitespace.
func opens(s string, j int) bool {
	r, _ := utf8.DecodeRuneInString(s[j:])
	return j < len(s) && !unicode.IsSpace(r)
}

// closes reports whether a delimiter at i can close: the previous rune is not
// whitespace.
func closes(s string, i int) bool {
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return i > 0 && !unicode.IsSpace(r)
}

func hasCloser(s, delim string) bool {
	for i := strings.Index(s, delim); i >= 0; {
		if closes(s, i) {
			return true
		}
		j := strings.Index(s[i+1:], delim)
		if j < 0 {
			return false
		}
		i += 1 + j
	}
	return false
}

// underscoreOK rejects intraword underscores such as snake_case.
func (p *inlineParser) underscoreOK(i, n int, closing bool) bool {
	s := p.src
	if closing {
		r, _ := utf8.DecodeRuneInString(s[i+n:])
		return i+n >= len(s) || !isWordRune(r)
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return i == 0 || !isWordRune(r)
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func isASCIIPunct(c byte) bool {
	return (c < 128 && unicode.IsPunct(rune(c))) || strings.IndexByte("$+<=>^`|~", c) >= 0
}
