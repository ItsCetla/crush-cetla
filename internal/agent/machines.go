package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fleet"
)

// machineAgents builds and caches one sub-agent per machine and model, so
// agent tool calls can run on the user's other computers.
type machineAgents struct {
	c      *coordinator
	prompt *prompt.Prompt
	task   config.Agent

	mu     sync.Mutex
	agents map[string]SessionAgent
}

func newMachineAgents(c *coordinator, p *prompt.Prompt, task config.Agent) *machineAgents {
	return &machineAgents{c: c, prompt: p, task: task, agents: map[string]SessionAgent{}}
}

// get returns the sub-agent for m, building it the first time.
func (ma *machineAgents) get(ctx context.Context, m fleet.Machine) (SessionAgent, error) {
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%d", m.Name, m.Model, m.URL, m.ContextWindow)
	ma.mu.Lock()
	defer ma.mu.Unlock()
	if a, ok := ma.agents[key]; ok {
		return a, nil
	}
	a, err := ma.c.buildMachineAgent(ctx, ma.prompt, ma.task, m)
	if err != nil {
		return nil, err
	}
	ma.agents[key] = a
	return a, nil
}

// buildMachineModel returns a model that runs on the machine's server.
func (c *coordinator) buildMachineModel(ctx context.Context, m fleet.Machine) (Model, error) {
	cfg := c.cfg.Config()
	pc, ok := cfg.MachineProvider(m.Name)
	if !ok {
		return Model{}, fmt.Errorf("machine %s has no provider", m.Name)
	}
	sel := config.SelectedModel{Provider: m.Name, Model: m.Model}
	var cm catwalk.Model
	if listed := cfg.GetModel(m.Name, m.Model); listed != nil {
		cm = *listed
	} else {
		// The machine came online after startup, so its provider
		// doesn't list the model yet.
		cm = cfg.MachineModelDefaults(m.Name, m.Model, m.Live.ContextWindow)
	}
	// Each slot gets only its share of the server's context.
	if w := m.Live.ContextWindow; w > 0 {
		cm.ContextWindow = w
		cm.DefaultMaxTokens = min(cm.DefaultMaxTokens, w/4)
	}
	provider, err := c.buildProvider(pc, sel, true)
	if err != nil {
		return Model{}, err
	}
	lm, err := provider.LanguageModel(ctx, m.Model)
	if err != nil {
		return Model{}, err
	}
	lm = newRequestTimeoutModel(lm, cfg.Options.GetRequestTimeout())
	return Model{Model: lm, CatwalkCfg: cm, ModelCfg: sel, FlatRate: true}, nil
}

// buildMachineAgent builds a sub-agent whose large and small models both
// run on the machine. Unlike buildAgent it finishes setup before
// returning, because it is built on demand in the middle of a tool call.
func (c *coordinator) buildMachineAgent(ctx context.Context, p *prompt.Prompt, task config.Agent, m fleet.Machine) (SessionAgent, error) {
	model, err := c.buildMachineModel(ctx, m)
	if err != nil {
		return nil, err
	}
	pc, _ := c.cfg.Config().MachineProvider(m.Name)
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPromptPrefix:   pc.SystemPromptPrefix,
		IsSubAgent:           true,
		DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
		IsYolo:               c.permissions.SkipRequests(),
		Sessions:             c.sessions,
		Messages:             c.messages,
		Cfg:                  c.cfg,
		Notify:               c.notify,
		RunComplete:          c.runComplete,
		Permissions:          c.permissions,
		Questions:            c.questions,
		WorkDir:              c.cfg.WorkingDir(),
		Control:              c.agentControl(),
	})
	initCtx := context.WithoutCancel(ctx)
	systemPrompt, err := p.Build(initCtx, model.Model.Provider(), model.Model.Model(), c.cfg)
	if err != nil {
		return nil, err
	}
	a.SetSystemPrompt(systemPrompt)
	tools, err := c.buildTools(initCtx, task, true)
	if err != nil {
		return nil, err
	}
	a.SetTools(tools)
	return a, nil
}

// machineRun is where an agent tool call runs.
type machineRun struct {
	agent   SessionAgent
	lease   *fleet.Lease
	machine fleet.Machine
}

// pickMachine leases a slot for an agent tool call. It returns a nil
// agent when the call should run on the main task agent.
func (c *coordinator) pickMachine(ctx context.Context, ma *machineAgents, name string) (machineRun, error) {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, fleet.Main) {
		return machineRun{}, nil
	}
	auto := name == "" || strings.EqualFold(name, fleet.Auto)
	cfg := c.cfg.Config()
	if auto && len(cfg.Machines) == 0 {
		return machineRun{}, nil
	}
	lease, err := fleet.Default().Acquire(ctx, cfg, name)
	if err != nil {
		if auto && errors.Is(err, fleet.ErrNoneFree) {
			return machineRun{}, nil
		}
		if errors.Is(err, fleet.ErrNoneFree) {
			err = fmt.Errorf("every slot on %s is busy", name)
		}
		return machineRun{}, err
	}
	agent, err := ma.get(ctx, lease.Machine)
	if err != nil {
		lease.Release()
		if auto {
			return machineRun{}, nil
		}
		return machineRun{}, fmt.Errorf("can't run on %s: %w", lease.Machine.Name, err)
	}
	return machineRun{agent: agent, lease: lease, machine: lease.Machine}, nil
}

// machinesToolHint is appended to the agent tool's description when the
// user has machines.
func machinesToolHint(cfg *config.Config) string {
	var lines []string
	for _, m := range fleet.Machines(cfg) {
		if m.Usable() {
			lines = append(lines, "- "+fleet.Describe(m))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\nSet machine to run the agent on one of the user's other computers. " +
		"The default, auto, uses the machine with the most free slots and falls back to running here when all are busy; " +
		"main always runs here. Machines run weaker local models, so give them small, specific tasks. Machines:\n" +
		strings.Join(lines, "\n")
}

// machineResult labels a response with where it ran.
func machineResult(resp fantasy.ToolResponse, m fleet.Machine) fantasy.ToolResponse {
	label := fmt.Sprintf("[ran on %s (%s)]\n", m.Name, m.Model)
	resp.Content = label + resp.Content
	return resp
}
