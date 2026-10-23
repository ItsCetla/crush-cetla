package chat

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// TestTextWidthCap is not parallel: it changes the package-wide cap,
// which parallel tests read.
func TestTextWidthCap(t *testing.T) {
	t.Cleanup(func() { SetMaxTextWidth(0) })

	SetMaxTextWidth(0)
	require.Equal(t, 198, cappedMessageWidth(200), "no cap fills the chat area")

	SetMaxTextWidth(120)
	require.Equal(t, 120, cappedMessageWidth(200))
	require.Equal(t, 78, cappedMessageWidth(80), "a narrow window still wins")

	SetMaxTextWidth(-5)
	require.Equal(t, 198, cappedMessageWidth(200), "a negative cap means none")
}

// TestAssistantTextFillsWideWindow checks a long reply wraps at the
// window edge instead of at 120 columns.
func TestAssistantTextFillsWideWindow(t *testing.T) {
	t.Cleanup(func() { SetMaxTextWidth(0) })
	SetMaxTextWidth(0)

	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "a1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: strings.Repeat("word ", 80)},
			message.Finish{Reason: message.FinishReasonEndTurn, Time: 1},
		},
	}
	item := NewAssistantMessageItem(&sty, msg)
	widest := func(s string) int {
		w := 0
		for l := range strings.SplitSeq(s, "\n") {
			w = max(w, lipgloss.Width(l))
		}
		return w
	}

	full := widest(item.Render(200))
	require.Greater(t, full, 150, "text should use the wide window")
	require.LessOrEqual(t, full, 200)

	SetMaxTextWidth(100)
	ClearItemCaches([]MessageItem{item})
	require.LessOrEqual(t, widest(item.Render(200)), 102)
}
