//go:build windows

package mcp

import (
	"os/exec"
	"time"

	"github.com/charmbracelet/crush/internal/proc"
)

// configureStdioProcess gives a stdio MCP server its own hidden console and
// makes context cancellation end its whole process tree.
//
// Windows has no POSIX process groups, and os/exec's default cancel kills
// only the direct child: an npx-based server's node (or a .cmd shim's
// child) keeps running and holding the stdio pipes. Sharing Ta-Da's console
// would also let the server read keys meant for the TUI or change the
// console mode.
func configureStdioProcess(cmd *exec.Cmd) {
	proc.HideConsole(cmd)
	cmd.Cancel = func() error { return proc.KillTree(cmd.Process) }
	// A leaked descendant holding a stdio pipe must not block Wait forever.
	cmd.WaitDelay = 5 * time.Second
}
