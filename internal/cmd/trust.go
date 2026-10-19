package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/spf13/cobra"
)

var trustCmd = &cobra.Command{
	Use:   "trust [path]",
	Short: "Allow Ta-Da to load a project's config",
	Long: `Allow Ta-Da to load the project-level config of a project.

Project-level config is every tada.json, .tada.json, tadarc and .tadarc
between the directory and the project root, plus the workspace config
(.tada/tada.json). It can run commands on your machine: tadarc is a shell
script, tada.json values are shell-expanded, and both can start MCP and LSP
servers, add hooks, auto-approve tools or redirect provider requests. Ta-Da
ignores it until you trust the project.

Trusting records a fingerprint of those files. When any of them changes,
appears or disappears, Ta-Da ignores the project config again until you
review it and run tada trust once more.

Directories listed in options.trusted_paths of your global config, and runs
with TADA_TRUST_PROJECT=1, are trusted without a record.`,
	Example: `
# Trust the project in the current directory
tada trust

# Trust another project
tada trust ~/code/project

# Stop trusting the current project
tada trust --revoke

# List trusted projects
tada trust --list
  `,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		list, _ := cmd.Flags().GetBool("list")
		revoke, _ := cmd.Flags().GetBool("revoke")
		dataDir, _ := cmd.Flags().GetString("data-dir")

		if list {
			return listTrustedProjects(cmd.OutOrStdout())
		}

		var dir string
		if len(args) == 1 {
			dir = args[0]
		} else {
			cwd, err := ResolveCwd(cmd)
			if err != nil {
				return err
			}
			dir = cwd
		}
		return runTrust(cmd.Context(), cmd.OutOrStdout(), dir, dataDir, revoke)
	},
}

func init() {
	trustCmd.Flags().Bool("revoke", false, "Stop trusting the project")
	trustCmd.Flags().Bool("list", false, "List trusted projects")
	trustCmd.MarkFlagsMutuallyExclusive("revoke", "list")
	rootCmd.AddCommand(trustCmd)
}

// runTrust trusts (or, with revoke, untrusts) the project containing dir.
func runTrust(ctx context.Context, out io.Writer, dir, dataDir string, revoke bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	dir, err := filepath.Abs(home.Long(dir))
	if err != nil {
		return err
	}
	if info, err := os.Stat(dir); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}

	if revoke {
		root, removed, err := config.RevokeProjectTrust(dir)
		if err != nil {
			return err
		}
		if removed {
			fmt.Fprintf(out, "Revoked trust for %s.\n", root)
		} else {
			fmt.Fprintf(out, "%s was not trusted.\n", root)
		}
		return nil
	}

	trust, err := config.TrustProject(ctx, dir, dataDir)
	if err != nil {
		return err
	}
	if len(trust.Files) == 0 {
		fmt.Fprintf(out, "No project config found in %s; nothing to trust.\n", trust.Root)
		return nil
	}
	fmt.Fprintf(out, "Project: %s\n", trust.Root)
	fmt.Fprintln(out, "Config files:")
	for _, f := range trust.DisplayFiles() {
		fmt.Fprintf(out, "  %s\n", f)
	}
	fmt.Fprintln(out, "Review these files: they can run commands on your machine whenever Ta-Da starts here.")
	fmt.Fprintln(out, "Trusted. Ta-Da ignores them again if any of them changes; run tada trust again after reviewing the change.")
	return nil
}

func listTrustedProjects(out io.Writer) error {
	projects, err := config.TrustedProjects()
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Fprintln(out, "No trusted projects.")
		return nil
	}
	for _, p := range projects {
		fmt.Fprintf(out, "%s\t%s\t%s\n", p.Root, p.TrustedAt.Local().Format(time.RFC3339), strings.Join(p.Files, ", "))
	}
	return nil
}

// trustNoticeTTL keeps the untrusted-config warning on screen long enough
// to be read at startup.
const trustNoticeTTL = 20 * time.Second

// projectTrustNotice returns the warning for project config skipped by the
// trust gate, or the empty string.
func projectTrustNotice(ws workspace.Workspace) string {
	switch w := ws.(type) {
	case *workspace.AppWorkspace:
		return w.Store().ProjectTrust().Notice()
	case *workspace.ClientWorkspace:
		return w.TrustNotice()
	}
	return ""
}

// sendTrustNotice shows the untrusted-config warning in the TUI status bar.
func sendTrustNotice(program *tea.Program, ws workspace.Workspace) {
	notice := projectTrustNotice(ws)
	if notice == "" {
		return
	}
	go program.Send(util.InfoMsg{Type: util.InfoTypeWarn, Msg: notice, TTL: trustNoticeTTL})
}

// printTrustNotice writes the untrusted-config warning to stderr as a single
// line, for non-interactive runs.
func printTrustNotice(notice string) {
	if notice == "" {
		return
	}
	fmt.Fprintln(os.Stderr, "Warning: "+notice)
}
