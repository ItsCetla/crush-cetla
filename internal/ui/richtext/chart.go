package richtext

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

var errTooNarrow = errors.New("too narrow")

const (
	maxSeries     = 8
	maxPoints     = 200
	maxChartWidth = 100
	maxDecimals   = 15
	minChartWidth = 20
)

type chartSpec struct {
	Type   string       `json:"type"`
	Title  string       `json:"title"`
	Labels []string     `json:"labels"`
	Series []seriesSpec `json:"series"`
	Unit   string       `json:"unit"`
}

type seriesSpec struct {
	Name   string     `json:"name"`
	Values []*float64 `json:"values"`
}

// series is a normalized data series; NaN marks a gap.
type series struct {
	name   string
	values []float64
	color  int
}

type chart struct {
	kind    string
	title   string
	unit    string
	labels  []string
	series  []series
	notes   []string
	width   int
	sampled bool
}

// RenderChart renders a ```chart JSON body as a Unicode chart no wider than
// width (and at most 100 columns).
func RenderChart(body string, width int, th Theme) (string, error) {
	if width < minChartWidth {
		return "", errTooNarrow
	}
	ch, err := parseChart(body)
	if err != nil {
		return "", err
	}
	ch.width = min(width, maxChartWidth)
	var plot []string
	switch ch.kind {
	case "hbar":
		plot, err = ch.hbar(th)
	case "bar":
		plot, err = ch.bar(th)
	case "line":
		plot, err = ch.line(th)
	default:
		plot, err = ch.spark(th)
	}
	if err != nil {
		return "", err
	}
	return ch.frame(th, plot), nil
}

func parseChart(body string) (*chart, error) {
	var spec chartSpec
	if err := json.Unmarshal([]byte(body), &spec); err != nil {
		return nil, jsonError(err)
	}
	kind := strings.ToLower(strings.TrimSpace(spec.Type))
	switch kind {
	case "bar", "hbar", "line", "spark":
	case "":
		return nil, errors.New("missing type")
	default:
		return nil, fmt.Errorf("unknown type %q (want bar, hbar, line or spark)", spec.Type)
	}
	if len(spec.Series) == 0 {
		return nil, errors.New("no series")
	}
	ch := &chart{kind: kind, title: strings.TrimSpace(cleanText(spec.Title, false)), unit: strings.TrimSpace(cleanText(spec.Unit, false))}
	if extra := len(spec.Series) - maxSeries; extra > 0 {
		spec.Series = spec.Series[:maxSeries]
		ch.notes = append(ch.notes, fmt.Sprintf("+%d series not shown", extra))
	}
	if err := ch.load(spec); err != nil {
		return nil, err
	}
	if len(ch.labels) > maxPoints {
		ch.downsample(maxPoints)
	}
	return ch, nil
}

func jsonError(err error) error {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return fmt.Errorf("invalid JSON at byte %d", syn.Offset)
	case errors.As(err, &typ) && typ.Field != "":
		return fmt.Errorf("invalid JSON: %s must be %s", typ.Field, typ.Type)
	case errors.As(err, &typ):
		return errors.New("invalid JSON: want an object")
	default:
		return errors.New("invalid JSON")
	}
}

// load pads every series to the longest one and fills missing labels.
func (ch *chart) load(spec chartSpec) error {
	n := 0
	for _, s := range spec.Series {
		n = max(n, len(s.Values))
	}
	seen := false
	for i, s := range spec.Series {
		vals := make([]float64, n)
		for j := range vals {
			vals[j] = math.NaN()
			if j < len(s.Values) && s.Values[j] != nil {
				v := *s.Values[j]
				if !finite(v) || math.Abs(v) > maxAbsValue {
					return fmt.Errorf("value %g out of range", v)
				}
				vals[j] = v
				seen = true
			}
		}
		name := strings.TrimSpace(cleanText(s.Name, false))
		if name == "" && len(spec.Series) > 1 {
			name = "series " + strconv.Itoa(i+1)
		}
		ch.series = append(ch.series, series{name: name, values: vals, color: i})
	}
	if !seen {
		return errors.New("no values")
	}
	ch.labels = make([]string, n)
	for i := range ch.labels {
		if i < len(spec.Labels) && strings.TrimSpace(cleanText(spec.Labels[i], false)) != "" {
			ch.labels[i] = strings.TrimSpace(cleanText(spec.Labels[i], false))
		} else {
			ch.labels[i] = strconv.Itoa(i + 1)
		}
	}
	return nil
}

