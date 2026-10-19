package config

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/opencode"
)

// OpenCodeApplied records what an OpenCode import changed beyond MCP
// servers and provider settings.
type OpenCodeApplied struct {
	// ContextPaths and GlobalContextPaths are the rule files added to the
	// project and global context.
	ContextPaths       []string
	GlobalContextPaths []string
	// SkillsPaths are the skill directories added.
	SkillsPaths []string
	// DisabledTools are the built-in tools turned off by OpenCode's tools
	// toggles or deny rules.
	DisabledTools []string
	// DisabledMCP are the imported MCP servers turned off by a
	// "<server>_*": false toggle.
	DisabledMCP []string
	// DisabledMCPTools are single tools of imported MCP servers turned off,
	// as "<server>: <tool>".
	DisabledMCPTools []string
	// ProjectAllowedTools are the file-writing tools that no longer prompt
	// for files inside the project.
	ProjectAllowedTools []string
	// DisabledProviders are the providers turned off by OpenCode's
	// enabled_providers or disabled_providers.
	DisabledProviders []string
	// AutoSummarizeOff is set when OpenCode's compaction settings turned
	// off auto-summarize.
	AutoSummarizeOff bool
	// Commands are the names of the imported custom commands.
	Commands []string
	// Models describes how each OpenCode model preference was used.
	Models []OpenCodeModelChoice
	// Notes describe what was found but not applied, and why.
	Notes []string
}

// OpenCodeModelChoice describes one OpenCode model preference.
type OpenCodeModelChoice struct {
	Type SelectedModelType
	// Ref is the "provider/model" reference from OpenCode's config.
	Ref string
	// Source names the setting the reference came from.
	Source string
	// Result is "selected" or why the reference was not used.
	Result string
}

func (a OpenCodeApplied) clone() OpenCodeApplied {
	a.ContextPaths = slices.Clone(a.ContextPaths)
	a.GlobalContextPaths = slices.Clone(a.GlobalContextPaths)
	a.SkillsPaths = slices.Clone(a.SkillsPaths)
	a.DisabledTools = slices.Clone(a.DisabledTools)
	a.DisabledMCP = slices.Clone(a.DisabledMCP)
	a.DisabledMCPTools = slices.Clone(a.DisabledMCPTools)
	a.ProjectAllowedTools = slices.Clone(a.ProjectAllowedTools)
	a.DisabledProviders = slices.Clone(a.DisabledProviders)
	a.Commands = slices.Clone(a.Commands)
	a.Models = slices.Clone(a.Models)
	a.Notes = slices.Clone(a.Notes)
	return a
}

// openCodeToolNames maps OpenCode built-in tool names to the Ta-Da tools a
// tools toggle turns off.
var openCodeToolNames = map[string][]string{
	"bash":      {"bash"},
	"edit":      {"edit", "multiedit"},
	"write":     {"write"},
	"read":      {"view"},
	"glob":      {"glob"},
	"grep":      {"grep"},
	"list":      {"ls"},
	"webfetch":  {"fetch"},
	"todowrite": {"todos"},
	"task":      {"agent"},
	"question":  {"question"},
}

// openCodeDenyTools maps OpenCode permission keys to the Ta-Da tools a
// whole-tool deny rule turns off. OpenCode's edit permission covers every
// tool that changes files.
var openCodeDenyTools = map[string][]string{
	"bash":      {"bash"},
	"edit":      {"edit", "multiedit", "write", "lsp_rename", "lsp_replace_symbol"},
	"read":      {"view"},
	"glob":      {"glob"},
	"grep":      {"grep"},
	"list":      {"ls"},
	"webfetch":  {"fetch", "agentic_fetch", "download"},
	"todowrite": {"todos"},
	"task":      {"agent"},
	"question":  {"question"},
}

// openCodeNoPromptTools are the OpenCode tools whose Ta-Da counterparts
// never prompt inside the project, so an allow rule changes nothing and an
// ask rule can't be honored.
var openCodeNoPromptTools = map[string]bool{
	"read":      true,
	"glob":      true,
	"grep":      true,
	"list":      true,
	"lsp":       true,
	"skill":     true,
	"task":      true,
	"todowrite": true,
	"question":  true,
}

// openCodeEditTools are the tools OpenCode's "edit": "allow" lets run
// without a prompt. Ta-Da only allows them for files inside the project.
var openCodeEditTools = []string{"edit", "multiedit", "write", "lsp_replace_symbol"}

