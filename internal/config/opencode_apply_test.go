package config

import (
	"path/filepath"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/opencode"
	"github.com/stretchr/testify/require"
)

// allOpenCodeContext turns on every part applyOpenCodeContext handles.
var allOpenCodeContext = openCodeSettings{
	enabled: true, instructions: true, skills: true, compaction: true,
	tools: true, permissions: true, commands: true,
}

var openCodeModelChoice = openCodeSettings{enabled: true, providers: true}

func TestApplyOpenCodeInstructions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inProject := filepath.Join(dir, "docs", "rules.md")
	outside := filepath.Join(t.TempDir(), "team.md")
	globalRules := filepath.Join(t.TempDir(), "AGENTS.md")
	imp := &openCodeImport{settings: allOpenCodeContext, config: &opencode.Config{
		Instructions: []string{inProject, outside, filepath.Join(dir, "TADA.md")},
		GlobalRules:  globalRules,
	}}

	cfg := &Config{Options: &Options{ContextPaths: []string{"tada.md"}}}
	cfg.applyOpenCodeContext(imp, dir)

	require.Equal(t, []string{"tada.md", inProject}, cfg.Options.ContextPaths, "existing paths are not added twice, however spelled")
	require.Equal(t, []string{outside, globalRules}, cfg.Options.GlobalContextPaths)
	require.Equal(t, []string{inProject}, imp.applied.ContextPaths)
	require.Equal(t, []string{outside, globalRules}, imp.applied.GlobalContextPaths)
}

func TestApplyOpenCodeSkills(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	imp := &openCodeImport{settings: allOpenCodeContext, config: &opencode.Config{
		SkillPaths: []string{"skills", filepath.Join(dir, "more")},
	}}
	cfg := &Config{Options: &Options{SkillsPaths: []string{filepath.Join(dir, "skills")}}}
	cfg.applyOpenCodeContext(imp, dir)

	require.Equal(t, []string{filepath.Join(dir, "skills"), filepath.Join(dir, "more")}, cfg.Options.SkillsPaths)
	require.Equal(t, []string{filepath.Join(dir, "more")}, imp.applied.SkillsPaths)
}

func TestApplyOpenCodeCompaction(t *testing.T) {
	t.Parallel()

	f, tr := false, true
	for _, tc := range []struct {
		name    string
		auto    *bool
		plugins []string
		off     bool
		note    bool
	}{
		{name: "unset", auto: nil},
		{name: "on", auto: &tr},
		{name: "off", auto: &f, off: true},
		{name: "off with plugins", auto: &f, plugins: []string{"dcp"}, note: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			imp := &openCodeImport{settings: allOpenCodeContext, config: &opencode.Config{
				CompactionAuto: tc.auto,
				Plugins:        tc.plugins,
			}}
			cfg := &Config{}
			cfg.applyOpenCodeContext(imp, t.TempDir())
			require.Equal(t, tc.off, cfg.Options.DisableAutoSummarize)
			require.Equal(t, tc.off, imp.applied.AutoSummarizeOff)
			if tc.note {
				require.Contains(t, imp.applied.Notes[0], "auto-summarize stays on")
			}
		})
	}
}

func TestApplyOpenCodeTools(t *testing.T) {
	t.Parallel()

	imp := &openCodeImport{
		settings:    allOpenCodeContext,
		importedMCP: map[string]bool{"github": true, "my.docs": true, "git": true},
		config: &opencode.Config{
			Tools: map[string]bool{
				"edit":                false,
				"bash":                false,
				"read":                true,
				"github_*":            false,
				"my_docs_search":      false,
				"git_log*":            false,
				"mystery":             false,
				"playwright_navigate": false,
			},
			Agents: map[string]opencode.Agent{
				"build": {Name: "build", Tools: map[string]bool{"bash": true}},
				"old":   {Name: "old", Disabled: true, Tools: map[string]bool{"edit": true}},
			},
		},
	}
	cfg := &Config{
		Options: &Options{DisabledTools: []string{"multiedit"}},
		MCP: MCPs{
			"github":  {Type: MCPHttp, URL: "https://example.invalid/mcp"},
			"my.docs": {Type: MCPHttp, URL: "https://example.invalid/docs"},
			"git":     {Type: MCPHttp, URL: "https://example.invalid/git"},
		},
	}
	cfg.applyOpenCodeContext(imp, t.TempDir())

	require.Equal(t, []string{"multiedit", "edit"}, cfg.Options.DisabledTools, "the disabled agent doesn't keep edit on")
	require.Equal(t, []string{"edit"}, imp.applied.DisabledTools)
	require.True(t, cfg.MCP["github"].Disabled)
	require.Equal(t, []string{"github"}, imp.applied.DisabledMCP)
	require.Equal(t, []string{"search"}, cfg.MCP["my.docs"].DisabledTools)
	require.Equal(t, []string{"my.docs: search"}, imp.applied.DisabledMCPTools)
	require.False(t, cfg.MCP["git"].Disabled)
	require.Empty(t, cfg.MCP["git"].DisabledTools)
	require.Equal(t, []string{
		"tools.bash: false skipped: agent build turns it back on",
		"tools.git_log*: false skipped: only <server>_* and single MCP tools are supported",
		"tools.mystery: false skipped: no matching Ta-Da tool or imported MCP server",
		"tools.playwright_navigate: false skipped: no matching Ta-Da tool or imported MCP server",
	}, imp.applied.Notes)
}

