package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ContentSource is the pseudo path used in [Config.Files] and
// [MCPServer.SourcePath] for the inline $OPENCODE_CONFIG_CONTENT document.
const ContentSource = "$OPENCODE_CONFIG_CONTENT"

// configNames are the per-directory config files, lowest priority first.
var configNames = []string{"opencode.json", "opencode.jsonc"}

// LoadOptions configures [LoadConfig].
type LoadOptions struct {
	Paths
	// Getenv reads environment variables, both OpenCode's own (such as
	// OPENCODE_CONFIG) and those referenced by {env:NAME}. Nil behaves
	// like an empty environment.
	Getenv func(string) string
	// Cwd is the working directory. Relative $OPENCODE_CONFIG paths and
	// {file:...} references in $OPENCODE_CONFIG_CONTENT resolve against
	// it, and the project walk ends at it.
	Cwd string
	// ProjectBoundary is the top-most directory of the project walk,
	// typically the repository root. When empty, or when Cwd is not inside
	// it, only Cwd itself is searched.
	ProjectBoundary string
	// IncludeProject enables project config files (opencode.json(c) and
	// .opencode/opencode.json(c) between ProjectBoundary and Cwd). Leave
	// it false for untrusted projects.
	IncludeProject bool
}

// Config is the merged result of every OpenCode config document, with all
// {env:...} and {file:...} references already substituted.
type Config struct {
	// MCP holds the configured MCP servers by name.
	MCP map[string]MCPServer
	// Providers holds the provider overrides by provider ID.
	Providers map[string]Provider
	// Model and SmallModel are "provider/model" references with any
	// "#variant" suffix removed. The last document that sets one wins.
	Model      string
	SmallModel string
	// EnabledProviders is the allow-list from the last document that set
	// enabled_providers (nil when none did).
	EnabledProviders []string
	// DisabledProviders is the union of every disabled_providers list.
	DisabledProviders []string
	// Instructions are the rule files matched by the instructions entries,
	// in order, without duplicates. URL entries are skipped.
	Instructions []string
	// GlobalRules is the global rules file OpenCode reads: AGENTS.md in the
	// config directory, else ~/.claude/CLAUDE.md. Empty when neither exists.
	GlobalRules string
	// SkillPaths are existing skill directories: skill(s) in each config
	// directory, then the skills.paths entries.
	SkillPaths []string
	// Plugins are the plugin specs from the plugin entries and the plugin
	// files in the config directories. They are listed, never run.
	Plugins []string
	// CompactionAuto is compaction.auto from the last document that set it.
	CompactionAuto *bool
	// Tools enables or disables tools globally, by name or wildcard.
	Tools map[string]bool
	// Permission holds the permission rules by tool name ("*" for all).
	Permission map[string]Permission
	// Commands holds the custom commands by name.
	Commands map[string]Command
	// Agents holds the agent definitions by name.
	Agents map[string]Agent
	// DefaultAgent is the default_agent from the last document that set it.
	DefaultAgent string
	// ConfigDirs are the existing directories scanned for skills, commands,
	// agents and plugins, lowest priority first.
	ConfigDirs []string
	// ExtraFiles are the markdown command and agent files that were read.
	ExtraFiles []string
	// Files lists the documents that were read and parsed, lowest
	// priority first. The inline document appears as [ContentSource].
	Files []string
	// Warnings describes everything that was skipped: unreadable or
	// malformed files, invalid entries and missing {file:...} targets.
	Warnings []string
}

// MCPServer is a normalized MCP server definition from either the V1
// (mcp.<name>) or the V2 (mcp.servers.<name>) config shape.
type MCPServer struct {
	Name string
	// Type is "local" (stdio) or "remote" (Streamable HTTP).
	Type        string
	Command     []string
	Cwd         string
	Environment map[string]string
	URL         string
	Headers     map[string]string
	// OAuth holds explicit OAuth settings for a remote server. Nil means
	// OpenCode's default: discover and register automatically.
	OAuth *MCPOAuth
	// OAuthDisabled is set when the config says "oauth": false.
	OAuthDisabled bool
	Disabled      bool
	// TimeoutMS is the startup timeout in milliseconds: the server's V2
	// timeout.startup, else its V1 timeout, else the V2 mcp.timeout.startup
	// default. Zero means unset.
	TimeoutMS int
	// SourcePath is the document that defined the server.
	SourcePath string
}

