package config

import (
	"os"
	"path/filepath"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/opencode"
	"github.com/stretchr/testify/require"
)

func TestLiteralValueSurvivesExpansion(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "pwned")
	resolver := NewShellVariableResolver(env.NewFromMap(map[string]string{"HOME": "/h", "SECRET": "s3cr3t"}))
	for _, in := range []string{
		"sk-plain",
		"sk-with$dollar",
		"$(touch " + marker + ")",
		"`touch " + marker + "`",
		"${SECRET}",
		`back\slash and trailing\`,
		`it's "quoted"`,
		"multi\nline",
	} {
		got, err := resolver.ResolveValue(literalValue(in))
		require.NoError(t, err, in)
		require.Equal(t, in, got)
	}
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "an escaped value must never execute")
}

func TestMCPFromOpenCode(t *testing.T) {
	t.Parallel()

	local, ok := mcpFromOpenCode(opencode.MCPServer{
		Type:        "local",
		Command:     []string{"npx", "-y", "@acme/mcp", "--token=$(whoami)"},
		Environment: map[string]string{"API_KEY": "k$y"},
		Cwd:         "tools",
		TimeoutMS:   1500,
	})
	require.True(t, ok)
	require.Equal(t, MCPStdio, local.Type)
	require.Equal(t, "npx", local.Command)
	require.Equal(t, []string{"-y", "@acme/mcp", `--token=\$(whoami)`}, local.Args)
	require.Equal(t, map[string]string{"API_KEY": `k\$y`}, local.Env)
	require.Equal(t, "tools", local.Cwd)
	require.Equal(t, 2, local.Timeout, "milliseconds round up to seconds")

	remote, ok := mcpFromOpenCode(opencode.MCPServer{
		Type:    "remote",
		URL:     "https://mcp.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer abc"},
	})
	require.True(t, ok)
	require.Equal(t, MCPHttp, remote.Type)
	require.False(t, remote.OAuth, "OAuth needs an explicit oauth block")
	require.Equal(t, importedMCPTimeout, remote.Timeout)

	withOAuth, ok := mcpFromOpenCode(opencode.MCPServer{
		Type:  "remote",
		URL:   "https://mcp.example.com/mcp",
		OAuth: &opencode.MCPOAuth{ClientID: "id", CallbackPort: 19876},
	})
	require.True(t, ok)
	require.True(t, withOAuth.OAuth)
	require.Equal(t, "id", withOAuth.OAuthClientID)
	require.Equal(t, 19876, withOAuth.OAuthCallbackPort)

	_, ok = mcpFromOpenCode(opencode.MCPServer{Type: "local"})
	require.False(t, ok)
	_, ok = mcpFromOpenCode(opencode.MCPServer{Type: "carrier-pigeon", URL: "x"})
	require.False(t, ok)
}

func TestApplyOpenCodeMCP(t *testing.T) {
	t.Parallel()

	token := &oauth.Token{AccessToken: "tok"}
	cfg := &Config{MCP: MCPs{
		// Fully defined by Crush: wins outright.
		"mine": {Type: MCPStdio, Command: "my-server"},
		// Partial entries Crush writes itself: layered on top.
		"toggled": {Disabled: true},
		"authed":  {OAuthToken: token},
	}}
	imp := &openCodeImport{
		settings: openCodeSettings{enabled: true, mcp: true},
		config: &opencode.Config{MCP: map[string]opencode.MCPServer{
			"mine":    {Type: "local", Command: []string{"their-server"}},
			"toggled": {Type: "local", Command: []string{"toggled-server"}},
			"authed":  {Type: "remote", URL: "https://a.example/mcp", OAuth: &opencode.MCPOAuth{}},
			"new":     {Type: "remote", URL: "https://n.example/mcp"},
		}},
	}
	cfg.applyOpenCodeMCP(imp)

	require.Equal(t, "my-server", cfg.MCP["mine"].Command)
	require.Equal(t, "toggled-server", cfg.MCP["toggled"].Command)
	require.True(t, cfg.MCP["toggled"].Disabled)
	require.Equal(t, "https://a.example/mcp", cfg.MCP["authed"].URL)
	require.Same(t, token, cfg.MCP["authed"].OAuthToken)
	require.Equal(t, "https://n.example/mcp", cfg.MCP["new"].URL)

	// setDefaults drops token-only entries; the imported server keeps its token.
	cfg.setDefaults(t.TempDir(), "")
	require.Contains(t, cfg.MCP, "authed")
	require.Same(t, token, cfg.MCP["authed"].OAuthToken)
}

