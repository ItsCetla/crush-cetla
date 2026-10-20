package opencode

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}

func TestLoadConfigInstructions(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	home := t.TempDir()
	abs := filepath.Join(home, "abs", "RULE.md")
	writeFile(t, abs, "abs")
	writeFile(t, filepath.Join(home, "home-rules.md"), "home")
	writeFile(t, filepath.Join(l.repo, "RULES.md"), "repo")
	writeFile(t, filepath.Join(l.sub, "RULES.md"), "sub")
	writeFile(t, filepath.Join(l.repo, "docs", "b.md"), "b")
	writeFile(t, filepath.Join(l.repo, "docs", "a.md"), "a")
	writeFile(t, filepath.Join(l.sub, "docs", "c.md"), "c")
	writeFile(t, filepath.Join(l.global, "RULES.md"), "global")
	writeFile(t, filepath.Join(l.global, "opencode.json"), `{"instructions": [
		"RULES.md",
		"docs/*.md",
		"~/home-rules.md",
		"https://example.com/rules.md",
		`+jsonString(t, abs)+`,
		"missing.md"
	]}`)

	t.Run("project", func(t *testing.T) {
		t.Parallel()
		opts := l.opts(nil)
		opts.Home = home
		cfg, err := LoadConfig(opts)
		require.NoError(t, err)
		require.Equal(t, []string{
			filepath.Join(l.sub, "RULES.md"),
			filepath.Join(l.repo, "RULES.md"),
			filepath.Join(l.sub, "docs", "c.md"),
			filepath.Join(l.repo, "docs", "a.md"),
			filepath.Join(l.repo, "docs", "b.md"),
			filepath.Join(home, "home-rules.md"),
			abs,
		}, cfg.Instructions)
		require.Equal(t, []string{
			"instructions: skipped https://example.com/rules.md (remote instructions are never fetched)",
		}, cfg.Warnings)
	})

	t.Run("project excluded matches in the config dir", func(t *testing.T) {
		t.Parallel()
		opts := l.opts(nil)
		opts.Home = home
		opts.IncludeProject = false
		cfg, err := LoadConfig(opts)
		require.NoError(t, err)
		require.Equal(t, []string{
			filepath.Join(l.global, "RULES.md"),
			filepath.Join(home, "home-rules.md"),
			abs,
		}, cfg.Instructions)
	})

	t.Run("project disabled by env", func(t *testing.T) {
		t.Parallel()
		opts := l.opts(map[string]string{"OPENCODE_DISABLE_PROJECT_CONFIG": "1"})
		cfg, err := LoadConfig(opts)
		require.NoError(t, err)
		// Without a home, "~/home-rules.md" stays relative and matches
		// nothing.
		require.Equal(t, []string{filepath.Join(l.global, "RULES.md"), abs}, cfg.Instructions)
	})
}

func TestLoadConfigGlobalRules(t *testing.T) {
	t.Parallel()

	claude := func(home string) string {
		return filepath.Join(home, ".claude", "CLAUDE.md")
	}

	t.Run("AGENTS.md wins", func(t *testing.T) {
		t.Parallel()
		l := newLayout(t)
		home := t.TempDir()
		writeFile(t, filepath.Join(l.global, "AGENTS.md"), "rules")
		writeFile(t, claude(home), "claude")
		opts := l.opts(nil)
		opts.Home = home
		cfg, err := LoadConfig(opts)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(l.global, "AGENTS.md"), cfg.GlobalRules)
	})

	t.Run("CLAUDE.md fallback", func(t *testing.T) {
		t.Parallel()
		l := newLayout(t)
		home := t.TempDir()
		writeFile(t, claude(home), "claude")
		opts := l.opts(nil)
		opts.Home = home
		cfg, err := LoadConfig(opts)
		require.NoError(t, err)
		require.Equal(t, claude(home), cfg.GlobalRules)
	})

	for _, key := range []string{"OPENCODE_DISABLE_CLAUDE_CODE", "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			l := newLayout(t)
			home := t.TempDir()
			writeFile(t, claude(home), "claude")
			opts := l.opts(map[string]string{key: "true"})
			opts.Home = home
			cfg, err := LoadConfig(opts)
			require.NoError(t, err)
			require.Empty(t, cfg.GlobalRules)
		})
	}
}

