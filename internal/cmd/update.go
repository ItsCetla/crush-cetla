package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/update"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update " + update.AppName + " to the latest release",
	Long: `Check GitHub for the latest ` + update.AppName + ` release and install it.

The download is verified against the release's SHA-256 checksum before
anything is replaced. Only api.github.com and GitHub's release downloads are
contacted. This command works even when the background update check is
turned off (options.update_check or ` + update.NoCheckEnv + `).`,
	Example: `
# Check for a new release and install it
tada update

# Only report whether a new release is out
tada update --check

# Install without asking
tada update --yes
`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		checkOnly, _ := cmd.Flags().GetBool("check")
		yes, _ := cmd.Flags().GetBool("yes")

		exe, _ := update.RunningExecutable()
		update.CleanupOld(exe)
		u := update.New(version.Version)
		inst := update.DetectInstall(version.Version, runtime.GOOS, runtime.GOARCH, exe)
		return runUpdate(cmd.Context(), updateRun{
			u:         u,
			inst:      inst,
			out:       cmd.OutOrStdout(),
			in:        cmd.InOrStdin(),
			tty:       term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()),
			checkOnly: checkOnly,
			yes:       yes,
			statePath: updateStatePath(),
		})
	},
}

func init() {
	updateCmd.Flags().Bool("check", false, "Only report whether a new release is out")
	updateCmd.Flags().BoolP("yes", "y", false, "Install without asking")
}

// updateStatePath is where the update check remembers its last result.
func updateStatePath() string {
	return filepath.Join(config.GlobalWorkspaceDir(), update.StateFile)
}

type updateRun struct {
	u         *update.Updater
	inst      update.Install
	out       io.Writer
	in        io.Reader
	tty       bool
	checkOnly bool
	yes       bool
	statePath string
}

func runUpdate(ctx context.Context, r updateRun) error {
	current := r.u.Current
	if r.inst.Kind == update.KindDev {
		fmt.Fprintf(r.out, "%s %s is a development build, which doesn't update itself.\n", update.AppName, current)
		fmt.Fprintf(r.out, "Download a release from %s, or rebuild from source.\n", update.ReleasesPage)
		return nil
	}

	fmt.Fprintln(r.out, "Checking for updates…")
	res, err := r.u.Check(ctx)
	if err != nil {
		return err
	}
	if r.statePath != "" {
		s := update.LoadState(r.statePath)
		s.Record(res, time.Now())
		_ = update.SaveState(r.statePath, s)
	}
	if res.Release == nil {
		fmt.Fprintf(r.out, "No %s release has been published yet. You have %s.\n", update.AppName, current)
		return nil
	}
	if !res.Newer {
		fmt.Fprintf(r.out, "%s %s is up to date (latest release: %s).\n", update.AppName, current, res.Latest)
		return nil
	}

	fmt.Fprintf(r.out, "%s %s → %s\n%s\n", update.AppName, current, res.Latest, res.URL)
	if r.checkOnly {
		fmt.Fprintf(r.out, "Run `%s update` to install it.\n", update.BinaryName)
		return nil
	}
	if !r.yes {
		if !r.tty {
			fmt.Fprintf(r.out, "Run `%s update --yes` to install it.\n", update.BinaryName)
			return nil
		}
		if !confirm(r.out, r.in, fmt.Sprintf("Install %s %s? [y/N] ", update.AppName, res.Latest)) {
			fmt.Fprintln(r.out, "Not updated.")
			return nil
		}
	}

	fmt.Fprintf(r.out, "Downloading %s %s…\n", update.AppName, res.Latest)
	done, err := r.u.Install(ctx, res.Release, r.inst)
	if err != nil {
		var nw *update.ErrNotWritable
		if errors.As(err, &nw) {
			return nw
		}
		return fmt.Errorf("update failed: %w", err)
	}
	if done.Background {
		fmt.Fprintf(r.out, "The %s %s installer is finishing in the background. Start %s again in a moment.\n",
			update.AppName, done.Version, update.AppName)
		return nil
	}
	fmt.Fprintf(r.out, "Updated %s to %s (%s).\n", update.AppName, done.Version, done.Path)
	return nil
}

func confirm(out io.Writer, in io.Reader, prompt string) bool {
	fmt.Fprint(out, prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// updateNoticeTTL is how long the new-release notice stays in the status
// bar.
const updateNoticeTTL = 20 * time.Second

// sendUpdateNotice runs the background release check and, at most once per
// version, shows the new-release notice in the TUI. It never blocks and
// stays silent on any failure.
func sendUpdateNotice(program *tea.Program, cfg *config.Config) {
	var opts *config.Options
	if cfg != nil {
		opts = cfg.Options
	}
	if !opts.UpdateCheckEnabled() {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		exe, _ := update.RunningExecutable()
		update.CleanupOld(exe)
		notice := update.New(version.Version).Background(context.Background(), updateStatePath(), time.Now())
		if notice == "" {
			return
		}
		program.Send(util.InfoMsg{Type: util.InfoTypeInfo, Msg: notice, TTL: updateNoticeTTL})
	}()
}
