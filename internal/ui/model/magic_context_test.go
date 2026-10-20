package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

type mcWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *mcWorkspace) Config() *config.Config { return w.cfg }
func (w *mcWorkspace) WorkingDir() string     { return "/" }

func newMCTestUI(enabled bool) *UI {
	cfg := &config.Config{Options: &config.Options{}}
	if enabled {
		cfg.Options.MagicContext = &config.MagicContextOptions{Enabled: true}
	}
	return &UI{com: common.DefaultCommon(&mcWorkspace{cfg: cfg})}
}

func TestParseMagicContextCommand(t *testing.T) {
	t.Parallel()
	u := newMCTestUI(true)

	name, args, ok := u.parseMagicContextCommand("/ctx-status")
	require.True(t, ok)
	require.Equal(t, "ctx-status", name)
	require.Empty(t, args)

	name, args, ok = u.parseMagicContextCommand("/ctx-recomp  3-9 ")
	require.True(t, ok)
	require.Equal(t, "ctx-recomp", name)
	require.Equal(t, "3-9", args)

	for _, v := range []string{
		"ctx-status",          // no slash: a normal prompt
		"/ctx-unknown",        // not a Magic Context command
		"/ctx-status\nplease", // multi-line text stays a prompt
		"/help",
		"",
	} {
		_, _, ok := u.parseMagicContextCommand(v)
		require.False(t, ok, v)
	}

	u.bangMode = true
	_, _, ok = u.parseMagicContextCommand("/ctx-status")
	require.False(t, ok, "bang mode sends text to the shell")
}

func TestParseMagicContextCommandDisabled(t *testing.T) {
	t.Parallel()
	_, _, ok := newMCTestUI(false).parseMagicContextCommand("/ctx-status")
	require.False(t, ok, "with Magic Context off, /ctx-* is an ordinary prompt")
}
