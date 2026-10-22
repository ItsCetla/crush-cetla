package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

func TestLedgerAddAndSince(t *testing.T) {
	t.Parallel()
	l, err := NewLedger(t.TempDir())
	require.NoError(t, err)
	now := time.Now()
	ctx := context.Background()
	require.NoError(t, l.Add(ctx, Usage{At: now.Add(-time.Hour), Model: "a", Cost: 1, Pricing: PricingList}))
	require.NoError(t, l.Add(ctx, Usage{At: now, Model: "b", Cost: 2, Pricing: PricingList}))

	rows, err := l.Since(now.Add(-2 * time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 2)

	// A second read only parses what was appended since.
	require.NoError(t, l.Add(ctx, Usage{At: now, Model: "c"}))
	rows, err = l.Since(now.Add(-30 * time.Minute))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, PricingUnpriced, rows[1].Pricing)

	// Another process (a second ledger) sees the same rows.
	l2, err := NewLedger(l.dir)
	require.NoError(t, err)
	rows, err = l2.Since(now.Add(-2 * time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 3)
}

func TestLedgerSkipsPartialLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l, err := NewLedger(dir)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, l.Add(context.Background(), Usage{At: now, Cost: 1, Pricing: PricingList}))
	f, err := os.OpenFile(l.path(monthKey(now)), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"at":"`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	rows, err := l.Since(now.Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestSumSeparatesPricing(t *testing.T) {
	t.Parallel()
	rows := []Usage{
		{Cost: 1, Pricing: PricingList, Input: 10},
		{Cost: 0.5, Pricing: PricingBilled},
		{Cost: 0.25, Pricing: PricingEstimate},
		{Cost: 3, Pricing: PricingFlat},
		{Pricing: PricingUnpriced, Output: 5},
	}
	tot := Sum(rows, nil)
	require.InDelta(t, 1.75, tot.Cost, 1e-9)
	require.Equal(t, 3, tot.Calls)
	require.InDelta(t, 3, tot.Flat, 1e-9)
	require.Equal(t, 1, tot.FlatCalls)
	require.Equal(t, 1, tot.Unpriced)
	require.EqualValues(t, 15, tot.Tokens)
}

func TestEvaluateBudget(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 20, 15, 0, 0, 0, time.Local)
	rows := []Usage{
		{At: now.AddDate(0, 0, -3), SessionID: "old", Cost: 10, Pricing: PricingList},
		{At: now.Add(-time.Hour), SessionID: "s1", Cost: 4, Pricing: PricingList},
		{At: now.Add(-time.Minute), SessionID: "s1", Cost: 1.12, Pricing: PricingList},
		{At: now, SessionID: "s1", Cost: 50, Pricing: PricingFlat},
	}
	b := Budget{Daily: 5, Monthly: 100, Session: 20}
	st := Evaluate(b, rows, "s1", now, nil)
	require.Len(t, st, 3)
	byWin := map[Window]BudgetStatus{}
	for _, s := range st {
		byWin[s.Window] = s
	}
	require.True(t, byWin[WindowToday].Over)
	require.InDelta(t, 5.12, byWin[WindowToday].Spent, 1e-9)
	require.Equal(t, 90, byWin[WindowToday].Warn)
	require.False(t, byWin[WindowMonth].Over)
	require.InDelta(t, 15.12, byWin[WindowMonth].Spent, 1e-9)
	require.False(t, byWin[WindowSession].Over)
	require.Equal(t, 0, byWin[WindowSession].Warn)

	msg := OverMessage(byWin[WindowToday])
	require.Equal(t, "Today's spend $5.12 reached the $5.00 daily budget. Allow continuing up to $6.37?", msg)

	// An extension lifts the limit.
	extra := map[string]float64{windowKey(WindowToday, "s1", now): 1.37}
	st = Evaluate(Budget{Daily: 5}, rows, "s1", now, extra)
	require.Len(t, st, 1)
	require.False(t, st[0].Over)

	// No session id means no session window.
	require.Len(t, Evaluate(Budget{Session: 1}, rows, "", now, nil), 0)
	require.False(t, Budget{}.Enabled())
}

