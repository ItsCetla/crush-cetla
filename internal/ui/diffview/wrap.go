package diffview

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"
)

// wrapRow is one visual row of a wrapped diff. left and right hold the line
// styles used to draw the row as a "…" placeholder when the height limit cuts
// the diff off there; they are nil for header rows and right is nil in
// unified layout.
type wrapRow struct {
	text        string
	left, right *LineStyle
}

// renderWrapped renders the diff with long code lines wrapped, then applies
// yOffset and height in visual rows.
func (dv *DiffView) renderWrapped() string {
	var rows []wrapRow
	switch dv.layout {
	case layoutUnified:
		rows = dv.unifiedWrappedRows()
	case layoutSplit:
		rows = dv.splitWrappedRows()
	default:
		panic("unknown diffview layout")
	}

	dv.totalLines = len(rows)
	dv.preventInfiniteYScroll()

	start := min(dv.yOffset, len(rows))
	end := len(rows)
	if dv.height > 0 {
		end = min(end, start+dv.height)
	}
	window := rows[start:end]

	texts := make([]string, len(window))
	for i, r := range window {
		texts[i] = r.text
	}
	if n := len(window); dv.height > 0 && n == dv.height && end < len(rows) && window[n-1].left != nil {
		texts[n-1] = dv.ellipsisRow(window[n-1])
	}
	return strings.Join(texts, "\n")
}

// ellipsisRow renders the "…" placeholder shown when the height limit hides
// the rest of the diff.
func (dv *DiffView) ellipsisRow(r wrapRow) string {
	var b strings.Builder
	if r.right == nil {
		if dv.lineNumbers {
			b.WriteString(r.left.LineNumber.Render(pad("…", dv.beforeNumDigits)))
			b.WriteString(r.left.LineNumber.Render(pad("…", dv.afterNumDigits)))
		}
		b.WriteString(lipgloss.NewStyle().MaxWidth(dv.fullCodeWidth).Render(
			r.left.Code.Width(dv.fullCodeWidth).Render("  …"),
		))
		return b.String()
	}
	afterWidth := dv.fullCodeWidth + btoi(dv.extraColOnAfter)
	if dv.lineNumbers {
		b.WriteString(r.left.LineNumber.Render(pad("…", dv.beforeNumDigits)))
	}
	b.WriteString(lipgloss.NewStyle().MaxWidth(dv.fullCodeWidth).Render(
		r.left.Code.Width(dv.fullCodeWidth).Render("  …"),
	))
	if dv.lineNumbers {
		b.WriteString(r.right.LineNumber.Render(pad("…", dv.afterNumDigits)))
	}
	b.WriteString(lipgloss.NewStyle().MaxWidth(afterWidth).Render(
		r.right.Code.Width(afterWidth).Render("  …"),
	))
	return b.String()
}

// nbHyphen is U+2011 NON-BREAKING HYPHEN.
const nbHyphen = '‑'

// wrapCode highlights a line once and splits it into pieces no wider than
// width, carrying styles over onto each piece.
func (dv *DiffView) wrapCode(content string, ls LineStyle, width int) []string {
	content = strings.TrimSuffix(content, "\n")
	content = dv.hightlightCode(content, ls.Code.GetBackground())
	if width <= 0 {
		return []string{""}
	}
	if ansi.StringWidth(content) <= width {
		return []string{content}
	}
	// ansi.Wrap always breaks after "-", which splits flags like "-Recurse".
	// Swap hyphens for a non-breaking hyphen of the same width while wrapping.
	swap := !strings.ContainsRune(content, nbHyphen)
	if swap {
		content = strings.ReplaceAll(content, "-", string(nbHyphen))
	}
	wrapped := lipgloss.Wrap(content, width, "")
	if swap {
		wrapped = strings.ReplaceAll(wrapped, string(nbHyphen), "-")
	}
	pieces := strings.Split(wrapped, "\n")
	// Word wrapping can leave a whitespace-only tail.
	for len(pieces) > 1 && strings.TrimSpace(ansi.Strip(pieces[len(pieces)-1])) == "" {
		pieces = pieces[:len(pieces)-1]
	}
	for i, p := range pieces {
		if ansi.StringWidth(p) > width {
			pieces[i] = ansi.Truncate(p, width, "")
		}
	}
	return pieces
}

// lineStylePtr returns a pointer to the style used for lines of kind k.
func (dv *DiffView) lineStylePtr(k udiff.OpKind) *LineStyle {
	switch k {
	case udiff.Equal:
		return &dv.style.EqualLine
	case udiff.Insert:
		return &dv.style.InsertLine
	case udiff.Delete:
		return &dv.style.DeleteLine
	default:
		return &dv.style.MissingLine
	}
}

// codeCell renders the symbol and code columns of one visual row. Only the
// first row of a line shows the +/- symbol.
func codeCell(ls *LineStyle, kind udiff.OpKind, first bool, piece string, codeWidth int) string {
	fullWidth := codeWidth + leadingSymbolsSize
	var cell string
	switch kind {
	case udiff.Insert, udiff.Delete:
		symbol := "  "
		if first {
			symbol = ternary(kind == udiff.Insert, "+ ", "- ")
		}
		cell = ls.Symbol.Render(symbol) + ls.Code.Width(codeWidth).Render(piece)
	default:
		cell = ls.Code.Width(fullWidth).Render("  " + piece)
	}
	return lipgloss.NewStyle().MaxWidth(fullWidth).Render(cell)
}

