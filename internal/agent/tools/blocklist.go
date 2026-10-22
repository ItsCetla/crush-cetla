package tools

import "github.com/charmbracelet/crush/internal/shell"

// BlockFuncs returns the bash tool's runtime block list, for other code
// that runs commands on the agent's behalf, such as mission checks.
func BlockFuncs() []shell.BlockFunc {
	return blockFuncs()
}
