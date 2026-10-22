package gitreview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// gitEnv keeps the tests independent of the user's Git setup.
var gitEnv = []string{
	"GIT_AUTHOR_NAME=Test",
	"GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=Test",
	"GIT_COMMITTER_EMAIL=test@example.com",
	"GIT_CONFIG_GLOBAL=" + os.DevNull,
	"GIT_CONFIG_NOSYSTEM=1",
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.CommandContext(context.Background(), "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// newRepo makes a repository whose first commit holds keep.txt,
// edit.txt and gone.txt.
func newRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "config", "user.name", "Test")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "keep.txt", "keep\n")
	write(t, dir, "edit.txt", "one\ntwo\nthree\n")
	write(t, dir, "gone.txt", "bye\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "first")
	return dir
}

func byPath(files []File) map[string]File {
	m := map[string]File{}
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

func TestStatus(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)

	write(t, dir, "edit.txt", "one\n2\nthree\nfour\n")
	require.NoError(t, os.Remove(filepath.Join(dir, "gone.txt")))
	write(t, dir, "new dir/new file.txt", "a\nb\n")
	write(t, dir, "staged.txt", "s\n")
	git(t, dir, "add", "staged.txt")
	write(t, dir, "bin.dat", "\x00\x01\x02")

	res, err := Status(t.Context(), filepath.Join(dir, "new dir"))
	require.NoError(t, err)
	require.True(t, res.HasHEAD)
	require.Equal(t, "main", res.Branch)
	root, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotRoot, err := filepath.EvalSymlinks(res.Root)
	require.NoError(t, err)
	require.Equal(t, root, gotRoot)

	files := byPath(res.Files)
	require.Len(t, files, 5)

	edit := files["edit.txt"]
	require.Equal(t, "M", edit.Code())
	require.True(t, edit.Unstaged)
	require.False(t, edit.Staged)
	require.Equal(t, 2, edit.Added)
	require.Equal(t, 1, edit.Removed)

	gone := files["gone.txt"]
	require.Equal(t, "D", gone.Code())
	require.Equal(t, 1, gone.Removed)

	newFile := files["new dir/new file.txt"]
	require.True(t, newFile.Untracked)
	require.Equal(t, "?", newFile.Code())
	require.Equal(t, 2, newFile.Added)

	staged := files["staged.txt"]
	require.True(t, staged.Staged)
	require.Equal(t, "A", staged.Code())
	require.Equal(t, 1, staged.Added)

	require.True(t, files["bin.dat"].Binary)
}

func TestNotRepo(t *testing.T) {
	t.Parallel()
	requireGit(t)
	dir := t.TempDir()

	_, err := Status(t.Context(), dir)
	require.ErrorIs(t, err, ErrNotRepo)

	_, err = Open(t.Context(), filepath.Join(dir, "missing"))
	require.ErrorIs(t, err, ErrNotRepo)
}

func TestFileAtHEAD(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "edit.txt", "changed\n")

	got, err := FileAtHEAD(t.Context(), dir, "edit.txt")
	require.NoError(t, err)
	require.Equal(t, "one\ntwo\nthree\n", got)

	got, err = FileAtHEAD(t.Context(), dir, "never.txt")
	require.NoError(t, err)
	require.Empty(t, got)

	got, err = FileInWorkTree(dir, "edit.txt")
	require.NoError(t, err)
	require.Equal(t, "changed\n", got)

	got, err = FileInWorkTree(dir, "never.txt")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestDiffAndLog(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "edit.txt", "one\nTWO\nthree\n")
	write(t, dir, "fresh.txt", "hello\n")

	res, err := Status(t.Context(), dir)
	require.NoError(t, err)
	diff, err := Diff(t.Context(), dir, res.Files)
	require.NoError(t, err)
	require.Contains(t, diff, "+TWO")
	require.Contains(t, diff, "-two")
	require.Contains(t, diff, "+hello")
	require.Contains(t, diff, "fresh.txt")

	log, err := Log(t.Context(), dir, 10)
	require.NoError(t, err)
	require.Contains(t, log, "first")
}

