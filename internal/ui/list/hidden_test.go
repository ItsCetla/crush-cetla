package list

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newHiddenTestList(t *testing.T) (*List, []*multiLineItem) {
	t.Helper()
	items := []*multiLineItem{
		newMultiLineItem("a", 2),
		newMultiLineItem("b", 3),
		newMultiLineItem("c", 1),
		newMultiLineItem("d", 2),
	}
	l := NewList()
	l.SetGap(1)
	l.SetSize(20, 30)
	for _, it := range items {
		l.AppendItems(it)
	}
	return l, items
}

// A hidden item takes no rows and leaves no gap behind.
func TestList_HiddenItemTakesNoSpace(t *testing.T) {
	t.Parallel()
	l, items := newHiddenTestList(t)

	full := l.TotalHeight()
	l.SetHidden(items[1], true)
	require.True(t, l.IsHidden(1))
	require.Equal(t, full-3-1, l.TotalHeight(), "the item's 3 rows and its gap are gone")

	out := l.Render()
	require.NotContains(t, out, "b:0")
	require.Contains(t, out, "a:1\n\nc:0", "neighbours meet with a single gap")

	l.SetHidden(items[1], false)
	require.Equal(t, full, l.TotalHeight())
	require.Contains(t, l.Render(), "b:2")
}

// Moving the selection steps over hidden items, and hiding the selected
// item moves the selection to a shown one.
func TestList_SelectionSkipsHiddenItems(t *testing.T) {
	t.Parallel()
	l, items := newHiddenTestList(t)

	l.SetHidden(items[1], true)
	l.SetHidden(items[2], true)

	l.SetSelected(0)
	require.True(t, l.SelectNext())
	require.Equal(t, 3, l.Selected())
	require.True(t, l.SelectPrev())
	require.Equal(t, 0, l.Selected())

	l.SetSelected(3)
	l.SetHidden(items[3], true)
	require.Equal(t, 0, l.Selected(), "selection moves up to the nearest shown item")

	l.SetHidden(items[0], true)
	require.Equal(t, -1, l.Selected(), "nothing left to select")
}

// Scrolling to the bottom with a hidden last item shows the last shown
// item at the bottom, not a blank row.
func TestList_ScrollToBottomWithHiddenTail(t *testing.T) {
	t.Parallel()
	l, items := newHiddenTestList(t)
	l.SetSize(20, 4)

	l.SetHidden(items[3], true)
	l.ScrollToBottom()
	require.True(t, l.AtBottom())
	lines := strings.Split(strings.TrimRight(l.Render(), "\n"), "\n")
	require.Equal(t, "c:0", lines[len(lines)-1])
}

// Removing a hidden item forgets that it was hidden.
func TestList_RemoveHiddenItem(t *testing.T) {
	t.Parallel()
	l, items := newHiddenTestList(t)

	l.SetHidden(items[1], true)
	l.RemoveItem(1)
	require.Equal(t, 3, l.Len())
	for i := range l.Len() {
		require.False(t, l.IsHidden(i))
	}
	require.Empty(t, l.hidden)
}
