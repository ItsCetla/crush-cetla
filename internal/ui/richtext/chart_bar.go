package richtext

import (
	"math"

	"github.com/charmbracelet/x/ansi"
)

var (
	leftEighths = []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	lowEighths  = []string{"", "▁", "▂", "▃", "▄", "▅", "▆", "▇"}
)

// barH is the height of a vertical bar chart's plot area.
const barH = 8

// hbar draws one row per label (and per series), bars growing right from the
// label column. With negatives, a dim zero axis splits the bar area in
// proportion to the data range; negative bars grow left from it at half-cell
// resolution and values move to a right-aligned column.
func (ch *chart) hbar(th Theme) ([]string, error) {
	lo, hi := ch.extent()
	labelW := min(maxWidth(ch.labels), ch.width/3)
	valW := 0
	for _, s := range ch.series {
		for _, v := range s.values {
			valW = max(valW, ansi.StringWidth(ch.format(v)))
		}
	}
	area := ch.width - labelW - 2 - valW
	if area < 4 {
		return nil, errTooNarrow
	}
	hb := hbarGeom{x0: labelW + 1, area: area, neg: lo < 0}
	hb.scale(lo, hi)
	nS := len(ch.series)
	rows := len(ch.labels) * nS
	if nS > 1 {
		rows += len(ch.labels) - 1
	}
	cv := newCanvas(ch.width, rows)
	y := 0
	for i, label := range ch.labels {
		if i > 0 && nS > 1 {
			hb.axis(cv, y)
			y++
		}
		cv.text(0, y, ansi.Truncate(label, labelW, "…"), inkText)
		for _, s := range ch.series {
			hb.row(cv, y, s.values[i], s.color, ch.format(s.values[i]))
			y++
		}
	}
	return cv.lines(th), nil
}

type hbarGeom struct {
	x0, area int
	neg      bool
	axisX    int     // zero axis column when neg
	perUnit  float64 // cells per data unit
	negCells int
}

func (hb *hbarGeom) scale(lo, hi float64) {
	hi = math.Max(hi, 0)
	if !hb.neg {
		if hi > 0 {
			hb.perUnit = float64(hb.area) / hi
		}
		return
	}
	cells := hb.area - 1
	hb.negCells = toInt(float64(cells)*-lo/(hi-lo), 1, max(cells, 1))
	hb.axisX = hb.x0 + hb.negCells
	hb.perUnit = float64(cells) / (hi - lo)
}

func (hb *hbarGeom) axis(cv *canvas, y int) {
	if hb.neg {
		cv.set(hb.axisX, y, "│", inkDim)
	}
}

func (hb *hbarGeom) row(cv *canvas, y int, v float64, ink int, label string) {
	hb.axis(cv, y)
	if math.IsNaN(v) {
		return
	}
	start := hb.x0
	if hb.neg {
		start = hb.axisX + 1
	}
	end := start
	if v < 0 {
		hb.negBar(cv, y, v, ink)
	} else {
		end = hb.posBar(cv, start, y, v, ink)
	}
	if hb.neg {
		cv.text(cv.w-ansi.StringWidth(label), y, label, inkDim)
		return
	}
	cv.text(end+1, y, label, inkDim)
}

// posBar draws v in eighth blocks from x and returns the column after it.
func (hb *hbarGeom) posBar(cv *canvas, x, y int, v float64, ink int) int {
	e := toInt(v*hb.perUnit*8, 0, max((cv.w-x)*8, 0))
	if v > 0 && cv.w > x {
		e = max(e, 1)
	}
	for range e / 8 {
		cv.set(x, y, "█", ink)
		x++
	}
	if r := e % 8; r > 0 {
		cv.set(x, y, leftEighths[r], ink)
		x++
	}
	return x
}

// negBar draws v leftward from the axis in half cells.
func (hb *hbarGeom) negBar(cv *canvas, y int, v float64, ink int) {
	h := toInt(-v*hb.perUnit*2, 1, max(hb.negCells*2, 1))
	x := hb.axisX - 1
	for range h / 2 {
		cv.set(x, y, "█", ink)
		x--
	}
	if h%2 == 1 {
		cv.set(x, y, "▐", ink)
	}
}

// vbar holds the layout of a vertical bar chart.
type vbar struct {
	barW, gap int
	px        int  // first plot column (after the axis)
	values    bool // value labels on the bars, else an axis
	posRows   int
	negRows   int
	top       int // rows above the plot for value labels
	perUnit   float64
}

