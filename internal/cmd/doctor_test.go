package cmd

import (
	"errors"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/doctor"
	"github.com/stretchr/testify/require"
)

func TestCredentialOf(t *testing.T) {
	t.Parallel()

	env := map[string]string{"SET_KEY": "sk-secret"}
	var resolved []string
	resolve := func(s string) (string, error) {
		resolved = append(resolved, s)
		m := pureEnvRef.FindStringSubmatch(s)
		if m == nil {
			return "", errors.New("unexpected resolve")
		}
		return env[m[1]], nil
	}

	for _, tc := range []struct {
		name    string
		p       config.ProviderConfig
		cred    doctor.Credential
		envName string
	}{
		{"env set", config.ProviderConfig{ID: "a", APIKey: "$SET_KEY"}, doctor.CredEnv, "SET_KEY"},
		{"env braces", config.ProviderConfig{ID: "a", APIKey: "${SET_KEY}"}, doctor.CredEnv, "SET_KEY"},
		{"env unset", config.ProviderConfig{ID: "a", APIKey: "$UNSET_KEY"}, doctor.CredMissing, "UNSET_KEY"},
		{"template wins", config.ProviderConfig{ID: "a", APIKey: "resolved", APIKeyTemplate: "$SET_KEY"}, doctor.CredEnv, "SET_KEY"},
		{"command", config.ProviderConfig{ID: "a", APIKey: "$(pass show key)"}, doctor.CredCommand, ""},
		{"backticks", config.ProviderConfig{ID: "a", APIKey: "`pass show key`"}, doctor.CredCommand, ""},
		{"mixed", config.ProviderConfig{ID: "a", APIKey: "Bearer $TOKEN"}, doctor.CredEnv, "TOKEN"},
		{"literal", config.ProviderConfig{ID: "a", APIKey: "sk-123"}, doctor.CredConfig, ""},
		{"bedrock", config.ProviderConfig{ID: "bedrock", Type: catwalk.TypeBedrock}, doctor.CredAWS, ""},
		{"vertex", config.ProviderConfig{ID: "vertexai"}, doctor.CredVertex, ""},
		{"ollama", config.ProviderConfig{ID: "ollama"}, doctor.CredLocal, ""},
		{"private host", config.ProviderConfig{ID: "box", BaseURL: "http://192.168.1.4:8080/v1"}, doctor.CredLocal, ""},
		{"unset local", config.ProviderConfig{ID: "box", APIKey: "$UNSET_KEY", BaseURL: "http://localhost:8080"}, doctor.CredLocal, ""},
		{"nothing", config.ProviderConfig{ID: "x", BaseURL: "https://api.example.com"}, doctor.CredMissing, ""},
	} {
		cred, envName := credentialOf(tc.p, resolve)
		require.Equal(t, tc.cred, cred, tc.name)
		require.Equal(t, tc.envName, envName, tc.name)
	}
	for _, s := range resolved {
		require.NotContains(t, s, "$(", "resolved a command")
		require.NotContains(t, s, "`", "resolved a command")
	}
}

func TestRedactText(t *testing.T) {
	t.Parallel()
	require.Equal(t,
		"Get https://api.example.com/v1/models: refused",
		redactText("Get https://user:sk-secret@api.example.com/v1/models: refused"),
	)
	require.Equal(t, "no url here", redactText("no url here"))
}

func TestDisplayURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://api.example.com/v1", displayURL("https://u:p@api.example.com/v1?key=x#f"))
	require.Empty(t, displayURL("$BASE_URL"))
	require.Empty(t, displayURL("unix:///tmp/sock"))
	require.Empty(t, displayURL(""))
}

func TestSortLoadRecords(t *testing.T) {
	t.Parallel()
	skipped, findings := sortLoadRecords([]loadRecord{
		{Msg: "No git repository detected"},
		{Msg: "Skipping provider due to missing API key", Attrs: map[string]string{"provider": "openai"}},
		{Msg: "Skipping provider due to missing API key", Attrs: map[string]string{"provider": "mine"}},
		{Msg: "Model discovery failed", Attrs: map[string]string{
			"provider": "box",
			"error":    "Get http://u:secret@box:8080/v1/models: refused",
		}},
		{Msg: "Something odd.", Attrs: map[string]string{"url": "https://k:secret@x.dev", "b": "2"}},
	}, map[string]string{"openai": "OPENAI_API_KEY"})

	require.Len(t, skipped, 3)
	require.Equal(t, "openai: skipped, $OPENAI_API_KEY isn't set", skipped[0].Text)
	require.Equal(t, "mine: skipped, no API key", skipped[1].Text)
	require.Equal(t, "box: couldn't list its models: Get http://box:8080/v1/models: refused", skipped[2].Text)

	require.Len(t, findings, 1)
	require.Equal(t, "Something odd (b: 2, url: https://x.dev)", findings[0].Text)
	for _, it := range append(skipped, findings...) {
		require.NotContains(t, it.Text, "secret")
	}
}

func TestLoadErrText(t *testing.T) {
	t.Parallel()
	require.Empty(t, loadErrText(nil))
	require.Equal(t,
		"tadarc:3: unknown builtin",
		loadErrText(errors.New("failed to load config from paths [a b]: tadarc:3: unknown builtin")),
	)
}

func TestLocalServers(t *testing.T) {
	t.Parallel()

	servers := localServers(nil)
	require.Len(t, servers, 2)
	require.True(t, servers[0].Ollama)

	servers = localServers([]doctor.ProviderInfo{
		{ID: "ollama", Credential: doctor.CredLocal, BaseURL: "http://localhost:11434/v1"},
		{ID: "box", Credential: doctor.CredLocal, BaseURL: "http://192.168.1.4:8080/v1/"},
		{ID: "openai", Credential: doctor.CredEnv, BaseURL: "https://api.openai.com/v1"},
	})
	require.Len(t, servers, 3)
	require.Equal(t, "Ollama", servers[0].Name)
	require.Equal(t, "http://localhost:11434/v1/models", servers[0].ListURL)
	require.False(t, servers[0].Remote)
	require.Equal(t, "http://192.168.1.4:8080/v1/models", servers[1].ListURL)
	require.True(t, servers[1].Remote)
	require.Equal(t, "LM Studio", servers[2].Name)
}

func TestIsLocalHost(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"localhost":      true,
		"127.0.0.1":      true,
		"10.0.0.3":       true,
		"box.local":      true,
		"api.openai.com": false,
		"8.8.8.8":        false,
		"":               false,
	} {
		require.Equal(t, want, isLocalHost(host), host)
	}
}
