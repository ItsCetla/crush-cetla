package cmd

import (
	"fmt"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/spf13/cobra"
)

var machinesCmd = &cobra.Command{
	Use:     "machines",
	Aliases: []string{"machine"},
	Short:   "List your machines and check which are online",
	Long: `Machines are other computers on your network that run local models
(Ollama, LM Studio, llama.cpp and so on). Agents hand side work to them,
each running as many agents at once as it has slots.

Add them here, in your tadarc with "machine add", or just tell the AI
about them.`,
	Example: `
# Show every machine, whether it's online and how many slots are free
tada machines

# An M1 that can run 4 agents and a PC with a 3060 Ti that can run 2
tada machines add m1 --url http://192.168.1.20:11434/v1 --type ollama --slots 4 --hardware "M1 Max"
tada machines add pc --url http://192.168.1.30:11434/v1 --type ollama --slots 2 --hardware "RTX 3060 Ti"

# Remove one
tada machines remove pc`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runMachinesTool(cmd, fleet.Params{Action: "check"})
	},
}

var machinesAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Save a machine to your global config",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p := fleet.Params{Action: "add", Name: args[0]}
		f := cmd.Flags()
		p.URL, _ = f.GetString("url")
		p.Type, _ = f.GetString("type")
		p.Model, _ = f.GetString("model")
		p.Hardware, _ = f.GetString("hardware")
		p.APIKey, _ = f.GetString("api-key")
		p.Notes, _ = f.GetString("notes")
		p.Role, _ = f.GetString("role")
		p.Use, _ = f.GetString("use")
		p.Slots, _ = f.GetInt("slots")
		p.ContextWindow, _ = f.GetInt64("context-window")
		p.Tags, _ = f.GetStringSlice("tag")
		if f.Changed("disabled") {
			disabled, _ := f.GetBool("disabled")
			p.Disabled = &disabled
		}
		if f.Changed("update") {
			if update, _ := f.GetBool("update"); update {
				p.Action = "update"
			}
		}
		return runMachinesTool(cmd, p)
	},
}

var machinesRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a machine",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMachinesTool(cmd, fleet.Params{Action: "remove", Name: args[0]})
	},
}

var machinesMCPCmd = &cobra.Command{
	Use:    "mcp",
	Short:  "Serve the machine tools over MCP (stdio)",
	Long:   "Serve the machines and machine_ask tools over stdio. Ta-Da starts this for the Claude Code engine.",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dir, _ := cmd.Flags().GetString("dir")
		if dir == "" {
			var err error
			if dir, err = ResolveCwd(cmd); err != nil {
				return err
			}
		}
		dataDir, _ := cmd.Flags().GetString("data-dir")
		store, err := config.Init(dir, dataDir, false)
		if err != nil {
			return err
		}
		return fleet.RunMCP(cmd.Context(), store, dir)
	},
}

func runMachinesTool(cmd *cobra.Command, p fleet.Params) error {
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
	out, err := fleet.RunTool(cmd.Context(), store, nil, p)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), out)
	return nil
}

func init() {
	f := machinesAddCmd.Flags()
	f.String("url", "", "OpenAI-compatible base URL, e.g. http://192.168.1.20:11434/v1")
	f.String("type", "", "Server type: openai-compat (default), ollama, lmstudio, llamacpp, omlx or litellm")
	f.String("model", "", "Model agents use; empty uses the first one the server lists")
	f.Int("slots", 0, "How many agents the machine can run at once (default 1)")
	f.String("hardware", "", `What the machine is, e.g. "M1 Max"`)
	f.String("api-key", "", "API key, only if the server needs one")
	f.Int64("context-window", 0, "Context window to assume if the server doesn't report one")
	f.StringSlice("tag", nil, "What the machine is good at (repeatable)")
	f.String("role", "", "The job the machine is assigned, e.g. code review and tests")
	f.String("use", "", "auto (default): picked whenever it has a free slot; named: only when asked for by name")
	f.String("notes", "", "Anything else worth knowing about the machine")
	f.Bool("disabled", false, "Save the machine but don't use it yet")
	f.Bool("update", false, "Change an existing machine instead of adding one")

	machinesMCPCmd.Flags().String("dir", "", "The working directory files can be read from")
	machinesCmd.AddCommand(machinesAddCmd, machinesRemoveCmd, machinesMCPCmd)
	rootCmd.AddCommand(machinesCmd)
}