func TestMoney(t *testing.T) {
	t.Parallel()
	require.Equal(t, "$0", Money(0))
	require.Equal(t, "$0.0042", Money(0.0042))
	require.Equal(t, "$0.420", Money(0.42))
	require.Equal(t, "$4.20", Money(4.2))
	require.Equal(t, "$4200", Money(4200))
}

func TestRenderHelpers(t *testing.T) {
	t.Parallel()
	require.Equal(t, "█████·····", Ladder(50, 10))
	require.Equal(t, "█·········", Ladder(1, 10))
	require.Equal(t, "█████████·", Ladder(99.9, 10))
	require.Equal(t, "██████████", Ladder(140, 10))
	require.Equal(t, "··········", Ladder(0, 10))

	require.Equal(t, "▮▮▮▯▯", Pips(3, 5, 10))
	require.Equal(t, "▮▮▮▮▮▯▯▯▯▯", Pips(10, 20, 10))
	require.Equal(t, "", Pips(0, 0, 10))

	require.Equal(t, "·▁█▅", Sparkline([]float64{0, 0.1, 8, 5}))

	require.Equal(t, "claude-sonnet-5-5", ShortModel("anthropic/claude-sonnet-5-5-20260601"))
	require.Equal(t, "gpt-5", ShortModel("openai/gpt-5"))
	require.Equal(t, "unknown", ShortModel(""))

	require.Equal(t, "950", Tokens(950))
	require.Equal(t, "12.3k", Tokens(12_300))
	require.Equal(t, "4.1M", Tokens(4_100_000))

	now := time.Now()
	require.Equal(t, "now", Ago(now, now))
	require.Equal(t, "3m ago", Ago(now.Add(-3*time.Minute), now))
	require.Equal(t, "2h ago", Ago(now.Add(-2*time.Hour), now))
	require.Equal(t, "never", Ago(time.Time{}, now))

	require.Equal(t, "●", MissionGlyph(MissionDone, 3, 3))
	require.Equal(t, "◐", MissionGlyph(MissionActive, 1, 3))
	require.Equal(t, "○", MissionGlyph(MissionActive, 0, 3))
	require.Equal(t, "⊘", MissionGlyph(MissionAbandoned, 0, 3))
}

func TestDailyCostAndBurn(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	rows := []Usage{
		{At: now.AddDate(0, 0, -2), Cost: 2, Pricing: PricingList},
		{At: now.AddDate(0, 0, -20), Cost: 9, Pricing: PricingList},
		{At: now, Cost: 1, Pricing: PricingList},
		{At: now, Cost: 7, Pricing: PricingFlat},
	}
	d := DailyCost(rows, 3, now)
	require.Equal(t, []float64{2, 0, 1}, d)

	_, ok := BurnRate(rows[:2])
	require.False(t, ok)
	burn, ok := BurnRate([]Usage{
		{At: now.Add(-30 * time.Minute), Cost: 1, Pricing: PricingList},
		{At: now.Add(-15 * time.Minute), Cost: 1, Pricing: PricingList},
		{At: now, Cost: 1, Pricing: PricingList},
	})
	require.True(t, ok)
	require.InDelta(t, 6, burn, 1e-9)
}

func TestTopModels(t *testing.T) {
	t.Parallel()
	rows := []Usage{
		{Model: "x/a", Cost: 1, Pricing: PricingList},
		{Model: "x/a", Cost: 1, Pricing: PricingList},
		{Model: "b", Cost: 3, Pricing: PricingList},
		{Model: "c", Input: 100, Pricing: PricingUnpriced},
	}
	top := TopModels(rows, 2)
	require.Len(t, top, 2)
	require.Equal(t, "b", top[0].Model)
	require.Equal(t, "a", top[1].Model)
	require.Equal(t, 2, top[1].Calls)
}

