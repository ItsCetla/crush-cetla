package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestGetModelIncludesChatGPTModels(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("openai", ProviderConfig{
		Models:        []catwalk.Model{{ID: "gpt-5.1"}},
		ChatGPTModels: []catwalk.Model{{ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex"}},
	})
	cfg := &Config{Providers: providers}

	model := cfg.GetModel("openai", "gpt-5.1-codex")
	require.NotNil(t, model)
	require.Equal(t, "GPT-5.1 Codex", model.Name)

	require.NotNil(t, cfg.GetModel("openai", "gpt-5.1"))
	require.Nil(t, cfg.GetModel("openai", "missing"))
}

func TestTokenFields(t *testing.T) {
	t.Parallel()

	t.Run("openai keeps the api key", func(t *testing.T) {
		t.Parallel()

		fields := tokenFields("openai", &oauth.Token{AccessToken: "at"})
		require.Contains(t, fields, "providers.openai.oauth")
		require.NotContains(t, fields, "providers.openai.api_key")
	})

	t.Run("other providers mirror the access token", func(t *testing.T) {
		t.Parallel()

		fields := tokenFields("hyper", &oauth.Token{AccessToken: "at"})
		require.Contains(t, fields, "providers.hyper.oauth")
		require.Equal(t, "at", fields["providers.hyper.api_key"])
	})
}

func TestTokenAccountIDRoundTrip(t *testing.T) {
	t.Parallel()

	// The account ID is persisted with the token in tada.json; verify
	// it survives the JSON round trip loadTokenFromDisk performs.
	token := &oauth.Token{
		AccessToken:  "at",
		RefreshToken: "rt",
		IDToken:      "idt",
		AccountID:    "acct-42",
		ExpiresIn:    3600,
		ExpiresAt:    4102444800,
	}

	data, err := json.Marshal(token)
	require.NoError(t, err)

	var restored oauth.Token
	require.NoError(t, json.Unmarshal(data, &restored))
	require.Equal(t, "acct-42", restored.AccountID)
	require.Equal(t, "idt", restored.IDToken)
	require.Equal(t, "rt", restored.RefreshToken)
}

// testResolver mimics the shell resolver for templates: variables
// resolve to empty when unset, literals pass through.
type testResolver struct{}

func (testResolver) ResolveValue(v string) (string, error) {
	if strings.HasPrefix(v, "$") {
		return "", nil
	}
	return v, nil
}

func TestHasAPIKey(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		require.False(t, (&ProviderConfig{}).HasAPIKey(testResolver{}))
	})

	t.Run("literal key", func(t *testing.T) {
		t.Parallel()

		require.True(t, (&ProviderConfig{APIKey: "sk-key"}).HasAPIKey(testResolver{}))
	})

	t.Run("unresolved template", func(t *testing.T) {
		t.Parallel()

		require.False(t, (&ProviderConfig{APIKey: "$OPENAI_API_KEY"}).HasAPIKey(testResolver{}))
	})
}

