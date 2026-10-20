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

func TestClaudeCodeModelsFromCLI(t *testing.T) {
	t.Parallel()
	models := ClaudeCodeModels([]claudecode.ModelInfo{
		{Value: "default", DisplayName: "Default (recommended)", SupportsEffort: true, SupportedEffortLevels: []string{"high", "low"}},
		{Value: "opus[1m]", DisplayName: "Opus (1M context)"},
		{Value: "sonnet", DisplayName: "Sonnet"},
	})
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	// The CLI's rows come first; built-in aliases it left out follow.
	require.Equal(t, []string{"default", "opus[1m]", "sonnet", "opus", "haiku"}, ids)
	require.Equal(t, "Claude Code · Default (recommended)", models[0].Name)
	require.True(t, models[0].CanReason)
	require.Equal(t, []string{"default", "low", "high"}, models[0].ReasoningLevels)
	require.Equal(t, "default", models[0].DefaultReasoningEffort)
	require.Equal(t, int64(1000000), models[1].ContextWindow)
	require.False(t, models[2].CanReason)
	require.Equal(t, int64(200000), models[2].ContextWindow)

	require.Equal(t, "high", ClaudeCodeEffort(models[0], SelectedModel{ReasoningEffort: "high"}))
	require.Empty(t, ClaudeCodeEffort(models[0], SelectedModel{ReasoningEffort: "default"}))
	require.Empty(t, ClaudeCodeEffort(models[0], SelectedModel{ReasoningEffort: "max"}), "not offered for this model")
	require.Empty(t, ClaudeCodeEffort(models[2], SelectedModel{ReasoningEffort: "high"}))
}

func TestSetClaudeCodeModelsOnlyReplacesFilledInLists(t *testing.T) {
	t.Parallel()
	cfg := &Config{Providers: csync.NewMap[string, ProviderConfig]()}
	cfg.Providers.Set("cc", prepareClaudeCodeProvider("cc", ProviderConfig{Type: catwalk.Type(claudecode.ProviderType)}))
	cfg.Providers.Set("mine", prepareClaudeCodeProvider("mine", ProviderConfig{
		Type:   catwalk.Type(claudecode.ProviderType),
		Models: []catwalk.Model{{ID: "opus"}},
	}))
	infos := []claudecode.ModelInfo{{Value: "best", DisplayName: "Best"}}
	require.True(t, cfg.SetClaudeCodeModels(infos))
	require.False(t, cfg.SetClaudeCodeModels(infos), "unchanged list")

	cc, _ := cfg.Providers.Get("cc")
	require.Equal(t, "best", cc.Models[0].ID)
	mine, _ := cfg.Providers.Get("mine")
	require.Equal(t, []catwalk.Model{{ID: "opus"}}, mine.Models)
}
