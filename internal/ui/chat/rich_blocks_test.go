package chat

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

const richReply = "Here are the results:\n\n" +
	"| Name | Score |\n|---|--:|\n| alpha | 1.5 |\n| beta | 12.25 |\n\n" +
	"And a chart:\n\n" +
	"```chart\n{\"type\":\"bar\",\"labels\":[\"a\",\"b\"],\"series\":[{\"name\":\"x\",\"values\":[1,3]}]}\n```\n\n" +
	"Done."

func richItem(t *testing.T, sty *styles.Styles, text string, finished bool) *AssistantMessageItem {
	t.Helper()
	parts := []message.ContentPart{message.TextContent{Text: text}}
	if finished {
		parts = append(parts, message.Finish{Reason: message.FinishReasonEndTurn, Time: 1})
	}
	msg := &message.Message{ID: "rich-" + t.Name(), Role: message.Assistant, Parts: parts}
	return NewAssistantMessageItem(sty, msg).(*AssistantMessageItem)
}

func TestRichBlocksTableAndChart(t *testing.T) {
	t.Parallel()
	sty := styles.Default()
	item := richItem(t, &sty, richReply, true)

	out := item.RawRender(80)
	plain := ansi.Strip(out)
	require.Contains(t, plain, "╭───────┬───────╮", "table draws with rounded borders")
	require.Contains(t, plain, "│ beta  │ 12.25 │")
	require.Contains(t, plain, "│ alpha │  1.5  │", "numbers line up on the decimal point")
	require.Contains(t, plain, "█████", "chart draws bars")
	require.NotContains(t, plain, "```chart", "chart source is replaced")
	require.NotContains(t, plain, "|---|", "table source is replaced")
	require.Contains(t, plain, "Here are the results:")
	require.Contains(t, plain, "Done.")
	require.Equal(t, out, item.RawRender(80), "cached render is byte-stable")
	require.Equal(t, richReply, item.message.Content().Text, "the message keeps its raw markdown")
}

func TestRichBlocksHeaderUsesThemeColor(t *testing.T) {
	t.Parallel()
	sty := styles.Default()
	item := richItem(t, &sty, "| Name | Score |\n|---|---|\n| a | 1 |\n", true)

	scr := renderANSIToScreen(item.RawRender(80))
	found := false
	for _, line := range scr.Lines {
		for _, cell := range line {
			if cell.Content == "N" {
				require.True(t, colorsEqual(sty.RichText.Header, cell.Style.Fg), "header uses the theme header color")
				found = true
			}
		}
	}
	require.True(t, found, "header cell drawn")
}

func TestRichBlocksWaitForFinish(t *testing.T) {
	t.Parallel()
	sty := styles.Default()
	item := richItem(t, &sty, richReply, false)

	plain := ansi.Strip(item.RawRender(80))
	require.NotContains(t, plain, "╭───────┬", "streaming replies use the plain markdown path")
	require.NotContains(t, plain, "\nchart: ", "a streaming chart never shows an error")
}

func TestRichBlocksBrokenChart(t *testing.T) {
	t.Parallel()
	sty := styles.Default()
	item := richItem(t, &sty, "Look:\n\n```chart\n{bad\n```\n", true)

	plain := ansi.Strip(item.RawRender(80))
	require.Contains(t, plain, "{bad", "the raw block stays visible")
	require.Contains(t, plain, "chart: invalid JSON")
}

func TestRichBlocksFitWidth(t *testing.T) {
	t.Parallel()
	sty := styles.Default()
	text := "| Region | Revenue | Notes |\n|---|--:|---|\n" +
		"| North America | 1,234,567.89 | strong quarter with a long explanation |\n" +
		"| EMEA | 98,765.4 | flat |\n"
	item := richItem(t, &sty, text, true)
	for _, w := range []int{30, 60, 100, 160} {
		out := item.RawRender(w)
		require.LessOrEqual(t, lipgloss.Width(out), cappedMessageWidth(w), "width %d", w)
		require.Contains(t, ansi.Strip(out), "1,234,567.89", "number columns are never cut at width %d", w)
	}
}

func TestRichBlocksPlainReplyUnchanged(t *testing.T) {
	t.Parallel()
	sty := styles.Default()
	text := "Just prose with a | pipe and the word chart.\n\n```go\nfmt.Println(1)\n```"
	a := richItem(t, &sty, text, true)
	b := richItem(t, &sty, text, false)
	require.Equal(t, ansi.Strip(b.RawRender(80)), ansi.Strip(a.RawRender(80)),
		"replies without tables or charts render as before")
}

// Not parallel: these flip package and process state.

func TestRichBlocksOff(t *testing.T) {
	SetRichBlocks(false)
	t.Cleanup(func() { SetRichBlocks(true) })
	sty := styles.Default()
	item := richItem(t, &sty, richReply, true)

	plain := ansi.Strip(item.RawRender(80))
	require.NotContains(t, plain, "╭───────┬")
	require.Contains(t, plain, `{"type":"bar"`, "chart block falls back to a code block")

	SetRichBlocks(true)
	require.Contains(t, ansi.Strip(item.RawRender(80)), "╭───────┬", "turning it on re-renders")
}

func TestRichBlocksNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sty := styles.Default()
	item := richItem(t, &sty, "| Name | Score |\n|---|---|\n| a | 1 |\n", true)

	out := item.RawRender(80)
	table := out[:strings.Index(out, "╯")]
	require.Equal(t, ansi.Strip(table), table, "NO_COLOR drops all styling from the table")
}