func TestImportedMCPForPrefersLongestPrefix(t *testing.T) {
	t.Parallel()

	imp := &openCodeImport{importedMCP: map[string]bool{"git": true, "git_hub": true}}
	cfg := &Config{}
	server, rest := cfg.importedMCPFor(imp, "git_hub_search")
	require.Equal(t, "git_hub", server)
	require.Equal(t, "search", rest)
	server, rest = cfg.importedMCPFor(imp, "git_log")
	require.Equal(t, "git", server)
	require.Equal(t, "log", rest)
	server, _ = cfg.importedMCPFor(imp, "gitlab_x")
	require.Empty(t, server)
}

func TestApplyOpenCodePermissions(t *testing.T) {
	t.Parallel()

	imp := &openCodeImport{settings: allOpenCodeContext, config: &opencode.Config{
		Permission: map[string]opencode.Permission{
			"edit":      {Patterns: map[string]string{"*": "allow"}},
			"webfetch":  {Action: "deny"},
			"read":      {Action: "allow"},
			"lsp":       {Action: "allow"},
			"todowrite": {Action: "allow"},
			"grep":      {Action: "ask"},
			"websearch": {Action: "ask"},
			"bash":      {Action: "allow"},
			"external":  {Patterns: map[string]string{"*": "ask", "/tmp/*": "allow"}},
			"*":         {Action: "ask"},
		},
	}}
	cfg := &Config{Permissions: &Permissions{AllowedTools: []string{"write"}}}
	cfg.applyOpenCodeContext(imp, t.TempDir())

	require.Equal(t, []string{"write"}, cfg.Permissions.AllowedTools)
	require.Equal(t, []string{"edit", "multiedit", "lsp_replace_symbol"}, cfg.Permissions.ProjectAllowedTools,
		"tools already allowed everywhere are left alone")
	require.Equal(t, cfg.Permissions.ProjectAllowedTools, imp.applied.ProjectAllowedTools)
	require.Equal(t, []string{"fetch", "agentic_fetch", "download"}, cfg.Options.DisabledTools)
	require.Equal(t, []string{
		"permission rule *: ask partly followed: Ta-Da prompts before writes, commands, web access and MCP tools, not before reads inside the project",
		"permission rules Ta-Da already follows: lsp, read, todowrite, websearch",
		"permission rules not imported (Ta-Da keeps prompting): bash, external",
		"permission rules not imported (Ta-Da doesn't prompt for these inside the project): grep",
	}, imp.applied.Notes)
}

func TestApplyOpenCodeDenyEdit(t *testing.T) {
	t.Parallel()

	imp := &openCodeImport{settings: allOpenCodeContext, config: &opencode.Config{
		Permission: map[string]opencode.Permission{"edit": {Action: "deny"}},
		Tools:      map[string]bool{"write": false},
	}}
	cfg := &Config{}
	cfg.applyOpenCodeContext(imp, t.TempDir())

	require.Equal(t, []string{"write", "edit", "multiedit", "lsp_rename", "lsp_replace_symbol"}, cfg.Options.DisabledTools)
	require.Equal(t, cfg.Options.DisabledTools, imp.applied.DisabledTools)
	require.Nil(t, cfg.Permissions)
}

func TestApplyOpenCodeCommands(t *testing.T) {
	t.Parallel()

	imp := &openCodeImport{settings: allOpenCodeContext, config: &opencode.Config{
		Commands: map[string]opencode.Command{
			"review":   {Name: "review", Template: "Review $ARGUMENTS", Description: "Review code"},
			"blank":    {Name: "blank", Template: "  \n"},
			"git/push": {Name: "git/push", Template: "Push it", Agent: "build", Subtask: true},
		},
	}}
	cfg := &Config{}
	cfg.applyOpenCodeContext(imp, t.TempDir())

	require.Equal(t, []OpenCodeCommand{
		{Name: "git/push", Template: "Push it"},
		{Name: "review", Description: "Review code", Template: "Review $ARGUMENTS"},
	}, cfg.OpenCodeCommands)
	require.Equal(t, []string{"git/push", "review"}, imp.applied.Commands)
	require.Equal(t, []string{
		"commands run in the current session; their agent, model and subtask settings are ignored: git/push",
	}, imp.applied.Notes)
}

