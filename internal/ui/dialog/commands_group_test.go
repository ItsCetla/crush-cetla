package dialog

import (
	"image"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

func newTestCommands(t *testing.T, recent []string) *Commands {
	t.Helper()
	s := styles.CharmtonePantera()
	cfg := &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
	com := &common.Common{Styles: &s, Workspace: &contextWorkspace{cfg: cfg}}
	c, err := NewCommands(com, "sess", true, false, false, nil, nil)
	require.NoError(t, err)
	c.SetRecent(recent)
	return c
}

// selectedCommandID returns the ID of the selected command, or "" when a
// non-command row is selected.
func selectedCommandID(c *Commands) string {
	if item, ok := c.list.SelectedItem().(*CommandItem); ok {
		return item.ID()
	}
	return ""
}

// itemRows returns section titles as "# Title" and commands by ID.
func itemRows[T any](items []T) []string {
	var rows []string
	for _, it := range items {
		switch it := any(it).(type) {
		case *commandSectionHeader:
			rows = append(rows, "# "+it.title)
		case *CommandItem:
			rows = append(rows, it.ID())
		}
	}
	return rows
}

func visibleRows(c *Commands) []string {
	return itemRows(c.list.FilteredItems())
}

func TestDefaultCommandsAllHaveGroups(t *testing.T) {
	t.Parallel()

	c := newTestCommands(t, nil)
	cmds := c.defaultCommands()
	require.NotEmpty(t, cmds)
	for _, cmd := range cmds {
		require.True(t, slices.Contains(commandGroupOrder, cmd.Group()),
			"command %q has no known group (%q)", cmd.ID(), cmd.Group())
	}
}

func TestPushRecentCommand(t *testing.T) {
	t.Parallel()

	var recent []string
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		recent = PushRecentCommand(recent, id)
	}
	require.Equal(t, []string{"f", "e", "d", "c", "b"}, recent)

	// Using one again moves it to the front without duplicating it.
	in := slices.Clone(recent)
	recent = PushRecentCommand(recent, "c")
	require.Equal(t, []string{"c", "f", "e", "d", "b"}, recent)
	require.Equal(t, []string{"f", "e", "d", "c", "b"}, in, "input must not change")

	// Empty IDs and duplicates already in the list are dropped.
	require.Equal(t, []string{"x", "y"}, PushRecentCommand([]string{"y", "", "x", "y"}, "x"))
	require.Equal(t, []string{"y"}, PushRecentCommand([]string{"y"}, ""))
}

func TestGroupCommandItemsRecentFirst(t *testing.T) {
	t.Parallel()

	s := styles.CharmtonePantera()
	items := []*CommandItem{
		NewCommandItem(&s, "a", "A", "", nil).InGroup(CommandGroupApp),
		NewCommandItem(&s, "s", "S", "", nil).InGroup(CommandGroupSession),
		NewCommandItem(&s, "v", "V", "", nil).InGroup(CommandGroupView),
		NewCommandItem(&s, "x", "X", "", nil),
	}
	got := itemRows(groupCommandItems(&s, items, []string{"v", "gone", "v", "a"}))
	require.Equal(t, []string{
		"# Recent", "v", "a",
		"# Session", "s",
		"# Other", "x",
	}, got, "recent commands show once, missing IDs drop, empty sections hide")
}

func TestGroupCommandItemsRecentLimit(t *testing.T) {
	t.Parallel()

	s := styles.CharmtonePantera()
	var items []*CommandItem
	var recent []string
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		items = append(items, NewCommandItem(&s, id, id, "", nil).InGroup(CommandGroupApp))
		recent = append(recent, id)
	}
	got := itemRows(groupCommandItems(&s, items, recent))
	require.Equal(t, []string{
		"# Recent", "a", "b", "c", "d", "e",
		"# App", "f", "g",
	}, got)
}

