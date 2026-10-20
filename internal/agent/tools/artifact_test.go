package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutsidePathsFollowsSymlinks(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	wd := filepath.Join(base, "project")
	secret := filepath.Join(base, "secret")
	require.NoError(t, os.MkdirAll(wd, 0o700))
	require.NoError(t, os.MkdirAll(secret, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(secret, "key.txt"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(wd, "page.html"), []byte("x"), 0o600))

	fileLink := filepath.Join(wd, "notes.txt")
	dirLink := filepath.Join(wd, "docs")
	if err := os.Symlink(filepath.Join(secret, "key.txt"), fileLink); err != nil {
		t.Skipf("Symlinks unavailable: %v", err)
	}
	require.NoError(t, os.Symlink(secret, dirLink))

	inside := []string{
		filepath.Join(wd, "page.html"),
		filepath.Join(wd, "not-yet.html"),
		filepath.Join(wd, "sub", "missing.css"),
	}
	require.Empty(t, outsidePaths(wd, inside))

	got := outsidePaths(wd, []string{fileLink, filepath.Join(dirLink, "key.txt")})
	require.Len(t, got, 2)
	real, err := filepath.EvalSymlinks(filepath.Join(secret, "key.txt"))
	require.NoError(t, err)
	require.Equal(t, []string{real, real}, got)

	// A working directory reached through a symlink still contains its
	// own files.
	wdLink := filepath.Join(base, "via-link")
	require.NoError(t, os.Symlink(wd, wdLink))
	require.Empty(t, outsidePaths(wdLink, []string{filepath.Join(wd, "page.html"), filepath.Join(wdLink, "new.html")}))
}
