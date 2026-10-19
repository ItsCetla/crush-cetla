//go:build !windows

package update

import (
	"context"
	"os/exec"
)

func startDetachedProcess(name string, args []string) error {
	cmd := exec.CommandContext(context.Background(), name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