func TestLoadConfigSkills(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	home := t.TempDir()
	for _, dir := range []string{
		filepath.Join(l.global, "skills", "one"),
		filepath.Join(home, ".opencode", "skill", "two"),
		filepath.Join(l.repo, ".opencode", "skills", "three"),
		filepath.Join(l.sub, "my-skills", "four"),
		filepath.Join(home, "elsewhere", "five"),
	} {
		writeFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: x\n---\n")
	}
	writeFile(t, filepath.Join(l.global, "opencode.json"), `{"skills": {
		"paths": ["my-skills", "~/elsewhere", "missing", "my-skills"],
		"urls": ["https://example.com/skills"]
	}}`)

	opts := l.opts(nil)
	opts.Home = home
	cfg, err := LoadConfig(opts)
	require.NoError(t, err)
	require.Equal(t, []string{
		filepath.Join(l.global, "skills"),
		filepath.Join(home, ".opencode", "skill"),
		filepath.Join(l.repo, ".opencode", "skills"),
		filepath.Join(l.sub, "my-skills"),
		filepath.Join(home, "elsewhere"),
	}, cfg.SkillPaths)
	require.Equal(t, []string{
		filepath.Join(l.global, "opencode.json") + ": skills.urls: skipped https://example.com/skills (remote skills are never fetched)",
	}, cfg.Warnings)

	opts.IncludeProject = false
	cfg, err = LoadConfig(opts)
	require.NoError(t, err)
	require.Equal(t, []string{
		filepath.Join(l.global, "skills"),
		filepath.Join(home, ".opencode", "skill"),
		filepath.Join(l.sub, "my-skills"),
		filepath.Join(home, "elsewhere"),
	}, cfg.SkillPaths)
}

func TestLoadConfigCommands(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	writeFile(t, filepath.Join(l.global, "opencode.json"), `{"command": {
		"review": {
			"template": "Review $ARGUMENTS",
			"description": "json review",
			"agent": "plan",
			"model": "anthropic/claude#high",
			"subtask": true
		},
		"test": {"template": "Run tests for $1", "model": "nope"},
		"broken": "not an object"
	}}`)
	writeFile(t, filepath.Join(l.global, "commands", "review.md"),
		"---\ndescription: md review\n---\n\nReview the diff for $1.\n")
	writeFile(t, filepath.Join(l.global, "command", "git", "commit.md"),
		"---\nmodel: openai/gpt\nsubtask: true\n---\nCommit it.")
	writeFile(t, filepath.Join(l.global, "commands", "plain.md"), "Just a body.\n")
	writeFile(t, filepath.Join(l.global, "commands", "bad.md"), "---\ndescription: x\n")
	writeFile(t, filepath.Join(l.repo, ".opencode", "commands", "local.md"), "Project command.")

	cfg, err := LoadConfig(l.opts(nil))
	require.NoError(t, err)

	reviewFile := filepath.Join(l.global, "commands", "review.md")
	require.Equal(t, Command{
		Name:        "review",
		Template:    "Review the diff for $1.",
		Description: "md review",
		Agent:       "plan",
		Model:       "anthropic/claude",
		Subtask:     true,
		SourcePath:  reviewFile,
	}, cfg.Commands["review"])
	require.Equal(t, Command{
		Name:       "test",
		Template:   "Run tests for $1",
		SourcePath: filepath.Join(l.global, "opencode.json"),
	}, cfg.Commands["test"])
	require.Equal(t, Command{
		Name:       "git/commit",
		Template:   "Commit it.",
		Model:      "openai/gpt",
		Subtask:    true,
		SourcePath: filepath.Join(l.global, "command", "git", "commit.md"),
	}, cfg.Commands["git/commit"])
	require.Equal(t, "Just a body.", cfg.Commands["plain"].Template)
	require.Equal(t, "Project command.", cfg.Commands["local"].Template)
	require.NotContains(t, cfg.Commands, "broken")
	require.NotContains(t, cfg.Commands, "bad")
	require.Contains(t, cfg.ExtraFiles, reviewFile)

	var badWarning bool
	for _, w := range cfg.Warnings {
		if w == filepath.Join(l.global, "commands", "bad.md")+": unclosed frontmatter" {
			badWarning = true
		}
	}
	require.True(t, badWarning, "warnings: %v", cfg.Warnings)

	opts := l.opts(nil)
	opts.IncludeProject = false
	cfg, err = LoadConfig(opts)
	require.NoError(t, err)
	require.NotContains(t, cfg.Commands, "local")
}

