package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/swarm"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

var swarmCmd = &cobra.Command{
	Use:   "swarm",
	Short: "Split a mission across your machines, then merge their branches",
	Long: `A swarm splits a mission's open criteria across your machines. Each
machine's agent works in its own worktree and branch, then the mission's
approved checks run there. Every tool call the agents make asks your
permission, and nothing reaches your branch until you merge it.

In the TUI, open a mission on the cockpit's Missions page and press w, or use
Start Swarm and Merge Board in the command palette.`,
}

var swarmStartCmd = &cobra.Command{
	Use:   "start <mission-id>",
	Short: "Show the plan for a mission, then start it once you confirm",
	Long: `Show which machine gets which criteria and start the swarm once you
confirm. It needs a terminal, both to confirm and to answer the agents'
permission prompts, and it refuses to run in auto mode.`,
	Args: cobra.ExactArgs(1),
	RunE: runSwarmStart,
}

var swarmStatusCmd = &cobra.Command{
	Use:   "status [run]",
	Short: "Show your swarms and how each machine's branch is doing",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runSwarmStatus,
}

var swarmMergeCmd = &cobra.Command{
	Use:   "merge <run> <machine>",
	Short: "Merge one machine's branch into the current branch",
	Long: `Merge one machine's branch into the branch the swarm started from. It
refuses while your checkout has uncommitted changes, aborts on a conflict and
ticks only the criteria whose checks passed.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSwarmSettle(cmd, control.OpSwarmMerge, args[0], args[1])
	},
}

var swarmDiscardCmd = &cobra.Command{
	Use:   "discard <run> <machine>",
	Short: "Throw one machine's worktree and branch away",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSwarmSettle(cmd, control.OpSwarmDiscard, args[0], args[1])
	},
}

func init() {
	swarmStatusCmd.Flags().Bool("json", false, "Print the runs as JSON")
	swarmDiscardCmd.Flags().Bool("force", false, "Don't ask before discarding")
	swarmCmd.AddCommand(swarmStartCmd, swarmStatusCmd, swarmMergeCmd, swarmDiscardCmd)
	rootCmd.AddCommand(swarmCmd)
}

// errSwarmAutoFlag is returned when a swarm command is run with --auto.
var errSwarmAutoFlag = errors.New("swarms never run in auto mode; drop --auto so every tool call asks you first")

func runSwarmStart(cmd *cobra.Command, args []string) error {
	if autoModeFlag(cmd) {
		return errSwarmAutoFlag
	}
	if !term.IsTerminal(os.Stdin.Fd()) {
		return errors.New("tada swarm start needs a terminal to confirm the plan and answer permission prompts")
	}
	ws, cleanup, err := setupWorkspaceWithProgressBar(cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	in := bufio.NewReader(cmd.InOrStdin())

	r, err := ws.Control(ctx, control.Command{Op: control.OpSwarmPlan, MissionID: args[0]})
	if err != nil {
		return err
	}
	if r.Plan == nil {
		return errors.New("the swarm plan came back empty")
	}
	writeSwarmPlan(out, *r.Plan)
	if len(r.Plan.Assignments) == 0 {
		return errors.New("nothing to start")
	}
	if !confirm(out, in, "Start this swarm? [y/N] ") {
		_, _ = fmt.Fprintln(out, "Not started.")
		return nil
	}

	// Subscribe before starting so no permission prompt is missed.
	var events <-chan pubsub.Event[tea.Msg]
	if aw, ok := ws.(*workspace.AppWorkspace); ok {
		events = aw.App().Events(ctx)
	}
	r, err = ws.Control(ctx, control.Command{Op: control.OpSwarmStart, MissionID: r.Plan.MissionID, Assign: r.Plan.Assign()})
	if err != nil {
		return err
	}
	if r.Swarm == nil {
		return errors.New("the swarm didn't start")
	}
	run := *r.Swarm
	_, _ = fmt.Fprintf(out, "Started swarm %s.\n", run.ID)
	if events == nil {
		_, _ = fmt.Fprintln(out, "It runs on the Ta-Da server: answer its permission prompts in the TUI, then review it on the merge board or with tada swarm status.")
		return nil
	}
	return watchSwarm(ctx, ws, events, out, in, run)
}

// watchSwarm answers the swarm's permission prompts on the terminal and
// waits for every machine to finish.
func watchSwarm(ctx context.Context, ws workspace.Workspace, events <-chan pubsub.Event[tea.Msg], out io.Writer, in *bufio.Reader, run control.SwarmRun) error {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	_, _ = fmt.Fprintln(out, "Each tool call the agents make asks here first. Press ctrl+c to stop the swarm.")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return errors.New("the app stopped")
			}
			inner, ok := ev.Payload.(pubsub.Event[permission.PermissionRequest])
			if !ok || inner.Type != pubsub.CreatedEvent {
				continue
			}
			req := inner.Payload
			writePermissionPrompt(out, req, machineForSession(run, req.SessionID))
			if confirm(out, in, "Allow? [y/N] ") {
				ws.PermissionGrant(req)
			} else {
				ws.PermissionDeny(req)
			}
		case <-tick.C:
			r, err := ws.Control(ctx, control.Command{Op: control.OpSwarmRuns})
			if err != nil {
				continue
			}
			for _, sr := range r.Swarms {
				if sr.ID != run.ID {
					continue
				}
				run = sr
				if !sr.Running() {
					_, _ = fmt.Fprintln(out)
					writeSwarmRun(out, sr, time.Now())
					_, _ = fmt.Fprintf(out, "\nReview it on the merge board, or run tada swarm merge %s <machine>.\n", sr.ID)
					return nil
				}
			}
		}
	}
}

func machineForSession(run control.SwarmRun, sessionID string) string {
	for _, a := range run.Assignments {
		if a.SessionID != "" && a.SessionID == sessionID {
			return a.Machine
		}
	}
	return ""
}

// writePermissionPrompt shows a permission request. The parameters are
// quoted so hidden or control characters show as escapes.
func writePermissionPrompt(w io.Writer, req permission.PermissionRequest, machine string) {
	who := "A swarm agent"
	if machine != "" {
		who = machine
	}
	_, _ = fmt.Fprintf(w, "\n%s wants to run %s", who, req.ToolName)
	if req.Path != "" {
		_, _ = fmt.Fprintf(w, " in %s", strconv.QuoteToASCII(req.Path))
	}
	_, _ = fmt.Fprintln(w)
	if req.Description != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", strconv.QuoteToASCII(req.Description))
	}
	if req.Params != nil {
		if b, err := json.Marshal(req.Params); err == nil {
			s := strconv.QuoteToASCII(string(b))
			if len(s) > 600 {
				s = s[:600] + "…"
			}
			_, _ = fmt.Fprintf(w, "  %s\n", s)
		}
	}
}

func writeSwarmPlan(w io.Writer, p control.SwarmPlan) {
	title := p.MissionTitle
	if title == "" {
		title = p.MissionID
	}
	_, _ = fmt.Fprintf(w, "Swarm for %s (%d open criteria)\n\n", title, p.Open)
	for _, a := range p.Assignments {
		_, _ = fmt.Fprintf(w, "%s", a.Machine)
		if a.Model != "" {
			_, _ = fmt.Fprintf(w, " (%s)", a.Model)
		}
		_, _ = fmt.Fprintf(w, " on %s\n", a.Branch)
		for i, id := range a.Criteria {
			text := ""
			if i < len(a.CriteriaText) {
				text = a.CriteriaText[i]
			}
			_, _ = fmt.Fprintf(w, "  #%d %s\n", id, cleanLine(text))
		}
	}
	for _, s := range p.Skipped {
		_, _ = fmt.Fprintf(w, "Left out %s: %s\n", s.Machine, cleanLine(s.Reason))
	}
	switch {
	case p.Open == 0:
		_, _ = fmt.Fprintln(w, "The mission has no open criteria.")
	case len(p.Assignments) == 0:
		_, _ = fmt.Fprintln(w, "No machine is ready. Turn one on with tada machines, then try again.")
	default:
		_, _ = fmt.Fprintln(w, "\nEach machine works in its own worktree and branch, and every tool call asks you first.")
	}
}

// cleanLine shows text on one line, with control and hidden characters
// escaped.
func cleanLine(s string) string {
	q := strconv.QuoteToGraphic(strings.Join(strings.Fields(s), " "))
	return q[1 : len(q)-1]
}

func writeSwarmRun(w io.Writer, r control.SwarmRun, now time.Time) {
	title := r.MissionTitle
	if title == "" {
		title = r.MissionID
	}
	_, _ = fmt.Fprintf(w, "%s  %s", r.ID, cleanLine(title))
	if r.Target != "" {
		_, _ = fmt.Fprintf(w, "  into %s", r.Target)
	}
	_, _ = fmt.Fprintf(w, "  %s  %s\n", control.Money(r.Cost()), control.Ago(r.Created, now))
	for _, a := range r.Assignments {
		check := a.CheckState()
		if !a.Status.Finished() {
			check = "pending"
		}
		_, _ = fmt.Fprintf(w, "  %-14s %-10s checks %-8s %8s  +%d -%d  %s\n",
			a.Machine, a.Status, check, control.Money(a.Cost), a.Added, a.Removed, a.Branch)
		if a.Error != "" {
			_, _ = fmt.Fprintf(w, "    ! %s\n", cleanLine(a.Error))
		}
	}
}

// runSwarmStatus lists the swarms from the store, without starting the
// app, so it works while the TUI or a swarm is running.
func runSwarmStatus(cmd *cobra.Command, args []string) error {
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return err
	}
	dataDir, _ := cmd.Flags().GetString("data-dir")
	asJSON, _ := cmd.Flags().GetBool("json")
	store, err := config.Load(cwd, dataDir, false)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %v", err)
	}
	runs := []control.SwarmRun{}
	if d := store.Config().Options.DataDirectory; d != "" {
		dir := swarm.StoreDir(filepath.Join(d, "control"))
		if _, err := os.Stat(dir); err == nil {
			st, err := swarm.NewStore(dir)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				r, err := st.Find(args[0])
				if err != nil {
					return err
				}
				runs = append(runs, r)
			} else if runs, err = st.List(); err != nil {
				return err
			}
		}
	}
	out := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(runs)
	}
	if len(runs) == 0 {
		_, _ = fmt.Fprintln(out, "No swarms yet. Start one with tada swarm start <mission-id>.")
		return nil
	}
	now := time.Now()
	for i, r := range runs {
		if i > 0 {
			_, _ = fmt.Fprintln(out)
		}
		writeSwarmRun(out, r, now)
	}
	return nil
}

// runSwarmSettle merges or discards one machine's branch.
func runSwarmSettle(cmd *cobra.Command, op control.Op, runID, machine string) error {
	if autoModeFlag(cmd) {
		return errSwarmAutoFlag
	}
	out := cmd.OutOrStdout()
	if op == control.OpSwarmDiscard {
		if force, _ := cmd.Flags().GetBool("force"); !force {
			if !term.IsTerminal(os.Stdin.Fd()) {
				return errors.New("discarding asks first; pass --force to discard without a terminal")
			}
			prompt := fmt.Sprintf("Discard %s's worktree and branch for good? [y/N] ", machine)
			if !confirm(out, bufio.NewReader(cmd.InOrStdin()), prompt) {
				_, _ = fmt.Fprintln(out, "Kept it.")
				return nil
			}
		}
	}
	ws, cleanup, err := setupWorkspaceWithProgressBar(cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	r, err := ws.Control(cmd.Context(), control.Command{Op: op, RunID: runID, Machine: machine})
	if err != nil {
		return err
	}
	switch {
	case r.Message != "":
		_, _ = fmt.Fprintln(out, r.Message)
	case op == control.OpSwarmDiscard:
		_, _ = fmt.Fprintf(out, "Discarded %s's branch.\n", machine)
	}
	return nil
}
