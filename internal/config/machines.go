package config

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
)

// Defaults for machine models whose server does not report limits.
const (
	defaultMachineContextWindow = 32_768
	defaultMachineMaxTokens     = 4_096
)

// MachineConfig describes a computer on the user's network that serves
// models over an OpenAI-compatible API, and how many agents it can run at
// once. Ta-Da sends sub-agent work to free machines and tells the AI what
// each one is good for.
type MachineConfig struct {
	Hardware      string       `json:"hardware,omitempty" jsonschema:"description=What the machine is\\, shown to the AI,example=M1 Max 64GB"`
	URL           string       `json:"url" jsonschema:"description=OpenAI-compatible base URL of the machine's model server,format=uri,example=http://192.168.1.20:11434/v1"`
	Type          catwalk.Type `json:"type,omitempty" jsonschema:"description=Server type: openai-compat\\, ollama\\, lmstudio\\, llamacpp\\, omlx or litellm,default=openai-compat"`
	APIKey        string       `json:"api_key,omitempty" jsonschema:"description=API key for the server\\, if it needs one"`
	Model         string       `json:"model,omitempty" jsonschema:"description=Model agents on this machine use. Empty uses the first model the server reports,example=qwen2.5-coder:14b"`
	ContextWindow int64        `json:"context_window,omitempty" jsonschema:"description=Context window to assume when the server doesn't report one,example=32768"`
	Slots         int          `json:"slots,omitempty" jsonschema:"description=How many agents the machine can run at once,default=1,minimum=1"`
	Tags          []string     `json:"tags,omitempty" jsonschema:"description=What the machine is good at\\, shown to the AI,example=fast,example=code"`
	Role          string       `json:"role,omitempty" jsonschema:"description=The job the machine is assigned\\, shown to the AI,example=code review and tests"`
	Notes         string       `json:"notes,omitempty" jsonschema:"description=Anything else the AI should know about the machine"`
	Disabled      bool         `json:"disabled,omitempty" jsonschema:"description=Keep the machine configured but don't use it,default=false"`
}

// SlotCount returns how many agents the machine runs at once (at least 1).
func (m MachineConfig) SlotCount() int {
	return max(m.Slots, 1)
}

// Machines maps a machine name to its config.
type Machines map[string]MachineConfig

// Names returns the machine names in sorted order.
func (ms Machines) Names() []string {
	return slices.Sorted(maps.Keys(ms))
}

// ValidMachineName reports whether name can name a machine: letters,
// digits, dashes and underscores, so it doubles as a provider ID and a
// directory name.
func ValidMachineName(name string) error {
	if name == "" {
		return fmt.Errorf("machine name is empty")
	}
	if strings.EqualFold(name, "auto") || strings.EqualFold(name, "main") {
		return fmt.Errorf("%q is reserved", name)
	}
	for _, r := range name {
		ok := r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !ok {
			return fmt.Errorf("machine name %q may only use letters, digits, - and _", name)
		}
	}
	return nil
}

// machineProviderIDs returns the IDs of the providers Ta-Da made for
// machines on this load.
func (c *Config) machineProviderIDs() map[string]bool {
	ids := map[string]bool{}
	for name, m := range c.Machines {
		if !m.Disabled && m.URL != "" {
			ids[name] = true
		}
	}
	return ids
}

// addMachineProviders gives each enabled machine a custom provider named
// after it, so its models are selectable and agents can run on it. A
// provider the user already configured under the same name is left alone
// and the machine uses it.
func (c *Config) addMachineProviders() {
	for _, name := range c.Machines.Names() {
		m := c.Machines[name]
		if m.Disabled || m.URL == "" {
			continue
		}
		if err := ValidMachineName(name); err != nil {
			slog.Warn("Skipping machine with an invalid name", "machine", name, "error", err)
			continue
		}
		if _, ok := c.Providers.Get(name); ok {
			slog.Info("Machine uses the provider configured under its name", "machine", name)
			continue
		}
		discoverModels := true
		var models []catwalk.Model
		if m.Model != "" {
			models = []catwalk.Model{{ID: m.Model, Name: m.Model, ContextWindow: m.ContextWindow}}
		}
		label := name
		if m.Hardware != "" {
			label = fmt.Sprintf("%s (%s)", name, m.Hardware)
		}
		c.Providers.Set(name, ProviderConfig{
			ID:                 name,
			Name:               label,
			BaseURL:            m.URL,
			Type:               cmp.Or(m.Type, catwalk.TypeOpenAICompat),
			APIKey:             m.APIKey,
			AutoDiscoverModels: &discoverModels,
			FlatRate:           true,
			Models:             models,
		})
	}
}

// finishMachineProviders fills in the limits machine servers didn't
// report, so a discovered model never runs with a zero context window or
// output budget.
func (c *Config) finishMachineProviders() {
	for name := range c.machineProviderIDs() {
		pc, ok := c.Providers.Get(name)
		if !ok {
			continue
		}
		m := c.Machines[name]
		models := slices.Clone(pc.Models)
		for i := range models {
			if models[i].ContextWindow == 0 {
				models[i].ContextWindow = cmp.Or(m.ContextWindow, defaultMachineContextWindow)
			}
			if models[i].DefaultMaxTokens == 0 {
				models[i].DefaultMaxTokens = min(defaultMachineMaxTokens, models[i].ContextWindow/4)
			}
		}
		pc.Models = models
		c.Providers.Set(name, pc)
	}
}

// MachineModel returns the model agents on the named machine use: the
// configured one, or the first its provider offers. It returns "" when
// the machine has no provider (it was offline with no model listed at
// startup, or is disabled).
func (c *Config) MachineModel(name string) string {
	m, ok := c.Machines[name]
	if !ok || m.Disabled {
		return ""
	}
	pc, ok := c.Providers.Get(name)
	if !ok || len(pc.Models) == 0 {
		return ""
	}
	if m.Model != "" {
		for _, model := range pc.Models {
			if model.ID == m.Model {
				return model.ID
			}
		}
	}
	return pc.Models[0].ID
}
