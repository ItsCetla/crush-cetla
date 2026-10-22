package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/stretchr/testify/require"
)

func TestReviewTouched(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	got := reviewTouched(root, sub, []string{
		filepath.Join(root, "a.go"),
		filepath.Join(root, "dir", "b.go"),
		"c.go", // Relative to the working folder.
		filepath.Join(filepath.Dir(root), "outside.go"),
		root,
		"",
	})
	require.Equal(t, map[string]bool{
		"a.go":     true,
		"dir/b.go": true,
		"sub/c.go": true,
	}, got)

	require.Nil(t, reviewTouched("", sub, []string{"a.go"}))
	require.Nil(t, reviewTouched(root, sub, nil))
}

func TestReviewTouchedThroughSymlink(t *testing.T) {
	t.Parallel()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))
	require.NoError(t, os.WriteFile(filepath.Join(real, "x.go"), []byte("x"), 0o644))

	// The repository root is reported resolved while history holds the
	// path the session was opened with, or the other way round.
	require.Equal(t, map[string]bool{"x.go": true},
		reviewTouched(real, link, []string{filepath.Join(link, "x.go")}))
	require.Equal(t, map[string]bool{"x.go": true},
		reviewTouched(link, real, []string{filepath.Join(real, "x.go")}))
}

func TestReviewDraftError(t *testing.T) {
	t.Parallel()
	require.Contains(t, reviewDraftError(agent.ErrNoCommitModel).Error(), "type one below")
	remote := fmt.Errorf("status code 500: %s", agent.ErrNoCommitModel.Error())
	require.Contains(t, reviewDraftError(remote).Error(), "type one below")
	require.Contains(t, reviewDraftError(context.DeadlineExceeded).Error(), "too long")
	require.Equal(t, "Couldn't draft a message: boom", reviewDraftError(errors.New("boom")).Error())
}
