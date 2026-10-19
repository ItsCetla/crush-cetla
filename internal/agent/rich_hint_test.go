package agent

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/ui/richtext"

	"github.com/stretchr/testify/require"
)

func TestWithRichBlocksHint(t *testing.T) {
	t.Parallel()
	const base = "system"
	require.Equal(t, base+richBlocksHint, withRichBlocksHint(base, true, false, true), "interactive top-level agent gets the hint")
	require.Equal(t, base, withRichBlocksHint(base, false, false, true), "tada run never gets it")
	require.Equal(t, base, withRichBlocksHint(base, true, true, true), "sub-agents never get it")
	require.Equal(t, base, withRichBlocksHint(base, true, false, false), "rich_blocks off drops it")
	require.Contains(t, richBlocksHint, "```chart\n{")
}

// The example in the hint must be a chart the TUI can draw.
func TestRichBlocksHintExampleRenders(t *testing.T) {
	t.Parallel()
	for _, seg := range richtext.Split(strings.TrimSpace(richBlocksHint)) {
		if seg.Kind != richtext.Chart {
			continue
		}
		_, err := richtext.RenderChart(seg.ChartBody(), 80, richtext.DarkTheme())
		require.NoError(t, err)
		return
	}
	t.Fatal("hint has no chart example")
}
