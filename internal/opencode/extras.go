package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// Command is a custom slash command from a "command" config entry or a
// markdown file in a command directory.
type Command struct {
	Name string
	// Template is the prompt, with OpenCode's placeholders ($ARGUMENTS,
	// $1, ...) left as written.
	Template    string
	Description string
	// Agent, Model and Subtask route the command inside OpenCode.
	Agent   string
	Model   string
	Subtask bool
	// SourcePath is the document or markdown file that defined it.
	SourcePath string
}

// Agent is an agent definition merged field by field across "agent" and
// legacy "mode" entries and markdown agent files.
type Agent struct {
	Name        string
	Description string
	// Mode is "primary", "subagent" or "all"; empty means unset.
	Mode string
	// Model is a "provider/model" reference.
	Model string
	// Tools enables or disables tools for this agent, by tool name or
	// wildcard pattern.
	Tools    map[string]bool
	Disabled bool
	// SourcePath is the last document or markdown file that changed it.
	SourcePath string
}

// Permission is one entry of the "permission" config: a single action for
// the whole tool, or actions by pattern (such as "git *" for bash).
type Permission struct {
	// Action is "allow", "ask" or "deny" when the entry is a plain string.
	Action string
	// Patterns maps patterns to actions when the entry is an object.
	Patterns map[string]string
}

// extraState collects the raw values that are only resolved once every
// document is merged.
type extraState struct {
	instructions []string
	skillPaths   []string
}

func validAction(s string) bool {
	return s == "allow" || s == "ask" || s == "deny"
}

// applyExtras merges the keys beyond MCP servers and providers: rules,
// skills, plugins, tools, permissions, commands, agents and compaction.
func (m *merger) applyExtras(doc document, source string) {
	if raw, ok := doc["instructions"]; ok {
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			m.warnf("%s: instructions: %v", source, err)
		} else {
			m.extra.instructions = appendUnique(m.extra.instructions, list...)
		}
	}
	if raw, ok := doc["plugin"]; ok {
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			m.warnf("%s: plugin: %v", source, err)
		} else {
			m.cfg.Plugins = appendUnique(m.cfg.Plugins, list...)
		}
	}
	if raw, ok := doc["skills"]; ok {
		var skills struct {
			Paths []string `json:"paths"`
			URLs  []string `json:"urls"`
		}
		if err := json.Unmarshal(raw, &skills); err != nil {
			m.warnf("%s: skills: %v", source, err)
		} else {
			m.extra.skillPaths = appendUnique(m.extra.skillPaths, skills.Paths...)
			for _, u := range skills.URLs {
				m.warnf("%s: skills.urls: skipped %s (remote skills are never fetched)", source, u)
			}
		}
	}
	if raw, ok := doc["tools"]; ok {
		if tools, ok := m.toolsMap(raw, source, "tools"); ok {
			for k, v := range tools {
				m.cfg.Tools[k] = v
			}
		}
	}
	if raw, ok := doc["permission"]; ok {
		m.applyPermission(raw, source)
	}
	if raw, ok := doc["default_agent"]; ok {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			m.warnf("%s: default_agent: %v", source, err)
		} else {
			m.cfg.DefaultAgent = name
		}
	}
	if raw, ok := doc["compaction"]; ok {
		var compaction struct {
			Auto *bool `json:"auto"`
		}
		if err := json.Unmarshal(raw, &compaction); err != nil {
			m.warnf("%s: compaction: %v", source, err)
		} else if compaction.Auto != nil {
			auto := *compaction.Auto
			m.cfg.CompactionAuto = &auto
		}
	}
	if raw, ok := doc["command"]; ok {
		m.applyCommands(raw, source)
	}
	for _, key := range []string{"mode", "agent"} {
		if raw, ok := doc[key]; ok {
			m.applyAgents(raw, source, key)
		}
	}
}

// toolsMap decodes a tools map, skipping entries that are not booleans.
func (m *merger) toolsMap(raw json.RawMessage, source, key string) (map[string]bool, bool) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		m.warnf("%s: %s: %v", source, key, err)
		return nil, false
	}
	tools := make(map[string]bool, len(entries))
	for name, v := range entries {
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			m.warnf("%s: %s.%s: not a boolean", source, key, name)
			continue
		}
		tools[name] = b
	}
	return tools, true
}

