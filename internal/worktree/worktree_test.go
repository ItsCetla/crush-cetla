package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
}

// runGit runs git in dir and returns its trimmed output.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)
	cmd := exec.CommandContext(context.Background(), "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return strings.TrimSpace(string(out))
}

// newRepo creates a repository with one commit on "main" at <tmp>/app and
// returns its canonical path. Managed worktrees then land in
// <tmp>/.tada-worktrees/app, inside the test's temp dir.
func newRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	repo := filepath.Join(tmp, "app")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	runGit(t, repo, "init", "--initial-branch=main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	commitFile(t, repo, "README.md", "hello\n", "initial commit")
	return repo
}

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", msg)
}

func TestSanitize(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"feature":             "feature",
		"  fix login bug  ":   "fix-login-bug",
		"feat/new-ui":         "feat/new-ui",
		"../../etc/passwd":    "etc/passwd",
		"a..b":                "a.b",
		"what?*[]~^:\\":       "what",
		"-leading":            "leading",
		".hidden/.dot":        "hidden/dot",
		"trailing.lock":       "trailing",
		"x//y":                "x/y",
		"ünïcode":             "ünïcode",
		"--":                  "",
		"@{u}":                "u",
		"":                    "",
		"tab\tand  spaces":    "tab-and-spaces",
		"/abs/path/":          "abs/path",
		"name.lock.lock":      "name",
		"refs/heads/my-thing": "refs/heads/my-thing",
	}
	for in, want := range cases {
		require.Equal(t, want, Sanitize(in), "Sanitize(%q)", in)
	}
}

func TestLocation(t *testing.T) {
	t.Parallel()
	repo := filepath.Join(string(filepath.Separator), "src", "app")
	require.Equal(t, filepath.Join(string(filepath.Separator), "src", DirName, "app"), Location(repo))
	require.Equal(t,
		filepath.Join(string(filepath.Separator), "wt", "app"),
		Location(repo, WithDir(filepath.Join(string(filepath.Separator), "wt"))))
	require.Equal(t,
		filepath.Join(string(filepath.Separator), "src", "trees", "app"),
		Location(repo, WithDir(filepath.Join("..", "trees"))))
	require.Equal(t, Location(repo), Location(repo, WithDir("")))
}

func TestRootAndMainRoot(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)
	sub := filepath.Join(repo, "pkg")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	root, err := Root(ctx, sub)
	require.NoError(t, err)
	require.Equal(t, repo, root)

	wt, err := Create(ctx, repo, "feature", "")
	require.NoError(t, err)

	root, err = Root(ctx, wt.Path)
	require.NoError(t, err)
	require.Equal(t, wt.Path, root)

	main, err := MainRoot(ctx, wt.Path)
	require.NoError(t, err)
	require.Equal(t, repo, main)

	_, err = Root(ctx, t.TempDir())
	require.ErrorIs(t, err, ErrNotRepo)
}

func TestCreateNewBranch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)

	wt, err := Create(ctx, repo, "feat/new ui", "")
	require.NoError(t, err)
	require.Equal(t, "feat/new-ui", wt.Branch)
	require.Equal(t, filepath.Join(filepath.Dir(repo), DirName, "app", "feat-new-ui"), wt.Path)
	require.True(t, wt.Managed)
	require.False(t, wt.Main)
	require.Equal(t, "main", wt.Base)
	require.Equal(t, repo, wt.Repo)
	require.FileExists(t, filepath.Join(wt.Path, "README.md"))
	require.Equal(t, "feat/new-ui", runGit(t, wt.Path, "branch", "--show-current"))

	// The main checkout is untouched and does not see the worktree as a
	// change.
	require.Equal(t, "main", runGit(t, repo, "branch", "--show-current"))
	require.Empty(t, runGit(t, repo, "status", "--porcelain"))
}

func TestCreateFromBase(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)
	runGit(t, repo, "checkout", "-b", "release")
	commitFile(t, repo, "release.txt", "r\n", "release commit")
	runGit(t, repo, "checkout", "main")

	wt, err := Create(ctx, repo, "hotfix", "release")
	require.NoError(t, err)
	require.Equal(t, "release", wt.Base)
	require.FileExists(t, filepath.Join(wt.Path, "release.txt"))

	_, err = Create(ctx, repo, "other", "no-such-branch")
	require.ErrorContains(t, err, "unknown base")
}

func TestCreateExistingBranch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)
	runGit(t, repo, "branch", "existing")
	runGit(t, repo, "checkout", "existing")
	commitFile(t, repo, "existing.txt", "e\n", "on existing")
	runGit(t, repo, "checkout", "main")

	wt, err := Create(ctx, repo, "existing", "")
	require.NoError(t, err)
	require.Equal(t, "existing", wt.Branch)
	require.True(t, wt.Managed)
	require.FileExists(t, filepath.Join(wt.Path, "existing.txt"))
}

