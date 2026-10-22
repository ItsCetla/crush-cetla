package model

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/worktree"
)

// infoMsgs runs cmd and returns the info messages it produces, looking
// inside batches.
func infoMsgs(cmd tea.Cmd) []util.InfoMsg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case util.InfoMsg:
		return []util.InfoMsg{msg}
	case tea.BatchMsg:
		var out []util.InfoMsg
		for _, c := range msg {
			out = append(out, infoMsgs(c)...)
		}
		return out
	}
	return nil
}

func TestWorktreeTabMark(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "alpha", "beta")
	u.setTabDir("sb", "/wt/feat")
	u.wt.info = map[string]worktreeTabInfo{
		"/wt/feat": {name: "feat", path: "/wt/feat", branch: "feat", managed: true, dirty: 2},
	}
	got := cellText(u.tabCells(u.activeTabIndex(), time.Now(), false))
	require.Contains(t, got, "beta ⎇ feat*")
	require.NotContains(t, got, "alpha ⎇")

	// A clean worktree has no star; one Ta-Da didn't make has no mark.
	u.wt.info["/wt/feat"] = worktreeTabInfo{name: "feat", branch: "feat", managed: true}
	got = cellText(u.tabCells(u.activeTabIndex(), time.Now(), false))
	require.Contains(t, got, "⎇ feat ")
	require.NotContains(t, got, "feat*")

	u.wt.info["/wt/feat"] = worktreeTabInfo{name: "feat", branch: "feat"}
	got = cellText(u.tabCells(u.activeTabIndex(), time.Now(), false))
	require.NotContains(t, got, "⎇")
}

func TestWorktreeTabGitBranch(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "alpha")
	u.gitBranch = "main"
	require.Equal(t, "main", u.tabGitBranch())

	u.session.WorkingDir = "/wt/feat"
	u.wt.info = map[string]worktreeTabInfo{"/wt/feat": {branch: "feat"}}
	require.Equal(t, "feat", u.tabGitBranch())
}

func TestWorktreeCloseHint(t *testing.T) {
	t.Parallel()

	u, _ := newTabsTestUI(t, "alpha", "beta", "gamma")
	u.wt.info = map[string]worktreeTabInfo{
		"/wt/feat": {name: "feat", path: "/wt/feat", branch: "feat", managed: true, dirty: 3},
	}
	u.setTabDir("sb", "/wt/feat")
	u.setTabDir("sc", "/wt/feat")

	// Another tab still works in the worktree: no hint.
	require.Empty(t, infoMsgs(u.closeTab(1)))

	msgs := infoMsgs(u.closeTab(1))
	require.Len(t, msgs, 1)
	require.Equal(t,
		"Worktree ⎇ feat stays on disk with 3 uncommitted change(s). /worktree rm feat removes it.",
		msgs[0].Msg)
}

func removeAction(name string) dialog.ActionRemoveWorktree {
	return dialog.ActionRemoveWorktree{Name: name}
}

// newWorktreeRepo makes a repository with one commit.
func newWorktreeRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"-c", "user.email=t@example.com", "-c", "user.name=T", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	return repo
}

func TestWorktreeListAndRemove(t *testing.T) {
	t.Parallel()

	repo := newWorktreeRepo(t)
	wt, err := worktree.Create(t.Context(), repo, "feat/list", "")
	require.NoError(t, err)

	u, _ := newTabsTestUI(t, "alpha")
	u.session.WorkingDir = wt.Path
	u.setTabDir("sa", wt.Path)

	list, ok := u.listWorktrees()().(worktreeListMsg)
	require.True(t, ok)
	require.NoError(t, list.err)
	require.Len(t, list.entries, 2)
	for _, e := range list.entries {
		require.Equal(t, e.Branch == "feat/list", e.Current, e.Path)
	}

	// The tab works in it, so it stays.
	gone, ok := u.removeWorktree(removeAction("feat/list"))().(worktreeRemovedMsg)
	require.True(t, ok)
	require.ErrorContains(t, gone.err, "an open tab works in feat/list")

	// Moved back home, with a change left behind, it asks first.
	u.session.WorkingDir = repo
	u.setTabDir("sa", repo)
	require.NoError(t, os.WriteFile(filepath.Join(wt.Path, "note.txt"), []byte("x"), 0o644))
	gone, ok = u.removeWorktree(removeAction("feat/list"))().(worktreeRemovedMsg)
	require.True(t, ok)
	require.NoError(t, gone.err)
	require.ErrorIs(t, gone.dirty, worktree.ErrDirty)

	force := removeAction("feat/list")
	force.Force = true
	gone, ok = u.removeWorktree(force)().(worktreeRemovedMsg)
	require.True(t, ok)
	require.NoError(t, gone.err)
	require.NoDirExists(t, wt.Path)

	msgs := infoMsgs(u.handleWorktreeRemoved(gone))
	require.Len(t, msgs, 1)
	require.Equal(t, "Removed worktree feat/list; branch feat/list is kept.", msgs[0].Msg)
}