// applyPermission merges a "permission" value. A plain string sets every
// tool; an object merges per tool, with a string entry replacing the tool's
// rules and an object entry merging into its patterns.
func (m *merger) applyPermission(raw json.RawMessage, source string) {
	var action string
	if err := json.Unmarshal(raw, &action); err == nil {
		if !validAction(action) {
			m.warnf("%s: permission: invalid action %q", source, action)
			return
		}
		m.cfg.Permission = map[string]Permission{"*": {Action: action}}
		return
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		m.warnf("%s: permission: %v", source, err)
		return
	}
	for _, tool := range sortedKeys(entries) {
		v := entries[tool]
		if err := json.Unmarshal(v, &action); err == nil {
			if !validAction(action) {
				m.warnf("%s: permission.%s: invalid action %q", source, tool, action)
				continue
			}
			m.cfg.Permission[tool] = Permission{Action: action}
			continue
		}
		var patterns map[string]string
		if err := json.Unmarshal(v, &patterns); err != nil {
			m.warnf("%s: permission.%s: %v", source, tool, err)
			continue
		}
		p := m.cfg.Permission[tool]
		if p.Patterns == nil {
			p = Permission{Patterns: map[string]string{}}
		}
		for pattern, act := range patterns {
			if !validAction(act) {
				m.warnf("%s: permission.%s.%s: invalid action %q", source, tool, pattern, act)
				continue
			}
			p.Patterns[pattern] = act
		}
		m.cfg.Permission[tool] = p
	}
}

func (m *merger) applyCommands(raw json.RawMessage, source string) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		m.warnf("%s: command: %v", source, err)
		return
	}
	for name, v := range entries {
		var c struct {
			Template    *string         `json:"template"`
			Description *string         `json:"description"`
			Agent       *string         `json:"agent"`
			Model       json.RawMessage `json:"model"`
			Subtask     *bool           `json:"subtask"`
		}
		if err := json.Unmarshal(v, &c); err != nil {
			m.warnf("%s: command.%s: %v", source, name, err)
			continue
		}
		cmd := m.cfg.Commands[name]
		cmd.Name = name
		cmd.SourcePath = source
		if c.Template != nil {
			cmd.Template = *c.Template
		}
		if c.Description != nil {
			cmd.Description = *c.Description
		}
		if c.Agent != nil {
			cmd.Agent = *c.Agent
		}
		if len(c.Model) > 0 {
			if ref, ok := parseModelRef(c.Model); ok {
				cmd.Model = ref
			}
		}
		if c.Subtask != nil {
			cmd.Subtask = *c.Subtask
		}
		m.cfg.Commands[name] = cmd
	}
}

// agentPatch holds the agent fields one source sets.
type agentPatch struct {
	description *string
	mode        *string
	model       string
	tools       map[string]bool
	disable     *bool
}

func (m *merger) mergeAgent(name string, p agentPatch, source string) {
	a := m.cfg.Agents[name]
	a.Name = name
	a.SourcePath = source
	if p.description != nil {
		a.Description = *p.description
	}
	if p.mode != nil {
		a.Mode = *p.mode
	}
	if p.model != "" {
		a.Model = p.model
	}
	if len(p.tools) > 0 {
		if a.Tools == nil {
			a.Tools = map[string]bool{}
		}
		for k, v := range p.tools {
			a.Tools[k] = v
		}
	}
	if p.disable != nil {
		a.Disabled = *p.disable
	}
	m.cfg.Agents[name] = a
}

// applyAgents merges "agent" entries, or legacy "mode" entries, which are
// primary agents.
func (m *merger) applyAgents(raw json.RawMessage, source, key string) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		m.warnf("%s: %s: %v", source, key, err)
		return
	}
	for _, name := range sortedKeys(entries) {
		var a struct {
			Description *string         `json:"description"`
			Mode        *string         `json:"mode"`
			Model       json.RawMessage `json:"model"`
			Tools       json.RawMessage `json:"tools"`
			Disable     *bool           `json:"disable"`
		}
		if err := json.Unmarshal(entries[name], &a); err != nil {
			m.warnf("%s: %s.%s: %v", source, key, name, err)
			continue
		}
		p := agentPatch{description: a.Description, mode: a.Mode, disable: a.Disable}
		if key == "mode" {
			primary := "primary"
			p.mode = &primary
		}
		if len(a.Model) > 0 {
			ref, ok := parseModelRef(a.Model)
			if !ok {
				m.warnf("%s: %s.%s.model: invalid model reference", source, key, name)
			}
			p.model = ref
		}
		if len(a.Tools) > 0 {
			p.tools, _ = m.toolsMap(a.Tools, source, key+"."+name+".tools")
		}
		m.mergeAgent(name, p, source)
	}
}

