package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/permission"
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
		Agent:         app.runSwarmAgent,
		Ready:         app.swarmReady,
		ApproveChecks: app.approveSwarmChecks,
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

// swarmChecksToolName is the tool name the permission prompt for running
// a machine's checks shows.
const swarmChecksToolName = "swarm_checks"

// approveSwarmChecks asks, through the permission prompt, before a
// mission's checks run on code a swarm agent wrote. It never approves on
// its own, including in auto mode.
func (app *App) approveSwarmChecks(ctx context.Context, req swarm.CheckApproval) (bool, error) {
	if app.Permissions == nil || app.Permissions.SkipRequests() {
		return false, nil
	}
	cmds := make([]string, 0, len(req.Checks))
	for _, c := range req.Checks {
		cmds = append(cmds, c.Command)
	}
	return app.Permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:   req.SessionID,
		ToolCallID:  "swarm-checks-" + req.Machine,
		ToolName:    swarmChecksToolName,
		Action:      "run",
		Path:        req.WorkDir,
		Description: fmt.Sprintf("Run the mission's checks on %s's work. They run the code the agent wrote, without its secrets in their environment.", req.Machine),
		Params:      map[string]any{"commands": cmds},
	})
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
