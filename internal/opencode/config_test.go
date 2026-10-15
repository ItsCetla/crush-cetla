package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// layout builds a fake installation: a global config dir, a managed dir, a
// repository with a nested working directory, and an explicit config file.
type layout struct {
	global, managed, repo, sub, explicit string
}

func newLayout(t *testing.T) layout {
	t.Helper()
	root := t.TempDir()
	l := layout{
		global:   filepath.Join(root, "config", "opencode"),
		managed:  filepath.Join(root, "etc", "opencode"),
		repo:     filepath.Join(root, "repo"),
		explicit: filepath.Join(root, "explicit.json"),
	}
	l.sub = filepath.Join(l.repo, "pkg", "sub")
	return l
}

func (l layout) opts(env map[string]string) LoadOptions {
	return LoadOptions{
		Paths:           Paths{ConfigDir: l.global, ManagedDir: l.managed},
		Getenv:          mapEnv(env),
		Cwd:             l.sub,
		ProjectBoundary: l.repo,
		IncludeProject:  true,
	}
}

func modelDoc(model string) string {
	return `{"model": "` + model + `"}`
}

func TestLoadConfigPrecedence(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	files := []struct{ path, model string }{
		{filepath.Join(l.global, "config.json"), "global/legacy"},
		{filepath.Join(l.global, "opencode.json"), "global/json"},
		{filepath.Join(l.global, "opencode.jsonc"), "global/jsonc"},
		{l.explicit, "explicit/file"},
		{filepath.Join(l.repo, "opencode.jsonc"), "direct/repo"},
		{filepath.Join(l.repo, "pkg", "opencode.json"), "direct/pkg"},
		{filepath.Join(l.sub, "opencode.json"), "direct/sub"},
		{filepath.Join(l.sub, "opencode.jsonc"), "direct/subc"},
		{filepath.Join(l.repo, ".opencode", "opencode.json"), "dot/repo"},
		{filepath.Join(l.sub, ".opencode", "opencode.json"), "dot/sub"},
		{filepath.Join(l.managed, "opencode.json"), "managed/json"},
	}
	for _, f := range files {
		writeFile(t, f.path, modelDoc(f.model))
	}
	// Above the boundary: never read.
	writeFile(t, filepath.Join(filepath.Dir(l.repo), "opencode.json"), modelDoc("outside/boundary"))

	env := map[string]string{
		"OPENCODE_CONFIG":         l.explicit,
		"OPENCODE_CONFIG_CONTENT": modelDoc("content/env"),
	}
	cfg, err := LoadConfig(l.opts(env))
	require.NoError(t, err)
	require.Empty(t, cfg.Warnings)

	want := make([]string, 0, len(files)+1)
	for _, f := range files[:len(files)-1] {
		want = append(want, f.path)
	}
	want = append(want, ContentSource, files[len(files)-1].path)
	require.Equal(t, want, cfg.Files)
	require.Equal(t, "managed/json", cfg.Model)

	// Peel the layers off from the top and check who wins.
	steps := []struct {
		remove string
		want   string
	}{
		{filepath.Join(l.managed, "opencode.json"), "content/env"},
		{"content", "dot/sub"},
		{filepath.Join(l.sub, ".opencode", "opencode.json"), "dot/repo"},
		{filepath.Join(l.repo, ".opencode", "opencode.json"), "direct/subc"},
		{filepath.Join(l.sub, "opencode.jsonc"), "direct/sub"},
		{filepath.Join(l.sub, "opencode.json"), "direct/pkg"},
		{filepath.Join(l.repo, "pkg", "opencode.json"), "direct/repo"},
		{filepath.Join(l.repo, "opencode.jsonc"), "explicit/file"},
		{l.explicit, "global/jsonc"},
		{filepath.Join(l.global, "opencode.jsonc"), "global/json"},
		{filepath.Join(l.global, "opencode.json"), "global/legacy"},
	}
	for _, step := range steps {
		if step.remove == "content" {
			delete(env, "OPENCODE_CONFIG_CONTENT")
		} else {
			writeFile(t, step.remove, `{}`)
		}
		cfg, err := LoadConfig(l.opts(env))
		require.NoError(t, err)
		require.Equal(t, step.want, cfg.Model, "after removing %s", step.remove)
	}
}

