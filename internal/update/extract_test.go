package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeArchive(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func TestExtractBinary(t *testing.T) {
	t.Parallel()

	t.Run("tar.gz wrapped in a directory", func(t *testing.T) {
		t.Parallel()
		archive := writeArchive(t, "tada_0.3.0_Linux_x86_64.tar.gz", makeTarGz(t, []archiveEntry{
			{name: "tada_0.3.0_Linux_x86_64/", dir: true},
			{name: "tada_0.3.0_Linux_x86_64/README.md", body: "readme"},
			{name: "tada_0.3.0_Linux_x86_64/completions/tada", body: "not the binary"},
			{name: "tada_0.3.0_Linux_x86_64/tada", body: "new binary"},
		}))
		dest := filepath.Join(t.TempDir(), "new-tada")
		require.NoError(t, ExtractBinary(archive, "tada", dest))
		got, err := os.ReadFile(dest)
		require.NoError(t, err)
		require.Equal(t, "new binary", string(got))
		if runtime.GOOS != "windows" {
			fi, err := os.Stat(dest)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
		}
	})

	t.Run("zip wrapped in a directory", func(t *testing.T) {
		t.Parallel()
		archive := writeArchive(t, "tada_0.3.0_Windows_x86_64.zip", makeZip(t, []archiveEntry{
			{name: "tada_0.3.0_Windows_x86_64/", dir: true},
			{name: "tada_0.3.0_Windows_x86_64/LICENSE.md", body: "license"},
			{name: "tada_0.3.0_Windows_x86_64/tada.exe", body: "new exe"},
		}))
		dest := filepath.Join(t.TempDir(), "new-tada.exe")
		require.NoError(t, ExtractBinary(archive, "tada.exe", dest))
		got, err := os.ReadFile(dest)
		require.NoError(t, err)
		require.Equal(t, "new exe", string(got))
	})

	t.Run("missing binary", func(t *testing.T) {
		t.Parallel()
		archive := writeArchive(t, "x.tar.gz", makeTarGz(t, []archiveEntry{{name: "x/README.md", body: "r"}}))
		require.ErrorIs(t, ExtractBinary(archive, "tada", filepath.Join(t.TempDir(), "out")), ErrBinaryNotFound)
	})

	traversal := []struct {
		name    string
		entries []archiveEntry
	}{
		{"dot dot", []archiveEntry{{name: "../tada", body: "evil"}}},
		{"nested dot dot", []archiveEntry{{name: "tada_0.3.0/../../etc/tada", body: "evil"}}},
		{"absolute", []archiveEntry{{name: "/tmp/tada", body: "evil"}}},
		{"backslash", []archiveEntry{{name: "..\\tada", body: "evil"}}},
		{"drive letter", []archiveEntry{{name: "C:/tada", body: "evil"}}},
	}
	for _, tt := range traversal {
		t.Run("tar.gz rejects "+tt.name, func(t *testing.T) {
			t.Parallel()
			entries := append([]archiveEntry{{name: "tada_0.3.0/tada", body: "ok"}}, tt.entries...)
			archive := writeArchive(t, "a.tar.gz", makeTarGz(t, entries))
			dir := t.TempDir()
			err := ExtractBinary(archive, "tada", filepath.Join(dir, "out"))
			require.Error(t, err)
			require.Contains(t, err.Error(), "unsafe entry")
		})
		t.Run("zip rejects "+tt.name, func(t *testing.T) {
			t.Parallel()
			entries := append([]archiveEntry{{name: "tada_0.3.0/tada.exe", body: "ok"}}, tt.entries...)
			archive := writeArchive(t, "a.zip", makeZip(t, entries))
			dir := t.TempDir()
			err := ExtractBinary(archive, "tada.exe", filepath.Join(dir, "out"))
			require.Error(t, err)
			require.Contains(t, err.Error(), "unsafe entry")
			require.NoFileExists(t, filepath.Join(dir, "out"))
		})
	}

	t.Run("tar.gz rejects an escaping symlink", func(t *testing.T) {
		t.Parallel()
		archive := writeArchive(t, "a.tar.gz", makeTarGz(t, []archiveEntry{
			{name: "tada_0.3.0/link", link: "../../etc/passwd"},
			{name: "tada_0.3.0/tada", body: "ok"},
		}))
		err := ExtractBinary(archive, "tada", filepath.Join(t.TempDir(), "out"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsafe entry")
	})

	t.Run("binary too deep is ignored", func(t *testing.T) {
		t.Parallel()
		archive := writeArchive(t, "a.tar.gz", makeTarGz(t, []archiveEntry{{name: "a/b/tada", body: "nested"}}))
		require.ErrorIs(t, ExtractBinary(archive, "tada", filepath.Join(t.TempDir(), "out")), ErrBinaryNotFound)
	})
}
