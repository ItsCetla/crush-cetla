package chat

import (
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestFormatTurnDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                               "<1s",
		400 * time.Millisecond:          "<1s",
		45 * time.Second:                "45s",
		72 * time.Second:                "1m 12s",
		time.Hour + 3*time.Minute + 9e9: "1h 3m",
	} {
		require.Equal(t, want, FormatTurnDuration(d), d.String())
	}
}

func TestSortToolCountsAndKinds(t *testing.T) {
	kinds := []ToolCount{
		{Name: "view", Count: 1},
		{Name: "bash", Count: 3},
		{Name: "edit", Count: 1},
		{Name: "grep", Count: 2},
		{Name: "glob", Count: 1},
		{Name: "fetch", Count: 1},
	}
	SortToolCounts(kinds)
	require.Equal(t, "bash", kinds[0].Name)
	require.Equal(t, "grep", kinds[1].Name)
	require.Equal(t, "edit", kinds[2].Name, "ties sort by name")
	require.Equal(t, "3 bash, 2 grep, 1 edit, 1 fetch, +2 more", formatKinds(kinds))
	require.Empty(t, formatKinds(nil))
}

func TestTurnFoldLine(t *testing.T) {
	sty := styles.CharmtonePantera()
	f := NewTurnFoldItem(&sty, "u1", time.Unix(0, 0))
	require.Equal(t, "u1:turn-fold", f.ID())

	f.SetSummary(FoldSummary{Tools: 1, Kinds: []ToolCount{{Name: "bash", Count: 1}}, Live: true})
	require.Equal(t, "▸ Working · 1 tool call · 1 bash", ansi.Strip(f.RawRender(80)))

	f.SetSummary(FoldSummary{Tools: 2, Kinds: []ToolCount{{Name: "view", Count: 2}}, Duration: 72 * time.Second})
	f.SetExpanded(true)
	require.Equal(t, "▾ Worked for 1m 12s · 2 tool calls · 2 view", ansi.Strip(f.RawRender(80)))
	require.False(t, f.UserSet(), "expanding from code is not a user choice")

	require.False(t, f.ToggleExpanded())
	require.True(t, f.UserSet())

	narrow := ansi.Strip(f.RawRender(20))
	require.LessOrEqual(t, ansi.StringWidth(narrow), 20-MessageLeftPaddingTotal)
	require.Contains(t, narrow, "…")
}
