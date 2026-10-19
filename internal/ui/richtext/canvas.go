package richtext

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Canvas color slots; values >= 0 index the series palette.
const (
	inkNone = -1 - iota
	inkDim
	inkText
)

// cell is one terminal cell. A wide glyph stores its text in its first cell
// and leaves the following cells empty ("").
type cell struct {
	s   string
	ink int
}

type canvas struct {
	w, h  int
	cells [][]cell
}

func newCanvas(w, h int) *canvas {
	cv := &canvas{w: w, h: h, cells: make([][]cell, h)}
	for y := range cv.cells {
		row := make([]cell, w)
		for x := range row {
			row[x] = cell{s: " ", ink: inkNone}
		}
		cv.cells[y] = row
	}
	return cv
}

// set places a single-width glyph.
func (cv *canvas) set(x, y int, s string, ink int) {
	if x < 0 || y < 0 || x >= cv.w || y >= cv.h {
		return
	}
	cv.cells[y][x] = cell{s: s, ink: ink}
}

// text places s at x (clipped to the canvas) and returns its width.
func (cv *canvas) text(x, y int, s string, ink int) int {
	if y < 0 || y >= cv.h || x < 0 || x >= cv.w {
		return 0
	}
	s = ansi.Truncate(s, cv.w-x, "")
	w := ansi.StringWidth(s)
	if w == 0 {
		return 0
	}
	cv.cells[y][x] = cell{s: s, ink: ink}
	for i := 1; i < w; i++ {
		cv.cells[y][x+i] = cell{s: "", ink: ink}
	}
	return w
}

func inkColor(th Theme, ink int) look {
	switch {
	case ink == inkDim:
		return look{fg: th.Dim}
	case ink == inkText:
		return look{fg: th.Text}
	case ink >= 0:
		return look{fg: th.seriesColor(ink)}
	}
	return look{}
}

// lines renders the canvas, merging runs of the same ink and trimming
// trailing blanks.
func (cv *canvas) lines(th Theme) []string {
	out := make([]string, cv.h)
	for y, row := range cv.cells {
		end := len(row)
		for end > 0 && row[end-1].s == " " {
			end--
		}
		var b, run strings.Builder
		ink := inkNone
		for _, c := range row[:end] {
			if c.ink != ink && c.s != " " {
				b.WriteString(th.paint(run.String(), inkColor(th, ink)))
				run.Reset()
				ink = c.ink
			}
			run.WriteString(c.s)
		}
		b.WriteString(th.paint(run.String(), inkColor(th, ink)))
		out[y] = b.String()
	}
	return out
}

// placeLabels writes dim labels centered on their x positions in row y,
// skipping any that would touch a placed neighbor; the last label always wins.
func (cv *canvas) placeLabels(y int, labels []string, centers []int, maxW int) {
	type span struct {
		x, w int
		s    string
	}
	touches := func(a, b span) bool { return a.x+a.w >= b.x }
	var spans []span
	maxW = min(maxW, cv.w)
	for i, l := range labels {
		l = ansi.Truncate(l, maxW, "…")
		w := ansi.StringWidth(l)
		if w == 0 {
			continue
		}
		sp := span{x: max(0, min(centers[i]-(w-1)/2, cv.w-w)), w: w, s: l}
		for i == len(labels)-1 && len(spans) > 0 && touches(spans[len(spans)-1], sp) {
			spans = spans[:len(spans)-1]
		}
		if len(spans) > 0 && touches(spans[len(spans)-1], sp) {
			continue
		}
		spans = append(spans, sp)
	}
	for _, sp := range spans {
		cv.text(sp.x, y, sp.s, inkDim)
	}
}

// hline draws a dim horizontal rule of n cells.
func (cv *canvas) hline(x, y, n int, s string) {
	for i := range n {
		cv.set(x+i, y, s, inkDim)
	}
}
