package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

func TestApplyControlPolicy(t *testing.T) {
	t.Parallel()

	state := func(cfg *config.Config) permission.Policy {
		perms := permission.NewPermissionService(t.TempDir(), false, nil, nil)
		applyControlPolicy(cfg, perms)
		return perms.(permission.Controller).ControlState().Policy
	}

	// Defaults: delete always asks, no auto budget.
	p := state(&config.Config{})
	require.Equal(t, []permission.Class{permission.ClassDelete}, p.AlwaysAsk)
	require.Empty(t, p.AutoBudget)

	// An explicit list replaces the default; unknown and duplicate
	// classes are dropped.
	p = state(&config.Config{Permissions: &config.Permissions{
		AlwaysAsk:  []string{"network", "bogus", "network"},
		AutoBudget: map[string]int{"outside": 3, "nope": 1},
	}})
	require.Equal(t, []permission.Class{permission.ClassNetwork}, p.AlwaysAsk)
	require.Equal(t, map[permission.Class]int{permission.ClassOutside: 3}, p.AutoBudget)

	// An empty list turns it off.
	p = state(&config.Config{Permissions: &config.Permissions{AlwaysAsk: []string{}}})
	require.Empty(t, p.AlwaysAsk)
}

func TestBudgetFromConfig(t *testing.T) {
	t.Parallel()
	require.Equal(t, control.Budget{}, budgetFromConfig(nil))
	b := budgetFromConfig(&config.BudgetOptions{Daily: 5, Monthly: 50, Session: 1, WarnAt: []int{80}})
	require.Equal(t, control.Budget{Daily: 5, Monthly: 50, Session: 1, WarnAt: []int{80}}, b)
}

func TestControlPathsFor(t *testing.T) {
	t.Parallel()
	p := controlPathsFor(&config.Config{Options: &config.Options{}})
	require.NotEmpty(t, p.ledger)
	require.Empty(t, p.actions)
	require.Empty(t, p.missions)

	dir := t.TempDir()
	p = controlPathsFor(&config.Config{Options: &config.Options{DataDirectory: dir}})
	require.Equal(t, filepath.Join(dir, "control", "actions.jsonl"), p.actions)
	require.Equal(t, filepath.Join(dir, "control", "missions"), p.missions)
}

func TestReadControlHubCreatesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := config.NewTestStore(&config.Config{Options: &config.Options{DataDirectory: dir}})

	hub := ReadControlHub(store)
	require.NotNil(t, hub)
	reply, err := hub.Do(t.Context(), control.Command{Op: control.OpSnapshot})
	require.NoError(t, err)
	require.NotNil(t, reply.Snapshot)

	_, err = os.Stat(filepath.Join(dir, "control"))
	require.True(t, os.IsNotExist(err), "a read-only hub must not create the control directory")
}
