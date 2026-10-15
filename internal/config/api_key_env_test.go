package config

import (
	"testing"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyEnvVars(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("openai", ProviderConfig{APIKey: "$OPENAI_API_KEY", APIKeyTemplate: "$OPENAI_API_KEY"})
	providers.Set("custom", ProviderConfig{APIKey: "resolved", APIKeyTemplate: "${MY_PROXY_KEY}"})
	providers.Set("prefixed", ProviderConfig{APIKey: "Bearer $TEAM_TOKEN-$OPENAI_API_KEY"})
	providers.Set("disabled", ProviderConfig{APIKey: "${OLD_KEY:-fallback}", Disable: true})
	providers.Set("literal", ProviderConfig{APIKey: "sk-dummy"})
	providers.Set("empty", ProviderConfig{})

	cfg := &Config{Providers: providers}
	require.Equal(t, []string{"MY_PROXY_KEY", "OLD_KEY", "OPENAI_API_KEY", "TEAM_TOKEN"}, cfg.APIKeyEnvVars())

	require.Empty(t, (&Config{}).APIKeyEnvVars())
}
