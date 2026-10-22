package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
	swarm  SwarmService

	// bgCtx ends background check runs on Close; bgWG waits for them.
	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgWG     sync.WaitGroup
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
	h.bgCtx, h.bgCancel = context.WithCancel(context.Background())
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

// ApproveChecks records check commands the user just allowed in a
// permission prompt, so they can run unattended later. Call it only after
// the prompt was granted.
func (h *Hub) ApproveChecks(ctx context.Context, commands ...string) error {
	if h == nil || h.missions == nil {
		return nil
	}
	return h.missions.ApproveChecks(ctx, commands...)
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
	// OpMissionVerify runs a mission's check commands: every one, or
	// just the criterion in Index.
	OpMissionVerify Op = "mission_verify"

	// OpSwarmPlan shows which machine a swarm would give which criteria.
	OpSwarmPlan Op = "swarm_plan"
	// OpSwarmStart starts a swarm with the plan in Assign.
	OpSwarmStart Op = "swarm_start"
	// OpSwarmRuns lists the swarms.
	OpSwarmRuns Op = "swarm_runs"
	// OpSwarmMerge merges one machine's branch of the swarm in RunID.
	OpSwarmMerge Op = "swarm_merge"
	// OpSwarmDiscard throws one machine's branch away.
	OpSwarmDiscard Op = "swarm_discard"
	// OpSwarmDiff returns the files one machine changed.
	OpSwarmDiff Op = "swarm_diff"
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
	// Check is the check command for mission_add. Without it, Text may
	// carry one as "criterion :: command". For mission_check and
	// mission_verify it's the command the user was shown, which may then
	// run even if it isn't approved yet.
	Check string `json:"check,omitempty"`
	// RunID and Machine pick a swarm assignment.
	RunID   string `json:"run_id,omitempty"`
	Machine string `json:"machine,omitempty"`
	// Assign is the swarm plan the user confirmed: machine name to
	// criterion ids.
	Assign map[string][]int `json:"assign,omitempty"`
}

// allowed is the shown command, as VerifyOptions.Allow.
func (c Command) allowed() []string {
	if s := strings.TrimSpace(c.Check); s != "" && !HiddenChars(s) {
		return []string{s}
	}
	return nil
}

// hiddenCharsNotice tells the cockpit user why a check can't run.
const hiddenCharsNotice = "This check's command has hidden characters, so it can't run from the cockpit. Edit the check to remove them."

