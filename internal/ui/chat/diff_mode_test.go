package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// diffModeEdit returns a finished edit tool item that turns "oldValue" into
// "newValue".
func diffModeEdit(t *testing.T, sty *styles.Styles) ToolMessageItem {
	t.Helper()
	input, err := json.Marshal(tools.EditParams{FilePath: "/tmp/main.go", OldString: "oldValue", NewString: "newValue"})
	require.NoError(t, err)
	meta, err := json.Marshal(tools.EditResponseMetadata{
		Additions:  1,
		Removals:   1,
		OldContent: "package main\n\nvar x = oldValue\n",
		NewContent: "package main\n\nvar x = newValue\n",
	})
	require.NoError(t, err)
	call := message.ToolCall{ID: "e1", Name: tools.EditToolName, Input: string(input), Finished: true}
	res := &message.ToolResult{ToolCallID: "e1", Name: tools.EditToolName, Content: "ok", Metadata: string(meta)}
	return NewEditToolMessageItem(sty, call, res, false)
}

// sideBySide reports whether some line of the render shows the old and the
// new line next to each other.
func sideBySide(out string) bool {
	for line := range strings.SplitSeq(ansi.Strip(out), "\n") {
		if strings.Contains(line, "oldValue") && strings.Contains(line, "newValue") {
			return true
		}
	}
	return false
}

func TestEditDiffMode(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	wide := common.DiffSplitMinWidth + MessageLeftPaddingTotal + toolBodyLeftPaddingTotal
	for _, tc := range []struct {
		name  string
		mode  string
		width int
		split bool
	}{
		{"auto narrow", "", 120, false},
		{"auto wide", "auto", wide, true},
		{"auto just too narrow", "auto", wide - 1, false},
		{"unified wide", "unified", wide + 40, false},
		{"split narrow", "split", 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := diffModeEdit(t, &sty)
			item.(DiffModeSetter).SetDiffMode(tc.mode)
			out := item.RawRender(tc.width)
			require.Equal(t, tc.split, sideBySide(out), ansi.Strip(out))
			for line := range strings.SplitSeq(out, "\n") {
				require.LessOrEqual(t, ansi.StringWidth(line), tc.width)
			}
		})
	}
}

// Changing the mode re-renders an item that was already drawn at that
// width.
func TestSetDiffModeInvalidatesCache(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := diffModeEdit(t, &sty)
	require.False(t, sideBySide(item.RawRender(100)))
	item.(DiffModeSetter).SetDiffMode("split")
	require.True(t, sideBySide(item.RawRender(100)))
}

// A denied write shows its diff at the full width, so it follows the diff
// mode like an edit does.
func TestWriteDiffMode(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	input, err := json.Marshal(tools.WriteParams{FilePath: "/tmp/main.go", Content: "var x = newValue\n"})
	require.NoError(t, err)
	meta, err := json.Marshal(tools.WriteResponseMetadata{
		Diff:      "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-var x = oldValue\n+var x = newValue\n",
		Additions: 1,
		Removals:  1,
	})
	require.NoError(t, err)
	call := message.ToolCall{ID: "w1", Name: tools.WriteToolName, Input: string(input), Finished: true}
	res := &message.ToolResult{ToolCallID: "w1", Name: tools.WriteToolName, Content: "denied", Metadata: string(meta), IsError: true}

	wide := common.DiffSplitMinWidth + MessageLeftPaddingTotal + toolBodyLeftPaddingTotal
	item := NewWriteToolMessageItem(&sty, call, res, false)
	require.True(t, sideBySide(item.RawRender(wide)))
	require.False(t, sideBySide(item.RawRender(120)))
	item.(DiffModeSetter).SetDiffMode("unified")
	require.False(t, sideBySide(item.RawRender(wide)))
}
