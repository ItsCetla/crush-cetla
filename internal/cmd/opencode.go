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
	Long: `Show the MCP servers, provider settings and models Crush imports from a
local OpenCode install. Everything is read from OpenCode's files on disk:
its config files, its cached models.dev catalog and, when enabled, the API
keys saved by "opencode auth login". Nothing is fetched.

Project-level OpenCode config (opencode.json and .opencode/ in the project)
is only read for projects you have trusted with "crush trust".

Configure the import with options.opencode in crush.json:

  {"options": {"opencode": {"mcp": true, "providers": true, "models": true, "credentials": false}}}

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
	if st.Model != "" {
		fmt.Fprintf(w, "  model:        %s (used when Crush has no selection)\n", st.Model)
	}
	if st.SmallModel != "" {
		fmt.Fprintf(w, "  small model:  %s\n", st.SmallModel)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Models catalog (%s)\n", onOff(st.ModelsEnabled))
	switch c := st.Catalog; {
	case !st.ModelsEnabled:
	case c == nil && st.CatalogErr != nil:
		fmt.Fprintf(w, "  could not read: %v\n", st.CatalogErr)
	case c == nil:
		fmt.Fprintln(w, "  no cached catalog found (run OpenCode once to create it)")
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

	if len(st.Warnings) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Warnings")
		for _, warn := range st.Warnings {
			fmt.Fprintf(w, "  %s\n", warn)
		}
	}
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