// MCPOAuth holds explicit OAuth client settings for a remote MCP server.
type MCPOAuth struct {
	ClientID              string
	ClientSecret          string
	Scope                 string
	CallbackPort          int
	RedirectURI           string
	AuthServerMetadataURL string
}

// Provider is a provider override merged field by field across documents.
type Provider struct {
	ID   string
	Name string
	// NPM is the AI SDK package (V1 npm, V2 package without "aisdk:").
	NPM string
	// BaseURL is the V1 api or options.baseURL, or the V2 settings.baseURL.
	BaseURL string
	// APIKey is the V1 options.apiKey or the V2 settings.apiKey.
	APIKey string
	Env    []string
	// Headers are merged key by key across documents.
	Headers map[string]string
	// Whitelist and Blacklist are the V1 model filters.
	Whitelist []string
	Blacklist []string
	Models    map[string]Model
}

// Model is a model override merged field by field across documents. Unset
// fields are zero or nil.
type Model struct {
	ID string
	// ModelID is the model ID sent to the API when it differs from ID.
	ModelID string
	Name    string
	Family  string
	NPM     string
	BaseURL string
	Limit   Limit
	// Cost is the base pricing tier, in USD per million tokens.
	Cost             *Cost
	ToolCall         *bool
	Reasoning        *bool
	Attachment       *bool
	InputModalities  []string
	OutputModalities []string
	Disabled         *bool
	Headers          map[string]string
}

// Limit holds token limits; zero means unset.
type Limit struct {
	Context int64
	Input   int64
	Output  int64
}

