package control

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/lock"
	"github.com/stretchr/testify/require"
)

func TestParseCriterion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, text, check string
	}{
		{"Tests pass :: go test ./...", "Tests pass", "go test ./..."},
		{"  Lint is clean  ::  golangci-lint run  ", "Lint is clean", "golangci-lint run"},
		{"Works by hand", "Works by hand", ""},
		{"Rename pkg::Type everywhere", "Rename pkg::Type everywhere", ""},
		{"Builds :: go build ./... :: extra", "Builds", "go build ./... :: extra"},
	} {
		text, check := ParseCriterion(tc.in)
		require.Equal(t, tc.text, text, tc.in)
		require.Equal(t, tc.check, check, tc.in)
	}

	var m Mission
	id := m.AddCriterion("Tests pass :: go test ./...")
	require.Equal(t, "Tests pass", m.Criterion(id).Text)
	require.Equal(t, "go test ./...", m.Criterion(id).Check)
	require.True(t, m.Criterion(id).SelfChecking())
	id = m.AddCriterion("By hand")
	require.False(t, m.Criterion(id).SelfChecking())
}

func TestRunCheck(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()

	r := RunCheck(ctx, dir, "echo ok", time.Minute, nil)
	require.True(t, r.Passed())
	require.Equal(t, 0, r.Exit)
	require.Equal(t, "ok", r.Output)
	require.False(t, r.At.IsZero())

	r = RunCheck(ctx, dir, "echo boom >&2; exit 3", time.Minute, nil)
	require.False(t, r.Passed())
	require.Equal(t, 3, r.Exit)
	require.Equal(t, "boom", r.Output)

	// Checks run in the directory they're given.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0o644))
	require.True(t, RunCheck(ctx, dir, "test -f marker", time.Minute, nil).Passed())
}

func TestRunCheckTimeout(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("sleep isn't available everywhere on Windows")
	}
	start := time.Now()
	r := RunCheck(t.Context(), t.TempDir(), "sleep 5", 200*time.Millisecond, nil)
	require.Less(t, time.Since(start), 5*time.Second)
	require.True(t, r.TimedOut)
	require.Equal(t, CheckTimedOut, r.Exit)
	require.False(t, r.Passed())
	require.Contains(t, r.Output, "timed out")
}

func TestRunCheckCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := RunCheck(ctx, t.TempDir(), "echo never", time.Minute, nil)
	require.True(t, r.Canceled)
	require.False(t, r.Passed())
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	t.Parallel()
	b := &tailBuffer{limit: checkOutputLimit}
	for i := range 2000 {
		_, _ = b.Write([]byte("line " + strings.Repeat("x", i%7) + "\n"))
	}
	_, _ = b.Write([]byte("\x1b[31mFAIL: the last line\x1b[0m\n"))
	out := b.String()
	require.True(t, strings.HasPrefix(out, "…\n"), "a cut tail is marked")
	require.LessOrEqual(t, len(out), checkOutputLimit+len("…\n"))
	require.True(t, strings.HasSuffix(out, "FAIL: the last line"), "ANSI is stripped and the end kept")
	require.True(t, strings.HasPrefix(strings.TrimPrefix(out, "…\n"), "line "), "the tail starts on a whole line")

	c := Criterion{LastOutput: out}
	require.True(t, strings.HasPrefix(c.FirstOutputLine(), "line "))

	short := &tailBuffer{limit: checkOutputLimit}
	_, _ = short.Write([]byte("  short\n"))
	require.Equal(t, "short", short.String())
}

func newCheckMissions(t *testing.T) (*Missions, string) {
	t.Helper()
	ms, err := NewMissions(t.TempDir())
	require.NoError(t, err)
	work := t.TempDir()
	ms.SetWorkDir(work)
	return ms, work
}