// downsample bucket-averages every series to n points.
func (ch *chart) downsample(n int) {
	if n <= 0 || len(ch.labels) <= n {
		return
	}
	for i := range ch.series {
		ch.series[i].values = bucketAvg(ch.series[i].values, n)
	}
	ch.labels = bucketFirst(ch.labels, n)
	if !ch.sampled {
		ch.sampled = true
		ch.notes = append(ch.notes, "downsampled")
	}
}

func bucketBounds(total, n, i int) (int, int) {
	return i * total / n, (i + 1) * total / n
}

// bucketAvg averages vals into n buckets, ignoring NaN gaps.
func bucketAvg(vals []float64, n int) []float64 {
	if len(vals) <= n {
		return vals
	}
	out := make([]float64, n)
	for i := range out {
		lo, hi := bucketBounds(len(vals), n, i)
		sum, cnt := 0.0, 0
		for _, v := range vals[lo:hi] {
			if !math.IsNaN(v) {
				sum += v
				cnt++
			}
		}
		out[i] = math.NaN()
		if cnt > 0 {
			out[i] = sum / float64(cnt)
		}
	}
	return out
}

func bucketFirst(labels []string, n int) []string {
	if len(labels) <= n {
		return labels
	}
	out := make([]string, n)
	for i := range out {
		lo, _ := bucketBounds(len(labels), n, i)
		out[i] = labels[lo]
	}
	return out
}

// extent returns the min and max of all non-NaN values.
func (ch *chart) extent() (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, s := range ch.series {
		for _, v := range s.values {
			if !math.IsNaN(v) {
				lo, hi = math.Min(lo, v), math.Max(hi, v)
			}
		}
	}
	return lo, hi
}

func (ch *chart) format(v float64) string { return formatValue(v, ch.unit) }

// frame adds the title, legend and notes around the plot lines.
func (ch *chart) frame(th Theme, plot []string) string {
	var lines []string
	if ch.title != "" {
		lines = append(lines, th.paint(ansi.Truncate(ch.title, ch.width, "…"), look{fg: th.Text, bold: true}))
	}
	if len(ch.series) > 1 && ch.kind != "spark" {
		lines = append(lines, ch.legend(th)...)
	}
	lines = append(lines, plot...)
	if len(ch.notes) > 0 {
		lines = append(lines, th.dim(ansi.Truncate(strings.Join(ch.notes, " · "), ch.width, "…")))
	}
	return strings.Join(lines, "\n")
}

func (ch *chart) legend(th Theme) []string {
	var lines []string
	var cur string
	curW := 0
	for _, s := range ch.series {
		name := ansi.Truncate(s.name, ch.width-2, "…")
		item := th.fg("■", th.seriesColor(s.color)) + " " + th.fg(name, th.Text)
		w := 2 + ansi.StringWidth(name)
		if curW > 0 && curW+2+w > ch.width {
			lines = append(lines, cur)
			cur, curW = "", 0
		}
		if curW > 0 {
			cur += "  "
			curW += 2
		}
		cur += item
		curW += w
	}
	return append(lines, cur)
}

// formatValue formats v compactly with unit as a currency prefix or suffix.
func formatValue(v float64, unit string) string {
	if math.IsNaN(v) {
		return ""
	}
	num := formatNum(math.Abs(v))
	sign := ""
	if v < 0 && num != "0" {
		sign = "-"
	}
	return sign + withUnit(num, unit)
}

func withUnit(num, unit string) string {
	switch {
	case unit == "":
		return num
	case isCurrency(unit):
		return unit + num
	case len(unit) > 2 && isLetter(unit[0]):
		return num + " " + unit
	default:
		return num + unit
	}
}

func isCurrency(u string) bool {
	switch u {
	case "$", "€", "£", "¥":
		return true
	}
	return false
}

