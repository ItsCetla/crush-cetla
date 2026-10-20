package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func TestClaudeCodeArtifactArgs(t *testing.T) {
	env := testEnv(t)
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	opts := cfg.Config().Options
	opts.Artifacts = &config.ArtifactsOptions{Port: 7599, AllowCDN: true}
	coord := &coordinator{cfg: cfg, interactive: true}

	require.Nil(t, coord.claudeCodeArtifactArgs(true), "sub-agents get no artifact server")

	args := coord.claudeCodeArtifactArgs(false)
	require.Len(t, args, 1)
	raw, ok := strings.CutPrefix(args[0], "--mcp-config=")
	require.True(t, ok)
	var mc struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &mc))
	srv, ok := mc.MCPServers["tada-artifacts"]
	require.True(t, ok)
	require.Equal(t, "stdio", srv.Type)
	require.NotEmpty(t, srv.Command)
	require.Equal(t, []string{"artifacts", "mcp", "--port", "7599", "--allow-cdn=true", "--no-open=false"}, srv.Args)

	// Headless sessions never open a browser.
	coord.interactive = false
	args = coord.claudeCodeArtifactArgs(false)
	require.Contains(t, args[0], `"--no-open=true"`)

	opts.Artifacts.Disabled = true
	require.Nil(t, coord.claudeCodeArtifactArgs(false))
}
