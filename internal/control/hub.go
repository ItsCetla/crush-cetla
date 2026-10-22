package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/charmbracelet/crush/internal/permission"
)

// ErrBudgetReached stops a turn that went past a spending budget the
// person didn't extend.
var ErrBudgetReached = errors.New("spending budget reached")

// Hub is the control cockpit's backend: who's in control, what the agent
// has done, what it costs, and the missions it's working on.
type Hub struct {
	perms    permission.Service
	ctrl     permission.Controller
	ledger   *Ledger
	actions  *ActionLog
	missions *Missions
	project  string
	policy   *permission.Policy
	network  *netlog.Ledger

	mu     sync.Mutex
	budget Budget
	extra  map[string]float64
}

// HubOptions configures a Hub. Every store is optional; the hub reports
// what it doesn't have as unavailable.
type HubOptions struct {
	Permissions permission.Service
	Ledger      *Ledger
	Actions     *ActionLog
	Missions    *Missions
	Budget      Budget
	// Project names the project in usage rows.
	Project string
	// Policy is reported in snapshots when there is no permission
	// service, as in "tada status".
	Policy *permission.Policy
	// Network is this process's network receipt.
	Network *netlog.Ledger
}

// NewHub builds a hub and starts recording the permission service's
// actions to the action log.
func NewHub(o HubOptions) *Hub {
	h := &Hub{
		perms:    o.Permissions,
		ledger:   o.Ledger,
		actions:  o.Actions,
		missions: o.Missions,
		project:  o.Project,
		policy:   o.Policy,
		network:  o.Network,
		budget:   o.Budget,
		extra:    make(map[string]float64),
	}
	if c, ok := o.Permissions.(permission.Controller); ok {
		h.ctrl = c
		if o.Actions != nil {
			c.SetRecorder(o.Actions.Add)
		}
	}
	return h
}

// SetBudget replaces the budget, for example after a config reload.
func (h *Hub) SetBudget(b Budget) {
	h.mu.Lock()
	h.budget = b
	h.mu.Unlock()
}

func (h *Hub) currentBudget() Budget {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.budget
}

// RecordUsage adds a usage row to the ledger. Errors are logged, not
// returned: losing a row must never break a turn.
func (h *Hub) RecordUsage(u Usage) {
	if h == nil || h.ledger == nil {
		return
	}
	if u.Project == "" {
		u.Project = h.project
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.ledger.Add(ctx, u); err != nil {
		slog.Warn("Failed to record usage", "error", err)
	}
}

func (h *Hub) rowsSince(t time.Time) []Usage {
	if h.ledger == nil {
		return nil
	}
	rows, err := h.ledger.Since(t)
	if err != nil {
		slog.Warn("Failed to read usage", "error", err)
	}
	return rows
}

// Budgets returns the session's budget windows.
func (h *Hub) Budgets(sessionID string) []BudgetStatus {
	b := h.currentBudget()
	if !b.Enabled() {
		return nil
	}
	now := time.Now()
	from := startOfMonth(now)
	if b.Session > 0 {
		// A session can span months; a year back is plenty.
		from = from.AddDate(-1, 0, 0)
	}
	rows := h.rowsSince(from)
	h.mu.Lock()
	extra := make(map[string]float64, len(h.extra))
	for k, v := range h.extra {
		extra[k] = v
	}
	h.mu.Unlock()
	return Evaluate(b, rows, sessionID, now, extra)
}

// CheckBudget asks the person before a turn goes past a budget. It
// returns ErrBudgetReached when they say no, or when nobody can be asked.
func (h *Hub) CheckBudget(ctx context.Context, sessionID string) error {
	if h == nil || h.perms == nil {
		return nil
	}
	for _, st := range h.Budgets(sessionID) {
		if !st.Over {
			continue
		}
		ok, err := h.perms.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolCallID:  fmt.Sprintf("budget-%s-%d", st.Window, time.Now().UnixNano()),
			ToolName:    permission.BudgetToolName,
			Action:      "continue",
			Description: OverMessage(st),
		})
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: %s %s of %s", ErrBudgetReached, st.Window.Label(), Money(st.Spent), Money(st.Limit))
		}
		h.mu.Lock()
		h.extra[windowKey(st.Window, sessionID, time.Now())] = NextLimit(st) - st.Base
		h.mu.Unlock()
	}
	return nil
}

// Missions returns the mission store, or nil when there is none.
func (h *Hub) Missions() *Missions {
	if h == nil {
		return nil
	}
	return h.missions
}

// MissionContext returns the system-prompt block for the session's
// mission, or "" when it has none open.
func (h *Hub) MissionContext(sessionID string) string {
	if h == nil || h.missions == nil {
		return ""
	}
	m, ok := h.missions.ForSession(sessionID)
	if !ok || !m.Status.Open() {
		return ""
	}
	return PromptContext(m)
}