func TestApplyOpenCodeMCPDisabled(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.applyOpenCodeMCP(&openCodeImport{
		settings: openCodeSettings{enabled: true, mcp: false, providers: true},
		config:   &opencode.Config{MCP: map[string]opencode.MCPServer{"x": {Type: "remote", URL: "https://x"}}},
	})
	require.Empty(t, cfg.MCP)
	cfg.applyOpenCodeMCP(nil)
	require.Empty(t, cfg.MCP)
}

func TestApplyOpenCodeProviders(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("anthropic", ProviderConfig{ID: "anthropic", APIKey: "crush-key"})
	cfg := &Config{Providers: providers}

	tru := true
	imp := &openCodeImport{
		settings: openCodeSettings{enabled: true, providers: true},
		config: &opencode.Config{Providers: map[string]opencode.Provider{
			"anthropic": {APIKey: "opencode-key", BaseURL: "https://proxy.example"},
			"google":    {APIKey: "g$key"},
			"lmstudio": {
				Name:    "LM Studio",
				NPM:     "@ai-sdk/openai-compatible",
				BaseURL: "http://127.0.0.1:1234/v1",
				Models: map[string]opencode.Model{
					"qwen": {Name: "Qwen", Limit: opencode.Limit{Context: 32768}, Reasoning: &tru},
					"off":  {Disabled: &tru},
				},
			},
		}},
		creds: map[string]string{"openai": "sk-from-auth", "anthropic": "ignored"},
	}
	cfg.applyOpenCodeProviders(imp)

	anthropic, _ := cfg.Providers.Get("anthropic")
	require.Equal(t, "crush-key", anthropic.APIKey, "crush config wins")
	require.Equal(t, "https://proxy.example", anthropic.BaseURL, "empty fields are filled")

	gemini, ok := cfg.Providers.Get("gemini")
	require.True(t, ok, "models.dev IDs map to catwalk IDs")
	require.Equal(t, `g\$key`, gemini.APIKey)

	lm, ok := cfg.Providers.Get("lmstudio")
	require.True(t, ok)
	require.Equal(t, "LM Studio", lm.Name)
	require.Equal(t, catwalk.TypeOpenAICompat, lm.Type)
	require.Len(t, lm.Models, 1)
	require.Equal(t, "qwen", lm.Models[0].ID)
	require.Equal(t, int64(32768), lm.Models[0].ContextWindow)
	require.Equal(t, int64(3276), lm.Models[0].DefaultMaxTokens)
	require.True(t, lm.Models[0].CanReason)

	openai, ok := cfg.Providers.Get("openai")
	require.True(t, ok)
	require.Equal(t, "sk-from-auth", openai.APIKey)
}

func TestApplyOpenCodeModelPreference(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("anthropic", ProviderConfig{ID: "anthropic", Models: []catwalk.Model{{ID: "claude-x"}, {ID: "claude-small"}}})
	imp := &openCodeImport{settings: openCodeModelChoice, config: &opencode.Config{Model: "anthropic/claude-x", SmallModel: "anthropic/missing"}}

	cfg := &Config{Providers: providers, Models: map[SelectedModelType]SelectedModel{}}
	cfg.applyOpenCodeModelPreference(imp)
	require.Equal(t, SelectedModel{Provider: "anthropic", Model: "claude-x"}, cfg.Models[SelectedModelTypeLarge])
	_, ok := cfg.Models[SelectedModelTypeSmall]
	require.False(t, ok, "unknown models are ignored")

	cfg = &Config{Providers: providers, Models: map[SelectedModelType]SelectedModel{
		SelectedModelTypeLarge: {Provider: "anthropic", Model: "claude-small"},
	}}
	cfg.applyOpenCodeModelPreference(imp)
	require.Equal(t, "claude-small", cfg.Models[SelectedModelTypeLarge].Model, "crush's selection wins")
}

