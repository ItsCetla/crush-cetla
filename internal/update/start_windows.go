//go:build windows

package update

import (
	"context"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func startDetachedProcess(name string, args []string) error {
	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
