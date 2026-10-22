package control

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

func TestApprovalsPersistPerProject(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control", ApprovalsFileName)
	projA, projB := t.TempDir(), t.TempDir()

	a, err := NewMissions(t.TempDir())
	require.NoError(t, err)
	a.SetWorkDir(projA)
	a.SetApprovals(path)
	require.False(t, a.CheckApproved("go test ./..."))
	require.NoError(t, a.ApproveChecks(ctx, " go test ./... ", "", "make"))
	require.True(t, a.CheckApproved("go test ./..."))
	require.True(t, a.CheckApproved("make"))

	// A second process sees the approval through the file.
	a2, err := NewMissions(t.TempDir())
	require.NoError(t, err)
	a2.SetWorkDir(projA)
	a2.SetApprovals(path)
	require.True(t, a2.CheckApproved("go test ./..."))

	// Another project doesn't.
	b, err := NewMissions(t.TempDir())
	require.NoError(t, err)
	b.SetWorkDir(projB)
	b.SetApprovals(path)
	require.False(t, b.CheckApproved("go test ./..."))

	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

// A mission file that arrives with the repository, never approved by the
// user, mustn't have its checks run by the agent or after a turn.
func TestPlantedMissionChecksDontRun(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("touch isn't available everywhere on Windows")
	}
	ctx := t.Context()
	h, _ := newTestHub(t, Budget{})
	work := t.TempDir()
	h.missions.SetWorkDir(work)
	h.missions.SetApprovals(filepath.Join(t.TempDir(), ApprovalsFileName))

	// Written straight to the store, as a planted file would be.
	m, err := h.missions.Create(ctx, "Planted", "", []string{"Owned :: touch pwned"}, "")
	require.NoError(t, err)

	out, err := RunMissionTool(ctx, h.missions, "s1", MissionParams{Action: "attach", MissionID: m.ID})
	require.NoError(t, err, out)
	_, err = RunMissionTool(ctx, h.missions, "s1", MissionParams{Action: "verify"})
	require.ErrorContains(t, err, "hasn't approved")
	_, err = RunMissionTool(ctx, h.missions, "s1", MissionParams{Action: "check", ID: 1})
	require.ErrorContains(t, err, "hasn't approved")

	h.VerifyAfterTurn("s1")
	h.bgWG.Wait()
	require.NoFileExists(t, filepath.Join(work, "pwned"))
	got, err := h.missions.Get(m.ID)
	require.NoError(t, err)
	require.False(t, got.Criterion(1).Ran())

	// Setting the check again, which asks the user, approves it.
	_, err = RunMissionTool(ctx, h.missions, "s1", MissionParams{Action: "set_check", ID: 1, Check: "touch pwned"})
	require.NoError(t, err)
	_, err = RunMissionTool(ctx, h.missions, "s1", MissionParams{Action: "verify"})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(work, "pwned"))
}

// Running a check from the cockpit is the user's approval.
func TestHubVerifyApprovesChecks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h, _ := newTestHub(t, Budget{})
	h.missions.SetWorkDir(t.TempDir())
	m, err := h.missions.Create(ctx, "Planted", "", []string{"Passes :: exit 0"}, "s1")
	require.NoError(t, err)
	require.False(t, h.missions.CheckApproved("exit 0"))

	_, err = h.Do(ctx, Command{Op: OpMissionVerify, MissionID: m.ID})
	require.NoError(t, err)
	require.True(t, h.missions.CheckApproved("exit 0"))
}

func TestRunCheckAppliesBlockList(t *testing.T) {
	t.Parallel()
	block := []shell.BlockFunc{shell.ArgumentsBlocker("go", []string{"test"}, []string{"-exec"})}
	r := RunCheck(t.Context(), t.TempDir(), "go test -exec /bin/sh ./...", time.Minute, block)
	require.False(t, r.Passed())
	require.Contains(t, r.Output, "not allowed")
}