func TestApplyOpenCodeContextOff(t *testing.T) {
	t.Parallel()

	f := false
	oc := &opencode.Config{
		Instructions:   []string{"/elsewhere/rules.md"},
		SkillPaths:     []string{"/elsewhere/skills"},
		Tools:          map[string]bool{"bash": false},
		Permission:     map[string]opencode.Permission{"edit": {Action: "allow"}},
		Commands:       map[string]opencode.Command{"x": {Template: "x"}},
		Plugins:        []string{"dcp"},
		CompactionAuto: &f,
	}
	imp := &openCodeImport{settings: openCodeSettings{enabled: true}, config: oc}
	cfg := &Config{OpenCodeCommands: []OpenCodeCommand{{Name: "stale", Template: "x"}}}
	cfg.applyOpenCodeContext(imp, t.TempDir())

	require.Empty(t, cfg.Options.ContextPaths)
	require.Empty(t, cfg.Options.GlobalContextPaths)
	require.Empty(t, cfg.Options.SkillsPaths)
	require.Empty(t, cfg.Options.DisabledTools)
	require.False(t, cfg.Options.DisableAutoSummarize)
	require.Nil(t, cfg.Permissions)
	require.Nil(t, cfg.OpenCodeCommands, "commands from an earlier load are cleared")
	require.Equal(t, []string{"plugins are never run: dcp"}, imp.applied.Notes)

	cfg = &Config{OpenCodeCommands: []OpenCodeCommand{{Name: "stale", Template: "x"}}}
	cfg.applyOpenCodeContext(nil, t.TempDir())
	require.Nil(t, cfg.OpenCodeCommands)
}

func TestApplyOpenCodeContextResetsApplied(t *testing.T) {
	t.Parallel()

	imp := &openCodeImport{settings: allOpenCodeContext, config: &opencode.Config{
		Tools: map[string]bool{"bash": false},
	}}
	// A reload applies the same import to a freshly loaded config.
	(&Config{}).applyOpenCodeContext(imp, t.TempDir())
	(&Config{}).applyOpenCodeContext(imp, t.TempDir())
	require.Equal(t, []string{"bash"}, imp.applied.DisabledTools, "a reload doesn't report a tool twice")
}

func newProviders(ids ...string) *csync.Map[string, ProviderConfig] {
	providers := csync.NewMap[string, ProviderConfig]()
	for _, id := range ids {
		providers.Set(id, ProviderConfig{ID: id, Models: []catwalk.Model{{ID: id + "-model"}}})
	}
	return providers
}

func TestApplyOpenCodeProviderFilter(t *testing.T) {
	t.Parallel()

	imp := &openCodeImport{
		settings:     openCodeSettings{enabled: true, providers: true},
		ownProviders: map[string]bool{"mine": true},
		config: &opencode.Config{
			EnabledProviders:  []string{"anthropic", "google", "openrouter"},
			DisabledProviders: []string{"openrouter"},
		},
	}
	cfg := &Config{
		Providers: newProviders("anthropic", "gemini", "openai", "openrouter", "mine", "busy"),
		Models: map[SelectedModelType]SelectedModel{
			SelectedModelTypeLarge: {Provider: "busy", Model: "busy-model"},
		},
	}
	cfg.applyOpenCodeProviderFilter(imp, nil)

	require.Equal(t, []string{"openai", "openrouter"}, imp.applied.DisabledProviders,
		"google is gemini in Ta-Da; disabled_providers wins over enabled_providers")
	for id, want := range map[string]bool{
		"anthropic": false, "gemini": false, "openai": true, "openrouter": true,
		"mine": false, "busy": false,
	} {
		pc, _ := cfg.Providers.Get(id)
		require.Equal(t, want, pc.Disable, id)
	}

	imp.settings.providers = false
	cfg = &Config{Providers: newProviders("openai")}
	cfg.applyOpenCodeProviderFilter(imp, nil)
	require.Empty(t, imp.applied.DisabledProviders)
	pc, _ := cfg.Providers.Get("openai")
	require.False(t, pc.Disable)
}

func TestApplyOpenCodeProviderFilterKnownProviders(t *testing.T) {
	t.Setenv("TADA_TEST_FILTER_KEY", "set")
	t.Setenv("TADA_TEST_UNSET_KEY", "")

	imp := &openCodeImport{
		settings: openCodeSettings{enabled: true, providers: true},
		config:   &opencode.Config{EnabledProviders: []string{}},
	}
	known := []catwalk.Provider{
		{ID: "keyed", APIKey: "${TADA_TEST_FILTER_KEY}"},
		{ID: "unkeyed", APIKey: "$TADA_TEST_UNSET_KEY"},
	}
	cfg := &Config{Providers: csync.NewMap[string, ProviderConfig]()}
	cfg.applyOpenCodeProviderFilter(imp, known)

	require.Equal(t, []string{"keyed"}, imp.applied.DisabledProviders, "an empty enabled_providers allows none")
	pc, ok := cfg.Providers.Get("keyed")
	require.True(t, ok)
	require.Equal(t, "keyed", pc.ID)
	require.True(t, pc.Disable)
	_, ok = cfg.Providers.Get("unkeyed")
	require.False(t, ok, "providers without a key are not configured anyway")
}

