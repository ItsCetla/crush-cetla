// Package intro plays a short animated splash before the main TUI shows.
//
// The intro wraps the real UI model: every message still reaches it, so
// sessions, MCPs and LSPs load in the background while the animation runs.
// Only the view is swapped. Any key or click skips straight to the UI.
package intro

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/logo"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

const (
	frameInterval = time.Second / 30
	// Duration is how long the intro plays when nobody skips it.
	Duration = 1700 * time.Millisecond

	// Timeline, in seconds from the start.
	fieldEnd    = 0.60 // dot field finishes spreading from the centre
	markStart   = 0.20 // wordmark starts sweeping in, left to right
	markEnd     = 0.95
	glowFor     = 0.18 // how long a freshly revealed column stays hot
	metaStart   = 0.90 // maker and version type in
	metaEnd     = 1.20
	subStart    = 1.00 // subtitle types in
	subEnd      = 1.35
	minWidth    = 40
	minHeight   = 8
	fieldGlyph  = styles.FieldGlyph
	pixelGlyphs = " ▀▄█"
)

type tickMsg struct{}

// Options configure the intro text and colours.
type Options struct {
	Styles   *styles.Styles
	Maker    string // small label above the left of the wordmark
	Version  string // shown above the right of the wordmark
	Subtitle string // shown centred under the wordmark, e.g. the project dir
}

// Model shows the intro, then hands everything to the wrapped model.
type Model struct {
	inner  tea.Model
	opts   Options
	bitmap [][]bool // unscaled wordmark pixels, [y][x]

	active        bool
	start         time.Time
	now           func() time.Time
	width, height int
}

// New wraps inner with the intro.
func New(inner tea.Model, opts Options) *Model {
	return &Model{
		inner:  inner,
		opts:   opts,
		bitmap: wordmarkBitmap(logo.Wordmark()),
		active: true,
		now:    time.Now,
	}
}

// Active reports whether the intro is still showing.
func (m *Model) Active() bool { return m.active }

// Init starts the inner model and the animation clock.
func (m *Model) Init() tea.Cmd {
	m.start = m.now()
	return tea.Batch(m.inner.Init(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(frameInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update forwards messages to the inner model, except input used to skip.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.active {
		if _, ok := msg.(tickMsg); ok {
			return m, nil
		}
		return m.forward(msg)
	}

	switch msg := msg.(type) {
	case tickMsg:
		if m.elapsed() >= Duration.Seconds() {
			m.active = false
			return m, nil
		}
		return m, tick()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m.forward(msg)
	case tea.KeyPressMsg, tea.MouseClickMsg:
		// Skip, and keep the key out of the editor.
		m.active = false
		return m, nil
	case tea.KeyReleaseMsg, tea.MouseMsg:
		return m, nil
	}
	return m.forward(msg)
}

func (m *Model) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.inner, cmd = m.inner.Update(msg)
	return m, cmd
}

// View draws the intro while it is active, then the inner model.
func (m *Model) View() tea.View {
	if !m.active {
		return m.inner.View()
	}
	var v tea.View
	v.AltScreen = true
	if m.opts.Styles != nil {
		v.BackgroundColor = m.opts.Styles.Background
	}
	v.Content = m.Frame(m.elapsed())
	return v
}

// Inner returns the wrapped model.
func (m *Model) Inner() tea.Model { return m.inner }

func (m *Model) elapsed() float64 {
	if m.start.IsZero() {
		return 0
	}
	return m.now().Sub(m.start).Seconds()
}

// cell is one terminal cell of a frame.
type cell struct {
	glyph string
	fg    color.Color
}

// Frame renders the intro at t seconds for the current window size.
func (m *Model) Frame(t float64) string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return ""
	}
	s := m.opts.Styles
	if s == nil {
		def := styles.Default()
		s = &def
	}
	gradA, gradB := s.Logo.TitleColorA, s.Logo.TitleColorB
	fieldCol, metaCol, dimCol := s.Logo.FieldColor, s.Logo.CharmColor, s.Logo.VersionColor
	hot := lerp(metaCol, color.White, 0.5)

	grid := make([][]cell, h)
	for y := range grid {
		grid[y] = make([]cell, w)
	}
	put := func(x, y int, glyph string, fg color.Color) {
		if y >= 0 && y < h && x >= 0 && x < w {
			grid[y][x] = cell{glyph, fg}
		}
	}

	// Wordmark geometry: the largest scale that fits with some margin.
	bh, bw := len(m.bitmap), len(m.bitmap[0])
	scale := 1
	for sc := 3; sc > 1; sc-- {
		if bw*sc <= w-12 && (bh*sc)/2+10 <= h {
			scale = sc
			break
		}
	}
	markW, markH := bw*scale, (bh*scale+1)/2
	mx, my := (w-markW)/2, (h-markH)/2

	// Dot field: a band behind the wordmark, spreading out from the centre.
	bandTop, bandBot := max(0, my-5), min(h-1, my+markH+4)
	cx, cy := float64(w)/2, float64(h)/2
	maxDist := math.Hypot(cx, float64(bandBot-bandTop+1)) + 1
	front := easeOut(clamp(t/fieldEnd, 0, 1))
	for y := bandTop; y <= bandBot; y++ {
		for x := 0; x < w; x += 2 {
			if x >= mx-4 && x < mx+markW+4 && y >= my-2 && y <= my+markH+1 {
				continue // keep the area round the wordmark clear
			}
			d := math.Hypot(float64(x)-cx, (float64(y)-cy)*2) / maxDist
			if d > front {
				continue
			}
			col := fieldCol
			if front < 1 && front-d < 0.08 {
				col = dimCol // the leading edge of the ripple
			}
			put(x, y, fieldGlyph, col)
		}
	}

	// Wordmark: columns sweep in left to right, glowing as they land.
	for row := range markH {
		for col := range markW {
			reveal := markStart + (markEnd-markStart)*easeInOut(float64(col)/float64(max(1, markW-1)))
			if t < reveal {
				continue
			}
			top := m.pixel(col/scale, (row*2)/scale)
			bot := m.pixel(col/scale, (row*2+1)/scale)
			idx := 0
			if top {
				idx |= 1
			}
			if bot {
				idx |= 2
			}
			if idx == 0 {
				continue
			}
			c := lerp(gradA, gradB, float64(col)/float64(max(1, markW-1)))
			if age := t - reveal; age < glowFor {
				c = lerp(hot, c, age/glowFor)
			}
			put(mx+col, my+row, string([]rune(pixelGlyphs)[idx]), c)
		}
	}

	// Maker and version above the wordmark, subtitle below.
	typeIn := func(text string, x, y int, from, to float64, fg color.Color) {
		runes := []rune(text)
		n := int(math.Round(float64(len(runes)) * clamp((t-from)/(to-from), 0, 1)))
		for i, r := range runes[:n] {
			put(x+i, y, string(r), fg)
		}
	}
	typeIn(m.opts.Maker, mx, my-2, metaStart, metaEnd, metaCol)
	ver := []rune(m.opts.Version)
	if len(ver) > markW/2 {
		ver = append(ver[:markW/2-1], '…')
	}
	typeIn(string(ver), mx+markW-len(ver), my-2, metaStart, metaEnd, dimCol)
	sub := []rune(m.opts.Subtitle)
	if len(sub) > w-4 {
		sub = append([]rune("…"), sub[len(sub)-(w-5):]...)
	}
	typeIn(string(sub), (w-len(sub))/2, my+markH+1, subStart, subEnd, dimCol)

	return renderGrid(grid)
}

