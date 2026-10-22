package agent

import (
	"context"
	"errors"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/stretchr/testify/require"
)

func pricedModel(in, out float64) Model {
	return Model{
		CatwalkCfg: catwalk.Model{CostPer1MIn: in, CostPer1MOut: out},
		ModelCfg:   config.SelectedModel{Provider: "p", Model: "m"},
	}
}

func TestUsageRowPricing(t *testing.T) {
	t.Parallel()
	usage := fantasy.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	billed := 0.42

	cases := []struct {
		name      string
		model     Model
		override  *float64
		estimated bool
		pricing   control.Pricing
		cost      float64
	}{
		{"list", pricedModel(3, 15), nil, false, control.PricingList, 18},
		{"billed", pricedModel(3, 15), &billed, false, control.PricingBilled, 0.42},
		{"estimate ignores override", pricedModel(3, 15), &billed, true, control.PricingEstimate, 18},
		{"unpriced", pricedModel(0, 0), nil, false, control.PricingUnpriced, 0},
		{"flat keeps list cost", func() Model { m := pricedModel(3, 15); m.FlatRate = true; return m }(), nil, false, control.PricingFlat, 18},
		{"flat unpriced", func() Model { m := pricedModel(0, 0); m.FlatRate = true; return m }(), nil, false, control.PricingFlat, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			row := usageRow(tc.model, "s1", "turn", usage, tc.override, tc.estimated)
			require.Equal(t, tc.pricing, row.Pricing)
			require.InDelta(t, tc.cost, row.Cost, 1e-9)
			require.Equal(t, "s1", row.SessionID)
			require.Equal(t, "p", row.Provider)
			require.Equal(t, "m", row.Model)
			require.Equal(t, int64(1_000_000), row.Input)
		})
	}
}

type fakeControl struct {
	mission  string
	budget   error
	recorded []control.Usage
}

func (f *fakeControl) RecordUsage(u control.Usage) { f.recorded = append(f.recorded, u) }

func (f *fakeControl) CheckBudget(context.Context, string) error { return f.budget }

func (f *fakeControl) MissionContext(string) string { return f.mission }

func TestClaudeCodeArgs(t *testing.T) {
	t.Parallel()

	a := &sessionAgent{}
	require.Empty(t, a.claudeCodeArgs("s1"))

	a = &sessionAgent{ccExtraArgs: func(id string) []string { return []string{"--mcp-config", id} }}
	require.Equal(t, []string{"--mcp-config", "s1"}, a.claudeCodeArgs("s1"))

	fc := &fakeControl{mission: "## Mission\nShip it"}
	a = &sessionAgent{
		ccExtraArgs: func(string) []string { return []string{"--x"} },
		control:     fc,
	}
	require.Equal(t, []string{"--x", "--append-system-prompt", "## Mission\nShip it"}, a.claudeCodeArgs("s1"))

	// Sub-agents don't get the mission.
	a.isSubAgent = true
	require.Equal(t, []string{"--x"}, a.claudeCodeArgs("s1"))
}

func TestControlHooksAreNilSafe(t *testing.T) {
	t.Parallel()
	a := &sessionAgent{}
	require.NoError(t, a.checkBudget(t.Context(), "s1"))
	require.Empty(t, a.missionContext("s1"))
	a.recordUsage(pricedModel(1, 1), "s1", "turn", fantasy.Usage{InputTokens: 5}, nil, false)
}

func TestCheckBudgetSubAgents(t *testing.T) {
	t.Parallel()
	fc := &fakeControl{budget: control.ErrBudgetReached}

	a := &sessionAgent{control: fc}
	require.ErrorIs(t, a.checkBudget(t.Context(), "s1"), control.ErrBudgetReached)

	// A sub-agent runs under a parent that checks the budget.
	a = &sessionAgent{control: fc, isSubAgent: true}
	require.NoError(t, a.checkBudget(t.Context(), "s1"))

	// A swarm agent has no parent, so it checks the budget itself.
	sa, ok := NewSessionAgent(SessionAgentOptions{IsSubAgent: true, EnforceBudget: true, Control: fc}).(*sessionAgent)
	require.True(t, ok)
	require.ErrorIs(t, sa.checkBudget(t.Context(), "s1"), control.ErrBudgetReached)
}

func TestRecordUsageSkipsClaudeCodeAndZero(t *testing.T) {
	t.Parallel()
	fc := &fakeControl{}
	a := &sessionAgent{control: fc}
	m := pricedModel(1, 1)
	a.recordUsage(m, "s1", "turn", fantasy.Usage{}, nil, false)
	cc := m
	cc.ClaudeCode = true
	a.recordUsage(cc, "s1", "turn", fantasy.Usage{InputTokens: 5}, nil, false)
	require.Empty(t, fc.recorded)
	a.recordUsage(m, "s1", "turn", fantasy.Usage{InputTokens: 5}, nil, false)
	require.Len(t, fc.recorded, 1)
}

func TestBudgetError(t *testing.T) {
	t.Parallel()
	other := errors.New("boom")
	require.Equal(t, other, budgetError(other))

	err := budgetError(control.ErrBudgetReached)
	var fe *fantasy.Error
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "Budget reached", fe.Title)
	require.ErrorIs(t, err, control.ErrBudgetReached)
}
