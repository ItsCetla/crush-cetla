package dialog

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

type contextWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *contextWorkspace) Config() *config.Config { return w.cfg }

func newTestContext(t *testing.T, mc *config.MagicContextOptions) *Context {
	t.Helper()
	s := styles.CharmtonePantera()
	cfg := &config.Config{Options: &config.Options{MagicContext: mc}}
	d := NewContext(&common.Common{Styles: &s, Workspace: &contextWorkspace{cfg: cfg}})
	d.width = 76
	return d
}

func TestContextQuery(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"/context", " /CONTEXT ", "/magic-context"} {
		require.True(t, ContextQuery(v), v)
	}
	for _, v := range []string{"context", "/ctx-status", "/context on"} {
		require.False(t, ContextQuery(v), v)
	}
}

func TestContextToggle(t *testing.T) {
	t.Parallel()

	d := newTestContext(t, nil)
	view := ansi.Strip(d.content())
	require.Contains(t, view, "Magic Context  Off")

	act := d.HandleMsg(keyMsg(' '))
	require.Equal(t, ActionSetMagicContext{Enabled: true}, act)
	require.Contains(t, ansi.Strip(d.content()), "Saving…")

	// A second press while saving does nothing.
	require.Nil(t, d.HandleMsg(keyMsg(' ')))

	d.SetResult("Magic Context is on.", nil)
	require.Contains(t, ansi.Strip(d.content()), "Magic Context is on.")
}

func TestContextOnShowsThreshold(t *testing.T) {
	t.Parallel()

	d := newTestContext(t, &config.MagicContextOptions{Enabled: true, ExecuteThresholdPercentage: 70})
	view := ansi.Strip(d.content())
	require.Contains(t, view, "Magic Context  On")
	require.Contains(t, view, "70% full")

	require.Equal(t, ActionSetMagicContext{Enabled: false}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	d.SetResult("", errors.New("disk full"))
	require.Contains(t, ansi.Strip(d.content()), "! disk full")
}

func TestContextClose(t *testing.T) {
	t.Parallel()
	d := newTestContext(t, nil)
	require.Equal(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}