func TestCommitOnlyChosenPaths(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	write(t, dir, "edit.txt", "edited\n")
	require.NoError(t, os.Remove(filepath.Join(dir, "gone.txt")))
	write(t, dir, "fresh.txt", "new\n")
	// Staged but left out of the commit, so it must stay staged.
	write(t, dir, "other.txt", "other\n")
	git(t, dir, "add", "other.txt")

	hash, err := Commit(t.Context(), dir, []string{"edit.txt", "gone.txt", "fresh.txt"}, "feat: change things\n\nBody line.\n")
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	require.Equal(t, "feat: change things\n\nBody line.", strings.TrimSpace(git(t, dir, "log", "-1", "--format=%B")))
	require.True(t, strings.HasPrefix(git(t, dir, "rev-parse", "HEAD"), hash))

	show := git(t, dir, "show", "--name-status", "--format=", "HEAD")
	require.Contains(t, show, "M\tedit.txt")
	require.Contains(t, show, "D\tgone.txt")
	require.Contains(t, show, "A\tfresh.txt")
	require.NotContains(t, show, "other.txt")

	res, err := Status(t.Context(), dir)
	require.NoError(t, err)
	require.Len(t, res.Files, 1)
	require.Equal(t, "other.txt", res.Files[0].Path)
	require.True(t, res.Files[0].Staged)
}

func TestCommitFirstCommit(t *testing.T) {
	t.Parallel()
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "config", "user.name", "Test")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "a.txt", "a\n")

	res, err := Status(t.Context(), dir)
	require.NoError(t, err)
	require.False(t, res.HasHEAD)
	require.Len(t, res.Files, 1)

	got, err := FileAtHEAD(t.Context(), dir, "a.txt")
	require.NoError(t, err)
	require.Empty(t, got)

	log, err := Log(t.Context(), dir, 10)
	require.NoError(t, err)
	require.Empty(t, log)

	diff, err := Diff(t.Context(), dir, res.Files)
	require.NoError(t, err)
	require.Contains(t, diff, "+a")

	hash, err := Commit(t.Context(), dir, []string{"a.txt"}, "chore: start")
	require.NoError(t, err)
	require.NotEmpty(t, hash)
}

func TestCommitHookFailure(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o755))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'lint failed' >&2\nexit 1\n"), 0o755))
	write(t, dir, "edit.txt", "edited\n")

	_, err := Commit(t.Context(), dir, []string{"edit.txt"}, "fix: thing")
	require.Error(t, err)
	require.Contains(t, err.Error(), "lint failed")
}

func TestCommitGuards(t *testing.T) {
	t.Parallel()
	_, err := Commit(t.Context(), t.TempDir(), nil, "msg")
	require.ErrorIs(t, err, ErrNoPaths)
	_, err = Commit(t.Context(), t.TempDir(), []string{"a"}, "  \n")
	require.ErrorIs(t, err, ErrEmptyMessage)
}

func TestParseStatusRename(t *testing.T) {
	t.Parallel()
	out := "2 R. N... 100644 100644 100644 abc def R100 new name.go\x00old name.go\x00" +
		"u UU N... 100644 100644 100644 100644 a b c conflict.go\x00" +
		"? untracked.go\x00"
	files := parseStatus(out)
	require.Len(t, files, 3)
	require.Equal(t, "new name.go", files[0].Path)
	require.Equal(t, "old name.go", files[0].OrigPath)
	require.Equal(t, "R", files[0].Code())
	require.Equal(t, []string{"old name.go", "new name.go"}, files[0].Paths())
	require.True(t, files[1].Conflicted)
	require.Equal(t, "U", files[1].Code())
	require.True(t, files[2].Untracked)
}

func TestParseNumstat(t *testing.T) {
	t.Parallel()
	out := "3\t1\ta.go\x00-\t-\timg.png\x002\t0\t\x00old.go\x00new.go\x00"
	counts := parseNumstat(out)
	require.Equal(t, Counts{Added: 3, Removed: 1}, counts["a.go"])
	require.Equal(t, Counts{Binary: true}, counts["img.png"])
	require.Equal(t, Counts{Added: 2}, counts["new.go"])
}

func TestTruncateDiff(t *testing.T) {
	t.Parallel()
	require.Equal(t, "short", TruncateDiff("short", 100))

	diff := strings.Repeat("line\n", 100)
	got := TruncateDiff(diff, 22)
	require.True(t, strings.HasPrefix(got, "line\nline\nline\nline\n\n[diff truncated"))
	require.Contains(t, got, "of 500 bytes")
}
