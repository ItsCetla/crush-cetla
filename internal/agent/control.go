package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/fleet"
)

// Control is what the agent needs from the control hub: a ledger for
// usage, a budget gate, and the session's mission. A nil Control turns
// all of it off.
type Control interface {
	RecordUsage(u control.Usage)
	CheckBudget(ctx context.Context, sessionID string) error
	MissionContext(sessionID string) string
}

// usageRow prices one model call the way the session cost does, and
// says how the price is known. Rows for estimated usage are priced from
// list prices too, so a budget never undercounts a provider that didn't
// report usage.
func usageRow(model Model, sessionID, kind string, usage fantasy.Usage, overrideCost *float64, estimated bool) control.Usage {
	c := model.CatwalkCfg
	cost := c.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		c.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		c.CostPer1MIn/1e6*float64(usage.InputTokens) +
		c.CostPer1MOut/1e6*float64(usage.OutputTokens)
	priced := c.CostPer1MIn > 0 || c.CostPer1MOut > 0
	pricing := control.PricingList
	switch {
	case model.FlatRate:
		pricing = control.PricingFlat
	case overrideCost != nil && !estimated:
		pricing = control.PricingBilled
		cost = *overrideCost
	case !priced:
		pricing = control.PricingUnpriced
		cost = 0
	case estimated:
		pricing = control.PricingEstimate
	}
	if model.FlatRate && !priced {
		cost = 0
	}
	return control.Usage{
		At:         time.Now(),
		SessionID:  sessionID,
		Provider:   model.ModelCfg.Provider,
		Model:      model.ModelCfg.Model,
		Kind:       kind,
		Input:      usage.InputTokens,
		Output:     usage.OutputTokens,
		CacheRead:  usage.CacheReadTokens,
		CacheWrite: usage.CacheCreationTokens,
		Cost:       cost,
		Pricing:    pricing,
	}
}

// recordUsage adds a model call to the usage ledger. Claude Code turns
// are recorded once per turn by the engine instead.
func (a *sessionAgent) recordUsage(model Model, sessionID, kind string, usage fantasy.Usage, overrideCost *float64, estimated bool) {
	if a.control == nil || model.ClaudeCode || usageIsZero(usage) {
		return
	}
	a.control.RecordUsage(usageRow(model, sessionID, kind, usage, overrideCost, estimated))
}

// checkBudget asks the person before a model call that would go past a
// budget. It returns nil when there's no budget or they let it continue.
func (a *sessionAgent) checkBudget(ctx context.Context, sessionID string) error {
	if a.control == nil || a.isSubAgent {
		return nil
	}
	return a.control.CheckBudget(ctx, sessionID)
}

// missionContext returns the system prompt block for the session's
// mission, or "" when it has none.
func (a *sessionAgent) missionContext(sessionID string) string {
	if a.control == nil || a.isSubAgent {
		return ""
	}
	return a.control.MissionContext(sessionID)
}

// budgetError turns a budget stop into the error the chat shows.
func budgetError(err error) error {
	if !errors.Is(err, control.ErrBudgetReached) {
		return err
	}
	return &fantasy.Error{
		Title:   "Budget reached",
		Message: err.Error() + ". It wasn't extended, so Claude stopped. Allow it when asked, or raise options.budget, to continue.",
		Cause:   err,
	}
}

// claudeCodeArgs returns the Claude Code CLI's extra arguments for a
// turn: Ta-Da's MCP servers, plus the mission and machines blocks when
// there are any.
func (a *sessionAgent) claudeCodeArgs(sessionID string) []string {
	var args []string
	if a.ccExtraArgs != nil {
		args = a.ccExtraArgs(sessionID)
	}
	// The CLI takes one --append-system-prompt, so the blocks share it.
	var extra []string
	if mc := a.missionContext(sessionID); mc != "" {
		extra = append(extra, mc)
	}
	if mc := a.machinesContext(); mc != "" {
		extra = append(extra, mc)
	}
	if len(extra) > 0 {
		args = append(args, "--append-system-prompt", strings.Join(extra, "\n\n"))
	}
	return args
}

// machinesContext returns the system prompt block listing the user's
// machines for the Claude Code engine, or "" when none is usable.
func (a *sessionAgent) machinesContext() string {
	if a.isSubAgent || a.cfg == nil {
		return ""
	}
	cfg := a.cfg.Config()
	if len(cfg.Machines) == 0 {
		return ""
	}
	// Ask the machines first, so the prompt has their live slots.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	fleet.Refresh(ctx, cfg)
	cancel()
	return fleet.PromptSection(cfg, "the "+fleet.AskToolName+" tool")
}