func (m *Model) pixel(x, y int) bool {
	return y >= 0 && y < len(m.bitmap) && x >= 0 && x < len(m.bitmap[y]) && m.bitmap[y][x]
}

// renderGrid turns cells into lines of truecolor text; the renderer
// downsamples for terminals with fewer colours.
func renderGrid(grid [][]cell) string {
	var b strings.Builder
	for y, row := range grid {
		if y > 0 {
			b.WriteByte('\n')
		}
		// Trim trailing blanks.
		end := len(row)
		for end > 0 && row[end-1].glyph == "" {
			end--
		}
		var cur color.Color
		for _, c := range row[:end] {
			if c.glyph == "" {
				b.WriteByte(' ')
				continue
			}
			if cur == nil || !sameColor(cur, c.fg) {
				r, g, bl := rgb8(c.fg)
				fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", r, g, bl)
				cur = c.fg
			}
			b.WriteString(c.glyph)
		}
		if cur != nil {
			b.WriteString("\x1b[m")
		}
	}
	return b.String()
}

// wordmarkBitmap turns half-block text into a pixel grid two rows per line.
func wordmarkBitmap(s string) [][]bool {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	width := 0
	for _, l := range lines {
		width = max(width, len([]rune(l)))
	}
	bm := make([][]bool, len(lines)*2)
	for i := range bm {
		bm[i] = make([]bool, width)
	}
	for y, l := range lines {
		for x, r := range []rune(l) {
			switch r {
			case '█':
				bm[y*2][x], bm[y*2+1][x] = true, true
			case '▀':
				bm[y*2][x] = true
			case '▄':
				bm[y*2+1][x] = true
			}
		}
	}
	return bm
}

func rgb8(c color.Color) (uint8, uint8, uint8) {
	if c == nil {
		return 255, 255, 255
	}
	r, g, b, _ := c.RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab := rgb8(a)
	br, bg, bb := rgb8(b)
	return ar == br && ag == bg && ab == bb
}

func lerp(a, b color.Color, f float64) color.Color {
	f = clamp(f, 0, 1)
	ar, ag, ab := rgb8(a)
	br, bg, bb := rgb8(b)
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*f)) }
	return color.RGBA{mix(ar, br), mix(ag, bg), mix(ab, bb), 255}
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func easeOut(t float64) float64 { return 1 - math.Pow(1-t, 3) }

func easeInOut(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}