// scanConfigDirs reads skills, commands, agents and plugins from the
// OpenCode config directories, in order, so a later directory wins.
func (l *loader) scanConfigDirs(dirs []string) {
	seen := map[string]bool{}
	for _, dir := range dirs {
		real := resolvePath(dir)
		if seen[real] {
			continue
		}
		seen[real] = true
		if info, err := os.Stat(real); err != nil || !info.IsDir() {
			continue
		}
		l.m.cfg.ConfigDirs = append(l.m.cfg.ConfigDirs, dir)
		for _, name := range []string{"skill", "skills"} {
			if isDir(filepath.Join(dir, name)) {
				l.m.cfg.SkillPaths = appendUnique(l.m.cfg.SkillPaths, filepath.Join(dir, name))
			}
		}
		for _, name := range []string{"command", "commands"} {
			l.scanMarkdown(filepath.Join(dir, name), "**/*.md", l.readCommand)
		}
		for _, name := range []string{"agent", "agents"} {
			l.scanMarkdown(filepath.Join(dir, name), "**/*.md", func(name, file, text string) {
				l.readAgent(name, file, text, "")
			})
		}
		for _, name := range []string{"mode", "modes"} {
			l.scanMarkdown(filepath.Join(dir, name), "*.md", func(name, file, text string) {
				l.readAgent(name, file, text, "primary")
			})
		}
		for _, name := range []string{"plugin", "plugins"} {
			for _, file := range globIn(filepath.Join(dir, name), "*.{ts,js}") {
				l.m.cfg.Plugins = appendUnique(l.m.cfg.Plugins, file)
			}
		}
	}
}

// scanMarkdown calls read for every file under dir matching pattern, with
// the file's name: its path relative to dir without ".md".
func (l *loader) scanMarkdown(dir, pattern string, read func(name, file, text string)) {
	for _, file := range globIn(dir, pattern) {
		data, err := os.ReadFile(file)
		if err != nil {
			l.warnf("%s: %v", file, err)
			continue
		}
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			continue
		}
		l.m.cfg.ExtraFiles = append(l.m.cfg.ExtraFiles, file)
		read(strings.TrimSuffix(filepath.ToSlash(rel), ".md"), file, string(data))
	}
}

func (l *loader) readCommand(name, file, text string) {
	front, body, err := splitFrontmatter(text)
	if err != nil {
		l.warnf("%s: %v", file, err)
		return
	}
	var meta struct {
		Description *string `yaml:"description"`
		Agent       *string `yaml:"agent"`
		Model       string  `yaml:"model"`
		Subtask     *bool   `yaml:"subtask"`
	}
	if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
		l.warnf("%s: frontmatter: %v", file, err)
		return
	}
	// Like OpenCode, the file merges over a same-named config entry.
	cmd := l.m.cfg.Commands[name]
	cmd.Name = name
	cmd.Template = strings.TrimSpace(body)
	cmd.SourcePath = file
	if meta.Description != nil {
		cmd.Description = *meta.Description
	}
	if meta.Agent != nil {
		cmd.Agent = *meta.Agent
	}
	if meta.Subtask != nil {
		cmd.Subtask = *meta.Subtask
	}
	if meta.Model != "" {
		ref, ok := modelRef(meta.Model)
		if !ok {
			l.warnf("%s: model: invalid model reference", file)
		}
		cmd.Model = ref
	}
	l.m.cfg.Commands[name] = cmd
}

func (l *loader) readAgent(name, file, text, mode string) {
	front, _, err := splitFrontmatter(text)
	if err != nil {
		l.warnf("%s: %v", file, err)
		return
	}
	var meta struct {
		Description *string        `yaml:"description"`
		Mode        *string        `yaml:"mode"`
		Model       string         `yaml:"model"`
		Tools       map[string]any `yaml:"tools"`
		Disable     *bool          `yaml:"disable"`
	}
	if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
		l.warnf("%s: frontmatter: %v", file, err)
		return
	}
	p := agentPatch{description: meta.Description, mode: meta.Mode, disable: meta.Disable}
	if mode != "" {
		p.mode = &mode
	}
	if meta.Model != "" {
		ref, ok := modelRef(meta.Model)
		if !ok {
			l.warnf("%s: model: invalid model reference", file)
		}
		p.model = ref
	}
	for tool, v := range meta.Tools {
		b, ok := v.(bool)
		if !ok {
			l.warnf("%s: tools.%s: not a boolean", file, tool)
			continue
		}
		if p.tools == nil {
			p.tools = map[string]bool{}
		}
		p.tools[tool] = b
	}
	l.m.mergeAgent(name, p, file)
}

