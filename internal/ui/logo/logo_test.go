package logo

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

func testOpts(width int) Opts {
	c := color.RGBA{R: 0xff, G: 0x6a, B: 0x3d, A: 0xff}
	return Opts{
		FieldColor:   c,
		TitleColorA:  c,
		TitleColorB:  c,
		CharmColor:   c,
		VersionColor: c,
		Width:        width,
	}
}

// The dotted field must end in a straight edge: every row of the wide
// logo is exactly as wide as the first one, at any terminal width.
func TestRenderRowsHaveEqualWidth(t *testing.T) {
	t.Parallel()
	for _, width := range []int{60, 80, 120, 160, 230, 320} {
		for range 20 { // Unstable picks a new stretched letter each time.
			o := testOpts(width)
			o.Unstable = true
			out := Render(lipgloss.NewStyle(), "0.3.0", false, o)
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) < 4 {
				t.Fatalf("width %d: want at least 4 rows, got %d", width, len(lines))
			}
			want := lipgloss.Width(lines[0])
			if want > width {
				t.Fatalf("width %d: row 0 is %d cells, wider than the terminal", width, want)
			}
			for i, l := range lines {
				if got := lipgloss.Width(l); got != want {
					t.Fatalf("width %d: row %d is %d cells, row 0 is %d:\n%s", width, i, got, want, ansi.Strip(out))
				}
			}
		}
	}
}

// A stretched D reads as a box, so it is never the stretched letter.
func TestRenderNeverStretchesD(t *testing.T) {
	t.Parallel()
	plainD := strings.Split(LetterD(false), "\n")[0]
	for range 200 {
		o := testOpts(120)
		o.Unstable = true
		out := ansi.Strip(Render(lipgloss.NewStyle(), "0.3.0", false, o))
		if !strings.Contains(out, plainD) {
			t.Fatalf("D was stretched:\n%s", out)
		}
	}
}

func TestStretchableIndexesAreValid(t *testing.T) {
	t.Parallel()
	for _, i := range tadaStretchable {
		if i < 0 || i >= len(TadaLetterforms) {
			t.Fatalf("index %d out of range", i)
		}
	}
}

func TestCompactRowsHaveEqualWidth(t *testing.T) {
	t.Parallel()
	out := Render(lipgloss.NewStyle(), "0.3.0", true, testOpts(40))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := lipgloss.Width(lines[0])
	for i, l := range lines {
		if got := lipgloss.Width(l); got != want {
			t.Fatalf("compact row %d is %d cells, row 0 is %d", i, got, want)
		}
	}
}

func TestSmallRenderFitsWidth(t *testing.T) {
	t.Parallel()
	sty := styles.Default()
	for _, w := range []int{20, 40, 80, 200} {
		out := SmallRender(&sty, w, testOpts(w))
		for i, l := range strings.Split(out, "\n") {
			if got := lipgloss.Width(l); got > w {
				t.Fatalf("width %d: line %d is %d cells", w, i, got)
			}
		}
	}
}