func TestCreateReusesCheckedOutBranch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)

	first, err := Create(ctx, repo, "reuse", "")
	require.NoError(t, err)
	second, err := Create(ctx, repo, "reuse", "")
	require.NoError(t, err)
	require.Equal(t, first.Path, second.Path)

	// Creating from inside the worktree finds the same one too.
	third, err := Create(ctx, first.Path, "reuse", "")
	require.NoError(t, err)
	require.Equal(t, first.Path, third.Path)

	// Naming the main checkout's branch returns the main checkout.
	main, err := Create(ctx, repo, "main", "")
	require.NoError(t, err)
	require.True(t, main.Main)
	require.Equal(t, repo, main.Path)

	list, err := List(ctx, repo)
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestCreateFromWorktreeBranchesFromItsBranch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)
	parent, err := Create(ctx, repo, "parent", "")
	require.NoError(t, err)
	commitFile(t, parent.Path, "p.txt", "p\n", "parent work")

	child, err := Create(ctx, parent.Path, "child", "")
	require.NoError(t, err)
	require.Equal(t, "parent", child.Base)
	require.FileExists(t, filepath.Join(child.Path, "p.txt"))
	// Linked worktrees still land next to the main checkout.
	require.Equal(t, filepath.Join(Location(repo), "child"), child.Path)
}

func TestCreateWithDir(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	wt, err := Create(ctx, repo, "custom", "", WithDir(dir))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "app", "custom"), wt.Path)
	require.True(t, wt.Managed)

	// The marker keeps it managed even without the option.
	list, err := List(ctx, repo)
	require.NoError(t, err)
	got, ok := Find(list, "custom")
	require.True(t, ok)
	require.True(t, got.Managed)
}

func TestCreateInvalid(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)

	_, err := Create(ctx, repo, "  ?*  ", "")
	require.ErrorIs(t, err, ErrInvalidName)

	_, err = Create(ctx, t.TempDir(), "x", "")
	require.ErrorIs(t, err, ErrNotRepo)

	// A non-empty directory in the way is not overwritten.
	blocker := filepath.Join(Location(repo), "blocked")
	require.NoError(t, os.MkdirAll(blocker, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(blocker, "keep.txt"), []byte("x"), 0o644))
	_, err = Create(ctx, repo, "blocked", "")
	require.ErrorContains(t, err, "already exists")
	require.FileExists(t, filepath.Join(blocker, "keep.txt"))
}

func TestCreateEmptyRepo(t *testing.T) {
	t.Parallel()
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "--initial-branch=main")
	_, err := Create(t.Context(), repo, "x", "")
	require.ErrorContains(t, err, "no commits")
}

func TestList(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)

	managed, err := Create(ctx, repo, "managed", "")
	require.NoError(t, err)

	// A worktree made by hand elsewhere is listed but not managed.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	manual := filepath.Join(tmp, "manual")
	runGit(t, repo, "worktree", "add", "-b", "manual", manual)

	// A detached worktree.
	detached := filepath.Join(tmp, "detached")
	runGit(t, repo, "worktree", "add", "--detach", detached)
	runGit(t, repo, "worktree", "lock", detached)

	list, err := List(ctx, managed.Path)
	require.NoError(t, err)
	require.Len(t, list, 4)

	require.True(t, list[0].Main)
	require.Equal(t, repo, list[0].Path)
	require.Equal(t, "main", list[0].Branch)
	require.False(t, list[0].Managed)
	for _, wt := range list {
		require.Equal(t, repo, wt.Repo)
		require.NotEmpty(t, wt.Head)
	}

	got, ok := Find(list, "managed")
	require.True(t, ok)
	require.True(t, got.Managed)
	require.Equal(t, "main", got.Base)

	got, ok = Find(list, "manual")
	require.True(t, ok)
	require.False(t, got.Managed)
	require.Equal(t, manual, got.Path)

	got, ok = Find(list, "detached")
	require.True(t, ok)
	require.True(t, got.Detached)
	require.True(t, got.Locked)
	require.Empty(t, got.Branch)
	require.Equal(t, "detached", got.Name())

	_, ok = Find(list, "nope")
	require.False(t, ok)

	_, err = List(ctx, t.TempDir())
	require.ErrorIs(t, err, ErrNotRepo)
}

func TestListPrunable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)
	wt, err := Create(ctx, repo, "gone", "")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(wt.Path))

	list, err := List(ctx, repo)
	require.NoError(t, err)
	got, ok := Find(list, "gone")
	require.True(t, ok)
	require.True(t, got.Prunable)
	// The marker went with the directory's .git file, but the location
	// still identifies it.
	require.True(t, got.Managed)
}

