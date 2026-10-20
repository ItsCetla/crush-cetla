package config

import (
	"context"
	"os"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/claudecode"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/stretchr/testify/require"
)

// withClaudeBinary pretends the `claude` CLI lives at path. Tests that use
// it must not run in parallel: it swaps a package variable.
func withClaudeBinary(t *testing.T, path string) {
	t.Helper()
	old := findClaudeBinary
	findClaudeBinary = func() string { return path }
	t.Cleanup(func() { findClaudeBinary = old })
}

func loadClaudeCodeProviders(t *testing.T, cfg *Config) {
	t.Helper()
	cfg.setDefaults("/tmp", "")
	testEnv := env.NewFromMap(map[string]string{"PATH": os.Getenv("PATH")})
	cfg.addClaudeCodeProvider()
	require.NoError(t, cfg.configureProviders(context.Background(), testStore(cfg), testEnv, NewShellVariableResolver(testEnv), []catwalk.Provider{}))
}

func TestClaudeCodeProviderAutoAdded(t *testing.T) {
	withClaudeBinary(t, "/opt/bin/claude")
	cfg := &Config{Providers: csync.NewMap[string, ProviderConfig]()}
	loadClaudeCodeProviders(t, cfg)

	pc, ok := cfg.Providers.Get(claudecode.ProviderID)
	require.True(t, ok)
	require.True(t, pc.IsClaudeCode())
	require.Equal(t, "Claude Code", pc.Name)
	require.True(t, pc.FlatRate)
	require.Equal(t, "/opt/bin/claude", pc.ClaudeCodeBinary())
	require.False(t, pc.ClaudeCodeUsesAPIKey())
	var ids []string
	for _, m := range pc.Models {
		ids = append(ids, m.ID)
	}
	require.Equal(t, []string{"default", "opus", "sonnet", "haiku"}, ids)
	require.True(t, cfg.IsConfigured(), "an installed CLI is enough to start")
}

func TestClaudeCodeProviderNotAddedWithoutCLI(t *testing.T) {
	withClaudeBinary(t, "")
	cfg := &Config{Providers: csync.NewMap[string, ProviderConfig]()}
	loadClaudeCodeProviders(t, cfg)
	_, ok := cfg.Providers.Get(claudecode.ProviderID)
	require.False(t, ok)
}

func TestClaudeCodeProviderRespectsDisableDefaults(t *testing.T) {
	withClaudeBinary(t, "/opt/bin/claude")
	cfg := &Config{Providers: csync.NewMap[string, ProviderConfig](), Options: &Options{DisableDefaultProviders: true}}
	cfg.addClaudeCodeProvider()
	_, ok := cfg.Providers.Get(claudecode.ProviderID)
	require.False(t, ok)
}

func TestClaudeCodeProviderUserConfig(t *testing.T) {
	withClaudeBinary(t, "/opt/bin/claude")
	cfg := &Config{Providers: csync.NewMapFrom(map[string]ProviderConfig{
		"my-cc": {
			Type:            catwalk.Type(claudecode.ProviderType),
			Name:            "Work Claude",
			ProviderOptions: map[string]any{ClaudeCodeBinaryOption: "/work/claude", ClaudeCodeUseAPIKeyOption: true},
		},
		claudecode.ProviderID: {Type: catwalk.Type(claudecode.ProviderType), Disable: true},
	})}
	loadClaudeCodeProviders(t, cfg)

	_, ok := cfg.Providers.Get(claudecode.ProviderID)
	require.False(t, ok, "a disabled Claude Code provider is dropped and not re-added")
	pc, ok := cfg.Providers.Get("my-cc")
	require.True(t, ok)
	require.Equal(t, "Work Claude", pc.Name)
	require.Equal(t, "/work/claude", pc.ClaudeCodeBinary())
	require.True(t, pc.ClaudeCodeUsesAPIKey())
	require.NotEmpty(t, pc.Models)
}
