package prompt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/worktree"
	"github.com/stretchr/testify/require"
)

func TestInDirReadsThatFolder(t *testing.T) {
	t.Parallel()
	launch := t.TempDir()
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(launch, "AGENTS.md"), []byte("launch rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(other, "AGENTS.md"), []byte("other rules"), 0o644))

	store := config.NewTestStore(&config.Config{Options: &config.Options{
		ContextPaths:   []string{"AGENTS.md"},
		DisabledSkills: []string{},
	}})
	tpl := "{{.WorkingDir}}|{{range .ContextFiles}}{{.Content}}{{end}}"
	base, err := NewPrompt("t", tpl, WithWorkingDir(launch))
	require.NoError(t, err)

	got, err := base.Build(t.Context(), "p", "m", store)
	require.NoError(t, err)
	require.Equal(t, filepath.ToSlash(launch)+"|launch rules", got)

	moved := base.InDir(other)
	require.Equal(t, other, moved.WorkingDir())
	got, err = moved.Build(t.Context(), "p", "m", store)
	require.NoError(t, err)
	require.Equal(t, filepath.ToSlash(other)+"|other rules", got)

	// The original prompt is untouched.
	require.Equal(t, launch, base.WorkingDir())
}

func TestWorktreeBranchInPrompt(t *testing.T) {
	t.Parallel()
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
	wt, err := worktree.Create(t.Context(), repo, "feat/prompt", "")
	require.NoError(t, err)

	store := config.NewTestStore(&config.Config{Options: &config.Options{DisabledSkills: []string{}}})
	p, err := NewPrompt("t", "[{{.Worktree}}]", WithWorkingDir(repo))
	require.NoError(t, err)

	got, err := p.Build(t.Context(), "p", "m", store)
	require.NoError(t, err)
	require.Equal(t, "[]", got)

	got, err = p.InDir(wt.Path).Build(t.Context(), "p", "m", store)
	require.NoError(t, err)
	require.Equal(t, "[feat/prompt]", got)
}