// Cost is pricing in USD per million tokens.
type Cost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// LoadConfig reads and merges every OpenCode config document in priority
// order, lowest first:
//
//  1. the global directory: legacy config.json, opencode.json,
//     opencode.jsonc;
//  2. $OPENCODE_CONFIG (relative to Cwd);
//  3. project opencode.json(c) files from ProjectBoundary down to Cwd;
//  4. project .opencode/opencode.json(c) files along the same walk;
//  5. $OPENCODE_CONFIG_CONTENT;
//  6. the managed directory: opencode.json, opencode.jsonc.
//
// Project files (3, 4) are only read when IncludeProject is set and
// neither OPENCODE_DISABLE_PROJECT_CONFIG nor
// OPENCODE_CONFIG_PROJECT_DISABLE is truthy. A file that cannot be parsed
// is skipped as a whole with a warning. Missing files are ignored.
//
// It then scans the config directories (the global one, ~/.opencode and,
// for included projects, each .opencode directory of the walk) for skills,
// markdown commands and agents, and plugin files, and resolves the
// instructions entries and the global rules file. Remote instructions and
// skills are never fetched and plugins are never run.
func LoadConfig(opts LoadOptions) (*Config, error) {
	getenv := envOrEmpty(opts.Getenv)
	cwd := opts.Cwd
	if cwd != "" {
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
		cwd = abs
	}

	l := &loader{
		sub: substituter{getenv: getenv, home: opts.Home},
		m:   newMerger(),
	}

	var globalDirs []string
	if opts.ConfigDir != "" {
		globalDirs = append(globalDirs, opts.ConfigDir)
		for _, name := range append([]string{"config.json"}, configNames...) {
			l.loadFile(filepath.Join(opts.ConfigDir, name))
		}
	}
	if opts.ManagedDir != "" {
		globalDirs = append(globalDirs, opts.ManagedDir)
	}

	if explicit := getenv("OPENCODE_CONFIG"); explicit != "" {
		if !filepath.IsAbs(explicit) && cwd != "" {
			explicit = filepath.Join(cwd, explicit)
		}
		l.loadFile(explicit)
	}

	projectDisabled := truthy(getenv("OPENCODE_CONFIG_PROJECT_DISABLE")) ||
		truthy(getenv("OPENCODE_DISABLE_PROJECT_CONFIG"))
	if opts.IncludeProject && !projectDisabled && cwd != "" {
		// Global sources must not come back through the project walk,
		// and working inside a global directory disables the walk.
		skipDirs := resolvedSet(globalDirs)
		var globalFiles []string
		for _, dir := range globalDirs {
			for _, name := range append([]string{"config.json"}, configNames...) {
				globalFiles = append(globalFiles, filepath.Join(dir, name))
			}
		}
		skipFiles := resolvedSet(globalFiles)
		loadProjectDir := func(dir string) {
			if skipDirs[resolvePath(dir)] {
				return
			}
			for _, name := range configNames {
				file := filepath.Join(dir, name)
				if !skipFiles[resolvePath(file)] {
					l.loadFile(file)
				}
			}
		}
		if !skipDirs[resolvePath(cwd)] {
			dirs := projectDirs(opts.ProjectBoundary, cwd)
			for _, dir := range dirs {
				loadProjectDir(dir)
			}
			for _, dir := range dirs {
				loadProjectDir(filepath.Join(dir, ".opencode"))
			}
		}
	}

	if content := getenv("OPENCODE_CONFIG_CONTENT"); content != "" {
		l.loadText(content, ContentSource, cwd)
	}

	if opts.ManagedDir != "" {
		for _, name := range configNames {
			l.loadFile(filepath.Join(opts.ManagedDir, name))
		}
	}

	// Markdown commands and agents override same-named config entries.
	configDirs := []string{opts.ConfigDir}
	if opts.Home != "" {
		configDirs = append(configDirs, filepath.Join(opts.Home, ".opencode"))
	}
	if opts.IncludeProject && !projectDisabled && cwd != "" {
		for _, dir := range projectDirs(opts.ProjectBoundary, cwd) {
			configDirs = append(configDirs, filepath.Join(dir, ".opencode"))
		}
	}
	l.scanConfigDirs(slices.DeleteFunc(configDirs, func(dir string) bool { return dir == "" }))
	l.resolveExtras(opts, cwd, projectDisabled || !opts.IncludeProject)
	l.warnings = append(l.warnings, l.m.cfg.Warnings...)
	l.m.cfg.Warnings = nil

	cfg := l.m.result()
	cfg.Files = l.files
	cfg.Warnings = l.warnings
	return cfg, nil
}

// projectDirs lists the directories from boundary down to cwd, both
// inclusive. Without a usable boundary only cwd is returned.
func projectDirs(boundary, cwd string) []string {
	if boundary == "" {
		return []string{cwd}
	}
	boundary, err := filepath.Abs(boundary)
	if err != nil {
		return []string{cwd}
	}
	rel, err := filepath.Rel(boundary, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return []string{cwd}
	}
	dirs := []string{cwd}
	for dir := cwd; dir != boundary; {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		dirs = append(dirs, dir)
	}
	slices.Reverse(dirs)
	return dirs
}

// resolvePath returns path with symlinks resolved when possible.
func resolvePath(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func resolvedSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[resolvePath(p)] = true
	}
	return set
}

type loader struct {
	sub      substituter
	m        *merger
	files    []string
	warnings []string
}

func (l *loader) warnf(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

func (l *loader) loadFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			l.warnf("%s: %v", path, err)
		}
		return
	}
	dir := filepath.Dir(path)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	l.loadText(string(data), path, dir)
}

