package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/stretchr/testify/require"
)

func TestMachineProviders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": [{"id": "qwen-coder"}, {"id": "llama"}]}`))
	}))
	defer server.Close()

	cfg := &Config{
		Providers: csync.NewMap[string, ProviderConfig](),
		Machines: Machines{
			"m1":       {URL: server.URL + "/v1", Hardware: "M1 Max", Model: "llama", Slots: 4},
			"gpu":      {URL: "http://127.0.0.1:1/v1", Model: "coder", ContextWindow: 16_384, Slots: 2},
			"off":      {URL: server.URL + "/v1", Disabled: true},
			"bad name": {URL: server.URL + "/v1"},
		},
	}
	cfg.setDefaults(t.TempDir(), "")

	e := env.NewFromMap(map[string]string{})
	err := cfg.configureProviders(context.Background(), testStore(cfg), e, NewShellVariableResolver(e), []catwalk.Provider{})
	require.NoError(t, err)

	m1, ok := cfg.Providers.Get("m1")
	require.True(t, ok)
	require.Equal(t, "m1 (M1 Max)", m1.Name)
	require.True(t, m1.FlatRate)
	require.Len(t, m1.Models, 2)
	require.Equal(t, "llama", cfg.MachineModel("m1"))
	for _, m := range m1.Models {
		require.Equal(t, int64(defaultMachineContextWindow), m.ContextWindow)
		require.Equal(t, int64(defaultMachineMaxTokens), m.DefaultMaxTokens)
	}

	// An offline machine keeps the model it lists.
	gpu, ok := cfg.Providers.Get("gpu")
	require.True(t, ok)
	require.Len(t, gpu.Models, 1)
	require.Equal(t, int64(16_384), gpu.Models[0].ContextWindow)
	require.Equal(t, int64(4_096), gpu.Models[0].DefaultMaxTokens)

	_, ok = cfg.Providers.Get("off")
	require.False(t, ok)
	require.Empty(t, cfg.MachineModel("off"))
	_, ok = cfg.Providers.Get("bad name")
	require.False(t, ok)
}

func TestMachineUsesExistingProvider(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"m1": {BaseURL: "https://example.com/v1", Models: []catwalk.Model{{ID: "mine"}}},
		}),
		Machines: Machines{"m1": {URL: "http://other/v1", Slots: 2}},
	}
	cfg.setDefaults(t.TempDir(), "")

	e := env.NewFromMap(map[string]string{})
	err := cfg.configureProviders(context.Background(), testStore(cfg), e, NewShellVariableResolver(e), []catwalk.Provider{})
	require.NoError(t, err)

	p, ok := cfg.Providers.Get("m1")
	require.True(t, ok)
	require.Equal(t, "https://example.com/v1", p.BaseURL)
	require.Equal(t, "mine", cfg.MachineModel("m1"))
}

func TestValidMachineName(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidMachineName("m1-max_2"))
	require.Error(t, ValidMachineName(""))
	require.Error(t, ValidMachineName("auto"))
	require.Error(t, ValidMachineName("Main"))
	require.Error(t, ValidMachineName("a/b"))
}
