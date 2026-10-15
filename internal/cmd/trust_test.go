package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func isolateTrustCmd(t *testing.T) {
	t.Helper()
	isolated := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(isolated, "config"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, "data"))
	t.Setenv(config.TrustProjectEnv, "")
}

func TestTrustCommand(t *testing.T) {
	isolateTrustCmd(t)
	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "crush.json"), []byte(`{}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".crushrc"), []byte("option debug\n"), 0o644))
	ctx := context.Background()

	trust, err := config.EvaluateProjectTrust(ctx, project, "")
	require.NoError(t, err)
	require.False(t, trust.Trusted)

	var out bytes.Buffer
	require.NoError(t, runTrust(ctx, &out, project, "", false))
	require.Contains(t, out.String(), "Project: "+config.ProjectRoot(project))
	require.Contains(t, out.String(), "  crush.json\n")
	require.Contains(t, out.String(), "  .crushrc\n")
	require.Contains(t, out.String(), "Review these files")

	trust, err = config.EvaluateProjectTrust(ctx, project, "")
	require.NoError(t, err)
	require.True(t, trust.Trusted)
	require.Equal(t, config.TrustSourceRecord, trust.Source)

	out.Reset()
	require.NoError(t, listTrustedProjects(&out))
	require.Contains(t, out.String(), config.ProjectRoot(project))
	require.Contains(t, out.String(), ".crushrc, crush.json")

	out.Reset()
	require.NoError(t, runTrust(ctx, &out, project, "", true))
	require.Contains(t, out.String(), "Revoked trust for "+config.ProjectRoot(project))
	trust, err = config.EvaluateProjectTrust(ctx, project, "")
	require.NoError(t, err)
	require.False(t, trust.Trusted)

	out.Reset()
	require.NoError(t, runTrust(ctx, &out, project, "", true))
	require.Contains(t, out.String(), "was not trusted")

	out.Reset()
	require.NoError(t, listTrustedProjects(&out))
	require.Equal(t, "No trusted projects.\n", out.String())
}

func TestTrustCommandWithoutProjectConfig(t *testing.T) {
	isolateTrustCmd(t)
	project := t.TempDir()

	var out bytes.Buffer
	require.NoError(t, runTrust(context.Background(), &out, project, "", false))
	require.Contains(t, out.String(), "nothing to trust")

	projects, err := config.TrustedProjects()
	require.NoError(t, err)
	require.Empty(t, projects)
}

func TestTrustCommandRejectsMissingDirectory(t *testing.T) {
	isolateTrustCmd(t)
	err := runTrust(context.Background(), &bytes.Buffer{}, filepath.Join(t.TempDir(), "missing"), "", false)
	require.Error(t, err)
}

func TestTrustCommandIsRegistered(t *testing.T) {
	t.Parallel()
	found, _, err := rootCmd.Find([]string{"trust"})
	require.NoError(t, err)
	require.Equal(t, trustCmd, found)
}
