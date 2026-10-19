package update

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplaceExecutable(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (target, newBin string) {
		t.Helper()
		dir := t.TempDir()
		target = filepath.Join(dir, "tada")
		require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))
		newBin = filepath.Join(t.TempDir(), "new-tada")
		require.NoError(t, os.WriteFile(newBin, []byte("new"), 0o600))
		return target, newBin
	}

	t.Run("unix", func(t *testing.T) {
		t.Parallel()
		target, newBin := setup(t)
		require.NoError(t, ReplaceExecutable(target, newBin, "linux"))
		got, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Equal(t, "new", string(got))
		if runtime.GOOS != "windows" {
			fi, err := os.Stat(target)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
		}
		entries, err := os.ReadDir(filepath.Dir(target))
		require.NoError(t, err)
		require.Len(t, entries, 1, "no temp files left behind")
	})

	t.Run("windows keeps the old file aside", func(t *testing.T) {
		t.Parallel()
		target, newBin := setup(t)
		require.NoError(t, os.WriteFile(target+".old", []byte("older"), 0o600))
		require.NoError(t, ReplaceExecutable(target, newBin, "windows"))
		got, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Equal(t, "new", string(got))
		old, err := os.ReadFile(target + ".old")
		require.NoError(t, err)
		require.Equal(t, "old", string(old))

		CleanupOld(target)
		require.NoFileExists(t, target+".old")
		require.FileExists(t, target)
	})

	t.Run("read-only directory", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs Unix permissions and a non-root user")
		}
		target, newBin := setup(t)
		dir := filepath.Dir(target)
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		err := ReplaceExecutable(target, newBin, "linux")
		var nw *ErrNotWritable
		require.True(t, errors.As(err, &nw), "got %v", err)
		require.Equal(t, dir, nw.Dir)
		require.Contains(t, err.Error(), "sudo")
		require.Contains(t, err.Error(), ReleasesPage)
	})
}
