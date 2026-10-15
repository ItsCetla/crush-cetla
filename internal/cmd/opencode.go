package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/spf13/cobra"
)

var openCodeCmd = &cobra.Command{
	Use:   "opencode",
	Short: "Show what Crush imports from your OpenCode install",
	Long: `Show what Crush imports from a local OpenCode install: MCP servers,
provider settings, model choices, the models catalog, instructions, skills,
commands, tool toggles and permissions. Everything is read from OpenCode's
files on disk: its config files, its cached models.dev catalog and, when
enabled, the API keys saved by "opencode auth login". Nothing is fetched,
and OpenCode plugins are never run.

Project-level OpenCode config (opencode.json and .opencode/ in the project)
is only read for projects you have trusted with "crush trust".

Imported settings live in memory only; Crush never writes them to its own
config. Your crush.json always wins over OpenCode's settings.

Each part can be turned off with options.opencode in crush.json; all are on
except credentials:

  {"options": {"opencode": {"mcp": true, "providers": true, "models": true,
    "credentials": false, "instructions": true, "skills": true,
    "commands": true, "tools": true, "permissions": true,
    "compaction": true}}}

Set CRUSH_DISABLE_OPENCODE=1 or options.opencode.disabled to turn it off.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return err
		}
		dataDir, _ := cmd.Flags().GetString("data-dir")
		debug, _ := cmd.Flags().GetBool("debug")

		store, err := config.Init(cwd, dataDir, debug)
		if err != nil {
			return err
		}
		printOpenCodeStatus(cmd.OutOrStdout(), store.OpenCodeStatus(cmd.Context()))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(openCodeCmd)
}

func printOpenCodeStatus(w io.Writer, st config.OpenCodeStatus) {
	if !st.Enabled {
		fmt.Fprintln(w, "OpenCode import is off (options.opencode.disabled or CRUSH_DISABLE_OPENCODE).")
		return
	}

	fmt.Fprintln(w, "OpenCode directories")
	fmt.Fprintf(w, "  config  %s\n", home.Short(st.Paths.ConfigDir))
	fmt.Fprintf(w, "  data    %s\n", home.Short(st.Paths.DataDir))
	fmt.Fprintf(w, "  cache   %s\n", home.Short(st.Paths.CacheDir))

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Config files read")
	if len(st.Files) == 0 {
		fmt.Fprintln(w, "  none found")
	}
	for _, f := range st.Files {
		fmt.Fprintf(w, "  %s\n", home.Short(f))
	}
	if st.ProjectIncluded {
		fmt.Fprintln(w, "  project-level OpenCode config: included (project is trusted)")
	} else {
		fmt.Fprintln(w, "  project-level OpenCode config: skipped (project not trusted; see `crush trust`)")
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "MCP servers (%s)\n", onOff(st.MCPEnabled))
	if len(st.MCP) == 0 {
		fmt.Fprintln(w, "  none")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, m := range st.MCP {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", m.Name, m.Type, truncate(m.Target, 60), m.State)
		}
		tw.Flush()
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Provider settings (%s)\n", onOff(st.ProvidersEnabled))
	if len(st.Providers) == 0 {
		fmt.Fprintln(w, "  none")
	} else {
		fmt.Fprintf(w, "  %s\n", strings.Join(st.Providers, ", "))
	}
	for _, p := range st.Applied.DisabledProviders {
		fmt.Fprintf(w, "  disabled %s (enabled_providers / disabled_providers)\n", p)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Model choice (%s)\n", onOff(st.ProvidersEnabled))
	switch {
	case len(st.Applied.Models) > 0:
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, m := range st.Applied.Models {
			fmt.Fprintf(tw, "  %s\t%s\t(%s)\t%s\n", m.Type, m.Ref, m.Source, m.Result)
		}
		tw.Flush()
	case st.Model == "" && st.SmallModel == "":
		fmt.Fprintln(w, "  none set in OpenCode")
	default:
		fmt.Fprintln(w, "  not applied")
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Models catalog (%s)\n", onOff(st.ModelsEnabled))
	switch c := st.Catalog; {
	case !st.ModelsEnabled:
	case c == nil && st.CatalogErr != nil:
		fmt.Fprintf(w, "  could not read: %v\n", st.CatalogErr)
	case c == nil:
		fmt.Fprintln(w, "  no cached catalog found; run OpenCode once to create it, or set")
		fmt.Fprintln(w, "  OPENCODE_MODELS_PATH to a models.dev api.json file. Looked in:")
		for _, loc := range st.CatalogLocations {
			fmt.Fprintf(w, "    %s\n", home.Short(loc))
		}
	default:
		updated := "unknown"
		if !c.UpdatedAt.IsZero() {
			updated = c.UpdatedAt.Local().Format(time.DateTime)
		}
		fmt.Fprintf(w, "  source   %s (%s), updated %s\n", c.Source, home.Short(c.Path), updated)
		fmt.Fprintf(w, "  cached   %d providers, %d models; %d usable by Crush\n", c.Providers, c.Models, c.Usable)
		fmt.Fprintf(w, "  in Crush %d extra models for known providers, %d new providers\n", c.AddedModels, c.Listed)
		fmt.Fprintln(w, "           (other providers appear once their API key env var is set)")
		if st.CatalogErr != nil {
			fmt.Fprintf(w, "  warning  %v\n", st.CatalogErr)
		}
	}

	fmt.Fprintln(w)
	if st.CredentialsEnabled {
		fmt.Fprintf(w, "Credentials (on): %d API keys from `opencode auth login` in use\n", st.Credentials)
	} else {
		fmt.Fprintln(w, "Credentials (off): set options.opencode.credentials to use API keys from `opencode auth login`")
	}

	printOpenCodeApplied(w, st)

	if len(st.Warnings) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Warnings")
		for _, warn := range st.Warnings {
			fmt.Fprintf(w, "  %s\n", warn)
		}
	}
}

func printOpenCodeApplied(w io.Writer, st config.OpenCodeStatus) {
	a := st.Applied

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Instructions (%s)\n", onOff(st.InstructionsEnabled))
	printList(w, "project", a.ContextPaths, true)
	printList(w, "global", a.GlobalContextPaths, true)
	if len(a.ContextPaths)+len(a.GlobalContextPaths) == 0 {
		fmt.Fprintln(w, "  none added")
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Skills (%s)\n", onOff(st.SkillsEnabled))
	printList(w, "paths", a.SkillsPaths, true)
	if len(a.SkillsPaths) == 0 {
		fmt.Fprintln(w, "  none added")
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Commands (%s)\n", onOff(st.CommandsEnabled))
	if len(a.Commands) == 0 {
		fmt.Fprintln(w, "  none")
	} else {
		fmt.Fprintf(w, "  %s\n", strings.Join(prefixed("opencode:", a.Commands), ", "))
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Tools (%s) and permissions (%s)\n", onOff(st.ToolsEnabled), onOff(st.PermissionsEnabled))
	printList(w, "disabled tools", a.DisabledTools, false)
	printList(w, "disabled MCP servers", a.DisabledMCP, false)
	printList(w, "disabled MCP tools", a.DisabledMCPTools, false)
	printList(w, "no prompt for project files", a.ProjectAllowedTools, false)
	if len(a.DisabledTools)+len(a.DisabledMCP)+len(a.DisabledMCPTools)+len(a.ProjectAllowedTools) == 0 {
		fmt.Fprintln(w, "  no changes")
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Compaction (%s)\n", onOff(st.CompactionEnabled))
	if a.AutoSummarizeOff {
		fmt.Fprintln(w, "  auto-summarize turned off (compaction.auto is false)")
	} else {
		fmt.Fprintln(w, "  no changes")
	}

	if len(a.Notes) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Notes")
		for _, n := range a.Notes {
			fmt.Fprintf(w, "  %s\n", n)
		}
	}
}

// printList prints a labeled list, one path per line when paths is set and
// comma-separated otherwise. It prints nothing for an empty list.
func printList(w io.Writer, label string, items []string, paths bool) {
	if len(items) == 0 {
		return
	}
	if !paths {
		fmt.Fprintf(w, "  %s: %s\n", label, strings.Join(items, ", "))
		return
	}
	fmt.Fprintf(w, "  %s:\n", label)
	for _, item := range items {
		fmt.Fprintf(w, "    %s\n", home.Short(item))
	}
}

func prefixed(prefix string, items []string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = prefix + strings.ReplaceAll(item, "/", ":")
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