func TestCommandsRecentSectionOnTop(t *testing.T) {
	t.Parallel()

	c := newTestCommands(t, []string{"quit", "no_such_command", "switch_model"})
	rows := visibleRows(c)
	require.Equal(t, []string{"# Recent", "quit", "switch_model", "# Session"}, rows[:4])
	count := 0
	for _, r := range rows {
		if r == "quit" {
			count++
		}
	}
	require.Equal(t, 1, count, "a recent command is not repeated in its own section")
	require.Equal(t, "quit", selectedCommandID(c), "first command is selected, not the header")
}

func TestCommandsNavigationSkipsHeaders(t *testing.T) {
	t.Parallel()

	c := newTestCommands(t, nil)
	rows := visibleRows(c)
	require.Equal(t, "# Session", rows[0])
	require.Equal(t, rows[1], selectedCommandID(c))

	// Walking down through the whole list never lands on a header and
	// wraps back to the first command.
	seen := map[string]bool{}
	for range len(rows) {
		c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
		id := selectedCommandID(c)
		require.NotEmpty(t, id, "selection landed on a header")
		seen[id] = true
	}
	for _, r := range rows {
		if r[0] != '#' {
			require.True(t, seen[r], "never selected %q", r)
		}
	}

	// Up from the first command wraps to the last.
	c.applyFilter("")
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Equal(t, rows[len(rows)-1], selectedCommandID(c))
	// Down from the last wraps to the first, skipping the leading header.
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Equal(t, rows[1], selectedCommandID(c))
}

func TestCommandsFilteringFlattens(t *testing.T) {
	t.Parallel()

	c := newTestCommands(t, []string{"switch_model"})
	c.applyFilter("tab")
	got := visibleRows(c)
	require.NotEmpty(t, got)
	for _, r := range got {
		require.NotEqual(t, '#', r[0], "headers show while filtering")
	}

	// The ranking matches a flat, ungrouped palette.
	flat := list.NewFilterableList(c.flatItems...)
	flat.SetFilter("tab")
	require.Equal(t, itemRows(flat.FilteredItems()), got)
	require.Equal(t, got[0], selectedCommandID(c))

	// Clearing the filter brings the sections back.
	c.applyFilter("")
	require.Equal(t, "# Recent", visibleRows(c)[0])
}

func TestCommandsChooseRecordsSystemCommand(t *testing.T) {
	t.Parallel()

	c := newTestCommands(t, nil)
	require.Empty(t, c.TakeChosen())
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "new_session", c.TakeChosen())
	require.Empty(t, c.TakeChosen(), "taking clears it")
}

// TestCommandsGolden pins the update-check state and $EDITOR, which the
// default commands read, so it cannot run in parallel.
func TestCommandsGolden(t *testing.T) {
	t.Setenv("TADA_GLOBAL_DATA", t.TempDir())
	t.Setenv("EDITOR", "")

	draw := func(c *Commands) string {
		scr := uv.NewScreenBuffer(100, 40)
		c.Draw(scr, image.Rect(0, 0, 100, 40))
		return scr.String()
	}

	t.Run("Grouped", func(t *testing.T) {
		golden.RequireEqual(t, []byte(draw(newTestCommands(t, nil))))
	})
	t.Run("WithRecent", func(t *testing.T) {
		c := newTestCommands(t, []string{"switch_model", "quit", "toggle_yolo"})
		golden.RequireEqual(t, []byte(draw(c)))
	})
	t.Run("ScrolledToSection", func(t *testing.T) {
		c := newTestCommands(t, nil)
		draw(c)
		// Walk down until the selection is the first command of a later
		// section, which should bring that section's header into view.
		for range 40 {
			c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
			if item, ok := c.list.SelectedItem().(*CommandItem); ok && item.Group() == CommandGroupView {
				break
			}
		}
		golden.RequireEqual(t, []byte(draw(c)))
	})
	t.Run("Filtered", func(t *testing.T) {
		c := newTestCommands(t, []string{"switch_model"})
		draw(c)
		for _, r := range "tab" {
			c.HandleMsg(keyMsg(r))
		}
		golden.RequireEqual(t, []byte(draw(c)))
	})
}
