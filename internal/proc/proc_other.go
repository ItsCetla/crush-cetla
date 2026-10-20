//go:build !windows

// Package proc holds small helpers for the child processes Ta-Da starts.
package proc

import (
	"os"
	"os/exec"
)

// HideConsole is a no-op outside Windows; Unix children are isolated with
// process groups or sessions where that matters.
func HideConsole(*exec.Cmd) {}

// KillTree kills p. Callers that need the whole tree on Unix put the child in
// its own process group and signal that instead.
func KillTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