func TestParsePorcelain(t *testing.T) {
	t.Parallel()
	out := strings.Join([]string{
		"worktree /src/app",
		"HEAD 1111111111111111111111111111111111111111",
		"branch refs/heads/main",
		"",
		"worktree /src/.tada-worktrees/app/feat-x",
		"HEAD 2222222222222222222222222222222222222222",
		"branch refs/heads/feat/x",
		"locked",
		"",
		"worktree /tmp/gone",
		"HEAD 3333333333333333333333333333333333333333",
		"detached",
		"prunable gitdir file points to non-existent location",
		"",
	}, "\n")
	list := parsePorcelain(out)
	require.Len(t, list, 3)
	require.True(t, list[0].Main)
	require.Equal(t, "main", list[0].Branch)
	require.Equal(t, "feat/x", list[1].Branch)
	require.True(t, list[1].Locked)
	require.False(t, list[1].Main)
	require.True(t, list[2].Detached)
	require.True(t, list[2].Prunable)

	bare := "worktree /src/app.git\nbare\n\nworktree /src/wt\nHEAD 1\nbranch refs/heads/x\n"
	list = parsePorcelain(bare)
	require.Len(t, list, 1)
	require.False(t, list[0].Main)
	require.Equal(t, "x", list[0].Branch)
}

func TestStatus(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)
	wt, err := Create(ctx, repo, "status", "")
	require.NoError(t, err)

	st, err := Status(ctx, wt)
	require.NoError(t, err)
	require.Equal(t, State{Base: "main"}, st)

	commitFile(t, wt.Path, "a.txt", "a\n", "a")
	commitFile(t, wt.Path, "b.txt", "b\n", "b")
	commitFile(t, repo, "m.txt", "m\n", "main moves on")
	require.NoError(t, os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("changed\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("new\n"), 0o644))

	st, err = Status(ctx, wt)
	require.NoError(t, err)
	require.Equal(t, State{Dirty: 2, Ahead: 2, Behind: 1, Base: "main"}, st)

	// Without Base on the value, the marker supplies it.
	st, err = Status(ctx, Worktree{Path: wt.Path})
	require.NoError(t, err)
	require.Equal(t, "main", st.Base)
	require.Equal(t, 2, st.Ahead)

	// A base that no longer exists still reports the dirty count.
	st, err = Status(ctx, Worktree{Path: wt.Path, Base: "deleted-branch"})
	require.NoError(t, err)
	require.Equal(t, 2, st.Dirty)
	require.Zero(t, st.Ahead)

	// The main checkout has no base.
	st, err = Status(ctx, Worktree{Path: repo, Main: true})
	require.NoError(t, err)
	require.Zero(t, st.Dirty)
	require.Empty(t, st.Base)
}

func TestRemove(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)

	clean, err := Create(ctx, repo, "clean", "")
	require.NoError(t, err)
	require.NoError(t, Remove(ctx, clean, false))
	require.NoDirExists(t, clean.Path)
	// The branch is kept.
	runGit(t, repo, "rev-parse", "--verify", "refs/heads/clean")

	dirty, err := Create(ctx, repo, "dirty", "")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dirty.Path, "wip.txt"), []byte("wip"), 0o644))
	err = Remove(ctx, dirty, false)
	require.ErrorIs(t, err, ErrDirty)
	require.ErrorContains(t, err, "1 changed path")
	require.FileExists(t, filepath.Join(dirty.Path, "wip.txt"))

	require.NoError(t, Remove(ctx, dirty, true))
	require.NoDirExists(t, dirty.Path)

	list, err := List(ctx, repo)
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.ErrorIs(t, Remove(ctx, list[0], true), ErrMain)
	require.ErrorIs(t, Remove(ctx, Worktree{Path: repo}, true), ErrMain)
}

func TestRemoveWithoutRepo(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)
	wt, err := Create(ctx, repo, "norepo", "")
	require.NoError(t, err)
	require.NoError(t, Remove(ctx, Worktree{Path: wt.Path, Branch: wt.Branch}, false))
	require.NoDirExists(t, wt.Path)
}

func TestLookup(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo := newRepo(t)

	_, ok := Lookup(repo)
	require.False(t, ok, "the main checkout is not a linked worktree")
	_, ok = Lookup(t.TempDir())
	require.False(t, ok)

	wt, err := Create(ctx, repo, "feat/look", "")
	require.NoError(t, err)
	sub := filepath.Join(wt.Path, "deep", "dir")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	for _, dir := range []string{wt.Path, sub} {
		got, ok := Lookup(dir)
		require.True(t, ok, dir)
		require.Equal(t, wt.Path, got.Path)
		require.Equal(t, "feat/look", got.Branch)
		require.Equal(t, repo, got.Repo)
		require.Equal(t, "main", got.Base)
		require.True(t, got.Managed)
	}

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	manual := filepath.Join(tmp, "manual")
	runGit(t, repo, "worktree", "add", "--detach", manual)
	got, ok := Lookup(manual)
	require.True(t, ok)
	require.False(t, got.Managed)
	require.True(t, got.Detached)
	require.Empty(t, got.Branch)
}
