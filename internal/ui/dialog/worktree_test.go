package dialog

import (
	"image"
	"testing"

	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/worktree"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newTestWorktreeCommon() *common.Common {
	s := styles.CharmtonePantera()
	return &common.Common{Styles: &s}
}

func TestWorktreeQuery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		ok   bool
		want WorktreeCommand
	}{
		{in: "/worktree", ok: true},
		{in: "  /WT  ", ok: true},
		{in: "/worktrees feat/x", ok: true, want: WorktreeCommand{Name: "feat/x"}},
		{in: "/wt feat/x main", ok: true, want: WorktreeCommand{Name: "feat/x", Base: "main"}},
		{in: "/wt a b c", ok: true, want: WorktreeCommand{Err: "Usage: /worktree <name> [base]"}},
		{in: "/worktree rm feat/x", ok: true, want: WorktreeCommand{Remove: true, Name: "feat/x"}},
		{in: "/worktree remove feat/x", ok: true, want: WorktreeCommand{Remove: true, Name: "feat/x"}},
		{in: "/worktree rm", ok: true, want: WorktreeCommand{Remove: true, Err: "Usage: /worktree rm <name>"}},
		{in: "/worktree rm a b", ok: true, want: WorktreeCommand{Remove: true, Err: "Usage: /worktree rm <name> (one worktree at a time)"}},
		{in: "/worktree a\nb"},
		{in: "/worktreex"},
		{in: "worktree a"},
		{in: ""},
	}
	for _, c := range cases {
		got, ok := WorktreeQuery(c.in)
		require.Equal(t, c.ok, ok, c.in)
		require.Equal(t, c.want, got, c.in)
	}
}

func TestWorktreeCommandAction(t *testing.T) {
	t.Parallel()

	require.Equal(t, ActionOpenWorktrees{}, WorktreeCommand{}.Action())
	require.Equal(t, ActionWorktree{Name: "a", Base: "b"}, WorktreeCommand{Name: "a", Base: "b"}.Action())
	require.Equal(t, ActionRemoveWorktree{Name: "a"}, WorktreeCommand{Remove: true, Name: "a"}.Action())

	act, ok := WorktreeCommand{Remove: true, Err: "Usage"}.Action().(ActionCmd)
	require.True(t, ok)
	require.NotNil(t, act.Cmd)
}

func TestSortWorktrees(t *testing.T) {
	t.Parallel()

	entries := []WorktreeEntry{
		{Worktree: worktree.Worktree{Path: "/r", Branch: "main", Main: true}},
		{Worktree: worktree.Worktree{Path: "/o", Branch: "other"}},
		{Worktree: worktree.Worktree{Path: "/z", Branch: "zed", Managed: true}},
		{Worktree: worktree.Worktree{Path: "/a", Branch: "alpha", Managed: true}},
	}
	SortWorktrees(entries)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.Equal(t, []string{"alpha", "zed", "other", "main"}, names)
}

func testWorktreeEntries() []WorktreeEntry {
	return []WorktreeEntry{
		{Worktree: worktree.Worktree{Path: "/repo", Branch: "main", Main: true}},
		{
			Worktree: worktree.Worktree{Path: "/wt/feat", Branch: "feat", Managed: true},
			Current:  true,
			State:    worktree.State{Dirty: 2, Ahead: 1},
		},
		{Worktree: worktree.Worktree{Path: "/wt/gone", Branch: "gone", Managed: true, Prunable: true}},
	}
}

func TestWorktreesPickerKeys(t *testing.T) {
	t.Parallel()

	d := NewWorktrees(newTestWorktreeCommon())
	d.SetEntries(testWorktreeEntries(), nil)

	// Sorted: feat, gone, main. The cursor starts on the current one.
	require.Equal(t, "feat", d.selected().Name())
	require.Equal(t, ActionSwitchWorktree{Path: "/wt/feat"}, d.HandleMsg(enter()))
	require.Equal(t, ActionNewWorktreeTab{}, d.HandleMsg(keyMsg('n')))

	// A worktree whose folder is gone can't be entered.
	require.Nil(t, d.HandleMsg(keyMsg('j')))
	require.Equal(t, "gone", d.selected().Name())
	require.Nil(t, d.HandleMsg(enter()))
	require.NotEmpty(t, d.err)

	// The main checkout can't be removed.
	d.HandleMsg(keyMsg('j'))
	require.True(t, d.selected().Main)
	require.Nil(t, d.HandleMsg(keyMsg('d')))
	require.Contains(t, d.err, "main checkout")

	// The cursor wraps both ways.
	d.HandleMsg(keyMsg('j'))
	require.Equal(t, "feat", d.selected().Name())
	d.HandleMsg(keyMsg('k'))
	require.True(t, d.selected().Main)

	// Removing marks the picker busy until a notice arrives.
	d.HandleMsg(keyMsg('k'))
	d.HandleMsg(keyMsg('k'))
	require.Equal(t, ActionRemoveWorktree{Name: "feat", Path: "/wt/feat"}, d.HandleMsg(keyMsg('d')))
	require.True(t, d.busy)
	require.Nil(t, d.HandleMsg(enter()))
	d.SetNotice("Removed", nil)
	require.False(t, d.busy)

	// A refreshed list keeps the cursor where it was.
	d.HandleMsg(keyMsg('j'))
	d.SetEntries(testWorktreeEntries(), nil)
	require.Equal(t, "gone", d.selected().Name())
}

func TestWorktreesPickerDraw(t *testing.T) {
	t.Parallel()

	d := NewWorktrees(newTestWorktreeCommon())
	d.SetEntries(testWorktreeEntries(), nil)
	scr := uv.NewScreenBuffer(100, 30)
	d.Draw(scr, image.Rect(0, 0, 100, 30))
	out := ansi.Strip(scr.Render())
	require.Contains(t, out, "Worktrees")
	require.Contains(t, out, "feat")
	require.Contains(t, out, "2 changed")
	require.Contains(t, out, "main checkout")
	require.Contains(t, out, "folder missing")

	// A tiny screen still draws without panicking.
	small := uv.NewScreenBuffer(10, 4)
	d.Draw(small, image.Rect(0, 0, 10, 4))
}

func TestWorktreeConfirmKeys(t *testing.T) {
	t.Parallel()

	d := NewWorktreeConfirm(newTestWorktreeCommon(), "feat", "/wt/feat", "3 changed")
	require.Equal(t, ActionRemoveWorktree{Name: "feat", Path: "/wt/feat", Force: true}, d.HandleMsg(keyMsg('y')))
	require.Equal(t, ActionClose{}, d.HandleMsg(keyMsg('n')))
	require.Equal(t, ActionClose{}, d.HandleMsg(enter()))

	scr := uv.NewScreenBuffer(80, 20)
	d.Draw(scr, image.Rect(0, 0, 80, 20))
	out := ansi.Strip(scr.Render())
	require.Contains(t, out, "for good")
	require.Contains(t, out, "branch itself is kept")
}