func isLetter(c byte) bool { return (c|0x20) >= 'a' && (c|0x20) <= 'z' }

var scales = []struct {
	div float64
	suf string
}{{1e3, "k"}, {1e6, "M"}, {1e9, "B"}, {1e12, "T"}}

// formatNum formats a non-negative number compactly: 950, 1.2k, 3.4M.
func formatNum(a float64) string {
	if a < 1000 {
		s := trimFloat(a, smallDecimals(a))
		if f, _ := strconv.ParseFloat(s, 64); f < 1000 {
			return s
		}
		a = 1000
	}
	for i := len(scales) - 1; i >= 0; i-- {
		sc := scales[i]
		if a < sc.div && i > 0 {
			continue
		}
		x := a / sc.div
		dec := 1
		if x >= 100 {
			dec = 0
		}
		s := trimFloat(x, dec)
		if f, _ := strconv.ParseFloat(s, 64); f >= 1000 && i+1 < len(scales) {
			return "1" + scales[i+1].suf
		}
		return s + sc.suf
	}
	return trimFloat(a, 0)
}

func smallDecimals(a float64) int {
	switch {
	case a == 0:
		return 0
	case a >= 100:
		return 1
	case a >= 1:
		return 2
	default:
		return toInt(2-math.Floor(math.Log10(a)), 0, maxDecimals)
	}
}

func trimFloat(x float64, dec int) string {
	s := strconv.FormatFloat(x, 'f', dec, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s
}

// niceNum rounds x to 1, 2, 5 or 10 times a power of ten (Heckbert).
func niceNum(x float64, round bool) float64 {
	exp := math.Floor(math.Log10(x))
	f := x / math.Pow(10, exp)
	var nf float64
	switch {
	case round && f < 1.5:
		nf = 1
	case round && f < 3:
		nf = 2
	case round && f < 7:
		nf = 5
	case round:
		nf = 10
	case f <= 1:
		nf = 1
	case f <= 2:
		nf = 2
	case f <= 5:
		nf = 5
	default:
		nf = 10
	}
	return nf * math.Pow(10, exp)
}

// niceTicks returns evenly spaced round tick values covering [lo, hi] using
// about n ticks, plus the step between them.
func niceTicks(lo, hi float64, n int) ([]float64, float64) {
	if hi < lo {
		lo, hi = hi, lo
	}
	if hi == lo {
		d := math.Abs(lo) * 0.1
		if d == 0 {
			d = 1
		}
		lo, hi = lo-d, hi+d
	}
	n = max(n, 2)
	step := niceNum(niceNum(hi-lo, false)/float64(n-1), true)
	start := math.Floor(lo/step) * step
	end := math.Ceil(hi/step) * step
	if !finite(step) || step <= 0 || !finite(start) || !finite(end) {
		return []float64{lo, hi}, hi - lo
	}
	count := toInt((end-start)/step, 1, 4*n) + 1
	ticks := make([]float64, count)
	for i := range ticks {
		v := start + float64(i)*step
		ticks[i] = math.Round(v/step) * step
		if ticks[i] == 0 {
			ticks[i] = 0 // normalize -0
		}
	}
	return ticks, step
}

// tickLabels formats ticks with a shared scale and just enough decimals for
// step, so neighbors never collapse to the same text.
func tickLabels(ticks []float64, step float64, unit string) []string {
	top := 0.0
	for _, t := range ticks {
		top = math.Max(top, math.Abs(t))
	}
	div, suf := 1.0, ""
	for _, sc := range scales {
		if top >= sc.div {
			div, suf = sc.div, sc.suf
		}
	}
	dec := toInt(-math.Floor(math.Log10(step/div)+1e-9), 0, maxDecimals)
	out := make([]string, len(ticks))
	for i, t := range ticks {
		num := trimFloat(math.Abs(t)/div, dec) + suf
		if t == 0 {
			num = "0"
		}
		sign := ""
		if t < 0 {
			sign = "-"
		}
		out[i] = sign + withUnit(num, unit)
	}
	return out
}

func maxWidth(ss []string) int {
	w := 0
	for _, s := range ss {
		w = max(w, ansi.StringWidth(s))
	}
	return w
}
