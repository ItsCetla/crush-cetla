package config

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/opencode"
)

// OpenCodeOptions controls what Crush imports from a local OpenCode
// install. Everything comes from OpenCode's files on disk; nothing is
// fetched.
type OpenCodeOptions struct {
	Disabled     bool  `json:"disabled,omitempty" jsonschema:"description=Turn off all importing from OpenCode,default=false"`
	MCP          *bool `json:"mcp,omitempty" jsonschema:"description=Import MCP servers from OpenCode config. A server with the same name in crush.json wins,default=true"`
	Providers    *bool `json:"providers,omitempty" jsonschema:"description=Import provider base URLs\\, API keys\\, headers and custom models from OpenCode config and honor its enabled_providers and disabled_providers. Values set in crush.json win,default=true"`
	Models       *bool `json:"models,omitempty" jsonschema:"description=Extend the model catalog with OpenCode's locally cached models.dev catalog,default=true"`
	Credentials  bool  `json:"credentials,omitempty" jsonschema:"description=Use API keys saved by opencode auth login. OAuth logins are never imported,default=false"`
	Instructions *bool `json:"instructions,omitempty" jsonschema:"description=Add OpenCode's instructions files and global AGENTS.md to the context,default=true"`
	Skills       *bool `json:"skills,omitempty" jsonschema:"description=Add OpenCode's skill directories,default=true"`
	Commands     *bool `json:"commands,omitempty" jsonschema:"description=Offer OpenCode's custom commands as opencode:<name> commands,default=true"`
	Tools        *bool `json:"tools,omitempty" jsonschema:"description=Apply OpenCode's global tools toggles to built-in tools and imported MCP servers. A toggle that any OpenCode agent turns back on is skipped,default=true"`
	Permissions  *bool `json:"permissions,omitempty" jsonschema:"description=Apply OpenCode's edit: allow as prompt-free file edits inside the project and its whole-tool deny rules as disabled tools. Nothing else is imported,default=true"`
	Compaction   *bool `json:"compaction,omitempty" jsonschema:"description=Turn off auto-summarize when OpenCode's compaction.auto is false and no OpenCode plugin is installed,default=true"`
}

// openCodeSettings is the effective form of [OpenCodeOptions].
type openCodeSettings struct {
	enabled      bool
	mcp          bool
	providers    bool
	models       bool
	credentials  bool
	instructions bool
	skills       bool
	commands     bool
	tools        bool
	permissions  bool
	compaction   bool
}

// readsConfig reports whether any enabled import needs OpenCode's config
// files.
func (s openCodeSettings) readsConfig() bool {
	return s.mcp || s.providers || s.instructions || s.skills || s.commands ||
		s.tools || s.permissions || s.compaction
}

func openCodeSettingsFor(o *Options) openCodeSettings {
	if v, ok := os.LookupEnv("CRUSH_DISABLE_OPENCODE"); ok {
		if off, _ := strconv.ParseBool(v); off {
			return openCodeSettings{}
		}
	}
	var oc OpenCodeOptions
	if o != nil && o.OpenCode != nil {
		oc = *o.OpenCode
	}
	if oc.Disabled {
		return openCodeSettings{}
	}
	on := func(b *bool) bool { return b == nil || *b }
	return openCodeSettings{
		enabled:      true,
		mcp:          on(oc.MCP),
		providers:    on(oc.Providers),
		models:       on(oc.Models),
		credentials:  oc.Credentials,
		instructions: on(oc.Instructions),
		skills:       on(oc.Skills),
		commands:     on(oc.Commands),
		tools:        on(oc.Tools),
		permissions:  on(oc.Permissions),
		compaction:   on(oc.Compaction),
	}
}

func init() {
	RegisterProjectConfigFinder(openCodeProjectFiles)
}

// openCodeProjectFileNames are the project-level OpenCode config files
// looked for in every directory from the project root down to the working
// directory.
var openCodeProjectFileNames = []string{
	"opencode.json",
	"opencode.jsonc",
	filepath.Join(".opencode", "opencode.json"),
	filepath.Join(".opencode", "opencode.jsonc"),
}

// openCodeProjectFiles reports the project-level OpenCode config files so
// the project trust fingerprint covers them: an imported local MCP server
// runs a command, so a repository that ships or changes one must be
// trusted again before Crush reads it.
func openCodeProjectFiles(workingDir, root string) []string {
	if off, _ := strconv.ParseBool(os.Getenv("CRUSH_DISABLE_OPENCODE")); off {
		return nil
	}
	globalDir := filepath.Clean(openCodePaths().ConfigDir)
	dir, root := filepath.Clean(workingDir), filepath.Clean(root)
	var files []string
	for {
		if dir != globalDir {
			for _, name := range openCodeProjectFileNames {
				files = append(files, filepath.Join(dir, name))
			}
		}
		parent := filepath.Dir(dir)
		if dir == root || parent == dir || !isWithinDir(root, parent) {
			return files
		}
		dir = parent
	}
}