// Op is a hub command.
type Op string

const (
	OpSnapshot       Op = "snapshot"
	OpTakeBack       Op = "takeback"
	OpSetEdits       Op = "set_edits"
	OpUsage          Op = "usage"
	OpActions        Op = "actions"
	OpMissions       Op = "missions"
	OpNetwork        Op = "network"
	OpMissionCreate  Op = "mission_create"
	OpMissionCheck   Op = "mission_check"
	OpMissionUncheck Op = "mission_uncheck"
	OpMissionNote    Op = "mission_note"
	OpMissionAdd     Op = "mission_add"
	OpMissionStatus  Op = "mission_status"
	OpMissionAttach  Op = "mission_attach"
	OpMissionDetach  Op = "mission_detach"
	OpMissionDelete  Op = "mission_delete"
)

// Command is a request to the hub. It travels over the client/server API,
// so it's plain data.
type Command struct {
	Op        Op            `json:"op"`
	SessionID string        `json:"session_id,omitempty"`
	Edits     *bool         `json:"edits,omitempty"`
	MissionID string        `json:"mission_id,omitempty"`
	Title     string        `json:"title,omitempty"`
	Goal      string        `json:"goal,omitempty"`
	Text      string        `json:"text,omitempty"`
	Criteria  []string      `json:"criteria,omitempty"`
	Index     int           `json:"index,omitempty"`
	Status    MissionStatus `json:"status,omitempty"`
	Limit     int           `json:"limit,omitempty"`
}

// Snapshot is everything the status bar and sidebar show.
type Snapshot struct {
	State          permission.ControlState `json:"state"`
	Recent         []permission.Action     `json:"recent,omitempty"`
	SessionActions int                     `json:"session_actions"`
	Mission        *Mission                `json:"mission,omitempty"`
	Today          Totals                  `json:"today"`
	Month          Totals                  `json:"month"`
	Session        Totals                  `json:"session"`
	Budgets        []BudgetStatus          `json:"budgets,omitempty"`
	HasLedger      bool                    `json:"has_ledger"`
	// Network sums the hosts the session talked to, for the status bar.
	// It's nil when the hub has no network receipt.
	Network *netlog.Totals `json:"network,omitempty"`
	At      time.Time      `json:"at"`
}

// Report is the cockpit's cost page.
type Report struct {
	Daily     []float64   `json:"daily"`
	Models    []ModelCost `json:"models,omitempty"`
	Burn      float64     `json:"burn"`
	HasBurn   bool        `json:"has_burn"`
	LastUsage time.Time   `json:"last_usage,omitzero"`
}

// Reply is the hub's answer. Only the fields the op fills are set.
type Reply struct {
	Snapshot *Snapshot           `json:"snapshot,omitempty"`
	Report   *Report             `json:"report,omitempty"`
	Actions  []permission.Action `json:"actions,omitempty"`
	Missions []Mission           `json:"missions,omitempty"`
	Mission  *Mission            `json:"mission,omitempty"`
	Network  *netlog.Report      `json:"network,omitempty"`
	Message  string              `json:"message,omitempty"`
}

// Do runs a command.
func (h *Hub) Do(ctx context.Context, c Command) (Reply, error) {
	if h == nil {
		return Reply{}, errors.New("control isn't available")
	}
	switch c.Op {
	case OpSnapshot, "":
		s := h.snapshot(c.SessionID)
		return Reply{Snapshot: &s}, nil
	case OpTakeBack:
		if h.ctrl == nil {
			return Reply{}, errors.New("take back isn't available")
		}
		h.ctrl.TakeBack()
		s := h.snapshot(c.SessionID)
		return Reply{Snapshot: &s, Message: "You have control: Auto and Edits are off, and the agent will ask before acting."}, nil
	case OpSetEdits:
		if h.ctrl == nil || c.Edits == nil {
			return Reply{}, errors.New("edits mode isn't available")
		}
		h.ctrl.SetEditsMode(*c.Edits)
		s := h.snapshot(c.SessionID)
		return Reply{Snapshot: &s}, nil
	case OpUsage:
		s := h.snapshot(c.SessionID)
		r := h.report()
		return Reply{Snapshot: &s, Report: &r}, nil
	case OpActions:
		return Reply{Actions: h.recentActions(c.SessionID, c.Limit)}, nil
	case OpNetwork:
		if h.network == nil {
			return Reply{}, errors.New("the network receipt isn't available")
		}
		r := h.network.Report(c.SessionID)
		return Reply{Network: &r}, nil
	}
	if strings.HasPrefix(string(c.Op), "mission") {
		return h.missionOp(ctx, c)
	}
	return Reply{}, fmt.Errorf("unknown control op %q", c.Op)
}