func TestApplyOpenCodeModelPreferenceAgent(t *testing.T) {
	t.Parallel()

	providers := newProviders("anthropic", "local")
	oc := &opencode.Config{
		Model:      "anthropic/anthropic-model",
		SmallModel: "local/small",
		Providers: map[string]opencode.Provider{
			"local": {Models: map[string]opencode.Model{"small": {ModelID: "local-model"}}},
		},
		Agents: map[string]opencode.Agent{
			"build": {Name: "build", Model: "local/local-model"},
			"fast":  {Name: "fast", Model: "nowhere/x"},
		},
	}

	imp := &openCodeImport{settings: openCodeModelChoice, config: oc}
	cfg := &Config{Providers: providers}
	cfg.applyOpenCodeModelPreference(imp)
	require.Equal(t, SelectedModel{Provider: "local", Model: "local-model"}, cfg.Models[SelectedModelTypeLarge],
		"the default agent's model comes first")
	require.Equal(t, SelectedModel{Provider: "local", Model: "local-model"}, cfg.Models[SelectedModelTypeSmall],
		"custom models are found by their config key")
	require.Equal(t, []OpenCodeModelChoice{
		{Type: SelectedModelTypeLarge, Ref: "local/local-model", Source: "agent build", Result: "selected"},
		{Type: SelectedModelTypeSmall, Ref: "local/small", Source: "small_model", Result: "selected"},
	}, imp.applied.Models)

	oc.DefaultAgent = "fast"
	imp = &openCodeImport{settings: openCodeModelChoice, config: oc}
	cfg = &Config{Providers: providers}
	cfg.applyOpenCodeModelPreference(imp)
	require.Equal(t, SelectedModel{Provider: "anthropic", Model: "anthropic-model"}, cfg.Models[SelectedModelTypeLarge])
	require.Equal(t, OpenCodeModelChoice{
		Type: SelectedModelTypeLarge, Ref: "nowhere/x", Source: "agent fast",
		Result: "not used: provider nowhere is not configured",
	}, imp.applied.Models[0])
	require.Equal(t, "selected", imp.applied.Models[1].Result)
}

func TestApplyOpenCodeModelPreferenceReasons(t *testing.T) {
	t.Parallel()

	providers := newProviders("anthropic", "off")
	pc, _ := providers.Get("off")
	pc.Disable = true
	providers.Set("off", pc)

	for ref, want := range map[string]string{
		"no-slash":                  "not used: not a provider/model reference",
		"anthropic/":                "not used: not a provider/model reference",
		"off/off-model":             "not used: provider off is disabled",
		"anthropic/missing":         "not used: model missing is not available from anthropic",
		"anthropic/anthropic-model": "selected",
	} {
		imp := &openCodeImport{settings: openCodeModelChoice, config: &opencode.Config{Model: ref}}
		cfg := &Config{Providers: providers}
		cfg.applyOpenCodeModelPreference(imp)
		require.Len(t, imp.applied.Models, 1, ref)
		require.Equal(t, want, imp.applied.Models[0].Result, ref)
	}

	imp := &openCodeImport{settings: openCodeModelChoice, config: &opencode.Config{Model: "anthropic/anthropic-model"}}
	cfg := &Config{Providers: providers, Models: map[SelectedModelType]SelectedModel{
		SelectedModelTypeLarge: {Provider: "off", Model: "off-model"},
	}}
	cfg.applyOpenCodeModelPreference(imp)
	require.Equal(t, "not used: Ta-Da already selects off/off-model", imp.applied.Models[0].Result)
}

func TestApplyOpenCodeModelPreferenceProvidersOff(t *testing.T) {
	t.Parallel()

	imp := &openCodeImport{
		settings: openCodeSettings{enabled: true},
		config:   &opencode.Config{Model: "anthropic/anthropic-model", SmallModel: "anthropic/anthropic-model"},
		applied:  OpenCodeApplied{Models: []OpenCodeModelChoice{{Ref: "stale"}}},
	}
	cfg := &Config{Providers: newProviders("anthropic")}
	cfg.applyOpenCodeModelPreference(imp)
	require.Empty(t, cfg.Models)
	require.Empty(t, imp.applied.Models)
}