// TestSetProviderAPIKeyOpenAIIsEitherOr proves the OpenAI provider holds
// exactly one credential: a ChatGPT login retires a previously entered
// API key, and entering an API key retires a previous ChatGPT login.
// Copilot keeps mirroring the access token into api_key as before.
func TestSetProviderAPIKeyOpenAIIsEitherOr(t *testing.T) {
	// Not parallel: t.Setenv below.

	// Point config discovery at the test sandbox: the write below
	// triggers an auto-reload, which must not pick up the developer's
	// real tada.json.
	t.Setenv("TADA_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("TADA_GLOBAL_DATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	token := &oauth.Token{
		AccessToken:  "chatgpt-at",
		RefreshToken: "chatgpt-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}

	newStore := func(t *testing.T, providerID string, initialConfig string) *ConfigStore {
		t.Helper()
		dir := t.TempDir()
		configPath := filepath.Join(dir, "tada.json")
		require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

		// The in-memory config mirrors what a real load would produce.
		// SetConfigFields auto-reloads from configPath, so any state the
		// assertions expect to survive must be on disk.
		providers := csync.NewMap[string, ProviderConfig]()
		require.NoError(t, json.Unmarshal([]byte(initialConfig), &struct {
			Providers *csync.Map[string, ProviderConfig] `json:"providers"`
		}{Providers: providers}))

		originalFetch := fetchOpenAIModels
		fetchOpenAIModels = func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
			return []catwalk.Model{{ID: "gpt-5.1-codex"}}, nil
		}
		t.Cleanup(func() { fetchOpenAIModels = originalFetch })

		return &ConfigStore{
			config:         &Config{Providers: providers},
			globalDataPath: configPath,
			workingDir:     dir,
		}
	}

	t.Run("chatgpt login retires the api key", func(t *testing.T) {
		store := newStore(t, "openai", `{
			"providers": {
				"openai": {
					"id": "openai",
					"api_key": "sk-keep",
					"models": [{"id": "gpt-5.1", "name": "GPT-5.1"}]
				}
			}
		}`)

		require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, "openai", token))

		pc, ok := store.Config().Providers.Get("openai")
		require.True(t, ok)
		require.Empty(t, pc.APIKey, "the ChatGPT login replaces the API key")
		require.Equal(t, token, pc.OAuthToken)
		require.Equal(t, "gpt-5.1-codex", pc.ChatGPTModels[0].ID, "the subscription catalog lands in its own field")
		require.Equal(t, "gpt-5.1", pc.Models[0].ID, "the API catalog is untouched")

		disk, err := os.ReadFile(store.globalDataPath)
		require.NoError(t, err)
		require.NotContains(t, string(disk), "sk-keep", "the retired key is gone from the config file")
		require.Contains(t, string(disk), "chatgpt-rt", "the login is persisted")
	})

	t.Run("api key retires the chatgpt login", func(t *testing.T) {
		store := newStore(t, "openai", `{
			"providers": {
				"openai": {
					"id": "openai",
					"api_key": "",
					"oauth": {"access_token": "old-at", "refresh_token": "old-rt"},
					"chatgpt_models": [{"id": "gpt-5.6-luna"}]
				}
			}
		}`)

		require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, "openai", "sk-new"))

		pc, ok := store.Config().Providers.Get("openai")
		require.True(t, ok)
		require.Equal(t, "sk-new", pc.APIKey)
		require.Nil(t, pc.OAuthToken, "the API key replaces the ChatGPT login")
		require.Empty(t, pc.ChatGPTModels, "the subscription catalog goes with it")

		disk, err := os.ReadFile(store.globalDataPath)
		require.NoError(t, err)
		require.NotContains(t, string(disk), "old-rt", "the retired login is gone from the config file")
		require.NotContains(t, string(disk), "gpt-5.6-luna")
	})

	t.Run("copilot", func(t *testing.T) {
		store := newStore(t, "copilot", `{"providers":{"copilot":{"id":"copilot"}}}`)

		require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, "copilot", token))

		pc, ok := store.Config().Providers.Get("copilot")
		require.True(t, ok)
		require.Equal(t, "chatgpt-at", pc.APIKey, "copilot still mirrors the access token into api_key")
		require.Equal(t, token, pc.OAuthToken)
	})
}