func TestMergeCatalogsAugment(t *testing.T) {
	t.Parallel()

	base := catalogSource{name: "base", load: func() ([]catwalk.Provider, error) {
		return []catwalk.Provider{
			{ID: "anthropic", Type: catwalk.TypeAnthropic, Models: []catwalk.Model{{ID: "a1", Name: "curated"}}},
			{ID: "bedrock", Type: catwalk.TypeBedrock, Models: []catwalk.Model{{ID: "us.b1"}}},
		}, nil
	}}
	extra := catalogSource{
		name: "extra",
		load: func() ([]catwalk.Provider, error) {
			return []catwalk.Provider{
				{ID: "anthropic", Type: catwalk.TypeAnthropic, Models: []catwalk.Model{{ID: "a1", Name: "other"}, {ID: "a2"}}},
				{ID: "bedrock", Type: catwalk.TypeBedrock, Models: []catwalk.Model{{ID: "b1"}}},
				{ID: "wanted", Type: catwalk.TypeOpenAICompat, Models: []catwalk.Model{{ID: "w"}}},
				{ID: "unwanted", Type: catwalk.TypeOpenAICompat, Models: []catwalk.Model{{ID: "u"}}},
			}, nil
		},
		augment: true,
		include: func(p catwalk.Provider) bool { return p.ID == "wanted" },
	}

	got, err := mergeCatalogs([]catalogSource{base, extra})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, []catwalk.Model{{ID: "a1", Name: "curated"}, {ID: "a2"}}, got[0].Models)
	require.Equal(t, []catwalk.Model{{ID: "us.b1"}}, got[1].Models, "bedrock is never augmented")
	require.Equal(t, catwalk.InferenceProvider("wanted"), got[2].ID)
}

func TestOpenCodeCatalogSourceInclude(t *testing.T) {
	t.Setenv("OC_TEST_SET_KEY", "x")
	t.Setenv("OC_TEST_UNSET_KEY", "")

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("configured", ProviderConfig{ID: "configured"})
	providers.Set("disabled", ProviderConfig{ID: "disabled", Disable: true})
	include := openCodeCatalogSource(&Config{Providers: providers}).include

	require.True(t, include(catwalk.Provider{ID: "configured"}))
	require.False(t, include(catwalk.Provider{ID: "disabled"}))
	require.True(t, include(catwalk.Provider{ID: "a", APIKey: "${OC_TEST_UNSET_KEY:-${OC_TEST_SET_KEY}}"}))
	require.False(t, include(catwalk.Provider{ID: "b", APIKey: "$OC_TEST_UNSET_KEY"}))
	require.False(t, include(catwalk.Provider{ID: "c"}))
}

func TestOpenCodeSettings(t *testing.T) {
	t.Setenv("CRUSH_DISABLE_OPENCODE", "")
	f := false
	all := openCodeSettings{
		enabled: true, mcp: true, providers: true, models: true,
		instructions: true, skills: true, commands: true, tools: true,
		permissions: true, compaction: true,
	}
	require.Equal(t, all, openCodeSettingsFor(nil))
	require.Equal(t, openCodeSettings{}, openCodeSettingsFor(&Options{OpenCode: &OpenCodeOptions{Disabled: true}}))
	want := all
	want.mcp, want.permissions, want.credentials = false, false, true
	require.Equal(t, want, openCodeSettingsFor(&Options{OpenCode: &OpenCodeOptions{
		MCP: &f, Permissions: &f, Credentials: true,
	}}))

	t.Setenv("CRUSH_DISABLE_OPENCODE", "1")
	require.Equal(t, openCodeSettings{}, openCodeSettingsFor(nil))
}

func TestSafeCatalogDropsShellSubstitution(t *testing.T) {
	t.Parallel()

	got := safeCatalog([]catwalk.Provider{
		{ID: "ok", APIKey: "$OK_API_KEY", APIEndpoint: "https://ok.example/v1"},
		{ID: "cmd", APIKey: "$(cat ~/.ssh/id_rsa)"},
		{ID: "hdr", APIKey: "$K", DefaultHeaders: map[string]string{"X": "`id`"}},
	}, "test")
	require.Len(t, got, 1)
	require.Equal(t, catwalk.InferenceProvider("ok"), got[0].ID)
}

// TestOpenCodeImportEndToEnd reads a real OpenCode config layout from a
// temporary XDG home through loadOpenCode.
func TestOpenCodeImportEndToEnd(t *testing.T) {
	root := isolateOpenCode(t)
	t.Setenv("OC_E2E_TOKEN", "tok$en")

	dir := filepath.Join(root, "config", "opencode")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "opencode.jsonc"), []byte(`{
		// global config
		"model": "anthropic/claude-x",
		"mcp": {
			"context7": {"type": "remote", "url": "https://mcp.context7.com/mcp", "headers": {"Authorization": "Bearer {env:OC_E2E_TOKEN}"}},
			"files": {"type": "local", "command": ["npx", "-y", "files-mcp"], "enabled": true},
		},
	}`), 0o644))

	project := filepath.Join(root, "project")
	require.NoError(t, os.MkdirAll(project, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, "opencode.json"), []byte(`{
		"mcp": {"evil": {"type": "local", "command": ["sh", "-c", "touch /tmp/pwned"]}}
	}`), 0o644))

	imp := loadOpenCode(nil, project, false)
	require.NotNil(t, imp)
	require.Equal(t, "anthropic/claude-x", imp.config.Model)

	cfg := &Config{}
	cfg.applyOpenCodeMCP(imp)
	require.Contains(t, cfg.MCP, "context7")
	require.Contains(t, cfg.MCP, "files")
	require.NotContains(t, cfg.MCP, "evil", "untrusted project config is never imported")

	resolver := NewShellVariableResolver(env.NewFromMap(nil))
	headers, err := cfg.MCP["context7"].ResolvedHeaders(resolver)
	require.NoError(t, err)
	require.Equal(t, "Bearer tok$en", headers["Authorization"])

	trusted := loadOpenCode(nil, project, true)
	cfg = &Config{}
	cfg.applyOpenCodeMCP(trusted)
	require.Contains(t, cfg.MCP, "evil", "trusted project config is imported")
	require.ElementsMatch(t, []string{filepath.Join(dir, "opencode.jsonc"), filepath.Join(project, "opencode.json")}, trusted.files())
}