func (vb vbar) groupW(nS int) int { return nS * vb.barW }

func (vb vbar) zeroY() int { return vb.top + vb.posRows }

// bar draws vertical bars, grouped per label when there are several series.
// Positive bars rise in eighth blocks; negative bars hang below the zero line
// in half blocks.
func (ch *chart) bar(th Theme) ([]string, error) {
	vb, err := ch.barLayout()
	if err != nil {
		return nil, err
	}
	lo, hi := ch.extent()
	vb.rows(math.Min(lo, 0), math.Max(hi, 0))
	hasNeg := lo < 0
	if hi < 0 {
		vb.top = 0 // no labels above the zero line
	}
	height := vb.zeroY() + vb.negRows + 1
	if !hasNeg {
		height++ // baseline
	}
	if hasNeg && vb.values {
		height++ // labels below negative bars
	}
	cv := newCanvas(ch.width, height)
	nS := len(ch.series)
	ext := barExtremes{maxRow: -1, minRow: -1}
	for i := range ch.labels {
		for j, s := range ch.series {
			x := vb.px + i*(vb.groupW(nS)+vb.gap) + j*vb.barW
			ch.drawBar(cv, vb, x, s.values[i], s.color, &ext)
		}
	}
	if !hasNeg {
		n := len(ch.labels)
		cv.hline(vb.px, vb.zeroY(), n*vb.groupW(nS)+(n-1)*vb.gap, "─")
	}
	if !vb.values {
		ch.barAxis(cv, vb, ext, hasNeg)
	}
	ch.barLabels(cv, vb, height-1)
	return cv.lines(th), nil
}

// barExtremes tracks where the tallest and deepest bars end, for the axis.
type barExtremes struct {
	maxRow, minRow int
	maxV, minV     float64
}

func (ch *chart) barValueWidth() int {
	w := 0
	for _, s := range ch.series {
		for _, v := range s.values {
			w = max(w, ansi.StringWidth(ch.format(v)))
		}
	}
	return w
}

// barLayout picks bar width and gap, falling back to an axis when values do
// not fit on the bars and downsampling when even 1-wide bars do not fit.
func (ch *chart) barLayout() (vbar, error) {
	nS, n := len(ch.series), len(ch.labels)
	// Bars in a group touch, so their values need a spare column.
	valW := ch.barValueWidth()
	if nS > 1 {
		valW++
	}
	ideal := min(max(valW, 2), 6)
	if nS == 1 {
		ideal = min(max(valW, maxWidth(ch.labels), 5), 8)
	}
	if barW, gap, ok := fitBars(ch.width, n, nS, ideal); ok && barW >= valW {
		return vbar{barW: barW, gap: gap, values: true, top: 1}, nil
	}
	// Downsampling averages values, which can widen the axis labels, so
	// repeat until the axis width settles.
	for range 3 {
		axisW := ch.barAxisWidth()
		avail := ch.width - axisW
		if avail < 2 {
			return vbar{}, errTooNarrow
		}
		if barW, gap, ok := fitBars(avail, len(ch.labels), nS, ideal); ok {
			return vbar{barW: barW, gap: gap, px: axisW}, nil
		}
		ch.downsample(max(1, (avail+1)/(nS+1)))
	}
	return vbar{}, errTooNarrow
}

func (ch *chart) barAxisWidth() int {
	lo, hi := ch.extent()
	return max(ansi.StringWidth(ch.format(lo)), ansi.StringWidth(ch.format(hi)), 1) + 1
}

// fitBars returns the widest bar (and largest gap) that fits n groups of nS
// bars into avail columns.
func fitBars(avail, n, nS, ideal int) (int, int, bool) {
	for barW := ideal; barW >= 1; barW-- {
		for _, gap := range []int{2, 1} {
			if n*nS*barW+(n-1)*gap <= avail {
				return barW, gap, true
			}
		}
	}
	return 0, 0, false
}

