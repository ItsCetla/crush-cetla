package control

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScrubEnv(t *testing.T) {
	t.Parallel()
	got := ScrubEnv([]string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"GOPATH=/home/me/go",
		"LANG=en_US.UTF-8",
		"PASSTHROUGH=1",
		"ANTHROPIC_API_KEY=sk-1",
		"OPENAI_API_KEY=sk-2",
		"MY_APIKEY=x",
		"GITHUB_TOKEN=x",
		"db_password=x",
		"AWS_REGION=us-east-1",
		"SSH_AUTH_SOCK=/tmp/agent",
		"STRIPE_SECRET=x",
		"NPM_CONFIG_PREFIX=x",
		"TADA_NO_RICH=1",
		"MALFORMED",
	})
	require.Equal(t, []string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"GOPATH=/home/me/go",
		"LANG=en_US.UTF-8",
		"PASSTHROUGH=1",
		"MALFORMED",
	}, got)
}

func TestRunPlannedChecksSkipsChangedChecks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	ms, work := newCheckMissions(t)
	ms.SetApprovals(filepath.Join(t.TempDir(), ApprovalsFileName))
	m, err := ms.Create(ctx, "Ship", "", []string{"One :: exit 0", "Two :: exit 0"}, "")
	require.NoError(t, err)
	require.NoError(t, ms.ApproveChecks(ctx, "exit 0", "exit 1"))

	planned, err := ms.PlanChecksIn(m.ID, []int{1, 2})
	require.NoError(t, err)
	require.Len(t, planned, 2)
	for _, c := range planned {
		require.Empty(t, c.Skipped)
	}

	// The second check changes after the user was asked about it.
	_, err = ms.Update(ctx, m.ID, func(m *Mission) error {
		m.Criterion(2).Check = "exit 1"
		return nil
	})
	require.NoError(t, err)

	got, err := ms.RunPlannedChecks(ctx, m.ID, work, planned)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.True(t, got[0].Passed)
	require.Empty(t, got[0].Skipped)
	require.False(t, got[1].Passed)
	require.Equal(t, "the check changed while the swarm ran", got[1].Skipped)
}
