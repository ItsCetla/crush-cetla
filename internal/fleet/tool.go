package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
)

// ToolName is the name of the agent's machines tool.
const ToolName = "machines"

// ToolDescription tells the model what the machines tool does.
const ToolDescription = `Manage the user's machines: other computers on their network that run local models and can take agent work. Actions:
- list (default): show the configured machines.
- check: probe every machine and show which are online and how many agent slots are free.
- add: save a new machine. Needs a name and the URL of its OpenAI-compatible model server (for Ollama that is http://<host>:11434/v1). Set slots to how many agents it can run at once, hardware to what it is (e.g. "M1 Max", "RTX 3060 Ti"), and model if the user named one.
- update: change fields of an existing machine by name. Only the fields you pass change.
- remove: delete a machine by name.
When the user describes their setup ("an M1 over here with 4 agents, a 3060 Ti with 2"), save each machine with add. Ask for a URL or IP if they didn't give one; don't guess. Machine names are short: letters, digits, - and _.`

// Params are the machines tool's arguments.
type Params struct {
	Action        string   `json:"action,omitempty" description:"list (default), check, add, update or remove" jsonschema:"list (default), check, add, update or remove"`
	Name          string   `json:"name,omitempty" description:"The machine name, e.g. m1 or pc (add, update, remove)" jsonschema:"The machine name, e.g. m1 or pc (add, update, remove)"`
	URL           string   `json:"url,omitempty" description:"OpenAI-compatible base URL, e.g. http://192.168.1.20:11434/v1 (add, update)" jsonschema:"OpenAI-compatible base URL, e.g. http://192.168.1.20:11434/v1 (add, update)"`
	Hardware      string   `json:"hardware,omitempty" description:"What the machine is, e.g. M1 Max or RTX 3060 Ti" jsonschema:"What the machine is, e.g. M1 Max or RTX 3060 Ti"`
	Type          string   `json:"type,omitempty" description:"Server type: openai-compat (default), ollama, lmstudio, llamacpp, omlx or litellm" jsonschema:"Server type: openai-compat (default), ollama, lmstudio, llamacpp, omlx or litellm"`
	APIKey        string   `json:"api_key,omitempty" description:"API key, only if the server needs one" jsonschema:"API key, only if the server needs one"`
	Model         string   `json:"model,omitempty" description:"Model agents on the machine use; empty uses the first one the server lists" jsonschema:"Model agents on the machine use; empty uses the first one the server lists"`
	Slots         int      `json:"slots,omitempty" description:"How many agents the machine can run at once" jsonschema:"How many agents the machine can run at once"`
	ContextWindow int64    `json:"context_window,omitempty" description:"Context window to assume if the server doesn't report one" jsonschema:"Context window to assume if the server doesn't report one"`
	Tags          []string `json:"tags,omitempty" description:"What the machine is good at, e.g. fast or code" jsonschema:"What the machine is good at, e.g. fast or code"`
	Notes         string   `json:"notes,omitempty" description:"Anything else worth knowing about the machine" jsonschema:"Anything else worth knowing about the machine"`
	Disabled      *bool    `json:"disabled,omitempty" description:"Keep the machine saved but don't use it (add, update)" jsonschema:"Keep the machine saved but don't use it (add, update)"`
}

// Changes reports whether the call edits the config.
func (p Params) Changes() bool {
	switch normAction(p.Action) {
	case "add", "update", "remove", "rm", "delete":
		return true
	}
	return false
}

// Store is the part of the config store the tool needs.
type Store interface {
	Config() *config.Config
	KnownProviders() []catwalk.Provider
	HasConfigField(scope config.Scope, key string) bool
	SetConfigField(scope config.Scope, key string, value any) error
	RemoveConfigField(scope config.Scope, key string) error
}

func normAction(a string) string {
	a = strings.ToLower(strings.TrimSpace(a))
	if a == "" {
		return "list"
	}
	return a
}

// RunTool carries out a machines tool call and returns the text the model
// sees. Changes are saved to the global config.
func RunTool(ctx context.Context, store Store, pool *Pool, p Params) (string, error) {
	if store == nil {
		return "", errors.New("config isn't available")
	}
	if pool == nil {
		pool = Default()
	}
	switch normAction(p.Action) {
	case "list":
		return list(store.Config()), nil
	case "check":
		ForgetProbes()
		return check(ctx, store.Config(), pool), nil
	case "add":
		return add(ctx, store, p)
	case "update":
		return update(ctx, store, p)
	case "remove", "rm", "delete":
		return remove(store, p)
	default:
		return "", fmt.Errorf("unknown action %q: use list, check, add, update or remove", p.Action)
	}
}

func list(cfg *config.Config) string {
	machines := Machines(cfg)
	if len(machines) == 0 {
		return "No machines configured. Add one with action add."
	}
	lines := make([]string, len(machines))
	for i, m := range machines {
		lines[i] = Describe(m) + ", " + m.URL
	}
	return strings.Join(lines, "\n")
}

func check(ctx context.Context, cfg *config.Config, pool *Pool) string {
	statuses := pool.Check(ctx, cfg)
	if len(statuses) == 0 {
		return "No machines configured. Add one with action add."
	}
	lines := make([]string, len(statuses))
	for i, s := range statuses {
		lines[i] = DescribeStatus(s)
	}
	return strings.Join(lines, "\n")
}

