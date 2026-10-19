package diffview_test

import (
	_ "embed"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/crush/internal/ui/diffview"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
)

//go:embed testdata/TestWrap.before
var TestWrapBefore string

//go:embed testdata/TestWrap.after
var TestWrapAfter string

func newWrapDiffView() *diffview.DiffView {
	return diffview.New().
		Before("copy.ps1", TestWrapBefore).
		After("copy.ps1", TestWrapAfter).
		Style(diffview.DefaultLightStyle()).
		ChromaStyle(styles.Get("catppuccin-latte")).
		TabWidth(4).
		Wrap(true)
}

func TestDiffViewWrap(t *testing.T) {
	t.Parallel()

	widths := map[string][]int{
		"Unified": {60, 100},
		"Split":   {100, 160},
	}
	for layoutName, layoutFunc := range LayoutFuncs {
		t.Run(layoutName, func(t *testing.T) {
			t.Parallel()

			for _, width := range widths[layoutName] {
				t.Run(fmt.Sprintf("WidthOf%03d", width), func(t *testing.T) {
					t.Parallel()

					dv := layoutFunc(newWrapDiffView().Width(width))
					output := dv.String()
					golden.RequireEqual(t, []byte(output))
					assertEveryLineWidth(t, width, output)
				})
			}
		})
	}
}

func TestDiffViewWrapFileName(t *testing.T) {
	t.Parallel()

	for layoutName, layoutFunc := range LayoutFuncs {
		t.Run(layoutName, func(t *testing.T) {
			t.Parallel()

			dv := layoutFunc(newWrapDiffView().Width(80).FileName("copy.ps1"))
			output := dv.String()
			golden.RequireEqual(t, []byte(output))
			assertEveryLineWidth(t, 80, output)
		})
	}
}

func TestDiffViewWrapInvariants(t *testing.T) {
	t.Parallel()

	for layoutName, layoutFunc := range LayoutFuncs {
		t.Run(layoutName, func(t *testing.T) {
			t.Parallel()

			for width := 30; width <= 200; width += 7 {
				dv := layoutFunc(newWrapDiffView().Width(width))
				output := dv.String()
				assertEveryLineWidth(t, width, output)

				for i, line := range strings.Split(ansi.Strip(output), "\n") {
					if strings.Contains(line, "@@") {
						continue
					}
					if strings.Contains(line, "…") {
						t.Errorf("width %d: line %d is truncated: %q", width, i, line)
					}
				}
			}
		})
	}
}

// TestDiffViewWrapKeepsContent checks that wrapping loses no code: the code
// columns of a wrapped render hold the same non-space text as an unwrapped
// render wide enough to fit every line.
func TestDiffViewWrapKeepsContent(t *testing.T) {
	t.Parallel()

	reference := func(layout func(*diffview.DiffView) *diffview.DiffView) *diffview.DiffView {
		return layout(newWrapDiffView().Wrap(false).ChromaStyle(nil))
	}

	t.Run("Unified", func(t *testing.T) {
		t.Parallel()

		want := unifiedCode(reference(UnifiedFunc).String())
		for width := 20; width <= 120; width += 5 {
			out := newWrapDiffView().Width(width).String()
			if got := unifiedCode(out); got != want {
				t.Errorf("width %d: content mismatch\n got: %s\nwant: %s", width, got, want)
			}
		}
	})

	t.Run("Split", func(t *testing.T) {
		t.Parallel()

		ref := reference(SplitFunc).String()
		refWidth := ansi.StringWidth(strings.SplitN(ref, "\n", 2)[0])
		wantLeft, wantRight := splitCode(ref, refWidth)
		for width := 30; width <= 200; width += 5 {
			out := newWrapDiffView().Split().Width(width).String()
			gotLeft, gotRight := splitCode(out, width)
			if gotLeft != wantLeft {
				t.Errorf("width %d: left content mismatch\n got: %s\nwant: %s", width, gotLeft, wantLeft)
			}
			if gotRight != wantRight {
				t.Errorf("width %d: right content mismatch\n got: %s\nwant: %s", width, gotRight, wantRight)
			}
		}
	})
}

