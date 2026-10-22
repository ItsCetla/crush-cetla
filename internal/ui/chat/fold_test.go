package chat

import (
	"strings"
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

func TestReceiptSegments(t *testing.T) {
	t.Parallel()

	full := FoldSummary{
		Tools:     9,
		Kinds:     []ToolCount{{Name: "bash", Count: 3}, {Name: "edit", Count: 5}, {Name: "view", Count: 1}},
		Duration:  134 * time.Second,
		Files:     5,
		Additions: 120,
		Removals:  34,
		Commands:  3,
		Tests:     TestsPassed,
	}
	require.Equal(t, []string{
		"Worked for 2m 14s",
		"5 files changed (+120 −34)",
		"3 commands",
		"tests passed",
		"$0.42",
		"9 tool calls",
		"3 bash, 5 edit, 1 view",
	}, ReceiptSegments(full, 0.42))

	// Only segments with data show.
	require.Equal(t, []string{"Worked for 5s", "2 tool calls"},
		ReceiptSegments(FoldSummary{Tools: 2, Duration: 5 * time.Second}, 0))

	one := ReceiptSegments(FoldSummary{Tools: 2, Live: true, Files: 1, Additions: 3, Commands: 1, Tests: TestsFailed}, 0.001)
	require.Equal(t, []string{"Working", "1 file changed (+3)", "1 command", "tests failed", "<$0.01", "2 tool calls"}, one)
}

func TestFitReceiptDropsFromTheRight(t *testing.T) {
	t.Parallel()

	segs := []string{"Worked for 2m 14s", "5 files changed (+120 −34)", "3 commands", "tests passed", "$0.42"}
	line := func(width int) string {
		return "▸ " + strings.Join(FitReceipt("▸ ", segs, width), " · ")
	}
	require.Equal(t, "▸ Worked for 2m 14s · 5 files changed (+120 −34) · 3 commands · tests passed · $0.42", line(200))
	full := ansi.StringWidth(line(200))
	require.Equal(t, "▸ Worked for 2m 14s · 5 files changed (+120 −34) · 3 commands · tests passed", line(full-1))
	require.Equal(t, "▸ Worked for 2m 14s · 5 files changed (+120 −34)", line(50))
	require.Equal(t, "▸ Worked for 2m 14s", line(30))
	require.Equal(t, "▸ Worked for 2m 14s", line(3), "the head always stays")
	for w := 0; w < 120; w++ {
		got := line(w)
		if len(FitReceipt("▸ ", segs, w)) > 1 {
			require.LessOrEqual(t, ansi.StringWidth(got), w, "width %d", w)
		}
	}
}

func TestTurnFoldReceiptAndCost(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	f := NewTurnFoldItem(&sty, "u1", time.Unix(0, 0))
	f.SetSummary(FoldSummary{
		Tools: 4, Kinds: []ToolCount{{Name: "edit", Count: 2}, {Name: "bash", Count: 2}},
		Duration: 134 * time.Second, Files: 2, Additions: 10, Removals: 4, Commands: 2, Tests: TestsPassed,
	})

	// Without a starting spend the cost stays unknown.
	f.SetSessionCost(3)
	require.Zero(t, f.Cost())

	f.SetCostBase(1.00)
	f.SetCostBase(2.00) // Only the first base counts.
	require.True(t, f.HasCostBase())
	f.SetSessionCost(1.42)
	require.InDelta(t, 0.42, f.Cost(), 1e-9)

	require.Equal(t,
		"▸ Worked for 2m 14s · 2 files changed (+10 −4) · 2 commands · tests passed · $0.42 · 4 tool calls · 2 edit, 2 bash",
		ansi.Strip(f.RawRender(200)))
	require.Equal(t, "▸ Worked for 2m 14s · 2 files changed (+10 −4)", ansi.Strip(f.RawRender(48+MessageLeftPaddingTotal)))
	require.Equal(t, "▸ Worked for 2m 14s", ansi.Strip(f.RawRender(30+MessageLeftPaddingTotal)))
}