func (h *Hub) recentActions(sessionID string, limit int) []permission.Action {
	if h.actions != nil {
		return h.actions.Recent(sessionID, limit)
	}
	if h.ctrl != nil {
		return h.ctrl.Actions(sessionID, limit)
	}
	return nil
}

func (h *Hub) snapshot(sessionID string) Snapshot {
	now := time.Now()
	s := Snapshot{At: now, HasLedger: h.ledger != nil}
	if h.ctrl != nil {
		s.State = h.ctrl.ControlState()
	} else if h.policy != nil {
		s.State.Policy = *h.policy
	}
	s.Recent = h.recentActions(sessionID, 6)
	if h.actions != nil && sessionID != "" {
		s.SessionActions = h.actions.Count(sessionID)
	}
	if h.missions != nil {
		if m, ok := h.missions.ForSession(sessionID); ok {
			s.Mission = &m
		}
	}
	day, month := startOfDay(now), startOfMonth(now)
	rows := h.rowsSince(month)
	s.Today = Sum(rows, func(u Usage) bool { return !u.At.Before(day) })
	s.Month = Sum(rows, nil)
	if sessionID != "" {
		s.Session = Sum(rows, func(u Usage) bool { return u.SessionID == sessionID })
	}
	s.Budgets = h.Budgets(sessionID)
	if h.network != nil {
		t := h.network.Summary(sessionID)
		s.Network = &t
	}
	return s
}

func (h *Hub) report() Report {
	now := time.Now()
	from := startOfDay(now).AddDate(0, 0, -13)
	rows := h.rowsSince(from)
	r := Report{Daily: DailyCost(rows, 14, now)}
	month := startOfMonth(now)
	var monthRows []Usage
	for _, u := range rows {
		if !u.At.Before(month) {
			monthRows = append(monthRows, u)
		}
		if u.At.After(r.LastUsage) {
			r.LastUsage = u.At
		}
	}
	r.Models = TopModels(monthRows, 5)
	// Burn rate covers the last hour of activity.
	var recent []Usage
	for _, u := range rows {
		if now.Sub(u.At) <= time.Hour {
			recent = append(recent, u)
		}
	}
	r.Burn, r.HasBurn = BurnRate(recent)
	return r
}

func (h *Hub) missionOp(ctx context.Context, c Command) (Reply, error) {
	if h.missions == nil {
		return Reply{}, errors.New("missions aren't available")
	}
	ms := h.missions
	one := func(m Mission, err error) (Reply, error) {
		if err != nil {
			return Reply{}, err
		}
		return Reply{Mission: &m}, nil
	}
	switch c.Op {
	case OpMissions:
		all, err := ms.List()
		return Reply{Missions: all}, err
	case OpMissionCreate:
		return one(ms.Create(ctx, c.Title, c.Goal, c.Criteria, c.SessionID))
	case OpMissionCheck, OpMissionUncheck:
		done := c.Op == OpMissionCheck
		return one(ms.Update(ctx, c.MissionID, func(m *Mission) error {
			cr := m.Criterion(c.Index)
			if cr == nil {
				return fmt.Errorf("mission %s has no criterion %d", m.ID, c.Index)
			}
			cr.Done = done
			cr.DoneAt = time.Time{}
			cr.Note = ""
			if done {
				cr.DoneAt = time.Now()
				cr.Note = strings.TrimSpace(c.Text)
			}
			return nil
		}))
	case OpMissionNote:
		if strings.TrimSpace(c.Text) == "" {
			return Reply{}, errors.New("a note needs text")
		}
		return one(ms.Update(ctx, c.MissionID, func(m *Mission) error {
			m.AddNote(c.Text, "you")
			return nil
		}))
	case OpMissionAdd:
		if strings.TrimSpace(c.Text) == "" {
			return Reply{}, errors.New("a criterion needs text")
		}
		return one(ms.Update(ctx, c.MissionID, func(m *Mission) error {
			m.AddCriterion(c.Text)
			return nil
		}))
	case OpMissionStatus:
		if !c.Status.Valid() {
			return Reply{}, fmt.Errorf("unknown mission status %q", c.Status)
		}
		return one(ms.Update(ctx, c.MissionID, func(m *Mission) error {
			m.SetStatus(c.Status)
			return nil
		}))
	case OpMissionAttach:
		return one(ms.Attach(ctx, c.MissionID, c.SessionID))
	case OpMissionDetach:
		return Reply{}, ms.Detach(ctx, c.SessionID)
	case OpMissionDelete:
		return Reply{}, ms.Delete(ctx, c.MissionID)
	}
	return Reply{}, fmt.Errorf("unknown control op %q", c.Op)
}
