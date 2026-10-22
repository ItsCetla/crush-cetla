package doctor

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeEnv is an Env with canned answers. It records the URLs it was asked
// to fetch.
type fakeEnv struct {
	mu        sync.Mutex
	paths     map[string]string
	responses map[string]string
	requests  []string
}

func (f *fakeEnv) env(online bool) *Env {
	return &Env{
		LookPath: func(file string) (string, error) {
			if p, ok := f.paths[file]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Getenv: func(string) string { return "" },
		Stat:   os.Stat,
		HTTP: func(_ context.Context, method, rawURL string) (int, []byte, error) {
			f.mu.Lock()
			f.requests = append(f.requests, method+" "+rawURL)
			f.mu.Unlock()
			body, ok := f.responses[rawURL]
			if !ok {
				return 0, nil, errors.New("connection refused")
			}
			return 200, []byte(body), nil
		},
		Online: online,
	}
}

func run(t *testing.T, c Check, env *Env) Result {
	t.Helper()
	r := Run(t.Context(), env, []Check{c})
	require.Len(t, r.Results, 1)
	return r.Results[0]
}

func texts(items []Item) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(string(it.Status) + " " + it.Text + "\n")
	}
	return b.String()
}

func TestProvidersCheck(t *testing.T) {
	t.Parallel()

	t.Run("none configured fails with a hint", func(t *testing.T) {
		t.Parallel()
		res := run(t, ProvidersCheck(ProvidersInfo{}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusFail, res.Status)
		require.Contains(t, res.Hint, "ANTHROPIC_API_KEY")
	})

	t.Run("credentials say where they come from, never what they are", func(t *testing.T) {
		t.Parallel()
		res := run(t, ProvidersCheck(ProvidersInfo{
			Providers: []ProviderInfo{
				{ID: "anthropic", Credential: CredEnv, EnvName: "ANTHROPIC_API_KEY"},
				{ID: "custom", Credential: CredConfig},
				{ID: "vault", Credential: CredCommand},
				{ID: "openai", Credential: CredMissing, EnvName: "OPENAI_API_KEY"},
				{ID: "my.thing", Credential: CredMissing},
			},
			Skipped: []Item{{Status: StatusWarn, Text: "gemini: skipped"}},
		}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusWarn, res.Status)
		require.Equal(t, "anthropic, custom, vault, openai, my.thing", res.Summary)
		out := texts(res.Items)
		require.Contains(t, out, "ok anthropic: API key set via $ANTHROPIC_API_KEY")
		require.Contains(t, out, "ok custom: API key from config")
		require.Contains(t, out, "ok vault: API key from a command in the config")
		require.Contains(t, out, "warn openai: no API key; $OPENAI_API_KEY isn't set")
		require.Contains(t, out, "warn gemini: skipped")
		require.Contains(t, res.Items[4].Hint, "$MY_THING_API_KEY")
	})

	t.Run("online sends one bare HEAD per remote host", func(t *testing.T) {
		t.Parallel()
		fake := &fakeEnv{responses: map[string]string{"https://api.example.com/v1": ""}}
		res := run(t, ProvidersCheck(ProvidersInfo{Providers: []ProviderInfo{
			{ID: "a", Credential: CredConfig, BaseURL: "https://api.example.com/v1"},
			{ID: "b", Credential: CredConfig, BaseURL: "https://api.example.com/v1"},
			{ID: "c", Credential: CredConfig, BaseURL: "https://down.example.com"},
			{ID: "ollama", Credential: CredLocal, BaseURL: "http://localhost:11434/v1"},
		}}), fake.env(true))
		require.Equal(t, []string{"HEAD https://api.example.com/v1", "HEAD https://down.example.com"}, fake.requests)
		out := texts(res.Items)
		require.Contains(t, out, "ok api.example.com answered (HTTP 200)")
		require.Contains(t, out, "warn Can't reach down.example.com: connection refused")
	})

	t.Run("offline contacts nothing", func(t *testing.T) {
		t.Parallel()
		fake := &fakeEnv{}
		run(t, ProvidersCheck(ProvidersInfo{Providers: []ProviderInfo{
			{ID: "a", Credential: CredConfig, BaseURL: "https://api.example.com"},
		}}), fake.env(false))
		require.Empty(t, fake.requests)
	})
}

func TestBareURL(t *testing.T) {
	t.Parallel()
	u, err := url.Parse("https://user:secret@api.example.com/v1?key=secret#frag")
	require.NoError(t, err)
	require.Equal(t, "https://api.example.com/v1", bareURL(u))
	require.Equal(t, "https://api.example.com/v1?…", redactURL("https://user:secret@api.example.com/v1?key=secret"))
}

func TestModelsCheck(t *testing.T) {
	t.Parallel()

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		m := ModelInfo{Provider: "anthropic", Model: "claude", Found: true}
		res := run(t, ModelsCheck(ModelsInfo{Providers: true, Large: m, Small: m}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusOK, res.Status)
		require.Equal(t, "large anthropic/claude", res.Summary)
	})

	t.Run("missing model fails, fallback warns", func(t *testing.T) {
		t.Parallel()
		res := run(t, ModelsCheck(ModelsInfo{
			Providers: true,
			Large:     ModelInfo{Provider: "openai", Model: "gone"},
			Small:     ModelInfo{Provider: "openai", Model: "mini", Found: true, FallbackFrom: "openai/old"},
		}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusFail, res.Status)
		out := texts(res.Items)
		require.Contains(t, out, "fail large: openai/gone isn't offered by openai")
		require.Contains(t, out, "warn small: openai/old isn't available, so Ta-Da switched to openai/mini")
	})

	t.Run("no providers skips", func(t *testing.T) {
		t.Parallel()
		res := run(t, ModelsCheck(ModelsInfo{}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusSkip, res.Status)
	})
}

func TestLocalModelsCheck(t *testing.T) {
	t.Parallel()

	t.Run("counts models and skips silent defaults", func(t *testing.T) {
		t.Parallel()
		fake := &fakeEnv{responses: map[string]string{
			"http://localhost:11434/api/tags": `{"models":[{"name":"llama"},{"name":"qwen"}]}`,
		}}
		res := run(t, LocalModelsCheck([]LocalServer{
			{Name: "Ollama", ListURL: "http://localhost:11434/api/tags", Ollama: true},
			{Name: "LM Studio", ListURL: "http://localhost:1234/v1/models"},
		}), fake.env(false))
		require.Equal(t, StatusOK, res.Status)
		require.Equal(t, "Ollama running", res.Summary)
		out := texts(res.Items)
		require.Contains(t, out, "ok Ollama at localhost:11434 has 2 models")
		require.Contains(t, out, "skip Nothing answers at localhost:1234 (LM Studio)")
	})

	t.Run("configured server that's down warns", func(t *testing.T) {
		t.Parallel()
		res := run(t, LocalModelsCheck([]LocalServer{
			{Name: "Ollama", ListURL: "http://localhost:11434/v1/models", Provider: "ollama"},
		}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusWarn, res.Status)
		require.Equal(t, "A configured local server isn't running", res.Summary)
	})

	t.Run("remote servers wait for online", func(t *testing.T) {
		t.Parallel()
		fake := &fakeEnv{}
		res := run(t, LocalModelsCheck([]LocalServer{
			{Name: "box", ListURL: "http://192.168.1.5:8080/v1/models", Provider: "box", Remote: true},
		}), fake.env(false))
		require.Empty(t, fake.requests)
		require.Equal(t, StatusSkip, res.Items[0].Status)
	})
}

func TestCountModels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body   string
		ollama bool
		n      int
		ok     bool
	}{
		{`{"models":[{},{}]}`, true, 2, true},
		{`{"data":[{}]}`, false, 1, true},
		{`{"data":[]}`, false, 0, true},
		{`{"object":"list"}`, false, 0, false},
		{`<html>`, false, 0, false},
	} {
		n, ok := countModels([]byte(tc.body), tc.ollama)
		require.Equal(t, tc.n, n, tc.body)
		require.Equal(t, tc.ok, ok, tc.body)
	}
}

func TestConfigCheck(t *testing.T) {
	t.Parallel()

	t.Run("syntax errors fail with file and line", func(t *testing.T) {
		t.Parallel()
		res := run(t, ConfigCheck(ConfigInfo{Files: []ConfigFile{
			{Path: "tadarc", Scope: "project", Err: "3:5: reached EOF without closing quote"},
		}}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusFail, res.Status)
		require.Equal(t, "The config doesn't load", res.Summary)
		require.Equal(t, "tadarc:3:5: reached EOF without closing quote", res.Items[0].Text)
	})

	t.Run("tada.json is deprecated, state files aren't", func(t *testing.T) {
		t.Parallel()
		res := run(t, ConfigCheck(ConfigInfo{Files: []ConfigFile{
			{Path: "~/.config/tada/tada.json", Scope: "global", JSON: true},
			{Path: "~/.local/share/tada/tada.json", Scope: "global", JSON: true, State: true},
			{Path: "tadarc", Scope: "project", Untrusted: true},
		}}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusWarn, res.Status)
		require.Equal(t, "2 files loaded, 1 skipped (untrusted), tada.json is deprecated", res.Summary)
	})

	t.Run("no files is fine", func(t *testing.T) {
		t.Parallel()
		res := run(t, ConfigCheck(ConfigInfo{}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusOK, res.Status)
	})
}

func TestValidateJSON(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateJSON([]byte(`{"a": 1}`)))
	require.NoError(t, ValidateJSON([]byte("  \n")))
	err := ValidateJSON([]byte("{\n  \"a\": 1,\n}"))
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "3:1: "), err.Error())
}

func TestPermissionsCheck(t *testing.T) {
	t.Parallel()

	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		res := run(t, PermissionsCheck(PermissionsInfo{}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusOK, res.Status)
		require.Equal(t, "Defaults", res.Summary)
	})

	t.Run("bash allowed and empty always_ask warn", func(t *testing.T) {
		t.Parallel()
		res := run(t, PermissionsCheck(PermissionsInfo{
			AllowedTools: []string{"view", "bash"},
			AlwaysAsk:    []string{},
		}), (&fakeEnv{}).env(false))
		require.Equal(t, StatusWarn, res.Status)
		require.Equal(t, "Customized; some actions skip the prompt", res.Summary)
	})
}

func TestTerminalCheck(t *testing.T) {
	t.Parallel()
	res := run(t, TerminalCheck(TerminalInfo{TTY: true, Width: 60, Height: 20, Term: "dumb"}), (&fakeEnv{}).env(false))
	require.Equal(t, StatusWarn, res.Status)
	require.Len(t, res.Items, 3)

	res = run(t, TerminalCheck(TerminalInfo{TTY: true, Width: 200, Height: 50, Term: "xterm-256color", Profile: "TrueColor"}), (&fakeEnv{}).env(false))
	require.Equal(t, StatusOK, res.Status)

	res = run(t, TerminalCheck(TerminalInfo{}), (&fakeEnv{}).env(false))
	require.Equal(t, StatusSkip, res.Status)
}

func TestDataDirCheck(t *testing.T) {
	t.Parallel()
	res := run(t, DataDirCheck(DataDirInfo{Path: "/x", WriteErr: "permission denied", FreeBytes: -1}), (&fakeEnv{}).env(false))
	require.Equal(t, StatusFail, res.Status)

	res = run(t, DataDirCheck(DataDirInfo{Path: "/x", Exists: true, FreeBytes: 100 << 20}), (&fakeEnv{}).env(false))
	require.Equal(t, StatusWarn, res.Status)
	require.Contains(t, texts(res.Items), "Only 100.0 MiB free")

	res = run(t, DataDirCheck(DataDirInfo{Path: "/x", Exists: true, FreeBytes: 10 << 30}), (&fakeEnv{}).env(false))
	require.Equal(t, StatusOK, res.Status)
}

func TestHooksCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := filepath.Join(dir, "guard.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"), 0o644))

	fake := &fakeEnv{paths: map[string]string{"jq": "/usr/bin/jq"}}
	res := run(t, HooksCheck([]Hook{
		{Event: "PreToolUse", Command: "jq -r .tool_name"},
		{Event: "PreToolUse", Name: "guard", Command: "./guard.sh"},
		{Event: "PreToolUse", Command: "nope --flag"},
		{Event: "PreToolUse", Command: "echo hi"},
	}, dir, dir), fake.env(false))
	require.Equal(t, StatusWarn, res.Status)
	out := texts(res.Items)
	require.Contains(t, out, "ok PreToolUse: /usr/bin/jq")
	require.Contains(t, out, "isn't executable")
	require.Contains(t, out, "warn PreToolUse: `nope` isn't on your PATH")
	require.Contains(t, out, "ok PreToolUse: echo (shell builtin)")
}

func TestRunRecoversPanics(t *testing.T) {
	t.Parallel()
	r := Run(t.Context(), (&fakeEnv{}).env(false), []Check{
		{ID: "boom", Name: "Boom", Run: func(context.Context, *Env) Result { panic("oops") }},
		{ID: "fine", Name: "Fine", Run: func(context.Context, *Env) Result {
			return Result{Items: []Item{{Status: StatusOK, Text: "x"}}}
		}},
	})
	require.Equal(t, 1, r.Fail)
	require.Equal(t, 1, r.OK)
	require.True(t, r.HasFail())
	require.Equal(t, "boom", r.Results[0].ID)
	require.Equal(t, StatusOK, r.Results[1].Status)
}
