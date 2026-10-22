package dialog

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/gitreview"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newTestReview(t *testing.T) *Review {
	t.Helper()
	s := styles.CharmtonePantera()
	d, act := NewReview(&common.Common{Styles: &s})
	require.Equal(t, ActionReviewStatus{}, act)
	return d
}

func testReviewResult() gitreview.Result {
	return gitreview.Result{
		Repo: gitreview.Repo{Root: "/repo", HasHEAD: true, Branch: "main"},
		Files: []gitreview.File{
			{Path: "a.go", X: '.', Y: 'M', Unstaged: true, Added: 3, Removed: 1},
			{Path: "b.go", X: 'A', Y: '.', Staged: true, Added: 10},
			{Path: "new.txt", Untracked: true, Added: 1},
		},
	}
}

func readyReview(t *testing.T) *Review {
	t.Helper()
	d := newTestReview(t)
	act := d.SetStatus(testReviewResult(), map[string]bool{"b.go": true}, nil)
	require.Equal(t, ActionReviewDiff{Root: "/repo", File: testReviewResult().Files[0]}, act)
	return d
}

func ctrlS() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl} }

func space() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "} }

func TestReviewQuery(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"/review", "/commit", " /COMMIT "} {
		require.True(t, ReviewQuery(v), v)
	}
	for _, v := range []string{"review", "/review now", "/resume"} {
		require.False(t, ReviewQuery(v), v)
	}
}