func TestDiffViewWrapHeightAndYOffset(t *testing.T) {
	t.Parallel()

	for layoutName, layoutFunc := range LayoutFuncs {
		t.Run(layoutName, func(t *testing.T) {
			t.Parallel()

			full := strings.Split(layoutFunc(newWrapDiffView().Width(60)).String(), "\n")
			if len(full) < 20 {
				t.Fatalf("expected wrapping to produce many rows, got %d", len(full))
			}

			for yOffset := 0; yOffset < len(full)+3; yOffset++ {
				out := layoutFunc(newWrapDiffView().Width(60).Height(5).YOffset(yOffset)).String()
				rows := strings.Split(out, "\n")
				start := min(yOffset, len(full)-5)
				assertHeight(t, 5, out)
				for i, row := range rows {
					if i == len(rows)-1 && start+5 < len(full) && !strings.Contains(full[start+i], "@@") {
						if !strings.Contains(ansi.Strip(row), "…") {
							t.Errorf("yOffset %d: expected ellipsis on last row, got %q", yOffset, ansi.Strip(row))
						}
						continue
					}
					if row != full[start+i] {
						t.Errorf("yOffset %d: row %d mismatch\n got: %q\nwant: %q", yOffset, i, ansi.Strip(row), ansi.Strip(full[start+i]))
					}
				}
			}

			out := layoutFunc(newWrapDiffView().Width(60).Height(5).YOffset(len(full) + 10).InfiniteYScroll(true)).String()
			if out != "" {
				t.Errorf("expected empty output past the end with infinite scroll, got %q", out)
			}
		})
	}
}

func TestDiffViewWrapOffByDefault(t *testing.T) {
	t.Parallel()

	for layoutName, layoutFunc := range LayoutFuncs {
		t.Run(layoutName, func(t *testing.T) {
			t.Parallel()

			off := layoutFunc(newWrapDiffView().Wrap(false).Width(80)).String()
			def := layoutFunc(newWrapDiffView().Width(80)).String()
			if off == def {
				t.Fatal("expected wrapped output to differ from truncated output")
			}
			plain := diffview.New().
				Before("copy.ps1", TestWrapBefore).
				After("copy.ps1", TestWrapAfter).
				Style(diffview.DefaultLightStyle()).
				ChromaStyle(styles.Get("catppuccin-latte")).
				TabWidth(4).
				Width(80)
			if got := layoutFunc(plain).String(); got != off {
				t.Fatal("expected wrap to be off by default")
			}
		})
	}
}

// wrapGutter is the width of one line number gutter for the TestWrap
// fixtures: two digits plus padding.
const wrapGutter = 4

func unifiedCode(output string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(ansi.Strip(output), "\n") {
		if strings.Contains(line, "@@") {
			continue
		}
		b.WriteString(ansi.Cut(line, 2*wrapGutter+2, ansi.StringWidth(line)))
	}
	return stripSpaces(b.String())
}

func splitCode(output string, width int) (left, right string) {
	codeWidth := (width - 2*wrapGutter - 4) / 2
	rightStart := wrapGutter + 2 + codeWidth + wrapGutter + 2
	var l, r strings.Builder
	for line := range strings.SplitSeq(ansi.Strip(output), "\n") {
		if strings.Contains(line, "@@") {
			continue
		}
		l.WriteString(ansi.Cut(line, wrapGutter+2, wrapGutter+2+codeWidth))
		r.WriteString(ansi.Cut(line, rightStart, width))
	}
	return stripSpaces(l.String()), stripSpaces(r.String())
}

func stripSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func assertEveryLineWidth(t *testing.T, expected int, output string) {
	t.Helper()
	for i, line := range strings.Split(output, "\n") {
		if w := ansi.StringWidth(line); w != expected {
			t.Errorf("line %d: expected width %d, got %d: %q", i, expected, w, ansi.Strip(line))
		}
	}
}