// applyOpenCodeContext applies the OpenCode settings that shape the agent
// rather than its connections: rule files, skills, compaction, tool
// toggles, permissions and custom commands. It runs after defaults are
// set so it adds to the default context paths instead of replacing them.
func (c *Config) applyOpenCodeContext(imp *openCodeImport, workingDir string) {
	c.OpenCodeCommands = nil
	if imp == nil || imp.config == nil {
		return
	}
	imp.applied = OpenCodeApplied{}
	if c.Options == nil {
		c.Options = &Options{}
	}
	s := imp.settings
	if s.instructions {
		c.applyOpenCodeInstructions(imp, workingDir)
	}
	if s.skills {
		c.applyOpenCodeSkills(imp, workingDir)
	}
	if s.compaction {
		c.applyOpenCodeCompaction(imp)
	}
	if s.tools {
		c.applyOpenCodeTools(imp)
	}
	if s.permissions {
		c.applyOpenCodePermissions(imp)
	}
	if s.commands {
		c.applyOpenCodeCommands(imp)
	}
	if s.mcp {
		c.checkOpenCodeMCPCommands(imp)
	}
	if len(imp.config.Plugins) > 0 {
		imp.notef("plugins are never run: %s", strings.Join(imp.config.Plugins, ", "))
	}
}

func (imp *openCodeImport) notef(format string, args ...any) {
	imp.applied.Notes = append(imp.applied.Notes, fmt.Sprintf(format, args...))
}

// pathKey normalizes a context or skills path so the same file is only
// added once however it is spelled.
func pathKey(p, workingDir string) string {
	p = home.Long(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(workingDir, p)
	}
	return strings.ToLower(filepath.Clean(p))
}

// addPaths returns paths with every new entry of add appended, and the
// entries that were new. It never modifies paths.
func addPaths(paths, add []string, seen map[string]bool, workingDir string) ([]string, []string) {
	var added []string
	for _, p := range add {
		key := pathKey(p, workingDir)
		if seen[key] {
			continue
		}
		seen[key] = true
		added = append(added, p)
	}
	if len(added) == 0 {
		return paths, nil
	}
	return slices.Concat(paths, added), added
}

func (c *Config) applyOpenCodeInstructions(imp *openCodeImport, workingDir string) {
	seen := map[string]bool{}
	for _, p := range slices.Concat(c.Options.ContextPaths, c.Options.GlobalContextPaths) {
		seen[pathKey(p, workingDir)] = true
	}
	var project, global []string
	for _, p := range imp.config.Instructions {
		if isWithinDir(workingDir, p) {
			project = append(project, p)
		} else {
			global = append(global, p)
		}
	}
	if imp.config.GlobalRules != "" {
		global = append(global, imp.config.GlobalRules)
	}
	c.Options.ContextPaths, imp.applied.ContextPaths = addPaths(c.Options.ContextPaths, project, seen, workingDir)
	c.Options.GlobalContextPaths, imp.applied.GlobalContextPaths = addPaths(c.Options.GlobalContextPaths, global, seen, workingDir)
}

func (c *Config) applyOpenCodeSkills(imp *openCodeImport, workingDir string) {
	seen := map[string]bool{}
	for _, p := range c.Options.SkillsPaths {
		seen[pathKey(p, workingDir)] = true
	}
	c.Options.SkillsPaths, imp.applied.SkillsPaths = addPaths(c.Options.SkillsPaths, imp.config.SkillPaths, seen, workingDir)
}

func (c *Config) applyOpenCodeCompaction(imp *openCodeImport) {
	auto := imp.config.CompactionAuto
	if auto == nil || *auto || c.Options.DisableAutoSummarize {
		return
	}
	// A plugin that turns compaction off usually manages the context
	// itself. Ta-Da doesn't run plugins, so keep summarizing.
	if len(imp.config.Plugins) > 0 {
		imp.notef("compaction.auto is false, but auto-summarize stays on because OpenCode plugins, which may manage the context instead, are not run")
		return
	}
	c.Options.DisableAutoSummarize = true
	imp.applied.AutoSummarizeOff = true
}

// openCodeToolReenabled reports whether a non-disabled OpenCode agent turns
// on a tool that pattern turns off globally. Such toggles are skipped:
// OpenCode users often switch tools off globally and back on for the
// agents that need them.
func (imp *openCodeImport) openCodeToolReenabled(pattern string) string {
	for _, name := range slices.Sorted(maps.Keys(imp.config.Agents)) {
		a := imp.config.Agents[name]
		if a.Disabled {
			continue
		}
		for p, on := range a.Tools {
			if on && (opencode.MatchTool(pattern, p) || opencode.MatchTool(p, pattern)) {
				return name
			}
		}
	}
	return ""
}