func (dv *DiffView) unifiedWrappedRows() []wrapRow {
	rows := make([]wrapRow, 0, dv.totalLines)
	blankBefore := pad(" ", dv.beforeNumDigits)
	blankAfter := pad(" ", dv.afterNumDigits)

	for i, h := range dv.unified.Hunks {
		if i == 0 && dv.fileName != "" {
			rows = append(rows, wrapRow{text: dv.unifiedHeaderRow(dv.style.Filename, "  "+dv.fileName)})
		}
		rows = append(rows, wrapRow{text: dv.unifiedHeaderRow(dv.style.DividerLine, dv.hunkLineFor(h))})

		beforeLine := h.FromLine
		afterLine := h.ToLine

		for _, l := range h.Lines {
			ls := dv.lineStylePtr(l.Kind)
			before, after := blankBefore, blankAfter
			switch l.Kind {
			case udiff.Equal:
				before, after = pad(beforeLine, dv.beforeNumDigits), pad(afterLine, dv.afterNumDigits)
				beforeLine++
				afterLine++
			case udiff.Insert:
				after = pad(afterLine, dv.afterNumDigits)
				afterLine++
			case udiff.Delete:
				before = pad(beforeLine, dv.beforeNumDigits)
				beforeLine++
			}

			for j, piece := range dv.wrapCode(l.Content, *ls, dv.codeWidth) {
				var b strings.Builder
				if dv.lineNumbers {
					if j > 0 {
						before, after = blankBefore, blankAfter
					}
					b.WriteString(ls.LineNumber.Render(before))
					b.WriteString(ls.LineNumber.Render(after))
				}
				b.WriteString(codeCell(ls, l.Kind, j == 0, piece, dv.codeWidth))
				rows = append(rows, wrapRow{text: b.String(), left: ls})
			}
		}
	}
	return rows
}

// splitSide holds the wrapped pieces of one side of a split line.
type splitSide struct {
	ls     *LineStyle
	kind   udiff.OpKind
	num    string
	blank  string
	pieces []string
}

func (dv *DiffView) splitWrappedRows() []wrapRow {
	rows := make([]wrapRow, 0, dv.totalLines)
	beforeCodeWidth := dv.codeWidth
	afterCodeWidth := dv.codeWidth + btoi(dv.extraColOnAfter)
	blankBefore := pad(" ", dv.beforeNumDigits)
	blankAfter := pad(" ", dv.afterNumDigits)

	side := func(l *udiff.Line, num int, digits int, blank string, codeWidth int) splitSide {
		if l == nil {
			return splitSide{ls: &dv.style.MissingLine, blank: blank, num: blank}
		}
		ls := dv.lineStylePtr(l.Kind)
		return splitSide{
			ls:     ls,
			kind:   l.Kind,
			num:    pad(num, digits),
			blank:  blank,
			pieces: dv.wrapCode(l.Content, *ls, codeWidth),
		}
	}

	render := func(b *strings.Builder, s splitSide, row, codeWidth int) {
		num := ternary(row == 0, s.num, s.blank)
		if dv.lineNumbers {
			b.WriteString(s.ls.LineNumber.Render(num))
		}
		if s.pieces == nil {
			fullWidth := codeWidth + leadingSymbolsSize
			b.WriteString(lipgloss.NewStyle().MaxWidth(fullWidth).Render(
				s.ls.Code.Width(fullWidth).Render("  "),
			))
			return
		}
		var piece string
		if row < len(s.pieces) {
			piece = s.pieces[row]
		}
		b.WriteString(codeCell(s.ls, s.kind, row == 0, piece, codeWidth))
	}

	for i, h := range dv.splitHunks {
		if i == 0 && dv.fileName != "" {
			rows = append(rows, wrapRow{text: dv.splitHeaderRow(dv.style.Filename, "  "+dv.fileName)})
		}
		rows = append(rows, wrapRow{text: dv.splitHeaderRow(dv.style.DividerLine, dv.hunkLineFor(dv.unified.Hunks[i]))})

		beforeLine := h.fromLine
		afterLine := h.toLine

		for _, l := range h.lines {
			left := side(l.before, beforeLine, dv.beforeNumDigits, blankBefore, beforeCodeWidth)
			right := side(l.after, afterLine, dv.afterNumDigits, blankAfter, afterCodeWidth)
			if l.before != nil {
				beforeLine++
			}
			if l.after != nil {
				afterLine++
			}

			n := max(len(left.pieces), len(right.pieces), 1)
			for r := range n {
				var b strings.Builder
				render(&b, left, r, beforeCodeWidth)
				render(&b, right, r, afterCodeWidth)
				rows = append(rows, wrapRow{text: b.String(), left: left.ls, right: right.ls})
			}
		}
	}
	return rows
}
