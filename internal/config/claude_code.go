package config

import (
	"cmp"
	"reflect"
	"slices"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/claudecode"
)

// Claude Code provider options (provider_options in the config):
//
//	binary       path to the `claude` CLI; found on PATH when empty
//	use_api_key  keep the whole environment for the CLI: ANTHROPIC_API_KEY,
//	             CLAUDE_CODE_OAUTH_TOKEN, a custom ANTHROPIC_BASE_URL,
//	             Bedrock/Vertex switches and model overrides. Without it
//	             the CLI only ever uses the user's saved Claude login.
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

// ClaudeCodeUsesAPIKey reports whether the CLI keeps the API key and the
// rest of the auth and routing environment.
func (pc ProviderConfig) ClaudeCodeUsesAPIKey() bool {
	v, _ := pc.ProviderOptions[ClaudeCodeUseAPIKeyOption].(bool)
	return v
}

// claudeCodeAliases are the model aliases every CLI accepts. "default"
// lets Claude Code pick the model for the user's plan.
var claudeCodeAliases = []claudecode.ModelInfo{
	{Value: "default", DisplayName: "Default model"},
	{Value: "opus", DisplayName: "Opus"},
	{Value: "sonnet", DisplayName: "Sonnet"},
	{Value: "haiku", DisplayName: "Haiku"},
}

// ClaudeCodeModelsPath is where the model list from the CLI's last
// initialize reply is kept, next to the providers catalog.
func ClaudeCodeModelsPath() string { return cachePathFor("claude_code_models") }

// claudeCodeEffortDefault is the reasoning level that passes no --effort,
// leaving the choice to the CLI and the user's Claude Code settings.
const claudeCodeEffortDefault = "default"

// ClaudeCodeModels turns the CLI's model list into catalog models: the
// CLI's own rows first, then any built-in alias it left out, so a saved
// selection never disappears.
func ClaudeCodeModels(infos []claudecode.ModelInfo) []catwalk.Model {
	var out []catwalk.Model
	seen := map[string]bool{}
	for _, m := range slices.Concat(infos, claudeCodeAliases) {
		if m.Value == "" || seen[m.Value] {
			continue
		}
		seen[m.Value] = true
		model := catwalk.Model{
			ID:               m.Value,
			Name:             "Claude Code · " + cmp.Or(m.DisplayName, m.Value),
			ContextWindow:    200000,
			DefaultMaxTokens: 32000,
			SupportsImages:   true,
		}
		if strings.Contains(m.Value, "[1m]") || strings.Contains(m.ResolvedModel, "[1m]") {
			model.ContextWindow = 1000000
		}
		if levels := m.Efforts(); len(levels) > 0 {
			model.CanReason = true
			model.ReasoningLevels = append([]string{claudeCodeEffortDefault}, levels...)
			model.DefaultReasoningEffort = claudeCodeEffortDefault
		}
		out = append(out, model)
	}
	return out
}

// ClaudeCodeEffort returns the --effort level for the selected model, or
// "" to let the CLI decide.
func ClaudeCodeEffort(model catwalk.Model, selected SelectedModel) string {
	e := selected.ReasoningEffort
	if e == claudeCodeEffortDefault || !slices.Contains(model.ReasoningLevels, e) {
		return ""
	}
	return e
}

// prepareClaudeCodeProvider fills in a Claude Code provider's defaults.
// It needs no endpoint or key: the CLI handles its own login.
func prepareClaudeCodeProvider(id string, pc ProviderConfig) ProviderConfig {
	pc.ID = id
	pc.Name = cmp.Or(pc.Name, "Claude Code")
	if len(pc.Models) == 0 {
		pc.Models = ClaudeCodeModels(claudecode.LoadModels(ClaudeCodeModelsPath()))
		pc.ClaudeCodeDefaultModels = true
	}
	// Subscription turns cost nothing per token; with an API key the CLI
	// reports the real cost, which the engine adds itself.
	pc.FlatRate = true
	return pc
}

// SetClaudeCodeModels replaces the model list of every Claude Code
// provider whose models Ta-Da filled in. It reports whether anything
// changed.
func (c *Config) SetClaudeCodeModels(infos []claudecode.ModelInfo) bool {
	models := ClaudeCodeModels(infos)
	changed := false
	for id, pc := range c.Providers.Seq2() {
		if !pc.IsClaudeCode() || !pc.ClaudeCodeDefaultModels || reflect.DeepEqual(pc.Models, models) {
			continue
		}
		pc.Models = models
		c.Providers.Set(id, pc)
		changed = true
	}
	return changed
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
