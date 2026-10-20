package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/crush/internal/artifacts"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/spf13/cobra"
)

var artifactsCmd = &cobra.Command{
	Use:     "artifacts",
	Aliases: []string{"artifact"},
	Short:   "Browse and serve artifacts: pages, documents and slide decks",
	Long: `Artifacts are HTML pages, Markdown documents and slide decks the agent
publishes with the artifact tool. They're stored on this machine and shown
in a local viewer on 127.0.0.1; nothing leaves the machine.

"tada artifacts mcp" serves the same tools to other MCP clients such as
Claude Desktop, Claude Code or Cursor:

  {"mcpServers": {"artifacts": {"command": "tada", "args": ["artifacts", "mcp"]}}}

Settings live in options.artifacts in tada.json:

  {"options": {"artifacts": {"port": 7480, "open_on_create": true,
    "allow_cdn": false, "disabled": false}}}

Artifacts are kept in ` + "`" + `~/.local/share/tada/artifacts` + "`" + ` (set
TADA_ARTIFACTS_DIR to move them).`,
}

func artifactsService(cmd *cobra.Command) (*artifacts.Service, error) {
	svc, err := artifacts.Default()
	if err != nil {
		return nil, err
	}
	opts := config.GlobalArtifactsOptions()
	svc.Server = artifacts.ServerOptions{Port: opts.Port, AllowCDN: opts.AllowCDN}
	svc.OpenOnCreate = *opts.OpenOnCreate
	if f := cmd.Flags().Lookup("port"); f != nil && f.Changed {
		svc.Server.Port, _ = cmd.Flags().GetInt("port")
	}
	if f := cmd.Flags().Lookup("allow-cdn"); f != nil && f.Changed {
		svc.Server.AllowCDN, _ = cmd.Flags().GetBool("allow-cdn")
	}
	if f := cmd.Flags().Lookup("no-open"); f != nil && f.Changed {
		no, _ := cmd.Flags().GetBool("no-open")
		svc.OpenOnCreate = !no
	}
	return svc, nil
}

var artifactsListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List artifacts, most recently updated first",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		svc, err := artifacts.Default()
		if err != nil {
			return err
		}
		query, _ := cmd.Flags().GetString("query")
		limit, _ := cmd.Flags().GetInt("limit")
		list, err := svc.Store.List(artifacts.ListOptions{Query: query, Limit: limit})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(list) == 0 {
			fmt.Fprintln(out, "No artifacts yet.")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTITLE\tKIND\tVERSION\tUPDATED")
		for _, a := range list {
			title := a.Title
			if a.Pinned {
				title = "★ " + title
			}
			fmt.Fprintf(w, "%s\t%s\t%s\tv%d\t%s\n", a.ID, title, a.Kind, a.Version, a.Updated.Local().Format(time.DateTime))
		}
		return w.Flush()
	},
}

var artifactsServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the artifacts viewer in the foreground",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		svc, err := artifactsService(cmd)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		url, done, err := artifacts.Serve(ctx, svc.Store, svc.Server)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Artifacts viewer at %s (Ctrl+C to stop)\nStore: %s\n", url, svc.Store.Root())
		if open, _ := cmd.Flags().GetBool("open"); open {
			_ = artifacts.OpenBrowser(svc.Store.SignInURL(url))
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			return err
		}
	},
}

var artifactsOpenCmd = &cobra.Command{
	Use:   "open [id]",
	Short: "Open the gallery, or one artifact, in the browser",
	Long: `Open the gallery, or one artifact, in the browser. The viewer runs while
this command does; press Ctrl+C to stop it.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := artifactsService(cmd)
		if err != nil {
			return err
		}
		path := "/"
		if len(args) == 1 {
			id, err := artifacts.ParseID(args[0])
			if err != nil {
				return err
			}
			if _, err := svc.Store.Get(id); err != nil {
				return err
			}
			path = artifacts.ViewerPath(id)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		url, done, err := artifacts.Serve(ctx, svc.Store, svc.Server)
		if err != nil {
			// A viewer is already running; just open it.
			base, eerr := svc.BaseURL()
			if eerr != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), base+path)
			return artifacts.OpenBrowser(svc.Store.SignInURL(base + path))
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s (Ctrl+C to stop the viewer)\n", url+path)
		_ = artifacts.OpenBrowser(svc.Store.SignInURL(url + path))
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			return err
		}
	},
}

var artifactsRmCmd = &cobra.Command{
	Use:     "rm <id>...",
	Aliases: []string{"delete"},
	Short:   "Delete artifacts permanently",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := artifacts.Default()
		if err != nil {
			return err
		}
		for _, a := range args {
			id, err := artifacts.ParseID(a)
			if err != nil {
				return err
			}
			m, err := svc.Store.Get(id)
			if err != nil {
				return fmt.Errorf("%s: %w", a, err)
			}
			if err := svc.Store.Delete(id); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s (%s)\n", id, m.Title)
		}
		return nil
	},
}

var artifactsMCPCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the artifact tools over MCP (stdio)",
	Long: `Serve the artifact, artifact_data and artifact_comments tools to an MCP
client over stdio. The viewer starts on first use and stops with this
process; an existing viewer for the same store is reused.

Claude Desktop (claude_desktop_config.json):

  {"mcpServers": {"artifacts": {"command": "tada", "args": ["artifacts", "mcp"]}}}

Claude Code:

  claude mcp add artifacts -- tada artifacts mcp`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		svc, err := artifactsService(cmd)
		if err != nil {
			return err
		}
		return artifacts.RunMCP(cmd.Context(), svc)
	},
}

func init() {
	for _, c := range []*cobra.Command{artifactsServeCmd, artifactsOpenCmd, artifactsMCPCmd} {
		c.Flags().Int("port", artifacts.DefaultPort, "Viewer port on 127.0.0.1")
		c.Flags().Bool("allow-cdn", false, "Let pages load scripts from the allowed CDNs and fonts from Google Fonts")
	}
	artifactsMCPCmd.Flags().Bool("no-open", false, "Don't open new artifacts in the browser")
	artifactsServeCmd.Flags().Bool("open", false, "Open the gallery in the browser")
	artifactsListCmd.Flags().StringP("query", "q", "", "Only artifacts whose title or description contains this")
	artifactsListCmd.Flags().IntP("limit", "n", 0, "The maximum number to list")
	artifactsCmd.AddCommand(artifactsListCmd, artifactsServeCmd, artifactsOpenCmd, artifactsRmCmd, artifactsMCPCmd)
	rootCmd.AddCommand(artifactsCmd)
}