func TestMissions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := NewMissions(t.TempDir())
	require.NoError(t, err)

	_, err = s.Create(ctx, " ", "", nil, "")
	require.Error(t, err)

	m, err := s.Create(ctx, "Ship v1", "Release it", []string{"tests pass", "", "docs"}, "s1")
	require.NoError(t, err)
	require.Len(t, m.ID, 6)
	require.Len(t, m.Criteria, 2)
	require.Equal(t, 2, m.Criteria[1].ID)

	got, ok := s.ForSession("s1")
	require.True(t, ok)
	require.Equal(t, m.ID, got.ID)

	// Attaching to a new mission detaches from the old one.
	m2, err := s.Create(ctx, "Other", "", nil, "s1")
	require.NoError(t, err)
	got, ok = s.ForSession("s1")
	require.True(t, ok)
	require.Equal(t, m2.ID, got.ID)
	old, err := s.Get(m.ID)
	require.NoError(t, err)
	require.Empty(t, old.Sessions)

	_, err = s.Attach(ctx, m.ID, "s1")
	require.NoError(t, err)
	got, _ = s.ForSession("s1")
	require.Equal(t, m.ID, got.ID)

	m, err = s.Update(ctx, m.ID, func(m *Mission) error {
		m.Criterion(1).Done = true
		m.AddNote("Checked CI", "claude")
		return nil
	})
	require.NoError(t, err)
	done, total := m.Progress()
	require.Equal(t, 1, done)
	require.Equal(t, 2, total)
	require.Len(t, m.Notes, 1)

	m.SetStatus(MissionDone)
	require.False(t, m.Completed.IsZero())
	m.SetStatus(MissionActive)
	require.True(t, m.Completed.IsZero())

	all, err := s.List()
	require.NoError(t, err)
	require.Len(t, all, 2)

	require.NoError(t, s.Detach(ctx, "s1"))
	_, ok = s.ForSession("s1")
	require.False(t, ok)

	require.NoError(t, s.Delete(ctx, m2.ID))
	_, err = s.Get(m2.ID)
	require.ErrorIs(t, err, ErrMissionNotFound)
	_, err = s.Get("../etc")
	require.ErrorIs(t, err, ErrMissionNotFound)
}

func TestPromptContextIsStable(t *testing.T) {
	t.Parallel()
	m := Mission{ID: "abc123", Title: "Ship", Goal: "Release", Status: MissionActive}
	m.AddCriterion("tests pass")
	m.AddCriterion("docs")
	m.Criteria[0].Done = true
	a := PromptContext(m)
	m.AddNote("something", "you")
	m.Updated = time.Now()
	require.Equal(t, a, PromptContext(m))
	require.Contains(t, a, "- [x] 1. tests pass")
	require.Contains(t, a, "- [ ] 2. docs")
	require.Contains(t, a, "(1 of 2 done)")
	require.True(t, strings.HasPrefix(a, "<mission>"))
}

func TestActionLogPersists(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "control", "actions.jsonl")
	l, err := OpenActionLog(path)
	require.NoError(t, err)
	l.Add(permission.Action{At: time.Now(), SessionID: "s1", ToolName: "bash", Decision: permission.DecisionAllowed})
	l.Add(permission.Action{At: time.Now(), SessionID: "s2", ToolName: "edit", Decision: permission.DecisionDenied})
	l.Add(permission.Action{At: time.Now(), ToolName: "take_back", Decision: permission.DecisionEvent})

	l2, err := OpenActionLog(path)
	require.NoError(t, err)
	got := l2.Recent("s1", 0)
	require.Len(t, got, 2)
	require.Equal(t, "take_back", got[0].ToolName)
	require.Equal(t, 1, l2.Count("s1"))
	require.Len(t, l2.Recent("", 2), 2)
}

func TestActionLogTrims(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "actions.jsonl")
	l, err := OpenActionLog(path)
	require.NoError(t, err)
	for range keepActions + trimSlack + 10 {
		l.Add(permission.Action{ToolName: "bash", Decision: permission.DecisionAllowed})
	}
	l2, err := OpenActionLog(path)
	require.NoError(t, err)
	require.LessOrEqual(t, l2.lines, keepActions+trimSlack)
	require.Len(t, l2.Recent("", 0), keepActions)
}

