package config

import (
	"cmp"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/claudecode"
)

// Claude Code provider options (provider_options in the config):
//
//	binary       path to the `claude` CLI; found on PATH when empty
//	use_api_key  keep ANTHROPIC_API_KEY in the CLI's environment so it
//	             bills the API key instead of the Claude subscription
const (
	ClaudeCodeBinaryOption    = "binary"
	ClaudeCodeUseAPIKeyOption = "use_api_key"
)

// IsClaudeCode reports whether the provider runs turns through the
// Claude Code CLI.
func (pc ProviderConfig) IsClaudeCode() bool {
	return string(pc.Type) == claudecode.ProviderType
}

// ClaudeCodeBinary returns the CLI the provider runs, or "" when none is
// configured or installed.
func (pc ProviderConfig) ClaudeCodeBinary() string {
	if s, ok := pc.ProviderOptions[ClaudeCodeBinaryOption].(string); ok && s != "" {
		return s
	}
	return findClaudeBinary()
}

// ClaudeCodeUsesAPIKey reports whether the CLI keeps ANTHROPIC_API_KEY.
func (pc ProviderConfig) ClaudeCodeUsesAPIKey() bool {
	v, _ := pc.ProviderOptions[ClaudeCodeUseAPIKeyOption].(bool)
	return v
}

// claudeCodeModels are the model aliases the CLI accepts. "default" lets
// Claude Code pick the model for the user's plan.
func claudeCodeModels() []catwalk.Model {
	mk := func(id, name string) catwalk.Model {
		return catwalk.Model{
			ID:               id,
			Name:             name,
			ContextWindow:    200000,
			DefaultMaxTokens: 32000,
			SupportsImages:   true,
		}
	}
	return []catwalk.Model{
		mk("default", "Claude Code (default model)"),
		mk("opus", "Claude Code · Opus"),
		mk("sonnet", "Claude Code · Sonnet"),
		mk("haiku", "Claude Code · Haiku"),
	}
}

// prepareClaudeCodeProvider fills in a Claude Code provider's defaults.
// It needs no endpoint or key: the CLI handles its own login.
func prepareClaudeCodeProvider(id string, pc ProviderConfig) ProviderConfig {
	pc.ID = id
	pc.Name = cmp.Or(pc.Name, "Claude Code")
	if len(pc.Models) == 0 {
		pc.Models = claudeCodeModels()
	}
	// Subscription turns cost nothing per token; with an API key the CLI
	// reports the real cost, which the engine adds itself.
	pc.FlatRate = true
	return pc
}

// findClaudeBinary locates the `claude` CLI. Tests swap it out so the
// developer's own install doesn't leak into them.
var findClaudeBinary = claudecode.FindBinary

// addClaudeCodeProvider offers the Claude Code engine when the CLI is
// installed and the user hasn't configured or disabled it.
func (c *Config) addClaudeCodeProvider() {
	if c.Options != nil && c.Options.DisableDefaultProviders {
		return
	}
	if _, ok := c.Providers.Get(claudecode.ProviderID); ok {
		return
	}
	if findClaudeBinary() == "" {
		return
	}
	c.Providers.Set(claudecode.ProviderID, ProviderConfig{
		ID:   claudecode.ProviderID,
		Type: catwalk.Type(claudecode.ProviderType),
	})
}
