package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print spend, budgets, recent actions and missions",
	Long: `Print one plain-text frame of the control cockpit and exit: what
your models have spent today and this month, your budgets, the latest actions
in this project and its missions. It never starts a session, so it's safe
for scripts, status bars and terminals without a TTY.`,
	Example: `
# Show the status for the current project
tada status

# ASCII only, for logs and old terminals
tada status --plain

# Machine-readable
tada status --json
  `,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return err
		}
		dataDir, _ := cmd.Flags().GetString("data-dir")
		plain, _ := cmd.Flags().GetBool("plain")
		asJSON, _ := cmd.Flags().GetBool("json")
		limit, _ := cmd.Flags().GetInt("actions")

		store, err := config.Load(cwd, dataDir, false)
		if err != nil {
			return fmt.Errorf("failed to load configuration: %v", err)
		}
		st, err := collectStatus(cmd, app.ReadControlHub(store), limit)
		if err != nil {
			return err
		}
		if asJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(st)
		}
		writeStatus(cmd.OutOrStdout(), st, time.Now(), plain)
		return nil
	},
}

func init() {
	statusCmd.Flags().Bool("plain", false, "Use ASCII only: no bars, glyphs or sparklines")
	statusCmd.Flags().Bool("json", false, "Print JSON")
	statusCmd.Flags().Int("actions", 8, "How many recent actions to list")
	rootCmd.AddCommand(statusCmd)
}

// statusReport is everything "tada status" prints.
type statusReport struct {
	Snapshot control.Snapshot    `json:"snapshot"`
	Report   control.Report      `json:"report"`
	Actions  []permission.Action `json:"actions,omitempty"`
	Missions []control.Mission   `json:"missions,omitempty"`
}

func collectStatus(cmd *cobra.Command, hub *control.Hub, limit int) (statusReport, error) {
	ctx := cmd.Context()
	var st statusReport
	r, err := hub.Do(ctx, control.Command{Op: control.OpUsage})
	if err != nil {
		return st, err
	}
	if r.Snapshot != nil {
		st.Snapshot = *r.Snapshot
	}
	if r.Report != nil {
		st.Report = *r.Report
	}
	if r, err := hub.Do(ctx, control.Command{Op: control.OpActions, Limit: max(limit, 0)}); err == nil {
		st.Actions = r.Actions
		if limit <= 0 {
			st.Actions = nil
		}
	}
	if r, err := hub.Do(ctx, control.Command{Op: control.OpMissions}); err == nil {
		st.Missions = r.Missions
	}
	return st, nil
}

// writeStatus prints the report. With plain set it uses ASCII only.
func writeStatus(w io.Writer, st statusReport, now time.Time, plain bool) {
	s := st.Snapshot
	section := func(name string) { fmt.Fprintf(w, "\n%s\n", name) }

	fmt.Fprintln(w, "Ta-Da status")

	section("Spend")
	if !s.HasLedger {
		fmt.Fprintln(w, "  No usage recorded yet.")
	} else {
		fmt.Fprintf(w, "  Today       %s\n", totalsLine(s.Today))
		fmt.Fprintf(w, "  This month  %s\n", totalsLine(s.Month))
		if st.Report.HasBurn {
			fmt.Fprintf(w, "  Burn rate   %s/h over the last hour\n", control.Money(st.Report.Burn))
		}
		if !plain && len(st.Report.Daily) > 0 {
			fmt.Fprintf(w, "  14 days     %s\n", control.Sparkline(st.Report.Daily))
		}
		for _, m := range st.Report.Models {
			fmt.Fprintf(w, "  %-11s %s, %s tokens, %d calls\n", m.Model, control.Money(m.Cost), control.Tokens(m.Tokens), m.Calls)
		}
	}

	section("Budgets")
	if len(s.Budgets) == 0 {
		fmt.Fprintln(w, "  None set. Add options.budget to tada.json to cap spend.")
	}
	for _, b := range s.Budgets {
		mark := ""
		switch {
		case b.Over:
			mark = "  OVER: Ta-Da will ask before spending more"
		case b.Warn > 0:
			mark = fmt.Sprintf("  past %d%%", b.Warn)
		}
		bar := control.Ladder(b.Pct, 20)
		if plain {
			bar = asciiBar(b.Pct, 20)
		}
		fmt.Fprintf(w, "  %-8s %s %s of %s (%.0f%%)%s\n", b.Window.Label(), bar, control.Money(b.Spent), control.Money(b.Limit), b.Pct, mark)
	}

	section("Recent actions")
	if len(st.Actions) == 0 {
		fmt.Fprintln(w, "  None yet in this project.")
	}
	for _, a := range st.Actions {
		mark := decisionMark(a.Decision, plain)
		via := ""
		if a.Via != "" {
			via = " (" + string(a.Via) + ")"
		}
		fmt.Fprintf(w, "  %s %-8s %s%s\n", mark, control.Ago(a.At, now), a.Summary(), via)
	}

	section("Missions")
	if len(st.Missions) == 0 {
		fmt.Fprintln(w, "  None. Start one from the cockpit (ctrl+\\ or alt+c).")
	}
	for _, m := range st.Missions {
		done, total := m.Progress()
		glyph := control.MissionGlyph(m.Status, done, total)
		pips := control.Pips(done, total, 10)
		if plain {
			glyph = "-"
			pips = ""
		}
		line := fmt.Sprintf("  %s %s  %d/%d", glyph, m.Title, done, total)
		if pips != "" {
			line += "  " + pips
		}
		if m.Status != control.MissionActive {
			line += "  (" + string(m.Status) + ")"
		}
		fmt.Fprintln(w, line)
	}
}

func totalsLine(t control.Totals) string {
	parts := []string{fmt.Sprintf("%s, %s tokens", control.Money(t.Cost), control.Tokens(t.Tokens))}
	if t.FlatCalls > 0 {
		parts = append(parts, fmt.Sprintf("%s list price on a flat plan (not counted)", control.Money(t.Flat)))
	}
	if t.Unpriced > 0 {
		parts = append(parts, fmt.Sprintf("%d calls without a price", t.Unpriced))
	}
	return strings.Join(parts, "; ")
}

func decisionMark(d permission.Decision, plain bool) string {
	switch d {
	case permission.DecisionAllowed:
		if plain {
			return "+"
		}
		return "✓"
	case permission.DecisionDenied:
		if plain {
			return "x"
		}
		return "✕"
	case permission.DecisionCanceled:
		return "-"
	}
	return "!"
}

// asciiBar is Ladder in ASCII: "[#####.....]".
func asciiBar(pct float64, width int) string {
	l := control.Ladder(pct, width)
	l = strings.ReplaceAll(l, "█", "#")
	return "[" + strings.ReplaceAll(l, "·", ".") + "]"
}
