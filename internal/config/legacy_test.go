package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrateLegacyDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	oldDir := filepath.Join(root, "crush")
	require.NoError(t, os.MkdirAll(filepath.Join(oldDir, "skills", "mine"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(oldDir, "logs"), 0o700))
	write := func(p, s string) {
		require.NoError(t, os.WriteFile(filepath.Join(oldDir, p), []byte(s), 0o600))
	}
	write("crush.json", `{"a":1}`)
	write("crushrc", "option ui intro false")
	write("providers.json", "[]")
	write("crush.json.lock", "")
	write("crush.json.bak-1", "old")
	write("tada.db", "x")
	write("logs/crush.log", "log")
	write("skills/mine/SKILL.md", "skill")

	newDir := filepath.Join(root, "tada")
	migrateLegacyDir(newDir)

	got := func(p string) string {
		b, err := os.ReadFile(filepath.Join(newDir, p))
		require.NoError(t, err, p)
		return string(b)
	}
	require.Equal(t, `{"a":1}`, got("tada.json"))
	require.Equal(t, "option ui intro false", got("tadarc"))
	require.Equal(t, "[]", got("providers.json"))
	require.Equal(t, "skill", got("skills/mine/SKILL.md"))
	for _, p := range []string{"crush.json", "crush.json.lock", "crush.json.bak-1", "tada.db", "logs"} {
		_, err := os.Stat(filepath.Join(newDir, p))
		require.True(t, os.IsNotExist(err), p)
	}

	// A second run must not touch an existing new folder.
	require.NoError(t, os.WriteFile(filepath.Join(newDir, "tada.json"), []byte("new"), 0o600))
	migrateLegacyDir(newDir)
	require.Equal(t, "new", got("tada.json"))
}

func TestMigrateLegacyDirSkipsCustomLocations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "crush"), 0o700))
	migrateLegacyDir(filepath.Join(root, "custom"))
	_, err := os.Stat(filepath.Join(root, "custom"))
	require.True(t, os.IsNotExist(err))
}

func TestImportLegacyEnv(t *testing.T) {
	t.Setenv("CRUSH_ZZ_ONLY_OLD", "old")
	t.Setenv("CRUSH_ZZ_BOTH", "old")
	t.Setenv("TADA_ZZ_BOTH", "new")
	ImportLegacyEnv()
	require.Equal(t, "old", os.Getenv("TADA_ZZ_ONLY_OLD"))
	require.Equal(t, "new", os.Getenv("TADA_ZZ_BOTH"))
	os.Unsetenv("TADA_ZZ_ONLY_OLD")
}