// resolveExtras turns the collected instruction and skill path entries
// into existing files and directories, and finds the global rules file.
//
// Relative instructions match in every directory from cwd up to the
// project boundary; with project config disabled they match in the global
// config directory instead. URLs are skipped: Crush never fetches them.
func (l *loader) resolveExtras(opts LoadOptions, cwd string, projectDisabled bool) {
	cfg := l.m.cfg
	getenv := envOrEmpty(opts.Getenv)
	for _, inst := range l.m.extra.instructions {
		if strings.HasPrefix(inst, "https://") || strings.HasPrefix(inst, "http://") {
			l.warnf("instructions: skipped %s (remote instructions are never fetched)", inst)
			continue
		}
		inst = expandHome(inst, opts.Home)
		var matches []string
		switch {
		case filepath.IsAbs(inst):
			matches = globFiles(inst)
		case projectDisabled:
			if opts.ConfigDir != "" {
				matches = globFiles(filepath.Join(opts.ConfigDir, inst))
			}
		case cwd != "":
			dirs := projectDirs(opts.ProjectBoundary, cwd)
			for i := len(dirs) - 1; i >= 0; i-- {
				matches = append(matches, globFiles(filepath.Join(dirs[i], inst))...)
			}
		}
		cfg.Instructions = appendUnique(cfg.Instructions, matches...)
	}

	for _, p := range l.m.extra.skillPaths {
		p = expandHome(p, opts.Home)
		if !filepath.IsAbs(p) {
			if cwd == "" {
				continue
			}
			p = filepath.Join(cwd, p)
		}
		if isDir(p) {
			cfg.SkillPaths = appendUnique(cfg.SkillPaths, filepath.Clean(p))
		}
	}

	var rules []string
	if opts.ConfigDir != "" {
		rules = append(rules, filepath.Join(opts.ConfigDir, "AGENTS.md"))
	}
	if opts.Home != "" && !truthy(getenv("OPENCODE_DISABLE_CLAUDE_CODE")) &&
		!truthy(getenv("OPENCODE_DISABLE_CLAUDE_CODE_PROMPT")) {
		rules = append(rules, filepath.Join(opts.Home, ".claude", "CLAUDE.md"))
	}
	for _, file := range rules {
		if isFile(file) {
			cfg.GlobalRules = file
			break
		}
	}
}

// globFiles returns the regular files matching an absolute pattern, in
// lexical order. Only the part from the first wildcard on is treated as a
// pattern, so special characters in the leading directories are literal.
func globFiles(pattern string) []string {
	pattern = filepath.Clean(pattern)
	parts := strings.Split(filepath.ToSlash(pattern), "/")
	i := slices.IndexFunc(parts, hasMeta)
	if i == -1 {
		if isFile(pattern) {
			return []string{pattern}
		}
		return nil
	}
	base := filepath.FromSlash(strings.Join(parts[:i], "/"))
	if base == "" || strings.HasSuffix(base, ":") {
		base += string(filepath.Separator)
	}
	return globIn(base, strings.Join(parts[i:], "/"))
}

// globIn returns the regular files under dir matching a slash-separated
// pattern, following symlinks.
func globIn(dir, pattern string) []string {
	if !isDir(dir) {
		return nil
	}
	matches, err := doublestar.Glob(os.DirFS(dir), pattern, doublestar.WithFilesOnly())
	if err != nil {
		return nil
	}
	files := make([]string, 0, len(matches))
	for _, match := range matches {
		files = append(files, filepath.Join(dir, filepath.FromSlash(match)))
	}
	slices.Sort(files)
	return files
}

func hasMeta(s string) bool {
	return strings.ContainsAny(s, "*?[{")
}

func expandHome(p, home string) string {
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/")) {
		return filepath.Join(home, p[1:])
	}
	return p
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func appendUnique(list []string, items ...string) []string {
	for _, item := range items {
		if !slices.Contains(list, item) {
			list = append(list, item)
		}
	}
	return list
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// splitFrontmatter separates YAML frontmatter from a markdown body. A file
// that does not start with "---" is all body.
func splitFrontmatter(content string) (front, body string, err error) {
	content = strings.TrimPrefix(content, "\uFEFF")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	start := slices.IndexFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) != ""
	})
	if start == -1 || strings.TrimSpace(lines[start]) != "---" {
		return "", content, nil
	}
	end := slices.IndexFunc(lines[start+1:], func(line string) bool {
		return strings.TrimSpace(line) == "---"
	})
	if end == -1 {
		return "", "", errors.New("unclosed frontmatter")
	}
	end += start + 1
	return strings.Join(lines[start+1:end], "\n"), strings.Join(lines[end+1:], "\n"), nil
}

// MatchTool reports whether an OpenCode tools key matches a tool name. Keys
// may use "*" and "?" wildcards.
func MatchTool(pattern, name string) bool {
	if pattern == name {
		return true
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// unsafeToolChars are the characters OpenCode replaces with "_" when it
// names MCP tools.
var unsafeToolChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// MCPToolName is the name OpenCode gives a tool of an MCP server, which is
// what tools and permission keys refer to: "<server>_<tool>" with unsafe
// characters replaced. An empty tool gives the server's prefix.
func MCPToolName(server, tool string) string {
	return unsafeToolChars.ReplaceAllString(server, "_") + "_" +
		unsafeToolChars.ReplaceAllString(tool, "_")
}

// modelRef parses a "provider/model" string from markdown frontmatter.
func modelRef(s string) (string, bool) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", false
	}
	return parseModelRef(raw)
}