func isWithinDir(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// stalenessPaths are the OpenCode config files to watch for changes: the
// ones that were read (including markdown commands and agents) plus the
// global ones that could appear.
func (imp *openCodeImport) stalenessPaths() []string {
	if imp == nil {
		return nil
	}
	dir := openCodePaths().ConfigDir
	files := imp.files()
	if imp.config != nil {
		files = append(files, imp.config.ExtraFiles...)
	}
	return append(files,
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "opencode.json"),
		filepath.Join(dir, "opencode.jsonc"),
	)
}

func openCodePaths() opencode.Paths {
	home, _ := os.UserHomeDir()
	return opencode.ResolvePaths(os.Getenv, home)
}

// openCodeImport is what one config load read from a local OpenCode
// install.
type openCodeImport struct {
	settings openCodeSettings
	// includeProject records whether project-level OpenCode files were
	// read (only for trusted projects).
	includeProject bool
	config         *opencode.Config
	// creds maps catwalk provider IDs to API keys from OpenCode's auth
	// store. Only loaded when credentials are enabled.
	creds map[string]string
	// ownProviders are the provider IDs Crush's own config defines,
	// recorded before anything is imported. They are never disabled.
	ownProviders map[string]bool
	// importedMCP names the MCP servers that came from OpenCode.
	importedMCP map[string]bool
	// applied records what was taken from OpenCode beyond MCP servers and
	// provider settings.
	applied OpenCodeApplied
}

// loadOpenCode reads the user's OpenCode config (and, when enabled, its
// stored API keys). Project-level OpenCode files are only read when
// includeProject is set, which the caller ties to project trust. It
// returns nil when importing is turned off.
func loadOpenCode(opts *Options, workingDir string, includeProject bool) *openCodeImport {
	settings := openCodeSettingsFor(opts)
	if !settings.enabled {
		return nil
	}
	imp := &openCodeImport{settings: settings, includeProject: includeProject}
	paths := openCodePaths()

	if settings.readsConfig() {
		cfg, err := opencode.LoadConfig(opencode.LoadOptions{
			Paths:           paths,
			Getenv:          os.Getenv,
			Cwd:             workingDir,
			ProjectBoundary: projectBoundary(workingDir),
			IncludeProject:  includeProject,
		})
		if err != nil {
			slog.Warn("Could not read OpenCode config", "error", err)
		}
		if cfg != nil {
			for _, w := range cfg.Warnings {
				slog.Warn("OpenCode config", "warning", w)
			}
			if len(cfg.Files) > 0 {
				slog.Debug("Loaded OpenCode config", "files", cfg.Files, "mcp", len(cfg.MCP), "providers", len(cfg.Providers))
			}
		}
		imp.config = cfg
	}

	if settings.credentials {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		creds, err := opencode.LoadCredentials(ctx, paths, os.Getenv)
		if err != nil {
			slog.Warn("Could not read OpenCode credentials", "error", err)
		}
		imp.creds = creds
	}
	return imp
}

// files returns the OpenCode config files that were read, for staleness
// tracking.
func (imp *openCodeImport) files() []string {
	if imp == nil || imp.config == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(imp.config.Files), func(f string) bool {
		return f == opencode.ContentSource
	})
}