func TestReviewNotRepo(t *testing.T) {
	t.Parallel()
	d := newTestReview(t)
	require.Nil(t, d.SetStatus(gitreview.Result{}, nil, gitreview.ErrNotRepo))
	view, cur := d.view(120, 40)
	require.Nil(t, cur)
	require.Contains(t, ansi.Strip(view), "isn't a Git repository")
	require.Nil(t, d.HandleMsg(ctrlS()))
	require.Equal(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}

func TestReviewLoadFailed(t *testing.T) {
	t.Parallel()
	d := newTestReview(t)
	require.Nil(t, d.SetStatus(gitreview.Result{}, nil, errors.New("boom\nbang")))
	view, _ := d.view(120, 40)
	require.Contains(t, ansi.Strip(view), "boom bang")
	require.Equal(t, ActionReviewStatus{}, d.HandleMsg(keyMsg('r')))
}

func TestReviewClean(t *testing.T) {
	t.Parallel()
	d := newTestReview(t)
	require.Nil(t, d.SetStatus(gitreview.Result{Repo: gitreview.Repo{Root: "/repo"}}, nil, nil))
	view, _ := d.view(120, 40)
	require.Contains(t, ansi.Strip(view), "Nothing to commit")
}

func TestReviewAllPickedByDefault(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	require.Len(t, d.picked(), 3)
	require.True(t, d.files[1].touched)
	require.False(t, d.files[0].touched)
}

func TestReviewToggles(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	require.Nil(t, d.HandleMsg(space()))
	require.False(t, d.files[0].selected)
	require.Len(t, d.picked(), 2)

	// Not everything is picked, so a picks all.
	d.HandleMsg(keyMsg('a'))
	require.Len(t, d.picked(), 3)

	// Everything is picked, so a clears all.
	d.HandleMsg(keyMsg('a'))
	require.Empty(t, d.picked())
}

func TestReviewMoveLoadsDiffOnce(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	files := testReviewResult().Files
	require.Equal(t, ActionReviewDiff{Root: "/repo", File: files[1]}, d.HandleMsg(keyMsg('j')))
	d.SetDiff("b.go", "", "x\n", nil)
	require.Equal(t, ActionReviewDiff{Root: "/repo", File: files[0]}, d.HandleMsg(keyMsg('k')))
	require.Equal(t, 0, d.cursor)
	// Back on b.go, its diff is cached.
	require.Nil(t, d.HandleMsg(keyMsg('j')))
	require.Equal(t, 1, d.cursor)
	// The cursor stops at the ends.
	d.HandleMsg(keyMsg('j'))
	require.Nil(t, d.HandleMsg(keyMsg('j')))
	require.Equal(t, 2, d.cursor)
}

func TestReviewFocusCycle(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	d.HandleMsg(tab)
	require.Equal(t, reviewFocusDiff, d.focus)
	act := d.HandleMsg(tab)
	require.Equal(t, reviewFocusMessage, d.focus)
	require.IsType(t, ActionCmd{}, act)
	require.True(t, d.ta.Focused())

	// In the message box letters are typed, not commands.
	d.HandleMsg(keyMsg('c'))
	require.Equal(t, "c", d.Message())

	// Esc leaves the message box rather than closing.
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
	require.Equal(t, reviewFocusFiles, d.focus)
	require.False(t, d.ta.Focused())
	require.Equal(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}

func TestReviewDiffFocusScrolls(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Nil(t, d.HandleMsg(keyMsg('j')))
	require.Equal(t, 0, d.cursor)
	require.Equal(t, 1, d.diffOffset)
}

func TestReviewGenerate(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	d.HandleMsg(space())
	act := d.HandleMsg(keyMsg('c'))
	files := testReviewResult().Files
	require.Equal(t, ActionReviewGenerate{Root: "/repo", Files: files[1:]}, act)
	require.True(t, d.generating)
	// A second press while drafting does nothing.
	require.Nil(t, d.HandleMsg(keyMsg('c')))

	act = d.SetGenerated("feat: add b\n", nil)
	require.IsType(t, ActionCmd{}, act)
	require.False(t, d.generating)
	require.Equal(t, "feat: add b", d.Message())
	require.Equal(t, reviewFocusMessage, d.focus)
}

func TestReviewGenerateFailedFocusesMessage(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	d.HandleMsg(keyMsg('c'))
	d.SetGenerated("", errors.New("no model"))
	require.Equal(t, "no model", d.err)
	require.Equal(t, reviewFocusMessage, d.focus)
}

func TestReviewGenerateNeedsPick(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	d.HandleMsg(keyMsg('a'))
	require.Nil(t, d.HandleMsg(keyMsg('c')))
	require.NotEmpty(t, d.err)
}

func TestReviewCommitGuards(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	require.Nil(t, d.HandleMsg(ctrlS()))
	require.Contains(t, d.err, "commit message")

	d.ta.SetValue("fix: things")
	d.HandleMsg(keyMsg('a'))
	require.Nil(t, d.HandleMsg(ctrlS()))
	require.Contains(t, d.err, "Pick at least one file")
}

func TestReviewCommit(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	d.HandleMsg(keyMsg('j'))
	d.HandleMsg(space())
	d.ta.SetValue("  fix: things  ")
	act := d.HandleMsg(ctrlS())
	require.Equal(t, ActionReviewCommit{
		Root:    "/repo",
		Paths:   []string{"a.go", "new.txt"},
		Message: "fix: things",
	}, act)
	require.True(t, d.committing)
	require.Nil(t, d.HandleMsg(ctrlS()))

	// A failed commit keeps the message and shows the error.
	require.Nil(t, d.SetCommitted("", errors.New("hook said no")))
	require.Equal(t, "Commit failed: hook said no", d.err)
	require.Equal(t, "  fix: things  ", d.Message())

	act = d.HandleMsg(ctrlS())
	require.IsType(t, ActionReviewCommit{}, act)
	require.Equal(t, ActionReviewStatus{}, d.SetCommitted("abc1234", nil))
	require.Equal(t, "Committed abc1234", d.notice)
	require.Empty(t, d.Message())
	require.True(t, d.refreshing)
}

func TestReviewCommitFromMessageBox(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	d.HandleMsg(keyMsg('e'))
	require.Equal(t, reviewFocusMessage, d.focus)
	for _, r := range "docs: x" {
		d.HandleMsg(keyMsg(r))
	}
	act := d.HandleMsg(ctrlS())
	require.Equal(t, "docs: x", act.(ActionReviewCommit).Message)
}

func TestReviewReloadKeepsPicksAndCursor(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	d.HandleMsg(space()) // Unpick a.go.
	d.HandleMsg(keyMsg('j'))
	d.SetDiff("b.go", "", "x\n", nil)

	res := testReviewResult()
	res.Files = append([]gitreview.File{{Path: "0.go", Unstaged: true, X: '.', Y: 'M'}}, res.Files...)
	act := d.SetStatus(res, nil, nil)
	require.Equal(t, ActionReviewDiff{Root: "/repo", File: res.Files[2]}, act)
	require.Equal(t, 2, d.cursor)
	require.True(t, d.files[0].selected, "new files start picked")
	require.False(t, d.files[1].selected, "a.go stays unpicked")
	require.True(t, d.files[2].selected)
}

func TestReviewViewFits(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{60, 20}, {100, 30}, {160, 45}, {240, 70}} {
		d := readyReview(t)
		d.SetDiff("a.go", "package a\n\nfunc A() {}\n", "package a\n\nfunc A() int { return 1 }\n", nil)
		view, _ := d.view(size[0], size[1])
		plain := ansi.Strip(view)
		require.Contains(t, plain, "a.go")
		require.Contains(t, plain, "Files")
		for _, l := range strings.Split(view, "\n") {
			require.LessOrEqual(t, ansi.StringWidth(l), size[0], "width %d", size[0])
		}
	}
}

func TestReviewCursorInMessageBox(t *testing.T) {
	t.Parallel()
	d := readyReview(t)
	d.HandleMsg(keyMsg('e'))
	view, cur := d.view(120, 40)
	require.NotNil(t, cur)
	lines := strings.Split(view, "\n")
	require.Less(t, cur.Y, len(lines))
	require.Contains(t, ansi.Strip(lines[cur.Y-1]), "Message")
}