// unapprovedNotice tells the cockpit user which checks didn't run.
func unapprovedNotice(cs []Criterion) string {
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, strconv.Itoa(c.ID))
	}
	return fmt.Sprintf("Not run, because you haven't approved these checks yet: %s. Select one to see its command and press space to run it.", strings.Join(ids, ", "))
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
	Swarm    *SwarmRun           `json:"swarm,omitempty"`
	Swarms   []SwarmRun          `json:"swarms,omitempty"`
	Plan     *SwarmPlan          `json:"plan,omitempty"`
	Diff     []SwarmFileDiff     `json:"diff,omitempty"`
	// RunID and Machine name the swarm card a reply is about, so a late
	// reply can't land on another one.
	RunID   string `json:"run_id,omitempty"`
	Machine string `json:"machine,omitempty"`
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
	if strings.HasPrefix(string(c.Op), "swarm") {
		return h.swarmOp(ctx, c)
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
		// The user typed these checks, which approves them.
		var checks []string
		for _, cr := range c.Criteria {
			_, check := ParseCriterion(cr)
			checks = append(checks, check)
		}
		if err := ms.ApproveChecks(ctx, checks...); err != nil {
			return Reply{}, err
		}
		return one(ms.Create(ctx, c.Title, c.Goal, c.Criteria, c.SessionID))
	case OpMissionCheck:
		if m, err := ms.Get(c.MissionID); err == nil {
			if cr := m.Criterion(c.Index); cr != nil && cr.SelfChecking() {
				return h.checkCriterion(ctx, c)
			}
		}
		return one(ms.Update(ctx, c.MissionID, func(m *Mission) error {
			cr := m.Criterion(c.Index)
			if cr == nil {
				return fmt.Errorf("mission %s has no criterion %d", m.ID, c.Index)
			}
			cr.Done, cr.DoneAt = true, time.Now()
			cr.Note, cr.CheckedBy = strings.TrimSpace(c.Text), CheckedByUser
			return nil
		}))
	case OpMissionUncheck:
		return one(ms.Update(ctx, c.MissionID, func(m *Mission) error {
			cr := m.Criterion(c.Index)
			if cr == nil {
				return fmt.Errorf("mission %s has no criterion %d", m.ID, c.Index)
			}
			cr.Done, cr.DoneAt, cr.Note, cr.CheckedBy = false, time.Time{}, "", ""
			return nil
		}))
	case OpMissionVerify:
		// Runs approved checks, plus any the cockpit showed the user.
		opts := VerifyOptions{Criterion: c.Index, NoWait: true, ApprovedOnly: true, Allow: c.allowed()}
		m, ran, err := ms.Verify(ctx, c.MissionID, opts)
		if err != nil {
			return Reply{}, err
		}
		skipped := ms.UnapprovedChecks(m, opts)
		if len(ran) == 0 {
			if len(skipped) > 0 {
				return Reply{Mission: &m, Message: unapprovedNotice(skipped)}, nil
			}
			return Reply{Mission: &m, Message: "This mission has no self-checking criteria."}, nil
		}
		msg := CheckSummary(m, ran)
		if len(skipped) > 0 {
			msg += ". " + unapprovedNotice(skipped)
		}
		return Reply{Mission: &m, Message: msg}, nil
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
		// The user typed the check, which approves it.
		check := c.Check
		if strings.TrimSpace(check) == "" {
			_, check = ParseCriterion(c.Text)
		}
		if err := ms.ApproveChecks(ctx, check); err != nil {
			return Reply{}, err
		}
		return one(ms.Update(ctx, c.MissionID, func(m *Mission) error {
			if strings.TrimSpace(c.Check) != "" {
				m.AddCriterionCheck(c.Text, c.Check)
			} else {
				m.AddCriterion(c.Text)
			}
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

// checkCriterion ticks a self-checking criterion by running its check:
// it's ticked only if the command passes, and a failure comes back as the
// reply's message with the first line of output.
func (h *Hub) checkCriterion(ctx context.Context, c Command) (Reply, error) {
	// The cockpit sends the command it showed; only that one may run
	// without an earlier approval.
	opts := VerifyOptions{Criterion: c.Index, ApprovedOnly: true, Allow: c.allowed()}
	m, ran, err := h.missions.Verify(ctx, c.MissionID, opts)
	if err != nil {
		return Reply{}, err
	}
	cr := m.Criterion(c.Index)
	if cr == nil {
		return Reply{Mission: &m}, nil
	}
	if len(ran) == 0 && cr.SelfChecking() {
		if HiddenChars(cr.Check) {
			return Reply{Mission: &m, Message: hiddenCharsNotice}, nil
		}
		if strings.TrimSpace(c.Check) != "" {
			return Reply{Mission: &m, Message: "The check changed since it was shown, so it didn't run. Look at it again and press space to run it."}, nil
		}
		return Reply{Mission: &m, Message: unapprovedNotice([]Criterion{*cr})}, nil
	}
	if !cr.Done {
		msg := fmt.Sprintf("Check failed (%s), so %d stays open", ExitLabel(cr.LastExit), cr.ID)
		if line := cr.FirstOutputLine(); line != "" {
			msg += ": " + line
		}
		return Reply{Mission: &m, Message: msg}, nil
	}
	if note := strings.TrimSpace(c.Text); note != "" {
		m, err = h.missions.Update(ctx, c.MissionID, func(m *Mission) error {
			if cr := m.Criterion(c.Index); cr != nil {
				cr.Note = note
			}
			return nil
		})
		if err != nil {
			return Reply{}, err
		}
	}
	return Reply{Mission: &m, Message: "Check passed."}, nil
}

// VerifyAfterTurn runs the open checks of the session's active mission
// in the background, after an agent turn. Only checks the user approved
// in this project run. It skips the run when the
// mission's checks are already running, and Close stops it.
func (h *Hub) VerifyAfterTurn(sessionID string) {
	if h == nil || h.missions == nil {
		return
	}
	m, ok := h.missions.ForSession(sessionID)
	if !ok || m.Status != MissionActive || !m.HasOpenChecks() {
		return
	}
	h.bgWG.Go(func() {
		_, ran, err := h.missions.Verify(h.bgCtx, m.ID, VerifyOptions{OpenOnly: true, NoWait: true, ApprovedOnly: true})
		switch {
		case errors.Is(err, ErrCheckRunning), errors.Is(err, context.Canceled):
		case err != nil:
			slog.Warn("Failed to run mission checks", "mission", m.ID, "error", err)
		default:
			slog.Debug("Ran mission checks after turn", "mission", m.ID, "ran", len(ran))
		}
	})
}

// Close stops background check runs and waits for them to end, or for
// ctx to.
func (h *Hub) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.bgCancel()
	done := make(chan struct{})
	go func() {
		if s := h.swarmService(); s != nil {
			if err := s.Close(ctx); err != nil {
				slog.Warn("Failed to stop swarm agents", "error", err)
			}
		}
		h.bgWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
