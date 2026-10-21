package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/workdir"
	"github.com/stretchr/testify/require"
)

func TestSystemPromptForFollowsTabFolder(t *testing.T) {
	t.Parallel()
	a := &sessionAgent{
		workDir:      "/launch",
		systemPrompt: csync.NewValue("launch prompt"),
		promptForDir: func(_ context.Context, dir string) (string, error) {
			if dir == filepath.Clean("/broken") {
				return "", errors.New("boom")
			}
			return "prompt for " + dir, nil
		},
	}
	ctx := context.Background()
	require.Equal(t, "launch prompt", a.systemPromptFor(ctx))
	require.Equal(t, "launch prompt", a.systemPromptFor(workdir.With(ctx, "/launch/")))
	require.Equal(t, "prompt for "+filepath.Clean("/other"), a.systemPromptFor(workdir.With(ctx, "/other")))
	require.Equal(t, "launch prompt", a.systemPromptFor(workdir.With(ctx, "/broken")))
}