// importedMCPFor finds the imported MCP server whose OpenCode tool prefix
// starts pattern, preferring the longest prefix. It returns the server
// name and the rest of the pattern.
func (c *Config) importedMCPFor(imp *openCodeImport, pattern string) (string, string) {
	var server, rest string
	best := 0
	for name := range imp.importedMCP {
		prefix := opencode.MCPToolName(name, "")
		if strings.HasPrefix(pattern, prefix) && len(prefix) > best {
			server, rest, best = name, strings.TrimPrefix(pattern, prefix), len(prefix)
		}
	}
	return server, rest
}

func (c *Config) applyOpenCodeTools(imp *openCodeImport) {
	disabled := slices.Clone(c.Options.DisabledTools)
	for _, pattern := range slices.Sorted(maps.Keys(imp.config.Tools)) {
		if imp.config.Tools[pattern] {
			continue
		}
		if agent := imp.openCodeToolReenabled(pattern); agent != "" {
			imp.notef("tools.%s: false skipped: agent %s turns it back on", pattern, agent)
			continue
		}
		if server, rest := c.importedMCPFor(imp, pattern); server != "" {
			m := c.MCP[server]
			switch {
			case rest == "*":
				if !m.Disabled {
					m.Disabled = true
					imp.applied.DisabledMCP = append(imp.applied.DisabledMCP, server)
				}
			case rest != "" && !strings.ContainsAny(rest, "*?["):
				if !slices.Contains(m.DisabledTools, rest) {
					m.DisabledTools = append(slices.Clone(m.DisabledTools), rest)
					imp.applied.DisabledMCPTools = append(imp.applied.DisabledMCPTools, server+": "+rest)
				}
			default:
				imp.notef("tools.%s: false skipped: only <server>_* and single MCP tools are supported", pattern)
				continue
			}
			c.MCP[server] = m
			continue
		}
		tools, ok := openCodeToolNames[pattern]
		if !ok {
			imp.notef("tools.%s: false skipped: no matching Ta-Da tool or imported MCP server", pattern)
			continue
		}
		for _, t := range tools {
			if !slices.Contains(disabled, t) {
				disabled = append(disabled, t)
				imp.applied.DisabledTools = append(imp.applied.DisabledTools, t)
			}
		}
	}
	c.Options.DisabledTools = disabled
}

// openCodeAction is a permission's action for every input: its plain
// action, or the action of a lone "*" pattern. It is empty when the rule
// depends on the input.
func openCodeAction(p opencode.Permission) string {
	if p.Action != "" {
		return p.Action
	}
	if len(p.Patterns) == 1 {
		return p.Patterns["*"]
	}
	return ""
}

// applyOpenCodePermissions imports the OpenCode permission rules that map
// safely: "edit": "allow" lets file edits inside the project run without a
// prompt, and a whole-tool "deny" turns the matching tools off. Nothing
// else is loosened.
func (c *Config) applyOpenCodePermissions(imp *openCodeImport) {
	var matched, skipped, unprompted []string
	askAll := false
	disabled := slices.Clone(c.Options.DisabledTools)
	for _, key := range slices.Sorted(maps.Keys(imp.config.Permission)) {
		action := openCodeAction(imp.config.Permission[key])
		switch {
		case key == "edit" && action == "allow":
			perms := Permissions{}
			if c.Permissions != nil {
				perms = *c.Permissions
			}
			allowed := slices.Clone(perms.ProjectAllowedTools)
			for _, t := range openCodeEditTools {
				if !slices.Contains(allowed, t) && !slices.Contains(perms.AllowedTools, t) {
					allowed = append(allowed, t)
					imp.applied.ProjectAllowedTools = append(imp.applied.ProjectAllowedTools, t)
				}
			}
			perms.ProjectAllowedTools = allowed
			c.Permissions = &perms
		case action == "deny" && openCodeDenyTools[key] != nil:
			for _, t := range openCodeDenyTools[key] {
				if !slices.Contains(disabled, t) {
					disabled = append(disabled, t)
					imp.applied.DisabledTools = append(imp.applied.DisabledTools, t)
				}
			}
		case key == "*" && action == "ask":
			askAll = true
		case action == "ask" && openCodeNoPromptTools[key]:
			unprompted = append(unprompted, key)
		case action == "ask" || action == "allow" && openCodeNoPromptTools[key]:
			matched = append(matched, key)
		default:
			skipped = append(skipped, key)
		}
	}
	c.Options.DisabledTools = disabled
	if askAll {
		imp.notef("permission rule *: ask partly followed: Ta-Da prompts before writes, commands, web access and MCP tools, not before reads inside the project")
	}
	if len(matched) > 0 {
		imp.notef("permission rules Ta-Da already follows: %s", strings.Join(matched, ", "))
	}
	if len(skipped) > 0 {
		imp.notef("permission rules not imported (Ta-Da keeps prompting): %s", strings.Join(skipped, ", "))
	}
	if len(unprompted) > 0 {
		imp.notef("permission rules not imported (Ta-Da doesn't prompt for these inside the project): %s", strings.Join(unprompted, ", "))
	}
}