func add(ctx context.Context, store Store, p Params) (string, error) {
	name := strings.TrimSpace(p.Name)
	if err := ValidName(store, name); err != nil {
		return "", err
	}
	if m, ok := Lookup(store.Config(), name); ok {
		return "", fmt.Errorf("machine %q already exists; use update to change it", m.Name)
	}
	if strings.TrimSpace(p.URL) == "" {
		return "", errors.New("add needs the url of the machine's model server")
	}
	m := config.MachineConfig{}
	if err := apply(&m, p); err != nil {
		return "", err
	}
	if err := store.SetConfigField(config.ScopeGlobal, "machines."+name, m); err != nil {
		return "", fmt.Errorf("saving machine: %w", err)
	}
	return "Saved " + name + ". " + report(ctx, store.Config(), name), nil
}

func update(ctx context.Context, store Store, p Params) (string, error) {
	m, ok := Lookup(store.Config(), strings.TrimSpace(p.Name))
	if !ok {
		return "", fmt.Errorf("no machine named %q; use add to save it", p.Name)
	}
	mc := m.MachineConfig
	if err := apply(&mc, p); err != nil {
		return "", err
	}
	if err := store.SetConfigField(config.ScopeGlobal, "machines."+m.Name, mc); err != nil {
		return "", fmt.Errorf("saving machine: %w", err)
	}
	return "Updated " + m.Name + ". " + report(ctx, store.Config(), m.Name), nil
}

func remove(store Store, p Params) (string, error) {
	m, ok := Lookup(store.Config(), strings.TrimSpace(p.Name))
	if !ok {
		return "", fmt.Errorf("no machine named %q", p.Name)
	}
	key := "machines." + m.Name
	for _, scope := range []config.Scope{config.ScopeWorkspace, config.ScopeGlobal} {
		if store.HasConfigField(scope, key) {
			if err := store.RemoveConfigField(scope, key); err != nil {
				return "", fmt.Errorf("removing machine: %w", err)
			}
		}
	}
	if _, still := Lookup(store.Config(), m.Name); !still {
		return "Removed " + m.Name + ".", nil
	}
	// Another config file, such as a tadarc, still defines it. Disable it
	// so it stops taking work.
	if err := store.SetConfigField(config.ScopeGlobal, key+".disabled", true); err != nil {
		return "", fmt.Errorf("disabling machine: %w", err)
	}
	return fmt.Sprintf("Disabled %s. It is still defined in another config file (likely a tadarc); delete it there to remove it for good.", m.Name), nil
}

// ValidName checks a new machine name, including that it doesn't shadow a
// built-in provider (the machine's provider is named after it).
func ValidName(store Store, name string) error {
	if err := config.ValidMachineName(name); err != nil {
		return err
	}
	for _, kp := range store.KnownProviders() {
		if strings.EqualFold(string(kp.ID), name) {
			return fmt.Errorf("%q is the name of a built-in provider; pick another name", name)
		}
	}
	return nil
}

// apply copies the fields set in p onto m.
func apply(m *config.MachineConfig, p Params) error {
	if v := strings.TrimSpace(p.URL); v != "" {
		if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return fmt.Errorf("url %q must start with http:// or https://", v)
		}
		m.URL = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(p.Type); v != "" {
		m.Type = catwalk.Type(strings.ToLower(v))
	}
	if p.Slots < 0 {
		return errors.New("slots can't be negative")
	}
	if p.Slots > 0 {
		m.Slots = p.Slots
	}
	if p.ContextWindow < 0 {
		return errors.New("context_window can't be negative")
	}
	if p.ContextWindow > 0 {
		m.ContextWindow = p.ContextWindow
	}
	if v := strings.TrimSpace(p.Hardware); v != "" {
		m.Hardware = v
	}
	if v := strings.TrimSpace(p.APIKey); v != "" {
		m.APIKey = v
	}
	if v := strings.TrimSpace(p.Model); v != "" {
		m.Model = v
	}
	if len(p.Tags) > 0 {
		m.Tags = p.Tags
	}
	if v := strings.TrimSpace(p.Notes); v != "" {
		m.Notes = v
	}
	if p.Disabled != nil {
		m.Disabled = *p.Disabled
	}
	return nil
}

// report describes a machine just saved, with a fresh probe.
func report(ctx context.Context, cfg *config.Config, name string) string {
	m, ok := Lookup(cfg, name)
	if !ok {
		return ""
	}
	if m.Disabled {
		return Describe(m) + "."
	}
	ForgetProbes()
	if err := Probe(ctx, m); err != nil {
		return fmt.Sprintf("%s. It isn't reachable right now (%v); agents will use it once it is.", Describe(m), err)
	}
	if m.Model == "" {
		return Describe(m) + ". It is online but lists no models; set one with update."
	}
	return Describe(m) + ". It is online and ready for agents."
}

// DescribeChange is the permission prompt text for a call that edits
// the config.
func DescribeChange(p Params) string {
	action := normAction(p.Action)
	switch action {
	case "remove", "rm", "delete":
		return fmt.Sprintf("Remove machine %q", p.Name)
	}
	var parts []string
	if p.URL != "" {
		parts = append(parts, "url "+p.URL)
	}
	if p.Hardware != "" {
		parts = append(parts, p.Hardware)
	}
	if p.Model != "" {
		parts = append(parts, "model "+p.Model)
	}
	if p.Slots > 0 {
		parts = append(parts, fmt.Sprintf("%d slots", p.Slots))
	}
	verb := "Add"
	if action == "update" {
		verb = "Update"
	}
	desc := fmt.Sprintf("%s machine %q", verb, p.Name)
	if len(parts) > 0 {
		desc += ": " + strings.Join(parts, ", ")
	}
	if p.URL != "" {
		desc += ". Agents will send code to this URL."
	}
	return desc
}

// Redacted returns p with the API key hidden, for permission prompts.
func (p Params) Redacted() Params {
	if p.APIKey != "" {
		p.APIKey = "(set)"
	}
	return p
}