// isolateOpenCode enables the OpenCode import (TestMain disables it) with
// every OpenCode location under a temp dir, which it returns. OpenCode's
// global config dir is <root>/config/opencode.
func isolateOpenCode(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CRUSH_DISABLE_OPENCODE", "")
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	for _, k := range []string{"OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_DB", "OPENCODE_MODELS_PATH", "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_CONFIG_PROJECT_DISABLE"} {
		t.Setenv(k, "")
	}
	return root
}

// TestLoadImportsOpenCode runs the full Load pipeline: global OpenCode MCP
// servers and the cached models.dev catalog are imported, while a
// project's opencode.json stays inert until the project is trusted.
func TestLoadImportsOpenCode(t *testing.T) {
	isolateTrust(t)
	root := isolateOpenCode(t)
	resetProviderState()
	t.Cleanup(resetProviderState)
	t.Setenv("OC_LOAD_TEST_API_KEY", "k")

	writeFile(t, filepath.Join(root, "config", "opencode", "opencode.json"), `{
		"mcp": {"docs": {"type": "remote", "url": "https://docs.example/mcp"}}
	}`)
	writeFile(t, filepath.Join(root, "cache", "opencode", "models.json"), `{
		"octest": {
			"id": "octest", "name": "OC Test", "env": ["OC_LOAD_TEST_API_KEY"],
			"npm": "@ai-sdk/openai-compatible", "api": "https://oc.example/v1",
			"models": {"m1": {"id": "m1", "name": "M1", "tool_call": true, "release_date": "2026-01-01",
				"modalities": {"input": ["text"], "output": ["text"]},
				"limit": {"context": 100000, "output": 8000}}}
		}
	}`)

	project := t.TempDir()
	marker := filepath.Join(t.TempDir(), "marker")
	// Forward slashes keep the JSON valid on Windows, where "\a" in a
	// backslash path is not a legal JSON escape.
	writeFile(t, filepath.Join(project, "opencode.json"),
		`{"mcp": {"evil": {"type": "local", "command": ["sh", "-c", "touch `+filepath.ToSlash(marker)+`"]}}}`)

	store, err := Load(project, "", false)
	require.NoError(t, err)
	cfg := store.Config()
	require.Contains(t, cfg.MCP, "docs")
	require.NotContains(t, cfg.MCP, "evil")
	trust := store.ProjectTrust()
	require.False(t, trust.Trusted)
	require.Contains(t, trust.DisplayFiles(), "opencode.json", "the trust notice names the OpenCode file")
	requireNoFile(t, marker, "nothing from an untrusted project may run")

	octest, ok := cfg.Providers.Get("octest")
	require.True(t, ok, "an OpenCode-only provider with its key set is configured")
	require.Equal(t, "https://oc.example/v1", octest.BaseURL)

	st := store.OpenCodeStatus(t.Context())
	require.NotNil(t, st.Catalog)
	require.Equal(t, 1, st.Catalog.Listed)
	require.False(t, st.ProjectIncluded)

	_, err = TrustProject(t.Context(), project, "")
	require.NoError(t, err)
	store, err = Load(project, "", false)
	require.NoError(t, err)
	require.Contains(t, store.Config().MCP, "evil", "trusted project OpenCode config is imported")
	require.True(t, store.OpenCodeStatus(t.Context()).ProjectIncluded)

	// Changing the project's OpenCode config revokes trust.
	writeFile(t, filepath.Join(project, "opencode.json"), `{"mcp": {}}`)
	store, err = Load(project, "", false)
	require.NoError(t, err)
	require.False(t, store.ProjectTrust().Trusted)
}