func TestMissionsVerify(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	ms, work := newCheckMissions(t)
	require.Equal(t, work, ms.WorkDir())

	m, err := ms.Create(ctx, "Ship", "", []string{
		"Passes :: exit 0",
		"Fails :: echo nope; exit 1",
		"By hand",
	}, "s1")
	require.NoError(t, err)
	require.True(t, m.HasOpenChecks())

	m, ran, err := ms.Verify(ctx, m.ID, VerifyOptions{})
	require.NoError(t, err)
	require.Equal(t, []int{1, 2}, ran)
	pass, fail := m.Criterion(1), m.Criterion(2)
	require.True(t, pass.Done)
	require.Equal(t, CheckedByCheck, pass.CheckedBy)
	require.True(t, pass.Ran())
	require.False(t, fail.Done)
	require.Equal(t, 1, fail.LastExit)
	require.Equal(t, "nope", fail.FirstOutputLine())
	require.False(t, m.Criterion(3).Ran())
	require.Contains(t, CheckSummary(m, ran), "1 check failed: 2. Fails (exit 1): nope")
	require.Contains(t, CheckFailure(*fail), "Command: echo nope; exit 1")

	// Results persist.
	got, err := ms.Get(m.ID)
	require.NoError(t, err)
	require.Equal(t, CheckedByCheck, got.Criterion(1).CheckedBy)
	require.Equal(t, 1, got.Criterion(2).LastExit)

	// OpenOnly skips what's already ticked.
	_, ran, err = ms.Verify(ctx, m.ID, VerifyOptions{OpenOnly: true})
	require.NoError(t, err)
	require.Equal(t, []int{2}, ran)

	// A single criterion, and one that can't check itself.
	_, ran, err = ms.Verify(ctx, m.ID, VerifyOptions{Criterion: 1})
	require.NoError(t, err)
	require.Equal(t, []int{1}, ran)
	_, _, err = ms.Verify(ctx, m.ID, VerifyOptions{Criterion: 3})
	require.ErrorContains(t, err, "no check command")
	_, _, err = ms.Verify(ctx, m.ID, VerifyOptions{Criterion: 9})
	require.Error(t, err)

	// A passing check that later fails unticks its criterion.
	m, err = ms.Update(ctx, m.ID, func(m *Mission) error {
		m.Criterion(1).Check = "exit 2"
		return nil
	})
	require.NoError(t, err)
	m, _, err = ms.Verify(ctx, m.ID, VerifyOptions{Criterion: 1})
	require.NoError(t, err)
	require.False(t, m.Criterion(1).Done)
	require.Empty(t, m.Criterion(1).CheckedBy)
	require.Equal(t, "Passes (exit 2)", strings.TrimPrefix(CheckSummary(m, []int{1}), "1 check failed: 1. "))
}

func TestMissionsVerifyNoWait(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	ms, _ := newCheckMissions(t)
	m, err := ms.Create(ctx, "Ship", "", []string{"Passes :: exit 0"}, "s1")
	require.NoError(t, err)

	release, err := lock.TryFile(ms.checkLockPath(m.ID))
	require.NoError(t, err)
	_, _, err = ms.Verify(ctx, m.ID, VerifyOptions{NoWait: true})
	require.ErrorIs(t, err, ErrCheckRunning)
	release()

	_, ran, err := ms.Verify(ctx, m.ID, VerifyOptions{NoWait: true})
	require.NoError(t, err)
	require.Equal(t, []int{1}, ran)

	require.NoError(t, ms.Delete(ctx, m.ID))
	_, err = os.Stat(ms.checkLockPath(m.ID))
	require.True(t, os.IsNotExist(err), "delete removes the check lock")
}