func TestLoadConfigProjectGating(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	writeFile(t, filepath.Join(l.global, "opencode.json"), modelDoc("global/m"))
	writeFile(t, filepath.Join(l.sub, "opencode.json"), modelDoc("project/m"))
	writeFile(t, filepath.Join(l.repo, ".opencode", "opencode.json"), modelDoc("dot/m"))
	writeFile(t, filepath.Join(l.sub, "rel.json"), `{"small_model": "explicit/s"}`)

	t.Run("include project", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadConfig(l.opts(nil))
		require.NoError(t, err)
		require.Equal(t, "dot/m", cfg.Model)
	})

	t.Run("IncludeProject false skips project files", func(t *testing.T) {
		t.Parallel()
		opts := l.opts(map[string]string{"OPENCODE_CONFIG": "rel.json"})
		opts.IncludeProject = false
		cfg, err := LoadConfig(opts)
		require.NoError(t, err)
		require.Equal(t, "global/m", cfg.Model)
		// $OPENCODE_CONFIG is the user's choice and still loads, relative
		// to the working directory.
		require.Equal(t, "explicit/s", cfg.SmallModel)
		require.Equal(t, []string{filepath.Join(l.global, "opencode.json"), filepath.Join(l.sub, "rel.json")}, cfg.Files)
	})

	for _, env := range []map[string]string{
		{"OPENCODE_DISABLE_PROJECT_CONFIG": "TRUE"},
		{"OPENCODE_DISABLE_PROJECT_CONFIG": "1"},
		{"OPENCODE_CONFIG_PROJECT_DISABLE": "true"},
	} {
		t.Run("env disables project", func(t *testing.T) {
			t.Parallel()
			cfg, err := LoadConfig(l.opts(env))
			require.NoError(t, err)
			require.Equal(t, "global/m", cfg.Model)
		})
	}

	t.Run("falsy env keeps project", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadConfig(l.opts(map[string]string{"OPENCODE_DISABLE_PROJECT_CONFIG": "yes"}))
		require.NoError(t, err)
		require.Equal(t, "dot/m", cfg.Model)
	})

	t.Run("cwd outside boundary only reads cwd", func(t *testing.T) {
		t.Parallel()
		opts := l.opts(nil)
		opts.ProjectBoundary = filepath.Join(l.repo, "elsewhere")
		cfg, err := LoadConfig(opts)
		require.NoError(t, err)
		require.Equal(t, "project/m", cfg.Model)
	})
}

func TestLoadConfigSkipsGlobalDirInProjectWalk(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	// The global config dir is the project's .opencode directory.
	dot := filepath.Join(l.repo, ".opencode")
	writeFile(t, filepath.Join(dot, "opencode.json"), modelDoc("dot/m"))
	opts := l.opts(nil)
	opts.ConfigDir = dot
	cfg, err := LoadConfig(opts)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(dot, "opencode.json")}, cfg.Files)

	// Working inside the global config dir disables the project walk.
	writeFile(t, filepath.Join(l.global, "opencode.json"), modelDoc("global/m"))
	opts = l.opts(nil)
	opts.Cwd = l.global
	opts.ProjectBoundary = l.global
	cfg, err = LoadConfig(opts)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(l.global, "opencode.json")}, cfg.Files)

	// A project file that links to a global file is not read twice.
	link := filepath.Join(l.sub, "opencode.json")
	require.NoError(t, os.MkdirAll(l.sub, 0o755))
	if err := os.Symlink(filepath.Join(l.global, "opencode.json"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg, err = LoadConfig(l.opts(nil))
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(l.global, "opencode.json"), filepath.Join(dot, "opencode.json")}, cfg.Files)
}

func TestLoadConfigBadFiles(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	writeFile(t, filepath.Join(l.global, "config.json"), modelDoc("legacy/m"))
	writeFile(t, filepath.Join(l.global, "opencode.json"), `{"model": "broken/m", }}`)
	writeFile(t, filepath.Join(l.global, "opencode.jsonc"), `["not", "an", "object"]`)
	writeFile(t, filepath.Join(l.sub, "opencode.json"), `{"model": 42, "small_model": "ok/s", "mcp": []}`)

	cfg, err := LoadConfig(l.opts(nil))
	require.NoError(t, err)
	require.Equal(t, "legacy/m", cfg.Model)
	require.Equal(t, "ok/s", cfg.SmallModel)
	require.Equal(t, []string{filepath.Join(l.global, "config.json"), filepath.Join(l.sub, "opencode.json")}, cfg.Files)
	require.Len(t, cfg.Warnings, 4, cfg.Warnings)
}

