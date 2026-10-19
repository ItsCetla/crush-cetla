package richtext

import (
	"errors"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

type colAlign int

const (
	alignNone colAlign = iota
	alignLeft
	alignCenter
	alignRight
)

// column holds the layout facts for one table column.
type column struct {
	align   colAlign
	numeric bool
	decimal bool // right-aligned on the decimal point
	left    int  // widest part before the decimal point
	right   int  // widest part from the decimal point
	natural int
	min     int
}

type parsedTable struct {
	header []inlines
	rows   [][]inlines
	cols   []column
}

const minTextCol = 4

// RenderTable renders a GFM markdown table as a bordered table no wider than
// width, falling back to a stacked "Header: value" layout when the columns
// cannot fit.
func RenderTable(src string, width int, th Theme) (string, error) {
	if width < 8 {
		return "", errTooNarrow
	}
	pt, err := parseTable(cleanText(src, true))
	if err != nil {
		return "", err
	}
	widths, ok := fitColumns(pt.cols, width)
	if !ok {
		return renderStacked(pt, width, th), nil
	}
	return renderGrid(pt, widths, th), nil
}

func parseTable(src string) (*parsedTable, error) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 2 {
		return nil, errors.New("not a table")
	}
	head := splitRow(lines[0])
	aligns := parseDelimiter(lines[1])
	if aligns == nil || len(aligns) != len(head) {
		return nil, errors.New("missing delimiter row")
	}
	pt := &parsedTable{cols: make([]column, len(head))}
	for _, h := range head {
		pt.header = append(pt.header, parseInline(h))
	}
	for _, line := range lines[2:] {
		raw := splitRow(line)
		row := make([]inlines, len(head))
		for c := range row {
			if c < len(raw) {
				row[c] = parseInline(raw[c])
			}
		}
		pt.rows = append(pt.rows, row)
	}
	for c := range pt.cols {
		pt.cols[c] = analyzeColumn(pt, c, aligns[c])
	}
	return pt, nil
}

// splitRow splits a table row on unescaped pipes outside code spans.
func splitRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	if strings.HasSuffix(s, "|") && !strings.HasSuffix(s, `\|`) {
		s = s[:len(s)-1]
	}
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			cur.WriteString(s[i : i+2])
			i += 2
		case s[i] == '`':
			n := backtickRun(s, i)
			end := i + n
			if j := closingRun(s[end:], n); j >= 0 {
				end += j + n
			}
			cur.WriteString(s[i:end])
			i = end
		case s[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			i++
		default:
			cur.WriteByte(s[i])
			i++
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func parseDelimiter(line string) []colAlign {
	cells := splitRow(line)
	out := make([]colAlign, len(cells))
	for i, c := range cells {
		l, r := strings.HasPrefix(c, ":"), strings.HasSuffix(c, ":")
		dashes := strings.Trim(c, ":")
		if dashes == "" || strings.Trim(dashes, "-") != "" {
			return nil
		}
		switch {
		case l && r:
			out[i] = alignCenter
		case l:
			out[i] = alignLeft
		case r:
			out[i] = alignRight
		}
	}
	return out
}

func analyzeColumn(pt *parsedTable, c int, align colAlign) column {
	col := column{align: align}
	cells := make([]string, len(pt.rows))
	for r, row := range pt.rows {
		cells[r] = row[c].plain()
	}
	col.numeric = isNumericColumn(cells)
	col.decimal = col.numeric && align != alignLeft && align != alignCenter
	values := 0
	for r, cell := range cells {
		if col.decimal {
			l, rt := decimalParts(cell)
			col.left, col.right = max(col.left, l), max(col.right, rt)
			continue
		}
		values = max(values, pt.rows[r][c].width())
	}
	if col.decimal {
		values = col.left + col.right
	}
	col.natural = max(pt.header[c].width(), values)
	col.min = min(col.natural, minTextCol)
	if col.numeric {
		col.min = values
	}
	return col
}

// decimalParts splits a numeric cell around its decimal point; placeholders
// end where the point would be.
func decimalParts(cell string) (int, int) {
	if l, r, ok := numParts(strings.TrimSpace(cell)); ok {
		return l, r
	}
	return lipgloss.Width(cell), 0
}

// gridWidth is the rendered width of a bordered table with the given
// content widths (one space of padding on each side of every cell).
func gridWidth(ws []int) int {
	total := len(ws) + 1
	for _, w := range ws {
		total += w + 2
	}
	return total
}

// fitColumns shrinks the widest text columns first, then numeric headers,
// until the grid fits width.
func fitColumns(cols []column, width int) ([]int, bool) {
	ws := make([]int, len(cols))
	for i, c := range cols {
		ws[i] = c.natural
	}
	for gridWidth(ws) > width {
		i := widestShrinkable(cols, ws, false)
		if i < 0 {
			i = widestShrinkable(cols, ws, true)
		}
		if i < 0 {
			return nil, false
		}
		ws[i]--
	}
	return ws, true
}

func widestShrinkable(cols []column, ws []int, numeric bool) int {
	best := -1
	for i, c := range cols {
		if c.numeric != numeric || ws[i] <= c.min {
			continue
		}
		if best < 0 || ws[i] > ws[best] {
			best = i
		}
	}
	return best
}

func renderGrid(pt *parsedTable, ws []int, th Theme) string {
	headLook := look{fg: th.Header, bold: true}
	headers := make([]string, len(pt.cols))
	for c, col := range pt.cols {
		a := col.align
		if col.decimal {
			a = alignRight
		}
		headers[c] = cellText(th, pt.header[c].truncate(ws[c]), ws[c], a, headLook)
	}
	rows := make([][]string, len(pt.rows))
	for r, row := range pt.rows {
		base := look{fg: th.Text}
		if r%2 == 1 {
			base.bg = th.Stripe
		}
		rows[r] = make([]string, len(pt.cols))
		for c, col := range pt.cols {
			rows[r][c] = bodyCell(th, col, row[c], ws[c], base)
		}
	}
	border := lipgloss.NewStyle()
	if !th.Plain && th.Border != nil {
		border = border.Foreground(th.Border)
	}
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(border).
		StyleFunc(table.DefaultStyles).
		Headers(headers...).
		Rows(rows...)
	return t.String()
}

func bodyCell(th Theme, col column, cell inlines, w int, base look) string {
	if !col.decimal {
		return cellText(th, cell.truncate(w), w, col.align, base)
	}
	l, r := decimalParts(cell.plain())
	lp := w - col.right - l
	rp := col.right - r
	return pad(th, lp, base) + cell.render(th, base) + pad(th, rp, base)
}

// cellText aligns already-truncated runs in a cell of width w, adding the
// one-space cell padding.
func cellText(th Theme, cell inlines, w int, a colAlign, base look) string {
	gap := max(0, w-cell.width())
	var lp int
	switch a {
	case alignRight:
		lp = gap
	case alignCenter:
		lp = gap / 2
	}
	return pad(th, lp, base) + cell.render(th, base) + pad(th, gap-lp, base)
}

// pad returns n spaces plus the cell's one-space padding, in base's
// background.
func pad(th Theme, n int, base look) string {
	return th.paint(strings.Repeat(" ", n+1), look{bg: base.bg})
}