func TestLoadConfigAgents(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	global := filepath.Join(l.global, "opencode.json")
	writeFile(t, global, `{
		"default_agent": "fast",
		"agent": {
			"build": {"model": "a/b", "tools": {"bash": false, "x": "no"}},
			"fast": {"model": "media/qwen", "mode": "primary", "disable": false},
			"bad": {"model": "nomodel"}
		},
		"mode": {"plan": {"description": "plans", "mode": "subagent"}}
	}`)
	reviewer := filepath.Join(l.global, "agents", "reviewer.md")
	writeFile(t, reviewer, `---
description: Reviews code
mode: subagent
model: c/d
tools:
  write: false
  edit: nope
---
You review code.
`)
	writeFile(t, filepath.Join(l.global, "agent", "build.md"), "---\ntools:\n  edit: false\n---\n")
	writeFile(t, filepath.Join(l.global, "modes", "focus.md"), "---\nmode: subagent\n---\n")

	cfg, err := LoadConfig(l.opts(nil))
	require.NoError(t, err)

	require.Equal(t, "fast", cfg.DefaultAgent)
	require.Equal(t, Agent{
		Name:       "build",
		Model:      "a/b",
		Tools:      map[string]bool{"bash": false, "edit": false},
		SourcePath: filepath.Join(l.global, "agent", "build.md"),
	}, cfg.Agents["build"])
	require.Equal(t, Agent{
		Name:       "fast",
		Mode:       "primary",
		Model:      "media/qwen",
		SourcePath: global,
	}, cfg.Agents["fast"])
	require.Equal(t, Agent{
		Name:        "plan",
		Description: "plans",
		Mode:        "primary",
		SourcePath:  global,
	}, cfg.Agents["plan"])
	require.Equal(t, Agent{
		Name:        "reviewer",
		Description: "Reviews code",
		Mode:        "subagent",
		Model:       "c/d",
		Tools:       map[string]bool{"write": false},
		SourcePath:  reviewer,
	}, cfg.Agents["reviewer"])
	require.Equal(t, "primary", cfg.Agents["focus"].Mode)
	require.Empty(t, cfg.Agents["bad"].Model)

	require.ElementsMatch(t, []string{
		global + ": agent.bad.model: invalid model reference",
		global + ": agent.build.tools.x: not a boolean",
		reviewer + ": tools.edit: not a boolean",
	}, cfg.Warnings)
}

func TestLoadConfigPermission(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	global := filepath.Join(l.global, "opencode.json")
	writeFile(t, global, `{"permission": {
		"edit": "allow",
		"bash": {"git *": "allow", "*": "ask"},
		"webfetch": "maybe",
		"external_directory": {"/tmp/*": "allow", "/etc/*": "sometimes"}
	}}`)
	writeFile(t, filepath.Join(l.sub, "opencode.json"), `{"permission": {
		"bash": {"rm *": "deny"},
		"edit": {"*.lock": "deny"}
	}}`)

	cfg, err := LoadConfig(l.opts(nil))
	require.NoError(t, err)
	require.Equal(t, map[string]Permission{
		"edit": {Patterns: map[string]string{"*.lock": "deny"}},
		"bash": {Patterns: map[string]string{
			"git *": "allow",
			"*":     "ask",
			"rm *":  "deny",
		}},
		"external_directory": {Patterns: map[string]string{"/tmp/*": "allow"}},
	}, cfg.Permission)
	require.ElementsMatch(t, []string{
		global + `: permission.external_directory./etc/*: invalid action "sometimes"`,
		global + `: permission.webfetch: invalid action "maybe"`,
	}, cfg.Warnings)

	writeFile(t, filepath.Join(l.repo, ".opencode", "opencode.json"), `{"permission": "ask"}`)
	cfg, err = LoadConfig(l.opts(nil))
	require.NoError(t, err)
	require.Equal(t, map[string]Permission{"*": {Action: "ask"}}, cfg.Permission)
}

