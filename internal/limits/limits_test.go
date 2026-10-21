package limits

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	t.Parallel()
	now := time.Unix(1760000600, 0)
	data := []byte(`{
		"_version": 1,
		"_meta": {"_ts": 1, "used5h": 1},
		"zeta": "not a provider",
		"other": {"foo": 1},
		"codex": {"ok": true, "_ts": 1760000000, "used5h": 90, "reset5h": 1760005000},
		"claude": {"name": "Claude", "ok": true, "_ts": 1760000000,
			"used5h": 142.5, "reset5h": 1760010000,
			"usedWeek": 18, "resetWeek": 1760000000, "tier": "max"},
		"old": {"_ts": 1759990000, "used5h": 10, "reset5h": 1760010000},
		"failed": {"ok": false, "_ts": 1760000000},
		"flag": {"stale": true, "_ts": 1760000000},
		"nots": {"usedWeek": 5, "resetWeek": 1760400000}
	}`)
	ps, err := Parse(data, now)
	require.NoError(t, err)

	keys := make([]string, len(ps))
	for i, p := range ps {
		keys[i] = p.Key
	}
	require.Equal(t, []string{"claude", "codex", "failed", "flag", "nots", "old"}, keys)

	claude := ps[0]
	require.Equal(t, "Claude", claude.Name)
	require.Equal(t, "max", claude.Tier)
	require.False(t, claude.Stale)
	require.True(t, claude.Five.Known)
	require.Equal(t, 100.0, claude.Five.Used, "clamped")
	require.True(t, claude.Five.Full())
	require.False(t, claude.Week.Known, "reset in the past hides the value")

	codex := ps[1]
	require.Equal(t, "codex", codex.Name, "name defaults to the key")
	require.True(t, codex.Five.Warn())
	require.False(t, codex.Five.Full())
	require.False(t, codex.Week.Known, "missing number is unknown")

	require.True(t, ps[2].Stale, "ok=false")
	require.True(t, ps[3].Stale, "stale=true")
	require.True(t, ps[4].Stale, "no _ts")
	require.True(t, ps[4].Week.Known)
	require.True(t, ps[5].Stale, "older than 15 minutes")
}

func TestParseBadJSON(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(`[1,2]`), time.Now())
	require.Error(t, err)
}

func TestReader(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "limits.json")
	r := &Reader{Path: path}
	now := time.Unix(1760000600, 0)

	ps, err := r.Read(now)
	require.NoError(t, err, "a missing file is not an error")
	require.Empty(t, ps)

	require.NoError(t, os.WriteFile(path, []byte(`{"a":{"_ts":1760000000}}`), 0o600))
	ps, err = r.Read(now)
	require.NoError(t, err)
	require.Len(t, ps, 1)
	require.False(t, ps[0].Stale)

	// Same file, later clock: re-parsed against the new time.
	ps, err = r.Read(now.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ps[0].Stale)

	// A changed file is picked up.
	require.NoError(t, os.WriteFile(path, []byte(`{"a":{"_ts":1760000000},"b":{"_ts":1760000000}}`), 0o600))
	later := time.Now().Add(time.Second)
	require.NoError(t, os.Chtimes(path, later, later))
	ps, err = r.Read(now)
	require.NoError(t, err)
	require.Len(t, ps, 2)

	require.NoError(t, os.Remove(path))
	ps, err = r.Read(now)
	require.NoError(t, err)
	require.Empty(t, ps)

	var nilReader *Reader
	ps, err = nilReader.Read(now)
	require.NoError(t, err)
	require.Empty(t, ps)
}

func TestWindowLabels(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	require.Equal(t, "◌", Window{}.Ring())
	require.Equal(t, "—", Window{}.Percent())
	require.Empty(t, Window{}.ResetLabel(now))
	for used, ring := range map[float64]string{0: "○", 20: "◔", 50: "◑", 84: "◕", 100: "●"} {
		require.Equal(t, ring, Window{Known: true, Used: used}.Ring(), used)
	}
	w := Window{Known: true, Used: 84.4, Reset: now.Add(6*time.Hour + 12*time.Minute)}
	require.Equal(t, "84%", w.Percent())
	require.Equal(t, "3:12 PM", w.ResetLabel(now))
	w.Reset = now.Add(3 * 24 * time.Hour)
	require.Equal(t, "Sat 9:00 AM", w.ResetLabel(now))
	w.Reset = now.Add(9 * 24 * time.Hour)
	require.Equal(t, "Oct 30", w.ResetLabel(now))
}