// literalEscaper escapes the characters Crush's value expansion treats
// specially (see shell.ExpandValue).
var literalEscaper = strings.NewReplacer(`\`, `\\`, `$`, `\$`, "`", "\\`")

// literalValue makes s survive Crush's shell-style value expansion
// unchanged. OpenCode values arrive with {env:...} and {file:...} already
// substituted; unescaped, a "$" in an API key would be expanded and a
// "$(...)" in any value would be executed when Crush resolves the config.
func literalValue(s string) string {
	return literalEscaper.Replace(s)
}

func literalValues(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = literalValue(s)
	}
	return out
}

func literalMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = literalValue(v)
	}
	return out
}

// importedMCPTimeout is the startup timeout, in seconds, for imported MCP
// servers that don't set one. It matches OpenCode's default; npx-launched
// servers often need more than Crush's own 10s default on a cold cache.
const importedMCPTimeout = 30

// mcpFromOpenCode converts an OpenCode MCP server to Crush's form.
// OpenCode remote servers speak Streamable HTTP, Crush's "http" type.
func mcpFromOpenCode(s opencode.MCPServer) (MCPConfig, bool) {
	var m MCPConfig
	switch s.Type {
	case "local":
		if len(s.Command) == 0 || strings.TrimSpace(s.Command[0]) == "" {
			return m, false
		}
		m = MCPConfig{
			Type:    MCPStdio,
			Command: literalValue(s.Command[0]),
			Args:    literalValues(s.Command[1:]),
			Env:     literalMap(s.Environment),
			Cwd:     literalValue(s.Cwd),
		}
	case "remote":
		if s.URL == "" {
			return m, false
		}
		m = MCPConfig{
			Type:    MCPHttp,
			URL:     literalValue(s.URL),
			Headers: literalMap(s.Headers),
		}
		// OpenCode attempts OAuth on any remote server that returns 401,
		// but Crush's OAuth flag demands a browser login up front. Only an
		// explicit oauth block opts in; otherwise add "oauth": true for
		// the server in crush.json.
		if s.OAuth != nil && !s.OAuthDisabled {
			m.OAuth = true
			m.OAuthClientID = literalValue(s.OAuth.ClientID)
			m.OAuthClientSecret = literalValue(s.OAuth.ClientSecret)
			m.OAuthCallbackPort = s.OAuth.CallbackPort
		}
	default:
		return m, false
	}
	m.Disabled = s.Disabled
	m.Timeout = importedMCPTimeout
	if s.TimeoutMS > 0 {
		m.Timeout = (s.TimeoutMS + 999) / 1000
	}
	return m, true
}

// isPartial reports whether m only carries settings layered onto a server
// defined elsewhere: Crush writes such entries when a server is toggled
// or an OAuth token is saved.
func (m MCPConfig) isPartial() bool {
	return m.Type == "" && m.Command == "" && m.URL == ""
}

// overlayMCP layers the settings from a partial Crush entry onto an
// imported server.
func overlayMCP(base, over MCPConfig) MCPConfig {
	if over.Disabled {
		base.Disabled = true
	}
	if over.DisabledTools != nil {
		base.DisabledTools = over.DisabledTools
	}
	if over.EnabledTools != nil {
		base.EnabledTools = over.EnabledTools
	}
	if over.Timeout > 0 {
		base.Timeout = over.Timeout
	}
	if over.Sessionless != nil {
		base.Sessionless = over.Sessionless
	}
	if over.Cwd != "" {
		base.Cwd = over.Cwd
	}
	for k, v := range over.Env {
		if base.Env == nil {
			base.Env = map[string]string{}
		}
		base.Env[k] = v
	}
	for k, v := range over.Headers {
		if base.Headers == nil {
			base.Headers = map[string]string{}
		}
		base.Headers[k] = v
	}
	if over.OAuth {
		base.OAuth = true
	}
	base.OAuthClientID = cmp.Or(over.OAuthClientID, base.OAuthClientID)
	base.OAuthClientSecret = cmp.Or(over.OAuthClientSecret, base.OAuthClientSecret)
	base.OAuthCallbackPort = cmp.Or(over.OAuthCallbackPort, base.OAuthCallbackPort)
	if over.OAuthToken != nil {
		base.OAuthToken = over.OAuthToken
	}
	if over.ChannelEnabled {
		base.ChannelEnabled = true
	}
	if over.ChannelReply != nil {
		base.ChannelReply = over.ChannelReply
	}
	return base
}

// applyOpenCodeMCP adds the imported MCP servers. A server Crush's own
// config defines in full wins outright; a partial Crush entry (a toggle
// or a saved OAuth token) is layered onto the imported server. It must run
// before setDefaults, which drops token-only entries it can't place.
func (c *Config) applyOpenCodeMCP(imp *openCodeImport) {
	if imp == nil || !imp.settings.mcp || imp.config == nil {
		return
	}
	for name, srv := range imp.config.MCP {
		m, ok := mcpFromOpenCode(srv)
		if !ok {
			slog.Warn("Skipping unsupported OpenCode MCP server", "name", name, "type", srv.Type)
			continue
		}
		if c.MCP == nil {
			c.MCP = make(MCPs)
		}
		existing, exists := c.MCP[name]
		switch {
		case !exists:
			c.MCP[name] = m
		case existing.isPartial():
			c.MCP[name] = overlayMCP(m, existing)
		default:
			continue
		}
		if imp.importedMCP == nil {
			imp.importedMCP = map[string]bool{}
		}
		imp.importedMCP[name] = true
	}
}

// applyOpenCodeProviders fills provider settings from OpenCode config and,
// when enabled, OpenCode's stored API keys. Only fields Crush's config
// leaves empty are filled, and custom models are added alongside Crush's.
func (c *Config) applyOpenCodeProviders(imp *openCodeImport) {
	if imp == nil {
		return
	}
	if c.Providers == nil {
		c.Providers = csync.NewMap[string, ProviderConfig]()
	}
	imp.ownProviders = map[string]bool{}
	for id := range c.Providers.Seq2() {
		imp.ownProviders[id] = true
	}
	if imp.settings.providers && imp.config != nil {
		for _, id := range slices.Sorted(maps.Keys(imp.config.Providers)) {
			cid := opencode.CatwalkID(id)
			pc, exists := c.Providers.Get(cid)
			if mergeOpenCodeProvider(&pc, cid, imp.config.Providers[id], exists) {
				c.Providers.Set(cid, pc)
			}
		}
	}
	for id, key := range imp.creds {
		pc, _ := c.Providers.Get(id)
		if pc.APIKey != "" || pc.OAuthToken != nil || key == "" {
			continue
		}
		pc.ID = cmp.Or(pc.ID, id)
		pc.APIKey = literalValue(key)
		c.Providers.Set(id, pc)
	}
}

// mergeOpenCodeProvider fills the empty fields of pc from an OpenCode
// provider and reports whether anything changed. The provider's identity
// (ID, name and API type) is only filled when Crush already has an entry
// for it or something else was imported, so an OpenCode block that adds
// nothing doesn't create a provider.
func mergeOpenCodeProvider(pc *ProviderConfig, id string, p opencode.Provider, exists bool) bool {
	changed := false
	if pc.APIKey == "" && p.APIKey != "" {
		pc.APIKey = literalValue(p.APIKey)
		changed = true
	}
	if pc.BaseURL == "" && p.BaseURL != "" {
		pc.BaseURL = literalValue(p.BaseURL)
		changed = true
	}
	for k, v := range p.Headers {
		if _, ok := pc.ExtraHeaders[k]; ok {
			continue
		}
		if pc.ExtraHeaders == nil {
			pc.ExtraHeaders = map[string]string{}
		}
		pc.ExtraHeaders[k] = literalValue(v)
		changed = true
	}
	for _, key := range slices.Sorted(maps.Keys(p.Models)) {
		om := p.Models[key]
		if om.Disabled != nil && *om.Disabled {
			continue
		}
		// Crush's agent can't work without tool calls.
		if om.ToolCall != nil && !*om.ToolCall {
			continue
		}
		m := modelFromOpenCode(key, om)
		if slices.ContainsFunc(pc.Models, func(x catwalk.Model) bool { return x.ID == m.ID }) {
			continue
		}
		pc.Models = append(pc.Models, m)
		changed = true
	}
	if !changed && !exists {
		return false
	}
	pc.ID = cmp.Or(pc.ID, id)
	if pc.Name == "" && p.Name != "" {
		pc.Name = p.Name
		changed = true
	}
	if pc.Type == "" && p.NPM != "" {
		if typ, _, ok := opencode.WireFor(p.NPM, p.BaseURL); ok {
			pc.Type = typ
			changed = true
		}
	}
	return changed
}

// modelFromOpenCode converts a model from an OpenCode provider block.
// Unset limits fall back to conservative values so a hand-written local
// model entry is still usable.
func modelFromOpenCode(key string, m opencode.Model) catwalk.Model {
	out := catwalk.Model{
		ID:            cmp.Or(m.ModelID, m.ID, key),
		Name:          cmp.Or(m.Name, key),
		ContextWindow: m.Limit.Context,
	}
	out.DefaultMaxTokens = m.Limit.Output
	if out.DefaultMaxTokens == 0 && out.ContextWindow > 0 {
		out.DefaultMaxTokens = out.ContextWindow / 10
	}
	if out.DefaultMaxTokens == 0 {
		out.DefaultMaxTokens = 4096
	}
	if m.Reasoning != nil {
		out.CanReason = *m.Reasoning
	}
	out.SupportsImages = (m.Attachment != nil && *m.Attachment) || slices.Contains(m.InputModalities, "image")
	if m.Cost != nil {
		out.CostPer1MIn = m.Cost.Input
		out.CostPer1MOut = m.Cost.Output
		out.CostPer1MInCached = m.Cost.CacheWrite
		out.CostPer1MOutCached = m.Cost.CacheRead
	}
	return out
}

// applyOpenCodeModelPreference selects OpenCode's models when Crush has no
// selection of its own and the referenced provider and model are
// available. The large model is the default agent's model, else "model";
// the small model is "small_model". It is part of the provider settings,
// so options.opencode.providers turns it off.
func (c *Config) applyOpenCodeModelPreference(imp *openCodeImport) {
	if imp == nil || imp.config == nil {
		return
	}
	imp.applied.Models = nil
	if !imp.settings.providers {
		return
	}
	type candidate struct{ ref, source string }
	var large []candidate
	agentName := cmp.Or(imp.config.DefaultAgent, "build")
	if a, ok := imp.config.Agents[agentName]; ok && !a.Disabled && a.Model != "" {
		large = append(large, candidate{a.Model, "agent " + agentName})
	}
	if imp.config.Model != "" {
		large = append(large, candidate{imp.config.Model, "model"})
	}
	var small []candidate
	if imp.config.SmallModel != "" {
		small = append(small, candidate{imp.config.SmallModel, "small_model"})
	}
	prefs := []struct {
		typ        SelectedModelType
		candidates []candidate
	}{
		{SelectedModelTypeLarge, large},
		{SelectedModelTypeSmall, small},
	}
	for _, pref := range prefs {
		for _, cand := range pref.candidates {
			choice := OpenCodeModelChoice{Type: pref.typ, Ref: cand.ref, Source: cand.source}
			if sel, set := c.Models[pref.typ]; set {
				choice.Result = "not used: Crush already selects " + sel.Provider + "/" + sel.Model
				imp.applied.Models = append(imp.applied.Models, choice)
				break
			}
			provider, model, reason := c.resolveOpenCodeModel(imp, cand.ref)
			if reason != "" {
				choice.Result = "not used: " + reason
				imp.applied.Models = append(imp.applied.Models, choice)
				continue
			}
			if c.Models == nil {
				c.Models = map[SelectedModelType]SelectedModel{}
			}
			c.Models[pref.typ] = SelectedModel{Provider: provider, Model: model}
			choice.Result = "selected"
			imp.applied.Models = append(imp.applied.Models, choice)
			slog.Debug("Using OpenCode model preference", "type", pref.typ, "source", cand.source, "provider", provider, "model", model)
			break
		}
	}
}

// resolveOpenCodeModel maps an OpenCode "provider/model" reference to a
// configured Crush provider and model ID, or says why it can't.
func (c *Config) resolveOpenCodeModel(imp *openCodeImport, ref string) (provider, model, reason string) {
	ocProvider, key, ok := strings.Cut(ref, "/")
	if !ok || key == "" {
		return "", "", "not a provider/model reference"
	}
	provider = opencode.CatwalkID(ocProvider)
	pc, ok := c.Providers.Get(provider)
	if !ok {
		return "", "", "provider " + provider + " is not configured"
	}
	if pc.Disable {
		return "", "", "provider " + provider + " is disabled"
	}
	model = key
	// OpenCode refers to a custom model by its config key, which can
	// differ from the model ID sent to the API.
	if p, ok := imp.config.Providers[ocProvider]; ok {
		if om, ok := p.Models[key]; ok {
			model = modelFromOpenCode(key, om).ID
		}
	}
	if c.GetModel(provider, model) == nil {
		return "", "", "model " + model + " is not available from " + provider
	}
	return provider, model, ""
}

// envRefRe finds the variables a catalog API key template reads.
var envRefRe = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// openCodeCatalogSource extends the catalog with OpenCode's locally cached
// models.dev catalog. Providers Catwalk already defines gain the models
// Catwalk lacks. Providers new to Crush are only listed when usable: their
// API key variable is set or Crush's config (including what was imported
// from OpenCode) configures them. Listing all ~200 would bury the model
// picker.
func openCodeCatalogSource(cfg *Config) catalogSource {
	return catalogSource{
		name: "opencode",
		load: func() ([]catwalk.Provider, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cat, err := opencode.LoadCatalog(ctx, openCodePaths(), os.Getenv)
			if cat == nil {
				return nil, err
			}
			slog.Debug("Loaded OpenCode models catalog", "source", cat.Source, "path", cat.Path, "updated", cat.UpdatedAt, "providers", len(cat.Providers))
			return opencode.ToCatwalk(cat.Providers), err
		},
		augment: true,
		include: func(p catwalk.Provider) bool {
			if cfg != nil && cfg.Providers != nil {
				if pc, ok := cfg.Providers.Get(string(p.ID)); ok && !pc.Disable {
					return true
				}
			}
			for _, ref := range envRefRe.FindAllStringSubmatch(p.APIKey, -1) {
				if os.Getenv(ref[1]) != "" {
					return true
				}
			}
			return false
		},
	}
}
