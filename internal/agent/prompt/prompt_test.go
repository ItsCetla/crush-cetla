package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
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
