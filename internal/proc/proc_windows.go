//go:build windows

// Package proc holds small helpers for the child processes Ta-Da starts.
package proc

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// HideConsole gives cmd its own hidden console instead of sharing Ta-Da's.
//
// A child on Ta-Da's console can read keys meant for the TUI (git credential
// prompts, `pause`, getpass), draw over it, and change the console mode
// (input/VT flags) for every process on that console, which breaks typing,
// paste and rendering. Its children inherit the hidden console too. Only use
// it for children that never need the user's terminal (not editors/pagers).
func HideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cmd.SysProcAttr.CreationFlags&windows.DETACHED_PROCESS != 0 {
		return // already has no console at all
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	cmd.SysProcAttr.HideWindow = true
}

// KillTree ends p and every process it started. Killing only p leaves
// grandchildren (a .cmd shim's node, npx's server) running and holding the
// output pipes, so readers and Wait never return.
func KillTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)) //nolint:noctx
	HideConsole(kill)
	err := kill.Run()
	// taskkill fails if p already exited; Kill is a cheap backstop.
	if kerr := p.Kill(); err != nil && kerr == nil {
		err = nil
	}
	return err
}
