package chat

import (
	"os"
	"strings"
	"sync/atomic"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/richtext"
	"github.com/charmbracelet/x/ansi"
)

// richBlocks turns on drawing markdown tables and ```chart blocks in
// finished assistant replies as styled tables and charts. It is on by
// default; the UI sets it from options.tui.rich_blocks and TADA_NO_RICH.
var richBlocks atomic.Bool

func init() { richBlocks.Store(true) }

// SetRichBlocks turns rich tables and charts on or off. Items notice the
// change on their next render because it is part of their cache key.
func SetRichBlocks(on bool) { richBlocks.Store(on) }

// RichBlocksEnabled reports whether rich tables and charts are on.
func RichBlocksEnabled() bool { return richBlocks.Load() }

// hasRichBlock is a cheap pre-check before running the markdown parser:
// a table needs a "|" and a chart needs a "```chart" fence.
func hasRichBlock(text string) bool {
	return strings.Contains(text, "|") || strings.Contains(text, "chart")
}

// richTheme returns the theme for rich blocks, dropping all color when
// NO_COLOR is set.
func richTheme(th richtext.Theme) richtext.Theme {
	if v, ok := os.LookupEnv("NO_COLOR"); ok && v != "" {
		th.Plain = true
	}
	return th
}

// renderRich renders markdown that contains tables or charts. Prose goes
// through glamour as usual; tables and charts go through richtext. A
// block that fails to render falls back to glamour, and a broken chart
// also gets a dim "chart: <error>" line under its code block. ok is false
// when the content has no rich blocks, so the caller can use its normal
// path.
func renderRich(content string, width int, renderer *glamour.TermRenderer, th richtext.Theme) (out string, ok bool) {
	segs := richtext.Split(content)
	rich := false
	for _, s := range segs {
		if s.Kind != richtext.Prose {
			rich = true
			break
		}
	}
	if !rich {
		return "", false
	}
	th = richTheme(th)

	glam := func(md string) string {
		mu := common.LockMarkdownRenderer(renderer)
		mu.Lock()
		defer mu.Unlock()
		r, err := renderer.Render(md)
		if err != nil {
			return md
		}
		return trimBlankLines(r)
	}

	blocks := make([]string, 0, len(segs))
	for _, s := range segs {
		var b string
		switch s.Kind {
		case richtext.Table:
			r, err := richtext.RenderTable(s.Text, width, th)
			if err != nil {
				b = glam(s.Text)
			} else {
				b = r
			}
		case richtext.Chart:
			r, err := richtext.RenderChart(s.ChartBody(), width, th)
			if err != nil {
				b = glam(s.Text) + "\n" + richtext.ErrorLine(err, th)
			} else {
				b = r
			}
		default:
			if strings.TrimSpace(s.Text) == "" {
				continue
			}
			b = glam(s.Text)
		}
		b = strings.TrimRight(b, "\n")
		if b != "" {
			blocks = append(blocks, b)
		}
	}
	return strings.Join(blocks, "\n\n"), true
}

// trimBlankLines drops leading and trailing lines that hold only
// whitespace (glamour pads its output with them).
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	blank := func(l string) bool { return strings.TrimSpace(ansi.Strip(l)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