func (c *Config) applyOpenCodeCommands(imp *openCodeImport) {
	var withOptions []string
	for _, name := range slices.Sorted(maps.Keys(imp.config.Commands)) {
		cmd := imp.config.Commands[name]
		if strings.TrimSpace(cmd.Template) == "" {
			continue
		}
		c.OpenCodeCommands = append(c.OpenCodeCommands, OpenCodeCommand{
			Name:        name,
			Description: cmd.Description,
			Template:    cmd.Template,
		})
		imp.applied.Commands = append(imp.applied.Commands, name)
		if cmd.Agent != "" || cmd.Model != "" || cmd.Subtask {
			withOptions = append(withOptions, name)
		}
	}
	if len(withOptions) > 0 {
		imp.notef("commands run in the current session; their agent, model and subtask settings are ignored: %s", strings.Join(withOptions, ", "))
	}
}

// checkOpenCodeMCPCommands notes imported stdio MCP servers whose command
// can't be found, so a missing tool shows up in the report rather than as
// a startup error.
func (c *Config) checkOpenCodeMCPCommands(imp *openCodeImport) {
	for _, name := range slices.Sorted(maps.Keys(imp.importedMCP)) {
		m, ok := c.MCP[name]
		if !ok || m.Disabled || m.Type != MCPStdio || m.Command == "" {
			continue
		}
		if _, err := exec.LookPath(home.Long(m.Command)); err != nil {
			imp.notef("MCP server %s: command %q not found", name, m.Command)
		}
	}
}

// applyOpenCodeProviderFilter turns off the providers OpenCode's
// enabled_providers or disabled_providers exclude. Providers defined in
// Ta-Da's own config and providers of selected models are left alone.
func (c *Config) applyOpenCodeProviderFilter(imp *openCodeImport, known []catwalk.Provider) {
	if imp == nil {
		return
	}
	imp.applied.DisabledProviders = nil
	if imp.config == nil || !imp.settings.providers {
		return
	}
	oc := imp.config
	if oc.EnabledProviders == nil && len(oc.DisabledProviders) == 0 {
		return
	}
	toSet := func(ids []string) map[string]bool {
		set := map[string]bool{}
		for _, id := range ids {
			set[opencode.CatwalkID(id)] = true
		}
		return set
	}
	var enabled map[string]bool
	if oc.EnabledProviders != nil {
		enabled = toSet(oc.EnabledProviders)
	}
	excluded := toSet(oc.DisabledProviders)
	inUse := map[string]bool{}
	for _, m := range c.Models {
		inUse[m.Provider] = true
	}
	// Only providers that would otherwise be configured matter: the ones
	// with config entries and the known ones whose API key is set.
	candidates := map[string]bool{}
	for id := range c.Providers.Seq2() {
		candidates[id] = true
	}
	for _, p := range known {
		for _, ref := range envRefRe.FindAllStringSubmatch(p.APIKey, -1) {
			if os.Getenv(ref[1]) != "" {
				candidates[string(p.ID)] = true
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(candidates)) {
		if imp.ownProviders[id] || inUse[id] {
			continue
		}
		if !excluded[id] && (enabled == nil || enabled[id]) {
			continue
		}
		pc, _ := c.Providers.Get(id)
		if pc.Disable {
			continue
		}
		pc.ID = cmp.Or(pc.ID, id)
		pc.Disable = true
		c.Providers.Set(id, pc)
		imp.applied.DisabledProviders = append(imp.applied.DisabledProviders, id)
	}
}