func TestMissionLoadsWithoutCheckFields(t *testing.T) {
	t.Parallel()
	ms, _ := newCheckMissions(t)
	old := `{"id":"old1","title":"Old","status":"active","criteria":[{"id":1,"text":"a","done":true}],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	require.NoError(t, os.WriteFile(filepath.Join(ms.Dir(), "old1.json"), []byte(old), 0o644))
	m, err := ms.Get("old1")
	require.NoError(t, err)
	c := m.Criterion(1)
	require.NotNil(t, c)
	require.True(t, c.Done)
	require.False(t, c.SelfChecking())
	require.False(t, c.Ran())
	require.Equal(t, DefaultCheckTimeout, c.CheckTimeout())
	require.False(t, m.HasOpenChecks())
}

func TestHubSelfCheckingCriteria(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, _ := newTestHub(t, Budget{})
	h.missions.SetWorkDir(t.TempDir())

	r, err := h.Do(ctx, Command{Op: OpMissionCreate, SessionID: "s1", Title: "Ship", Criteria: []string{"Fails :: echo broken; exit 1"}})
	require.NoError(t, err)
	id := r.Mission.ID
	require.Equal(t, "echo broken; exit 1", r.Mission.Criterion(1).Check)

	r, err = h.Do(ctx, Command{Op: OpMissionAdd, MissionID: id, Text: "Passes", Check: "exit 0"})
	require.NoError(t, err)
	require.Equal(t, "exit 0", r.Mission.Criterion(2).Check)
	r, err = h.Do(ctx, Command{Op: OpMissionAdd, MissionID: id, Text: "Also passes :: true"})
	require.NoError(t, err)
	require.Equal(t, "true", r.Mission.Criterion(3).Check)
	r, err = h.Do(ctx, Command{Op: OpMissionAdd, MissionID: id, Text: "By hand"})
	require.NoError(t, err)

	// Ticking a self-checking criterion runs its check.
	r, err = h.Do(ctx, Command{Op: OpMissionCheck, MissionID: id, Index: 1})
	require.NoError(t, err)
	require.False(t, r.Mission.Criterion(1).Done)
	require.Equal(t, "Check failed (exit 1), so 1 stays open: broken", r.Message)

	r, err = h.Do(ctx, Command{Op: OpMissionCheck, MissionID: id, Index: 2, Text: "ran it"})
	require.NoError(t, err)
	require.True(t, r.Mission.Criterion(2).Done)
	require.Equal(t, CheckedByCheck, r.Mission.Criterion(2).CheckedBy)
	require.Equal(t, "ran it", r.Mission.Criterion(2).Note)
	require.Equal(t, "Check passed.", r.Message)

	// A manual criterion is ticked by the person.
	r, err = h.Do(ctx, Command{Op: OpMissionCheck, MissionID: id, Index: 4})
	require.NoError(t, err)
	require.Equal(t, CheckedByUser, r.Mission.Criterion(4).CheckedBy)
	r, err = h.Do(ctx, Command{Op: OpMissionUncheck, MissionID: id, Index: 4})
	require.NoError(t, err)
	require.False(t, r.Mission.Criterion(4).Done)
	require.Empty(t, r.Mission.Criterion(4).CheckedBy)

	r, err = h.Do(ctx, Command{Op: OpMissionVerify, MissionID: id})
	require.NoError(t, err)
	require.True(t, r.Mission.Criterion(3).Done)
	require.Equal(t, "1 check failed: 1. Fails (exit 1): broken", r.Message)

	// The prompt tells the agent which criteria check themselves.
	require.Contains(t, h.MissionContext("s1"), "self-checking: `echo broken; exit 1`")
}

func TestHubVerifyEmptyMission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, _ := newTestHub(t, Budget{})
	r, err := h.Do(ctx, Command{Op: OpMissionCreate, SessionID: "s1", Title: "Ship", Criteria: []string{"By hand"}})
	require.NoError(t, err)
	r, err = h.Do(ctx, Command{Op: OpMissionVerify, MissionID: r.Mission.ID})
	require.NoError(t, err)
	require.Equal(t, "This mission has no self-checking criteria.", r.Message)
}

func TestHubVerifyAfterTurn(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("sleep isn't available everywhere on Windows")
	}
	ctx := context.Background()
	h, _ := newTestHub(t, Budget{})
	h.missions.SetWorkDir(t.TempDir())
	r, err := h.Do(ctx, Command{Op: OpMissionCreate, SessionID: "s1", Title: "Ship", Criteria: []string{"Passes :: exit 0"}})
	require.NoError(t, err)
	id := r.Mission.ID

	h.VerifyAfterTurn("nobody")
	h.VerifyAfterTurn("s1")
	h.bgWG.Wait()

	m, err := h.missions.Get(id)
	require.NoError(t, err)
	require.True(t, m.Criterion(1).Done)
	require.Equal(t, CheckedByCheck, m.Criterion(1).CheckedBy)

	// Close stops a run that's still going.
	_, err = h.Do(ctx, Command{Op: OpMissionAdd, MissionID: id, Text: "Slow :: sleep 30"})
	require.NoError(t, err)
	h.VerifyAfterTurn("s1")
	start := time.Now()
	require.NoError(t, h.Close(ctx))
	require.Less(t, time.Since(start), 20*time.Second)
	m, err = h.missions.Get(id)
	require.NoError(t, err)
	require.False(t, m.Criterion(2).Ran(), "a canceled run isn't recorded")

	// A nil hub is safe.
	var nilHub *Hub
	nilHub.VerifyAfterTurn("s1")
	require.NoError(t, nilHub.Close(ctx))
}

func TestRunMissionToolChecks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	ms, _ := newCheckMissions(t)
	run := func(p MissionParams) (string, error) { return RunMissionTool(ctx, ms, "s1", p) }

	_, err := run(MissionParams{Action: "create", Title: "Ship", Criteria: []string{"Broken :: echo FAIL: TestX; exit 1", "By hand"}})
	require.NoError(t, err)
	_, err = run(MissionParams{Action: "verify"})
	require.NoError(t, err)

	// A failing check leaves the criterion open and hands back the output.
	_, err = run(MissionParams{Action: "check", ID: 1})
	require.ErrorContains(t, err, "FAIL: TestX")
	require.ErrorContains(t, err, "exit 1")

	// Fixing the check ticks it; the note is kept.
	out, err := run(MissionParams{Action: "set_check", ID: 1, Check: "exit 0", TimeoutSeconds: 30})
	require.NoError(t, err)
	require.Contains(t, out, "self-checking: `exit 0`")
	out, err = run(MissionParams{Action: "check", ID: 1, Text: "fixed the test"})
	require.NoError(t, err)
	require.Contains(t, out, "The check passed, so criterion 1 is ticked.")
	m, ok := ms.ForSession("s1")
	require.True(t, ok)
	c := m.Criterion(1)
	require.True(t, c.Done)
	require.Equal(t, CheckedByCheck, c.CheckedBy)
	require.Equal(t, "fixed the test", c.Note)
	require.Equal(t, 30*time.Second, c.CheckTimeout())

	// Changing the check unticks what the old one ticked.
	_, err = run(MissionParams{Action: "set_check", ID: 1, Check: "exit 4"})
	require.NoError(t, err)
	m, _ = ms.ForSession("s1")
	require.False(t, m.Criterion(1).Done)
	require.False(t, m.Criterion(1).Ran())

	out, err = run(MissionParams{Action: "verify"})
	require.NoError(t, err)
	require.Contains(t, out, "1 check failed")
	require.Contains(t, out, "Command: exit 4")

	// Manual criteria still need a note, and are signed by the agent.
	_, err = run(MissionParams{Action: "check", ID: 2})
	require.Error(t, err)
	_, err = run(MissionParams{Action: "check", ID: 2, Text: "looked"})
	require.NoError(t, err)
	m, _ = ms.ForSession("s1")
	require.Equal(t, CheckedByAgent, m.Criterion(2).CheckedBy)

	// Clearing the check makes it manual again; verify then has nothing.
	_, err = run(MissionParams{Action: "set_check", ID: 1})
	require.NoError(t, err)
	_, err = run(MissionParams{Action: "verify"})
	require.ErrorContains(t, err, "no self-checking criteria")

	_, err = run(MissionParams{Action: "add_criterion", Text: "Vet :: go vet ./..."})
	require.NoError(t, err)
	m, _ = ms.ForSession("s1")
	require.Equal(t, "go vet ./...", m.Criterion(3).Check)
}

func TestMissionParamsChecks(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"go test ./..."}, MissionParams{Action: "create", Criteria: []string{"a", "b :: go test ./..."}}.Checks())
	require.Equal(t, []string{"make"}, MissionParams{Action: "add_criterion", Text: "x :: ignored", Check: "make"}.Checks())
	require.Equal(t, []string{"ls"}, MissionParams{Action: "add_criterion", Text: "x :: ls"}.Checks())
	require.Equal(t, []string{"true"}, MissionParams{Action: "SET_CHECK", Check: " true "}.Checks())
	require.Empty(t, MissionParams{Action: "set_check"}.Checks(), "clearing a check needs no permission")
	require.Empty(t, MissionParams{Action: "check", Check: "rm -rf /"}.Checks())
}
