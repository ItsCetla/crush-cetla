package cmd

import (
	"errors"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/spf13/cobra"
)

var missionCmd = &cobra.Command{
	Use:   "mission",
	Short: "Work with missions",
	Long: `Missions are goals with a checklist of done criteria that outlive a
single session. Manage them in the cockpit (ctrl+\ or alt+c, Missions page).`,
}

var missionMCPCmd = &cobra.Command{
	Use:    "mcp",
	Short:  "Serve the mission tool for one session over MCP (stdio)",
	Long:   "Serve the mission tool for one session over stdio. Ta-Da starts this for the Claude Code engine.",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dir, _ := cmd.Flags().GetString("dir")
		session, _ := cmd.Flags().GetString("session")
		if dir == "" || session == "" {
			return errors.New("--dir and --session are required")
		}
		ms, err := control.NewMissions(dir)
		if err != nil {
			return err
		}
		if cwd, _ := cmd.Flags().GetString("cwd"); cwd != "" {
			ms.SetWorkDir(cwd)
		}
		if approvals, _ := cmd.Flags().GetString("approvals"); approvals != "" {
			ms.SetApprovals(approvals)
		}
		ms.SetBlockFuncs(tools.BlockFuncs())
		return control.RunMissionMCP(cmd.Context(), ms, session)
	},
}

func init() {
	missionMCPCmd.Flags().String("dir", "", "The missions directory")
	missionMCPCmd.Flags().String("session", "", "The Ta-Da session the tool works for")
	missionMCPCmd.Flags().String("cwd", "", "The project directory mission checks run in")
	missionMCPCmd.Flags().String("approvals", "", "The file of check commands the user approved")
	missionCmd.AddCommand(missionMCPCmd)
	rootCmd.AddCommand(missionCmd)
}
