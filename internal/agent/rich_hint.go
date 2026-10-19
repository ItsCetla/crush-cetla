package agent

// richBlocksHint tells the model that the TUI draws markdown tables and
// ```chart blocks. It is added only for interactive, top-level agents with
// options.tui.rich_blocks on: `tada run` prints raw text, so it never sees
// it, and coder.md.tpl stays unchanged.
const richBlocksHint = `

<rich_output>
The chat view draws markdown tables as styled tables, so use one when
comparing several items across the same fields. Keep cells short; numbers
are aligned for you.

To show numbers as a chart, write a fenced block with the language "chart"
holding JSON. Use it only when a chart reads better than a sentence or
table, and never for code or file contents.

` + "```chart" + `
{"type": "bar", "title": "Test time (s)", "labels": ["unit", "e2e"],
 "series": [{"name": "before", "values": [42, 310]},
            {"name": "after", "values": [18, 240]}],
 "unit": "s"}
` + "```" + `

"type" is one of "bar", "hbar", "line" or "spark". Up to 8 series and
200 points; each series has as many values as there are labels.
</rich_output>`

// withRichBlocksHint appends the rich output hint when it applies.
func withRichBlocksHint(systemPrompt string, interactive, isSubAgent, enabled bool) string {
	if !interactive || isSubAgent || !enabled {
		return systemPrompt
	}
	return systemPrompt + richBlocksHint
}