func TestLoadConfigMissingEverything(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(LoadOptions{Paths: Paths{ConfigDir: filepath.Join(t.TempDir(), "nope")}})
	require.NoError(t, err)
	require.Empty(t, cfg.Files)
	require.Empty(t, cfg.Warnings)
	require.Empty(t, cfg.MCP)
	require.Empty(t, cfg.Providers)
}

func TestLoadConfigMCP(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	writeFile(t, filepath.Join(l.global, "opencode.jsonc"), `{
		// V1 shape.
		"mcp": {
			"files": {
				"type": "local",
				"command": ["npx", "-y", "server-files"],
				"cwd": "/srv",
				"environment": {"TOKEN": "{env:FILES_TOKEN}"},
				"timeout": 9000,
			},
			"remote1": {
				"type": "remote",
				"url": "https://mcp.example.com/mcp",
				"headers": {"Authorization": "Bearer x"},
				"oauth": {"clientId": "cid", "clientSecret": "sec", "scope": "read", "callbackPort": 4567, "redirectUri": "http://localhost/cb"},
				"enabled": false,
			},
			"nooauth": {"type": "remote", "url": "https://n.example.com", "oauth": false},
			// A V1 server that happens to be called "servers".
			"servers": {"type": "local", "command": ["srv"]},
			"replaced": {"type": "local", "command": ["old"], "environment": {"A": "1"}},
			"toggled": {"type": "local", "command": ["t"]},
			"bad": {"type": "local"},
			"weird": {"type": "stdio", "command": ["x"]},
		},
	}`)
	writeFile(t, filepath.Join(l.sub, "opencode.json"), `{
		"mcp": {
			"timeout": {"startup": 12000, "catalog": 1},
			"servers": {
				"v2local": {"type": "local", "command": ["v2"], "disabled": true, "timeout": {"startup": 3000}},
				"v2remote": {
					"type": "remote",
					"url": "https://v2.example.com",
					"oauth": {"client_id": "c2", "scope": "s", "callback_port": 1, "auth_server_metadata_url": "https://as.example.com"}
				},
				"replaced": {"type": "local", "command": ["new"]}
			},
			"toggled": {"enabled": false},
			"pending": {"enabled": false}
		}
	}`)
	writeFile(t, filepath.Join(l.sub, ".opencode", "opencode.json"), `{
		"mcp": {"pending": {"type": "remote", "url": "https://later.example.com"}}
	}`)

	cfg, err := LoadConfig(l.opts(map[string]string{"FILES_TOKEN": "t0k"}))
	require.NoError(t, err)
	require.Len(t, cfg.Warnings, 2, cfg.Warnings)

	global := filepath.Join(l.global, "opencode.jsonc")
	require.Equal(t, MCPServer{
		Name:        "files",
		Type:        "local",
		Command:     []string{"npx", "-y", "server-files"},
		Cwd:         "/srv",
		Environment: map[string]string{"TOKEN": "t0k"},
		TimeoutMS:   9000,
		SourcePath:  global,
	}, cfg.MCP["files"])
	require.Equal(t, MCPServer{
		Name:    "remote1",
		Type:    "remote",
		URL:     "https://mcp.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer x"},
		OAuth: &MCPOAuth{
			ClientID: "cid", ClientSecret: "sec", Scope: "read",
			CallbackPort: 4567, RedirectURI: "http://localhost/cb",
		},
		Disabled:   true,
		TimeoutMS:  12000,
		SourcePath: global,
	}, cfg.MCP["remote1"])
	require.True(t, cfg.MCP["nooauth"].OAuthDisabled)
	require.Nil(t, cfg.MCP["nooauth"].OAuth)
	require.Equal(t, []string{"srv"}, cfg.MCP["servers"].Command)

	project := filepath.Join(l.sub, "opencode.json")
	require.Equal(t, MCPServer{
		Name: "v2local", Type: "local", Command: []string{"v2"},
		Disabled: true, TimeoutMS: 3000, SourcePath: project,
	}, cfg.MCP["v2local"])
	require.Equal(t, &MCPOAuth{
		ClientID: "c2", Scope: "s", CallbackPort: 1,
		AuthServerMetadataURL: "https://as.example.com",
	}, cfg.MCP["v2remote"].OAuth)

	// Later documents replace a server whole.
	require.Equal(t, MCPServer{
		Name: "replaced", Type: "local", Command: []string{"new"},
		TimeoutMS: 12000, SourcePath: project,
	}, cfg.MCP["replaced"])

	// A V1 toggle disables a server defined in an earlier document...
	require.True(t, cfg.MCP["toggled"].Disabled)
	require.Equal(t, []string{"t"}, cfg.MCP["toggled"].Command)
	// ...and carries over to a later definition that does not say.
	require.True(t, cfg.MCP["pending"].Disabled)
	require.Equal(t, "https://later.example.com", cfg.MCP["pending"].URL)

	require.NotContains(t, cfg.MCP, "bad")
	require.NotContains(t, cfg.MCP, "weird")
	require.NotContains(t, cfg.MCP, "timeout")
}