func TestLoadConfigToolsPluginsCompaction(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	global := filepath.Join(l.global, "opencode.json")
	writeFile(t, global, `{
		"tools": {"bash": false, "github_*": false, "x": 1},
		"plugin": ["opencode-foo@1.0.0", "file:///plugins/bar.js"],
		"compaction": {"auto": false}
	}`)
	writeFile(t, filepath.Join(l.sub, "opencode.json"), `{
		"tools": {"bash": true},
		"plugin": ["opencode-foo@1.0.0"]
	}`)
	writeFile(t, filepath.Join(l.global, "plugins", "notify.js"), "")
	writeFile(t, filepath.Join(l.global, "plugin", "status.ts"), "")
	writeFile(t, filepath.Join(l.global, "plugins", "README.md"), "")

	cfg, err := LoadConfig(l.opts(nil))
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"bash": true, "github_*": false}, cfg.Tools)
	require.Equal(t, []string{
		"opencode-foo@1.0.0",
		"file:///plugins/bar.js",
		filepath.Join(l.global, "plugin", "status.ts"),
		filepath.Join(l.global, "plugins", "notify.js"),
	}, cfg.Plugins)
	require.NotNil(t, cfg.CompactionAuto)
	require.False(t, *cfg.CompactionAuto)
	require.Equal(t, []string{global + ": tools.x: not a boolean"}, cfg.Warnings)
}

// Plugins may come as [spec, options] pairs, and an entry that can't be
// read still counts as a plugin.
func TestLoadConfigPluginForms(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	global := filepath.Join(l.global, "opencode.json")
	writeFile(t, global, `{
		"plugin": ["a@1", ["@cortexkit/opencode-magic-context@latest", {"x": 1}], 7]
	}`)

	cfg, err := LoadConfig(l.opts(nil))
	require.NoError(t, err)
	require.Equal(t, []string{"a@1", "@cortexkit/opencode-magic-context@latest", "7"}, cfg.Plugins)
	require.Equal(t, []string{global + ": plugin: unreadable entry 7"}, cfg.Warnings)
}

func TestLoadConfigConfigDirs(t *testing.T) {
	t.Parallel()

	l := newLayout(t)
	home := t.TempDir()
	writeFile(t, filepath.Join(l.global, "opencode.json"), `{}`)
	writeFile(t, filepath.Join(home, ".opencode", "agents", "a.md"), "body")
	writeFile(t, filepath.Join(l.repo, ".opencode", "commands", "c.md"), "body")

	opts := l.opts(nil)
	opts.Home = home
	cfg, err := LoadConfig(opts)
	require.NoError(t, err)
	require.Equal(t, []string{
		l.global,
		filepath.Join(home, ".opencode"),
		filepath.Join(l.repo, ".opencode"),
	}, cfg.ConfigDirs)
	require.Equal(t, []string{
		filepath.Join(home, ".opencode", "agents", "a.md"),
		filepath.Join(l.repo, ".opencode", "commands", "c.md"),
	}, cfg.ExtraFiles)

	// The home directory is often the project boundary; its .opencode
	// directory is scanned once.
	opts.ProjectBoundary = home
	opts.Cwd = home
	cfg, err = LoadConfig(opts)
	require.NoError(t, err)
	require.Equal(t, []string{l.global, filepath.Join(home, ".opencode")}, cfg.ConfigDirs)
}

func TestSplitFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, front, body string
		err                   bool
	}{
		{name: "none", in: "just body\n", body: "just body\n"},
		{name: "empty", in: "", body: ""},
		{name: "basic", in: "---\na: 1\n---\nbody\n", front: "a: 1", body: "body\n"},
		{name: "bom", in: "\uFEFF---\na: 1\n---\nbody", front: "a: 1", body: "body"},
		{name: "crlf", in: "---\r\na: 1\r\nb: 2\r\n---\r\nbody\r\n", front: "a: 1\nb: 2", body: "body\n"},
		{name: "leading blank lines", in: "\n\n---\na: 1\n---\nbody", front: "a: 1", body: "body"},
		{name: "dashes later", in: "body\n---\na: 1\n---\n", body: "body\n---\na: 1\n---\n"},
		{name: "unclosed", in: "---\na: 1\nbody", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			front, body, err := splitFrontmatter(tt.in)
			if tt.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.front, front)
			require.Equal(t, tt.body, body)
		})
	}
}

func TestMatchTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"bash", "bash", true},
		{"bash", "edit", false},
		{"github_*", "github_create_issue", true},
		{"github_*", "gitlab_create_issue", false},
		{"*", "anything", true},
		{"mcp?", "mcp1", true},
		{"[", "[", true},
		{"[", "x", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, MatchTool(tt.pattern, tt.name), "%s ~ %s", tt.pattern, tt.name)
	}
}

func TestMCPToolName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "my_docs_search", MCPToolName("my.docs", "search"))
	require.Equal(t, "my_docs_", MCPToolName("my.docs", ""))
	require.Equal(t, "git-hub_list_issues", MCPToolName("git-hub", "list issues"))
	require.Equal(t, "bench_add_note", MCPToolName("bench", "add_note"))
}