// rows splits barH between positive and negative regions on one scale.
func (vb *vbar) rows(lo, hi float64) {
	span := hi - lo
	switch {
	case span == 0:
		return // all zero: labels sit on the baseline
	case lo == 0:
		vb.posRows = barH
	case hi == 0:
		vb.negRows = barH
	default:
		vb.posRows = toInt(barH*hi/span, 1, barH-1)
		vb.negRows = barH - vb.posRows
	}
	vb.perUnit = math.Inf(1)
	if hi > 0 {
		vb.perUnit = float64(vb.posRows) / hi
	}
	if lo < 0 {
		vb.perUnit = math.Min(vb.perUnit, float64(vb.negRows)/-lo)
	}
	// Trim rows the shared scale leaves empty.
	vb.posRows = toInt(math.Ceil(hi*vb.perUnit-1e-9), 0, vb.posRows)
	vb.negRows = toInt(math.Ceil(-lo*vb.perUnit-1e-9), 0, vb.negRows)
}

func (ch *chart) drawBar(cv *canvas, vb vbar, x int, v float64, ink int, ext *barExtremes) {
	if math.IsNaN(v) {
		return
	}
	labelY := 0
	if v >= 0 {
		top := vb.posBar(cv, x, v, ink)
		labelY = top - 1
		if ext.maxRow < 0 || v > ext.maxV {
			ext.maxRow, ext.maxV = min(top, vb.zeroY()-1), v
		}
	} else {
		bottom := vb.negBar(cv, x, v, ink)
		labelY = bottom
		if ext.minRow < 0 || v < ext.minV {
			ext.minRow, ext.minV = bottom-1, v
		}
	}
	if vb.values {
		s := ch.format(v)
		cv.text(x+(vb.barW-ansi.StringWidth(s)+1)/2, labelY, s, inkDim)
	}
}

// posBar fills a bar upward from the zero line and returns its top row.
func (vb vbar) posBar(cv *canvas, x int, v float64, ink int) int {
	e := toInt(v*vb.perUnit*8, 0, vb.posRows*8)
	if v > 0 && vb.posRows > 0 {
		e = max(e, 1)
	}
	y := vb.zeroY() - 1
	for range e / 8 {
		vb.fill(cv, x, y, "█", ink)
		y--
	}
	if r := e % 8; r > 0 {
		vb.fill(cv, x, y, lowEighths[r], ink)
		y--
	}
	return y + 1
}

// negBar fills a bar downward from the zero line and returns the row below it.
func (vb vbar) negBar(cv *canvas, x int, v float64, ink int) int {
	h := toInt(-v*vb.perUnit*2, 1, max(vb.negRows*2, 1))
	y := vb.zeroY()
	for range h / 2 {
		vb.fill(cv, x, y, "█", ink)
		y++
	}
	if h%2 == 1 {
		vb.fill(cv, x, y, "▀", ink)
		y++
	}
	return y
}

func (vb vbar) fill(cv *canvas, x, y int, s string, ink int) {
	for i := range vb.barW {
		cv.set(x+i, y, s, ink)
	}
}

// barAxis labels the tallest bar, zero and the deepest bar on a left axis.
func (ch *chart) barAxis(cv *canvas, vb vbar, ext barExtremes, hasNeg bool) {
	axisX := vb.px - 1
	last := vb.zeroY() + vb.negRows
	if !hasNeg {
		last++
	}
	for y := vb.top; y < last; y++ {
		cv.set(axisX, y, "│", inkDim)
	}
	zeroRow := vb.zeroY()
	if hasNeg {
		zeroRow = max(vb.zeroY()-1, vb.top)
	}
	marks := []struct {
		row int
		v   float64
	}{{ext.maxRow, ext.maxV}, {ext.minRow, ext.minV}, {zeroRow, 0}}
	used := map[int]bool{}
	for _, m := range marks {
		if m.row < 0 || used[m.row] {
			continue
		}
		used[m.row] = true
		s := ansi.Truncate(ch.format(m.v), axisX, "")
		cv.text(axisX-ansi.StringWidth(s), m.row, s, inkDim)
		cv.set(axisX, m.row, "┤", inkDim)
	}
}

// barLabels writes the x labels under each group.
func (ch *chart) barLabels(cv *canvas, vb vbar, y int) {
	gw := vb.groupW(len(ch.series))
	centers := make([]int, len(ch.labels))
	for i := range centers {
		centers[i] = vb.px + i*(gw+vb.gap) + (gw-1)/2
	}
	maxW := max(gw+vb.gap-1, min(maxWidth(ch.labels), 8))
	cv.placeLabels(y, append([]string(nil), ch.labels...), centers, maxW)
}