func (l *loader) loadText(text, source, dir string) {
	text, warnings := l.sub.substitute(text, source, dir)
	l.warnings = append(l.warnings, warnings...)
	doc, err := parseDocument([]byte(text))
	if err != nil {
		l.warnf("%s: rejected malformed config: %v", source, err)
		return
	}
	l.files = append(l.files, source)
	l.m.apply(doc, source)
	// Keep warnings in document order.
	l.warnings = append(l.warnings, l.m.cfg.Warnings...)
	l.m.cfg.Warnings = nil
}

// document holds the top-level keys this package understands, still raw.
type document map[string]json.RawMessage

func parseDocument(data []byte) (document, error) {
	clean, err := jsoncToJSON(data)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(clean)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("root is not an object")
	}
	var doc document
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// merger folds documents into a Config in priority order.
type merger struct {
	cfg *Config
	// mcpEnabled is the last explicit enabled state seen per MCP server,
	// from a V1 toggle or a definition, emulating V1's deep merge of the
	// enabled flag.
	mcpEnabled map[string]bool
	// v1Timeout and v2Timeout keep per-server timeouts until the global
	// default is known.
	v1Timeout     map[string]int
	v2Timeout     map[string]int
	globalStartup int
	extra         extraState
}

func newMerger() *merger {
	return &merger{
		cfg: &Config{
			MCP:        map[string]MCPServer{},
			Providers:  map[string]Provider{},
			Tools:      map[string]bool{},
			Permission: map[string]Permission{},
			Commands:   map[string]Command{},
			Agents:     map[string]Agent{},
		},
		mcpEnabled: map[string]bool{},
		v1Timeout:  map[string]int{},
		v2Timeout:  map[string]int{},
	}
}

func (m *merger) warnf(format string, args ...any) {
	m.cfg.Warnings = append(m.cfg.Warnings, fmt.Sprintf(format, args...))
}

func (m *merger) apply(doc document, source string) {
	if raw, ok := doc["mcp"]; ok {
		m.applyMCP(raw, source)
	}
	m.applyProviders(doc, source)
	if raw, ok := doc["model"]; ok {
		if ref, ok := parseModelRef(raw); ok {
			m.cfg.Model = ref
		} else {
			m.warnf("%s: model: invalid model reference", source)
		}
	}
	if raw, ok := doc["small_model"]; ok {
		if ref, ok := parseModelRef(raw); ok {
			m.cfg.SmallModel = ref
		} else {
			m.warnf("%s: small_model: invalid model reference", source)
		}
	}
	if raw, ok := doc["enabled_providers"]; ok {
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			m.warnf("%s: enabled_providers: %v", source, err)
		} else {
			m.cfg.EnabledProviders = append([]string{}, list...)
		}
	}
	if raw, ok := doc["disabled_providers"]; ok {
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			m.warnf("%s: disabled_providers: %v", source, err)
		} else {
			for _, id := range list {
				if !slices.Contains(m.cfg.DisabledProviders, id) {
					m.cfg.DisabledProviders = append(m.cfg.DisabledProviders, id)
				}
			}
		}
	}
	m.applyExtras(doc, source)
}

func (m *merger) result() *Config {
	for name, srv := range m.cfg.MCP {
		switch {
		case m.v2Timeout[name] > 0:
			srv.TimeoutMS = m.v2Timeout[name]
		case m.v1Timeout[name] > 0:
			srv.TimeoutMS = m.v1Timeout[name]
		default:
			srv.TimeoutMS = m.globalStartup
		}
		m.cfg.MCP[name] = srv
	}
	return m.cfg
}

// parseModelRef accepts "provider/model", "provider/model#variant" or the
// V2 object form and returns "provider/model".
func parseModelRef(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var obj struct {
			ProviderID string `json:"providerID"`
			Model      string `json:"model"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return "", false
		}
		s = obj.ProviderID + "/" + obj.Model
	}
	provider, model, ok := strings.Cut(s, "/")
	if !ok || provider == "" || strings.Contains(provider, "#") {
		return "", false
	}
	model, _, _ = strings.Cut(model, "#")
	if model == "" {
		return "", false
	}
	return provider + "/" + model, true
}
