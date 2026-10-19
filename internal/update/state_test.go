package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStateFile(t *testing.T) {
	t.Parallel()

	t.Run("missing or corrupt is empty", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.Equal(t, State{}, LoadState(filepath.Join(dir, "nope.json")))
		bad := filepath.Join(dir, "bad.json")
		require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o600))
		require.Equal(t, State{}, LoadState(bad))
	})

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "nested", StateFile)
		s := State{
			LastChecked:     time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
			Latest:          "0.3.0",
			URL:             "https://github.com/ItsCetla/tada/releases/tag/v0.3.0",
			NotifiedVersion: "0.3.0",
		}
		require.NoError(t, SaveState(path, s))
		got := LoadState(path)
		require.True(t, s.LastChecked.Equal(got.LastChecked))
		got.LastChecked = s.LastChecked
		require.Equal(t, s, got)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(data), `"lastChecked"`)
		require.Contains(t, string(data), `"notifiedVersion"`)
	})
}

func TestDueForCheck(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	require.True(t, State{}.DueForCheck(now))
	require.False(t, State{LastChecked: now.Add(-time.Hour)}.DueForCheck(now))
	require.True(t, State{LastChecked: now.Add(-6 * time.Hour)}.DueForCheck(now))
	require.True(t, State{LastChecked: now.Add(time.Hour)}.DueForCheck(now), "a future time means the clock moved")
}

func TestPendingNotice(t *testing.T) {
	t.Parallel()
	s := State{Latest: "0.3.0"}
	v, ok := s.PendingNotice("0.2.0")
	require.True(t, ok)
	require.Equal(t, "0.3.0", v)

	s.NotifiedVersion = "0.3.0"
	_, ok = s.PendingNotice("0.2.0")
	require.False(t, ok, "already announced")

	_, ok = State{Latest: "0.3.0"}.PendingNotice("0.3.0")
	require.False(t, ok, "not newer")

	_, ok = State{}.PendingNotice("0.2.0")
	require.False(t, ok, "nothing known")

	v, ok = State{Latest: "0.4.0", NotifiedVersion: "0.3.0"}.PendingNotice("0.2.0")
	require.True(t, ok, "a newer version is announced again")
	require.Equal(t, "0.4.0", v)
}

func TestNotice(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Ta-Da 0.3.0 is out. Run `tada update` (or use the command palette) to install it.", Notice("0.3.0"))
}

func TestBackground(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	t.Run("notifies once per version", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		gh.setRelease("v0.3.0", nil, true)
		path := filepath.Join(t.TempDir(), StateFile)
		u := gh.updater("0.2.0")

		require.Equal(t, Notice("0.3.0"), u.Background(t.Context(), path, now))
		require.Equal(t, int32(1), gh.checks.Load())
		s := LoadState(path)
		require.Equal(t, "0.3.0", s.Latest)
		require.Equal(t, "0.3.0", s.NotifiedVersion)
		require.True(t, now.Equal(s.LastChecked))

		// Next start, within 6 h: no request and no repeat notice.
		require.Empty(t, u.Background(t.Context(), path, now.Add(time.Hour)))
		require.Equal(t, int32(1), gh.checks.Load())

		// After 6 h it checks again; same version, still quiet.
		require.Empty(t, u.Background(t.Context(), path, now.Add(7*time.Hour)))
		require.Equal(t, int32(2), gh.checks.Load())

		// A newer release is announced.
		gh.setRelease("v0.4.0", nil, true)
		require.Equal(t, Notice("0.4.0"), u.Background(t.Context(), path, now.Add(14*time.Hour)))
		require.Empty(t, u.Background(t.Context(), path, now.Add(21*time.Hour)))
	})

	t.Run("up to date is quiet", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		gh.setRelease("v0.3.0", nil, true)
		path := filepath.Join(t.TempDir(), StateFile)
		require.Empty(t, gh.updater("0.3.0").Background(t.Context(), path, now))
		require.Equal(t, "0.3.0", LoadState(path).Latest)
	})

	t.Run("no release yet is quiet", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		path := filepath.Join(t.TempDir(), StateFile)
		require.Empty(t, gh.updater("0.3.0").Background(t.Context(), path, now))
		require.True(t, now.Equal(LoadState(path).LastChecked))
	})

	t.Run("failure is quiet and retried next start", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		gh.status = 500
		path := filepath.Join(t.TempDir(), StateFile)
		u := gh.updater("0.2.0")
		require.Empty(t, u.Background(t.Context(), path, now))
		require.True(t, LoadState(path).LastChecked.IsZero())
		require.Empty(t, u.Background(t.Context(), path, now.Add(time.Minute)))
		require.Equal(t, int32(2), gh.checks.Load())
	})

	t.Run("dev builds never check", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		gh.setRelease("v0.3.0", nil, true)
		path := filepath.Join(t.TempDir(), StateFile)
		require.Empty(t, gh.updater("0.1.0-dev.abc1234").Background(t.Context(), path, now))
		require.Zero(t, gh.checks.Load())
		require.NoFileExists(t, path)
	})
}

func TestBackgroundCheckDisabledByEnv(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"1", true},
		{"true", true},
		{"yes", true},
	} {
		t.Setenv(NoCheckEnv, tt.value)
		require.Equal(t, tt.want, BackgroundCheckDisabledByEnv(), tt.value)
	}
}
