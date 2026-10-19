package richtext

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// renderStacked lays a table out as one "Header: value" line per cell, rows
// separated by a dim rule, for widths too small for a grid.
func renderStacked(pt *parsedTable, width int, th Theme) string {
	labelW := 0
	for _, h := range pt.header {
		labelW = max(labelW, h.width())
	}
	labelW = min(labelW, max(width/3, minTextCol))
	valueW := width - labelW - 2
	inline := valueW >= 10
	if !inline {
		valueW = width - 2
	}
	rule := th.dim(strings.Repeat("─", min(width, 40)))
	headLook := look{fg: th.Header, bold: true}

	var lines []string
	for r, row := range pt.rows {
		if r > 0 {
			lines = append(lines, rule)
		}
		for c, h := range pt.header {
			hd := h.truncate(labelW)
			label := hd.render(th, headLook) + th.dim(":")
			value := wrapLines(row[c].render(th, look{fg: th.Text}), valueW)
			if !inline {
				lines = append(lines, label)
				lines = append(lines, indent(value, 2)...)
				continue
			}
			label += strings.Repeat(" ", labelW-hd.width()+1)
			lines = append(lines, label+value[0])
			lines = append(lines, indent(value[1:], labelW+2)...)
		}
	}
	return strings.Join(lines, "\n")
}

func wrapLines(s string, w int) []string {
	return strings.Split(ansi.Wrap(s, w, ""), "\n")
}

func indent(lines []string, n int) []string {
	out := make([]string, len(lines))
	sp := strings.Repeat(" ", n)
	for i, l := range lines {
		out[i] = sp + l
	}
	return out
}