func newTestHub(t *testing.T, b Budget) (*Hub, permission.Service) {
	t.Helper()
	dir := t.TempDir()
	perms := permission.NewPermissionService(dir, false, nil, nil)
	ledger, err := NewLedger(filepath.Join(dir, "usage"))
	require.NoError(t, err)
	actions, err := OpenActionLog(filepath.Join(dir, "actions.jsonl"))
	require.NoError(t, err)
	missions, err := NewMissions(filepath.Join(dir, "missions"))
	require.NoError(t, err)
	h := NewHub(HubOptions{
		Permissions: perms,
		Ledger:      ledger,
		Actions:     actions,
		Missions:    missions,
		Budget:      b,
		Project:     "proj",
	})
	return h, perms
}

func TestHubSnapshotAndTakeBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, perms := newTestHub(t, Budget{Daily: 10})
	perms.SetSkipRequests(true)
	h.RecordUsage(Usage{SessionID: "s1", Model: "m", Cost: 2, Pricing: PricingList})

	r, err := h.Do(ctx, Command{Op: OpSnapshot, SessionID: "s1"})
	require.NoError(t, err)
	require.True(t, r.Snapshot.State.Auto)
	require.InDelta(t, 2, r.Snapshot.Today.Cost, 1e-9)
	require.InDelta(t, 2, r.Snapshot.Session.Cost, 1e-9)
	require.Len(t, r.Snapshot.Budgets, 1)

	r, err = h.Do(ctx, Command{Op: OpTakeBack, SessionID: "s1"})
	require.NoError(t, err)
	require.False(t, r.Snapshot.State.Auto)
	require.False(t, r.Snapshot.State.TakenBackAt.IsZero())
	require.NotEmpty(t, r.Snapshot.Recent)
	require.Equal(t, permission.TakeBackToolName, r.Snapshot.Recent[0].ToolName)

	on := true
	r, err = h.Do(ctx, Command{Op: OpSetEdits, Edits: &on})
	require.NoError(t, err)
	require.True(t, r.Snapshot.State.Edits)

	r, err = h.Do(ctx, Command{Op: OpUsage})
	require.NoError(t, err)
	require.Len(t, r.Report.Daily, 14)
	require.Len(t, r.Report.Models, 1)

	_, err = h.Do(ctx, Command{Op: "nope"})
	require.Error(t, err)
}

func TestHubMissionOps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, _ := newTestHub(t, Budget{})
	r, err := h.Do(ctx, Command{Op: OpMissionCreate, SessionID: "s1", Title: "Ship", Criteria: []string{"a"}})
	require.NoError(t, err)
	id := r.Mission.ID

	_, err = h.Do(ctx, Command{Op: OpMissionAdd, MissionID: id, Text: "b"})
	require.NoError(t, err)
	r, err = h.Do(ctx, Command{Op: OpMissionCheck, MissionID: id, Index: 2, Text: "verified"})
	require.NoError(t, err)
	require.True(t, r.Mission.Criterion(2).Done)
	require.Equal(t, "verified", r.Mission.Criterion(2).Note)
	_, err = h.Do(ctx, Command{Op: OpMissionCheck, MissionID: id, Index: 9})
	require.Error(t, err)

	require.Contains(t, h.MissionContext("s1"), "Title: Ship")

	_, err = h.Do(ctx, Command{Op: OpMissionStatus, MissionID: id, Status: MissionDone})
	require.NoError(t, err)
	require.Empty(t, h.MissionContext("s1"))
	_, err = h.Do(ctx, Command{Op: OpMissionStatus, MissionID: id, Status: "bogus"})
	require.Error(t, err)

	r, err = h.Do(ctx, Command{Op: OpMissions})
	require.NoError(t, err)
	require.Len(t, r.Missions, 1)

	_, err = h.Do(ctx, Command{Op: OpMissionDelete, MissionID: id})
	require.NoError(t, err)
}

