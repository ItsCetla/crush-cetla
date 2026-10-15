//go:build !windows

package config

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTrust_FIFOConfigDoesNotBlock guards the fingerprinting of untrusted
// files: a project shipping a FIFO as crush.json must not hang startup.
func TestTrust_FIFOConfigDoesNotBlock(t *testing.T) {
	isolateTrust(t)
	project := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(project, "crush.json"), 0o644))

	done := make(chan ProjectTrust, 1)
	go func() {
		store, err := Load(project, "", false)
		if err != nil {
			done <- ProjectTrust{}
			return
		}
		done <- store.ProjectTrust()
	}()
	select {
	case trust := <-done:
		require.False(t, trust.Trusted)
		require.Equal(t, []string{"crush.json"}, trust.DisplayFiles())
	case <-time.After(30 * time.Second):
		t.Fatal("loading a project with a FIFO config blocked")
	}
}
