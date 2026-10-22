package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/charmbracelet/crush/internal/permission"
)

// newControlHub builds the control hub. The usage ledger is global, so
// budgets count every project; the action log and missions belong to the
// project's data directory. A store that can't be opened is left out
// and the hub reports it as unavailable.
func newControlHub(store *config.ConfigStore, perms permission.Service) *control.Hub {
	cfg := store.Config()
	applyControlPolicy(cfg, perms)

	paths := controlPathsFor(cfg)
	opts := control.HubOptions{
		Permissions: perms,
		Project:     store.WorkingDir(),
		// The network receipt is per process: it lives as long as this
		// app and is never written to disk.
		Network: netlog.Default(),
	}
	if l, err := control.NewLedger(paths.ledger); err != nil {
		slog.Warn("Usage ledger unavailable", "error", err)
	} else {
		opts.Ledger = l
	}
	if paths.actions != "" {
		if a, err := control.OpenActionLog(paths.actions); err != nil {
			slog.Warn("Action log unavailable", "error", err)
		} else {
			opts.Actions = a
		}
		if m, err := control.NewMissions(paths.missions); err != nil {
			slog.Warn("Missions unavailable", "error", err)
		} else {
			configureMissions(m, store.WorkingDir(), paths)
			opts.Missions = m
		}
	}
	opts.Budget = budgetFromConfig(cfg.Options.Budget)
	return control.NewHub(opts)
}

// configureMissions points missions at the project, the global list of
// check commands the user approved, and the bash tool's block list.
// Approvals live outside the project so a repository can't approve its
// own checks.
func configureMissions(m *control.Missions, workDir string, paths controlPaths) {
	m.SetWorkDir(workDir)
	m.SetApprovals(filepath.Join(paths.ledger, control.ApprovalsFileName))
	m.SetBlockFuncs(tools.BlockFuncs())
}

// controlPaths is where the control stores live.
type controlPaths struct {
	ledger   string
	actions  string
	missions string
}

func controlPathsFor(cfg *config.Config) controlPaths {
	p := controlPaths{ledger: filepath.Join(filepath.Dir(config.GlobalConfigData()), "control")}
	if dataDir := cfg.Options.DataDirectory; dataDir != "" {
		dir := filepath.Join(dataDir, "control")
		p.actions = filepath.Join(dir, "actions.jsonl")
		p.missions = filepath.Join(dir, "missions")
	}
	return p
}

// ReadControlHub builds a hub for reading only, for "tada status". It
// opens only the stores that already exist, so it never creates
// directories, and it has no permission service.
func ReadControlHub(store *config.ConfigStore) *control.Hub {
	cfg := store.Config()
	paths := controlPathsFor(cfg)
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
	policy := policyFromConfig(cfg)
	opts := control.HubOptions{
		Project: store.WorkingDir(),
		Budget:  budgetFromConfig(cfg.Options.Budget),
		Policy:  &policy,
	}
	if exists(paths.ledger) {
		if l, err := control.NewLedger(paths.ledger); err == nil {
			opts.Ledger = l
		}
	}
	if paths.actions != "" && exists(paths.actions) {
		if a, err := control.OpenActionLog(paths.actions); err == nil {
			opts.Actions = a
		}
	}
	if paths.missions != "" && exists(paths.missions) {
		if m, err := control.NewMissions(paths.missions); err == nil {
			configureMissions(m, store.WorkingDir(), paths)
			opts.Missions = m
		}
	}
	return control.NewHub(opts)
}

// applyControlPolicy hands the permissions.always_ask and auto_budget
// settings to the permission service.
func applyControlPolicy(cfg *config.Config, perms permission.Service) {
	if c, ok := perms.(permission.Controller); ok {
		c.SetPolicy(policyFromConfig(cfg))
	}
}

// policyFromConfig builds the control policy from the
// permissions.always_ask and auto_budget settings.
func policyFromConfig(cfg *config.Config) permission.Policy {
	p := permission.DefaultPolicy()
	p.AlwaysAsk = nil
	for _, name := range cfg.Permissions.AlwaysAskClasses() {
		if cl := permission.Class(name); cl.Valid() && !slices.Contains(p.AlwaysAsk, cl) {
			p.AlwaysAsk = append(p.AlwaysAsk, cl)
		} else if !cl.Valid() {
			slog.Warn("Unknown permissions.always_ask class", "class", name)
		}
	}
	if cfg.Permissions != nil && len(cfg.Permissions.AutoBudget) > 0 {
		p.AutoBudget = make(map[permission.Class]int, len(cfg.Permissions.AutoBudget))
		for name, n := range cfg.Permissions.AutoBudget {
			if cl := permission.Class(name); cl.Valid() {
				p.AutoBudget[cl] = n
			} else {
				slog.Warn("Unknown permissions.auto_budget class", "class", name)
			}
		}
	}
	return p
}

func budgetFromConfig(b *config.BudgetOptions) control.Budget {
	if b == nil {
		return control.Budget{}
	}
	return control.Budget{Daily: b.Daily, Monthly: b.Monthly, Session: b.Session, WarnAt: b.WarnAt}
}