func TestLoadConfigMCPToggleOverriddenByExplicitEnable(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	writeFile(t, filepath.Join(l.global, "opencode.json"), `{"mcp": {"x": {"enabled": false}}}`)
	writeFile(t, filepath.Join(l.sub, "opencode.json"), `{"mcp": {"x": {"type": "local", "command": ["x"], "enabled": true}}}`)
	cfg, err := LoadConfig(l.opts(nil))
	require.NoError(t, err)
	require.False(t, cfg.MCP["x"].Disabled)

	// A toggle without any definition yields no server.
	writeFile(t, filepath.Join(l.sub, "opencode.json"), `{}`)
	cfg, err = LoadConfig(l.opts(nil))
	require.NoError(t, err)
	require.Empty(t, cfg.MCP)
}

func TestLoadConfigProviders(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	writeFile(t, filepath.Join(l.global, "opencode.json"), `{
		"provider": {
			"acme": {
				"name": "Acme",
				"npm": "@ai-sdk/openai-compatible",
				"api": "https://api.acme.dev/v1",
				"env": ["ACME_API_KEY"],
				"whitelist": ["m1"],
				"options": {
					"apiKey": "{env:ACME_KEY}",
					"baseURL": "https://ignored.example.com",
					"headers": {"X-One": "1", "X-Num": 2}
				},
				"models": {
					"m1": {
						"name": "Model One",
						"tool_call": true,
						"reasoning": true,
						"limit": {"context": 128000, "output": 8192},
						"cost": {"input": 1, "output": 2, "cache_read": 0.1, "cache_write": 1.25},
						"modalities": {"input": ["text", "image"], "output": ["text"]}
					},
					"old": {"status": "deprecated"}
				}
			},
			"opts-only": {"options": {"baseURL": "https://opts.example.com"}}
		}
	}`)
	writeFile(t, filepath.Join(l.sub, "opencode.json"), `{
		"providers": {
			"acme": {
				"package": "aisdk:@ai-sdk/anthropic",
				"settings": {"apiKey": "override"},
				"headers": {"X-Two": "2"},
				"models": {
					"m1": {"name": "Renamed", "disabled": true, "limit": {"output": 4096}},
					"m2": {
						"modelID": "acme-m2-2026",
						"package": "aisdk:@ai-sdk/openai",
						"settings": {"baseURL": "https://m2.acme.dev"},
						"capabilities": {"tools": false, "input": ["text"]},
						"cost": [{"input": 3, "output": 4, "cache": {"read": 0.3}}, {"tier": {"type": "context", "size": 200000}, "input": 6, "output": 8}]
					}
				}
			}
		}
	}`)

	cfg, err := LoadConfig(l.opts(map[string]string{"ACME_KEY": "k-1"}))
	require.NoError(t, err)
	require.Empty(t, cfg.Warnings)

	acme := cfg.Providers["acme"]
	require.Equal(t, "acme", acme.ID)
	require.Equal(t, "Acme", acme.Name)
	require.Equal(t, "@ai-sdk/anthropic", acme.NPM)
	require.Equal(t, "https://api.acme.dev/v1", acme.BaseURL, "V1 api beats options.baseURL")
	require.Equal(t, "override", acme.APIKey)
	require.Equal(t, []string{"ACME_API_KEY"}, acme.Env)
	require.Equal(t, []string{"m1"}, acme.Whitelist)
	require.Equal(t, map[string]string{"X-One": "1", "X-Two": "2"}, acme.Headers)

	tr := true
	require.Equal(t, Model{
		ID:               "m1",
		Name:             "Renamed",
		Limit:            Limit{Context: 128000, Output: 4096},
		Cost:             &Cost{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.25},
		ToolCall:         &tr,
		Reasoning:        &tr,
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
		Disabled:         &tr,
	}, acme.Models["m1"])
	require.True(t, *acme.Models["old"].Disabled)

	m2 := acme.Models["m2"]
	require.Equal(t, "acme-m2-2026", m2.ModelID)
	require.Equal(t, "@ai-sdk/openai", m2.NPM)
	require.Equal(t, "https://m2.acme.dev", m2.BaseURL)
	require.False(t, *m2.ToolCall)
	require.Equal(t, &Cost{Input: 3, Output: 4, CacheRead: 0.3}, m2.Cost)

	require.Equal(t, "https://opts.example.com", cfg.Providers["opts-only"].BaseURL)
}

