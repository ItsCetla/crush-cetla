package app

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/swarm"
	"github.com/charmbracelet/crush/internal/worktree"
)

// swarmRunner is the part of the coordinator that runs swarm agents.
type swarmRunner interface {
	RunSwarmAgent(ctx context.Context, req agent.SwarmAgentRequest) (agent.SwarmAgentResult, error)
}

// setupSwarm gives the control hub a swarm engine. Swarms need missions
// and a data directory; without them the hub reports swarms as
// unavailable.
func (app *App) setupSwarm(store *config.ConfigStore) {
	ms := app.Control.Missions()
	dataDir := store.Config().Options.DataDirectory
	if ms == nil || dataDir == "" {
		return
	}
	st, err := swarm.NewStore(swarm.StoreDir(filepath.Join(dataDir, "control")))
	if err != nil {
		slog.Warn("Swarms unavailable", "error", err)
		return
	}
	engine, err := swarm.NewEngine(swarm.Options{
		Missions: ms,
		Store:    st,
		WorkDir:  store.WorkingDir(),
		Worktree: []worktree.Option{worktree.WithDir(store.Config().Options.WorktreeDir)},
		Machines: func(ctx context.Context) []fleet.Status {
			return fleet.Default().Check(ctx, store.Config())
		},
		Acquire: func(ctx context.Context, name string) (*fleet.Lease, error) {
			return fleet.Default().Acquire(ctx, store.Config(), name)
		},
		Agent: app.runSwarmAgent,
		Ready: app.swarmReady,
	})
	if err != nil {
		slog.Warn("Swarms unavailable", "error", err)
		return
	}
	app.swarm = engine
	app.Control.SetSwarm(engine)
}

// swarmReady refuses to start a swarm while auto mode would approve its
// agents' tool calls without asking.
func (app *App) swarmReady() error {
	if app.Permissions != nil && app.Permissions.SkipRequests() {
		return agent.ErrSwarmAutoMode
	}
	return nil
}

// runSwarmAgent runs one machine's agent through the coordinator, which
// is built after the hub, so it is looked up when the swarm starts.
func (app *App) runSwarmAgent(ctx context.Context, job swarm.AgentJob) (swarm.AgentResult, error) {
	r, ok := app.AgentCoordinator.(swarmRunner)
	if !ok || r == nil {
		return swarm.AgentResult{}, errors.New("the agent isn't ready yet")
	}
	res, err := r.RunSwarmAgent(ctx, agent.SwarmAgentRequest{
		Machine: job.Machine,
		WorkDir: job.WorkDir,
		Prompt:  job.Prompt,
		Title:   job.Title,
		Session: job.Session,
	})
	return swarm.AgentResult{SessionID: res.SessionID, Cost: res.Cost, Text: res.Text}, err
}
