package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// isolateConfigDirs points every global config location at a temporary
// directory and returns the global config and global data directories.
func isolateConfigDirs(t *testing.T) (string, string) {
	t.Helper()
	isolated := t.TempDir()
	cfgDir := filepath.Join(isolated, "config")
	dataDir := filepath.Join(isolated, "data")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("TADA_GLOBAL_CONFIG", cfgDir)
	t.Setenv("TADA_GLOBAL_DATA", dataDir)
	t.Setenv(TrustProjectEnv, "")
	return cfgDir, dataDir
}

func TestFieldSources(t *testing.T) {
	cfgDir, globalData := isolateConfigDirs(t)
	t.Setenv(TrustProjectEnv, "1")

	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "tadarc"),
		[]byte("option ui compact true\noption ui mouse false\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(globalData, "tada.json"),
		[]byte(`{"options":{"tui":{"mouse":true,"notify_bell":true}}}`), 0o644))

	workDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workDir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "tada.json"),
		[]byte(`{"options":{"tui":{"diff_mode":"split"}}}`), 0o644))
	dataDir := filepath.Join(workDir, ".tada")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "tada.json"),
		[]byte(`{"options":{"tui":{"vertical_tabs":true}}}`), 0o644))

	keys := []string{
		"options.tui.compact_mode",
		"options.tui.mouse",
		"options.tui.notify_bell",
		"options.tui.diff_mode",
		"options.tui.vertical_tabs",
		"options.tui.ambient",
	}
	got, err := FieldSources(t.Context(), workDir, dataDir, keys)
	require.NoError(t, err)

	require.Equal(t, FieldSourceGlobal, got["options.tui.compact_mode"].Kind)
	require.True(t, got["options.tui.compact_mode"].Shell)

	// The global data config merges after tadarc, so it wins.
	require.Equal(t, FieldSourceGlobal, got["options.tui.mouse"].Kind)
	require.False(t, got["options.tui.mouse"].Shell)
	require.Equal(t, filepath.Join(globalData, "tada.json"), got["options.tui.mouse"].Path)

	require.Equal(t, FieldSourceProject, got["options.tui.diff_mode"].Kind)
	require.Equal(t, FieldSourceWorkspace, got["options.tui.vertical_tabs"].Kind)

	_, ok := got["options.tui.ambient"]
	require.False(t, ok, "unset keys are left out")
}

func TestFieldSourcesSkipsUntrustedProject(t *testing.T) {
	isolateConfigDirs(t)

	workDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workDir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "tada.json"),
		[]byte(`{"options":{"tui":{"diff_mode":"split"}}}`), 0o644))
	// A tadarc that would fail if it were run.
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "tadarc"),
		[]byte("exit 3\n"), 0o644))

	got, err := FieldSources(t.Context(), workDir, filepath.Join(workDir, ".tada"),
		[]string{"options.tui.diff_mode"})
	require.NoError(t, err)
	require.Empty(t, got)
}
