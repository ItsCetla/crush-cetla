package model

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/limits"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newLimitsTestUI(path string) *UI {
	u := newTestUI()
	u.com.Workspace = &testWorkspace{cfg: &config.Config{
		Options: &config.Options{TUI: &config.TUIOptions{LimitRings: path}},
	}}
	return u
}

func TestLimitsInfoOffByDefault(t *testing.T) {
	t.Parallel()
	u := newLimitsTestUI("")
	u.lim.providers = []limits.Provider{{Name: "Claude"}}
	require.Empty(t, u.limitsInfo(30, time.Now()), "no file configured, no section")

	msg := u.limitsPoll(0)()
	require.Equal(t, limitsMsg{}, msg, "the poll reads nothing while off")
	require.Nil(t, u.lim.reader)
}

func TestLimitsPollAndRender(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "limits.json")
	u := newLimitsTestUI(path)

	// A missing file keeps the section hidden.
	msg := u.limitsPoll(0)().(limitsMsg)
	require.NotNil(t, u.handleLimits(msg))
	require.Empty(t, u.limitsInfo(30, time.Now()))

	now := time.Now()
	ts := now.Unix()
	data := []byte(`{"claude":{"name":"Claude","_ts":` + itoa(ts) +
		`,"used5h":84,"reset5h":` + itoa(ts+3600) +
		`,"usedWeek":18,"resetWeek":` + itoa(ts+3*86400) + `},` +
		`"codex":{"ok":false,"_ts":` + itoa(ts) + `}}`)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	msg = u.limitsPoll(0)().(limitsMsg)
	u.handleLimits(msg)
	out := ansi.Strip(u.limitsInfo(32, now))
	require.Contains(t, out, "Limits")
	require.Contains(t, out, "Claude")
	require.Contains(t, out, "◕ 5h    84%")
	require.Contains(t, out, "◔ week  18%")
	require.Contains(t, out, "codex")
	require.Contains(t, out, "stale")
	for _, line := range strings.Split(out, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 32, line)
	}

	// A reading for a path no longer configured is dropped.
	u.handleLimits(limitsMsg{path: "/elsewhere", providers: msg.providers})
	require.Empty(t, u.limitsInfo(32, now))
}

func TestLimitsInfoReadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	u := newLimitsTestUI(dir)
	u.handleLimits(u.limitsPoll(0)().(limitsMsg))
	require.Contains(t, ansi.Strip(u.limitsInfo(40, time.Now())), "Can't read the limits file")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