// TestRefetchOpenAIModelsRefreshesExpiredToken pins the behavior of the
// catalog refresh when the stored ChatGPT token has expired: the refresh
// renews the token before fetching, so the request is not rejected with a
// 401, and the fetched catalog replaces the persisted one.
func TestRefetchOpenAIModelsRefreshesExpiredToken(t *testing.T) {
	// Not parallel: swaps the fetchOpenAIModels package variable.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "tada.json")

	expired := &oauth.Token{
		AccessToken:  "expired-at",
		RefreshToken: "expired-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(-time.Hour).Unix(),
	}
	fresh := &oauth.Token{
		AccessToken:  "fresh-at",
		RefreshToken: "fresh-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("openai", ProviderConfig{
		ID:            "openai",
		OAuthToken:    expired,
		ChatGPTModels: []catwalk.Model{{ID: "gpt-stale"}},
	})
	store := &ConfigStore{
		config:         &Config{Providers: providers},
		globalDataPath: configPath,
		exchangeToken: func(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error) {
			require.Equal(t, "openai", providerID)
			require.Equal(t, "expired-rt", refreshToken)
			return fresh, nil
		},
	}

	originalFetch := fetchOpenAIModels
	var fetchToken *oauth.Token
	fetchOpenAIModels = func(_ context.Context, token *oauth.Token) ([]catwalk.Model, error) {
		fetchToken = token
		return []catwalk.Model{{ID: "gpt-fresh"}}, nil
	}
	t.Cleanup(func() { fetchOpenAIModels = originalFetch })

	store.refetchOpenAIModels(t.Context(), ScopeGlobal)

	require.Equal(t, fresh, fetchToken, "the catalog is fetched with the renewed token")

	pc, ok := store.Config().Providers.Get("openai")
	require.True(t, ok)
	require.Equal(t, fresh, pc.OAuthToken, "the renewed token is kept in the config")
	require.Equal(t, "gpt-fresh", pc.ChatGPTModels[0].ID, "the existing catalog is replaced")

	disk, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(disk), "gpt-fresh", "the fetched catalog is persisted")
	require.Contains(t, string(disk), "fresh-rt", "the renewed token is persisted")
}

// chatGPTLoginConfig is a project config for a ChatGPT login whose
// persisted model catalog has gone stale.
func chatGPTLoginConfig(token *oauth.Token) string {
	tokenJSON, err := json.Marshal(token)
	if err != nil {
		panic(err)
	}
	return `{
		"providers": {
			"openai": {
				"id": "openai",
				"oauth": ` + string(tokenJSON) + `,
				"chatgpt_models": [{"id": "gpt-stale", "name": "GPT Stale"}]
			}
		}
	}`
}

// chatGPTLoginToken returns a token that is valid long enough for a test
// run, so the catalog refresh does not spend time renewing it.
func chatGPTLoginToken() *oauth.Token {
	return &oauth.Token{
		AccessToken:  "chatgpt-at",
		RefreshToken: "chatgpt-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}
}

// isolateLoadEnv points the config discovery at throwaway directories.
func isolateLoadEnv(t *testing.T) (workDir, dataDir string) {
	t.Helper()

	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("TADA_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "tada"))
	t.Setenv("TADA_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "tada"))
	return t.TempDir(), t.TempDir()
}

// stubChatGPTModels replaces the ChatGPT catalog fetch with one that
// reports whether it ran and what it returns.
func stubChatGPTModels(t *testing.T, models []catwalk.Model) *bool {
	t.Helper()

	originalFetch := fetchOpenAIModels
	called := false
	fetchOpenAIModels = func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
		called = true
		return models, nil
	}
	t.Cleanup(func() { fetchOpenAIModels = originalFetch })
	return &called
}

// TestLoadDoesNotRefreshChatGPTModels verifies that startup stays offline
// for the model catalogs that shadow Catwalk's: Load neither fetches the
// provider catalog nor refreshes a persisted ChatGPT model catalog, and the
// default models still resolve from the local catalog.
func TestLoadDoesNotRefreshChatGPTModels(t *testing.T) {
	// Not parallel: swaps package-level provider state and the fetch
	// stub, and sets environment variables.
	workDir, dataDir := isolateLoadEnv(t)
	forbidCatalogRequests(t)
	resetProviderState()
	t.Cleanup(resetProviderState)

	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, "tada.json"),
		[]byte(chatGPTLoginConfig(chatGPTLoginToken())),
		0o600,
	))

	fetched := stubChatGPTModels(t, []catwalk.Model{{ID: "gpt-fresh", Name: "GPT Fresh"}})

	store, err := Load(workDir, dataDir, false)
	require.NoError(t, err)
	require.False(t, *fetched, "startup does not refresh the ChatGPT catalog")

	pc, ok := store.Config().Providers.Get("openai")
	require.True(t, ok)
	require.Equal(t, "gpt-stale", pc.ChatGPTModels[0].ID, "the persisted catalog is kept")

	large, ok := store.Config().Models[SelectedModelTypeLarge]
	require.True(t, ok)
	require.Equal(t, "openai", large.Provider, "the default model resolves from the local catalog")
}