func TestLoadConfigProviderLists(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	writeFile(t, filepath.Join(l.global, "opencode.json"), `{"enabled_providers": ["a", "b"], "disabled_providers": ["x"]}`)
	writeFile(t, filepath.Join(l.sub, "opencode.json"), `{"enabled_providers": ["c"], "disabled_providers": ["y", "x"]}`)
	cfg, err := LoadConfig(l.opts(nil))
	require.NoError(t, err)
	require.Equal(t, []string{"c"}, cfg.EnabledProviders)
	require.Equal(t, []string{"x", "y"}, cfg.DisabledProviders)
}

func TestParseModelRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{`"anthropic/claude-sonnet-4-6"`, "anthropic/claude-sonnet-4-6", true},
		{`"openrouter/anthropic/claude-opus-5"`, "openrouter/anthropic/claude-opus-5", true},
		{`"openai/gpt-5.6#high"`, "openai/gpt-5.6", true},
		{`{"providerID": "google", "model": "gemini-3-pro", "variant": "low"}`, "google/gemini-3-pro", true},
		{`"no-slash"`, "", false},
		{`"/model"`, "", false},
		{`"provider/"`, "", false},
		{`"provider/#variant"`, "", false},
		{`"pro#vider/model"`, "", false},
		{`42`, "", false},
	}
	for _, tt := range tests {
		got, ok := parseModelRef(json.RawMessage(tt.in))
		require.Equal(t, tt.ok, ok, tt.in)
		require.Equal(t, tt.want, got, tt.in)
	}
}

func TestLoadConfigContentRelativeFile(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	writeFile(t, filepath.Join(l.sub, "key.txt"), "from-cwd")
	cfg, err := LoadConfig(l.opts(map[string]string{
		"OPENCODE_CONFIG_CONTENT": `{"mcp": {"c": {"type": "remote", "url": "https://c.example.com", "headers": {"K": "{file:key.txt}"}}}}`,
	}))
	require.NoError(t, err)
	require.Equal(t, "from-cwd", cfg.MCP["c"].Headers["K"])
	require.Equal(t, ContentSource, cfg.MCP["c"].SourcePath)
}

func TestResolvePaths(t *testing.T) {
	t.Parallel()

	home := filepath.FromSlash("/home/u")
	p := ResolvePaths(mapEnv(nil), home)
	require.Equal(t, filepath.Join(home, ".config", "opencode"), p.ConfigDir)
	require.Equal(t, filepath.Join(home, ".local", "share", "opencode"), p.DataDir)
	require.Equal(t, filepath.Join(home, ".cache", "opencode"), p.CacheDir)
	require.Equal(t, home, p.Home)
	require.NotEmpty(t, p.ManagedDir)

	p = ResolvePaths(mapEnv(map[string]string{
		"XDG_CONFIG_HOME": "/xdg/config",
		"XDG_DATA_HOME":   "/xdg/data",
		"XDG_CACHE_HOME":  "/xdg/cache",
	}), home)
	require.Equal(t, filepath.Join("/xdg/config", "opencode"), p.ConfigDir)
	require.Equal(t, filepath.Join("/xdg/data", "opencode"), p.DataDir)
	require.Equal(t, filepath.Join("/xdg/cache", "opencode"), p.CacheDir)

	p = ResolvePaths(mapEnv(map[string]string{
		"XDG_CONFIG_HOME":     "/xdg/config",
		"OPENCODE_CONFIG_DIR": "/custom",
	}), home)
	require.Equal(t, "/custom", p.ConfigDir)

	require.Equal(t, "/etc/opencode", managedDir("linux", mapEnv(nil)))
	require.Equal(t, "/Library/Application Support/opencode", managedDir("darwin", mapEnv(nil)))
}
