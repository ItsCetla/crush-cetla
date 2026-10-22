package swarm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/stretchr/testify/require"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	dir := StoreDir(t.TempDir())
	s, err := NewStore(dir)
	require.NoError(t, err)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	older := control.SwarmRun{
		ID:        "20260101-120000-aaaa",
		MissionID: "m1",
		Created:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Assignments: []control.SwarmAssignment{
			{Machine: "alpha", Criteria: []int{1}, Status: control.SwarmRunning},
		},
	}
	newer := control.SwarmRun{
		ID:        "20260102-120000-bbbb",
		MissionID: "m2",
		Created:   time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, s.Save(ctx, older))
	require.NoError(t, s.Save(ctx, newer))

	got, err := s.Get(older.ID)
	require.NoError(t, err)
	require.Equal(t, "m1", got.MissionID)
	require.Equal(t, control.SwarmRunning, got.Assignments[0].Status)

	updated, err := s.Update(ctx, older.ID, func(r *control.SwarmRun) error {
		r.Assignment("alpha").Status = control.SwarmDone
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, control.SwarmDone, updated.Assignments[0].Status)
	got, err = s.Get(older.ID)
	require.NoError(t, err)
	require.Equal(t, control.SwarmDone, got.Assignments[0].Status)

	runs, err := s.List()
	require.NoError(t, err)
	require.Len(t, runs, 2)
	require.Equal(t, newer.ID, runs[0].ID, "newest first")

	// Find takes the full id or a unique prefix.
	got, err = s.Find("20260102")
	require.NoError(t, err)
	require.Equal(t, newer.ID, got.ID)
	_, err = s.Find("2026")
	require.ErrorContains(t, err, "matches 2 swarm runs")
	_, err = s.Find("nope")
	require.ErrorIs(t, err, ErrRunNotFound)
	_, err = s.Find("")
	require.Error(t, err)
}

func TestStoreRejectsBadIDs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "swarms"))
	require.NoError(t, err)

	for _, id := range []string{"", "../escape", "a/b", "UPPER", "x.json"} {
		require.Error(t, s.Save(ctx, control.SwarmRun{ID: id}), id)
		_, err := s.Get(id)
		require.Error(t, err, id)
	}
	_, err = os.Stat(filepath.Join(dir, "escape.json"))
	require.True(t, os.IsNotExist(err))
}

func TestNewRunIDIsValid(t *testing.T) {
	t.Parallel()

	id := newRunID(time.Date(2026, 10, 22, 9, 30, 0, 0, time.UTC))
	require.True(t, validRunID(id))
	require.Regexp(t, `^20261022-093000-[0-9a-f]{4}$`, id)
}