func TestCheckBudgetAsksAndExtends(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, perms := newTestHub(t, Budget{Daily: 1})
	require.NoError(t, h.CheckBudget(ctx, "s1"))
	h.RecordUsage(Usage{SessionID: "s1", Cost: 1.5, Pricing: PricingList})

	// Auto mode doesn't skip a budget prompt.
	perms.SetSkipRequests(true)
	sub := perms.Subscribe(ctx)
	done := make(chan error, 1)
	go func() { done <- h.CheckBudget(ctx, "s1") }()
	ev := <-sub
	require.Equal(t, permission.BudgetToolName, ev.Payload.ToolName)
	require.Equal(t, permission.ClassSpend, ev.Payload.Class)
	perms.Grant(ev.Payload)
	require.NoError(t, <-done)

	// The grant lifted the limit to $1.75.
	st := h.Budgets("s1")
	require.InDelta(t, 1.75, st[0].Limit, 1e-9)
	require.NoError(t, h.CheckBudget(ctx, "s1"))

	h.RecordUsage(Usage{SessionID: "s1", Cost: 1, Pricing: PricingList})
	go func() { done <- h.CheckBudget(ctx, "s1") }()
	ev = <-sub
	perms.Deny(ev.Payload)
	require.ErrorIs(t, <-done, ErrBudgetReached)
}

func TestCheckBudgetHeadlessStops(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	perms := permission.NewPermissionService(dir, false, nil, nil)
	perms.AutoApproveSession("s1")
	ledger, err := NewLedger(dir)
	require.NoError(t, err)
	h := NewHub(HubOptions{Permissions: perms, Ledger: ledger, Budget: Budget{Daily: 1}})
	h.RecordUsage(Usage{Cost: 2, Pricing: PricingList})
	require.ErrorIs(t, h.CheckBudget(context.Background(), "s1"), ErrBudgetReached)
}

func TestNilHubIsSafe(t *testing.T) {
	t.Parallel()
	var h *Hub
	h.RecordUsage(Usage{})
	require.NoError(t, h.CheckBudget(context.Background(), "s"))
	require.Empty(t, h.MissionContext("s"))
	_, err := h.Do(context.Background(), Command{})
	require.Error(t, err)
}

func TestHubReportsPolicyWithoutPermissions(t *testing.T) {
	t.Parallel()

	require.Empty(t, NewHub(HubOptions{}).snapshot("").State.Policy.AlwaysAsk)

	p := permission.DefaultPolicy()
	s := NewHub(HubOptions{Policy: &p}).snapshot("")
	require.Equal(t, []permission.Class{permission.ClassDelete}, s.State.Policy.AlwaysAsk)
}

func TestHubNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, err := NewHub(HubOptions{}).Do(ctx, Command{Op: OpNetwork})
	require.Error(t, err, "no receipt without a ledger")
	require.Nil(t, NewHub(HubOptions{}).snapshot("s1").Network)

	l := netlog.NewLedger()
	l.Add("s1", "api.anthropic.com", netlog.PurposeProvider, 10, 100)
	l.Add("s1", "localhost:3000", netlog.PurposeMCP, 1, 1)
	l.Add("s2", "duckduckgo.com", netlog.PurposeWeb, 1, 1)
	h := NewHub(HubOptions{Network: l})

	r, err := h.Do(ctx, Command{Op: OpNetwork, SessionID: "s1"})
	require.NoError(t, err)
	require.NotNil(t, r.Network)
	require.Equal(t, 2, r.Network.Totals.Hosts)
	require.Equal(t, 1, r.Network.Totals.External)

	all, err := h.Do(ctx, Command{Op: OpNetwork})
	require.NoError(t, err)
	require.Equal(t, 3, all.Network.Totals.Hosts)

	s := h.snapshot("s1")
	require.Equal(t, &netlog.Totals{Hosts: 2, External: 1, Requests: 2, BytesOut: 11, BytesIn: 101}, s.Network)

	// The reply crosses the client/server API as JSON.
	b, err := json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(b), `"bytes_out":10`)
	var back Reply
	require.NoError(t, json.Unmarshal(b, &back))
	require.Equal(t, r.Network.Totals, back.Network.Totals)
	require.Equal(t, r.Network.Hosts[0].Host, back.Network.Hosts[0].Host)
	require.Equal(t, r.Network.Hosts[0].Purposes, back.Network.Hosts[0].Purposes)
}
