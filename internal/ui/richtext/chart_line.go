package richtext

import (
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// lineH is the height of a line chart's plot area in rows.
const lineH = 10

// brailleBits maps a dot at (row 0-3, column 0-1) within a cell to its bit.
var brailleBits = [4][2]uint8{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

var sparkLevels = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// dots is a braille plot: 2×4 dots per cell, colored per cell (last wins).
type dots struct {
	w, h int // in cells
	bits [][]uint8
	ink  [][]int
}

func newDots(w, h int) *dots {
	d := &dots{w: w, h: h, bits: make([][]uint8, h), ink: make([][]int, h)}
	for y := range h {
		d.bits[y] = make([]uint8, w)
		d.ink[y] = make([]int, w)
	}
	return d
}

func (d *dots) plot(x, y, ink int) {
	cx, cy := x/2, y/4
	if x < 0 || y < 0 || cx >= d.w || cy >= d.h {
		return
	}
	d.bits[cy][cx] |= brailleBits[y%4][x%2]
	d.ink[cy][cx] = ink
}

// segment draws a Bresenham line between two dots.
func (d *dots) segment(x0, y0, x1, y1, ink int) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := sign(x1-x0), sign(y1-y0)
	e := dx + dy
	for {
		d.plot(x0, y0, ink)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * e
		if e2 >= dy {
			e += dy
			x0 += sx
		}
		if e2 <= dx {
			e += dx
			y0 += sy
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

// lineGeom maps data to dot coordinates.
type lineGeom struct {
	n, dotsX, dotsY int
	yMin, yMax      float64
}

func (g lineGeom) x(i int) int {
	if g.n <= 1 {
		return 0
	}
	return int(math.Round(float64(i) * float64(g.dotsX-1) / float64(g.n-1)))
}

func (g lineGeom) y(v float64) int {
	return int(math.Round((g.yMax - v) / (g.yMax - g.yMin) * float64(g.dotsY-1)))
}

// line draws a braille line plot with a nice-tick y axis and x labels.
// Gaps (null values) break the line.
func (ch *chart) line(th Theme) ([]string, error) {
	lo, hi := ch.extent()
	ticks, step := niceTicks(lo, hi, 5)
	labels := tickLabels(ticks, step, ch.unit)
	axisW := maxWidth(labels)
	plotW := ch.width - axisW - 1
	if plotW < 8 {
		return nil, errTooNarrow
	}
	ch.downsample(2 * plotW)
	g := lineGeom{n: len(ch.labels), dotsX: 2 * plotW, dotsY: 4 * lineH, yMin: ticks[0], yMax: ticks[len(ticks)-1]}
	d := newDots(plotW, lineH)
	for _, s := range ch.series {
		drawSeries(d, g, s)
	}
	cv := newCanvas(ch.width, lineH+2)
	lineAxis(cv, g, ticks, labels, axisW)
	for y := range lineH {
		for x := range plotW {
			if b := d.bits[y][x]; b != 0 {
				cv.set(axisW+1+x, y, string(rune(0x2800+int(b))), d.ink[y][x])
			}
		}
	}
	cv.set(axisW, lineH, "└", inkDim)
	cv.hline(axisW+1, lineH, plotW, "─")
	ch.lineLabels(cv, g, axisW+1, lineH+1)
	return cv.lines(th), nil
}

func drawSeries(d *dots, g lineGeom, s series) {
	px, py, have := 0, 0, false
	for i, v := range s.values {
		if math.IsNaN(v) {
			have = false
			continue
		}
		x, y := g.x(i), g.y(v)
		if have {
			d.segment(px, py, x, y, s.color)
		} else {
			d.plot(x, y, s.color)
		}
		px, py, have = x, y, true
	}
}

// lineAxis draws the y axis with right-aligned tick labels.
func lineAxis(cv *canvas, g lineGeom, ticks []float64, labels []string, axisW int) {
	for y := range lineH {
		cv.set(axisW, y, "│", inkDim)
	}
	used := map[int]bool{}
	for i, t := range ticks {
		row := g.y(t) / 4
		if used[row] {
			continue
		}
		used[row] = true
		cv.text(axisW-ansi.StringWidth(labels[i]), row, labels[i], inkDim)
		cv.set(axisW, row, "┤", inkDim)
	}
}

// lineLabels writes x labels at a stride that fits, always including the last.
func (ch *chart) lineLabels(cv *canvas, g lineGeom, x0, y int) {
	n := len(ch.labels)
	lw := min(maxWidth(ch.labels), 12)
	slots := max(1, (cv.w-x0)/(lw+2))
	stride := max(1, (n+slots-1)/slots)
	var labels []string
	var centers []int
	for i := 0; i < n; i += stride {
		labels = append(labels, ch.labels[i])
		centers = append(centers, x0+g.x(i)/2)
	}
	if (n-1)%stride != 0 {
		labels = append(labels, ch.labels[n-1])
		centers = append(centers, x0+g.x(n-1)/2)
	}
	cv.placeLabels(y, labels, centers, lw)
}

// spark draws one sparkline per series, each scaled to its own range, with
// the last value and the series min and max.
func (ch *chart) spark(th Theme) ([]string, error) {
	nameW := 0
	for _, s := range ch.series {
		nameW = max(nameW, ansi.StringWidth(s.name))
	}
	nameW = min(nameW, ch.width/4)
	lead := 0
	if nameW > 0 {
		lead = nameW + 1
	}
	tails := make([]sparkTail, len(ch.series))
	full, short := 0, 0
	for i, s := range ch.series {
		tails[i] = ch.sparkTail(s)
		full = max(full, tails[i].width(true))
		short = max(short, tails[i].width(false))
	}
	withRange := ch.width-lead-1-full >= 8
	tailW := short
	if withRange {
		tailW = full
	}
	sparkW := ch.width - lead - 1 - tailW
	if sparkW < 1 {
		return nil, errTooNarrow
	}
	ch.downsample(sparkW)
	lines := make([]string, len(ch.series))
	for i, s := range ch.series {
		lines[i] = ch.sparkLine(th, s, nameW, tails[i], withRange)
	}
	return lines, nil
}

type sparkTail struct{ last, rng string }

func (t sparkTail) width(withRange bool) int {
	w := ansi.StringWidth(t.last)
	if withRange && t.rng != "" {
		w += 1 + ansi.StringWidth(t.rng)
	}
	return w
}

func (ch *chart) sparkTail(s series) sparkTail {
	lo, hi, last := math.Inf(1), math.Inf(-1), math.NaN()
	for _, v := range s.values {
		if !math.IsNaN(v) {
			lo, hi, last = math.Min(lo, v), math.Max(hi, v), v
		}
	}
	if math.IsNaN(last) {
		return sparkTail{}
	}
	return sparkTail{last: ch.format(last), rng: "min " + ch.format(lo) + " max " + ch.format(hi)}
}

func (ch *chart) sparkLine(th Theme, s series, nameW int, tail sparkTail, withRange bool) string {
	var b strings.Builder
	if nameW > 0 {
		name := ansi.Truncate(s.name, nameW, "…")
		b.WriteString(th.fg(name, th.Text))
		b.WriteString(strings.Repeat(" ", nameW-ansi.StringWidth(name)+1))
	}
	b.WriteString(th.fg(sparkline(s.values), th.seriesColor(s.color)))
	b.WriteString(" " + th.fg(tail.last, th.Text))
	if withRange && tail.rng != "" {
		b.WriteString(" " + th.dim(tail.rng))
	}
	return strings.TrimRight(b.String(), " ")
}

// sparkline maps values onto eight block levels; flat data sits mid-height
// and gaps are blank.
func sparkline(vals []float64) string {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		if !math.IsNaN(v) {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	var b strings.Builder
	for _, v := range vals {
		switch {
		case math.IsNaN(v):
			b.WriteByte(' ')
		case hi == lo:
			b.WriteString("▄")
		default:
			b.WriteString(sparkLevels[int(math.Round((v-lo)/(hi-lo)*7))])
		}
	}
	return b.String()
}
