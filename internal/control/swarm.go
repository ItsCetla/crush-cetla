package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SwarmStatus is where a swarm assignment stands.
type SwarmStatus string

const (
	// SwarmPlanned is an assignment that hasn't started yet.
	SwarmPlanned SwarmStatus = "planned"
	// SwarmRunning is an assignment whose agent is still working.
	SwarmRunning SwarmStatus = "running"
	// SwarmDone is an assignment whose agent finished and whose checks
	// ran.
	SwarmDone SwarmStatus = "done"
	// SwarmFailed is an assignment that couldn't finish.
	SwarmFailed SwarmStatus = "failed"
	// SwarmMerged is an assignment whose branch was merged.
	SwarmMerged SwarmStatus = "merged"
	// SwarmDiscarded is an assignment whose branch was thrown away.
	SwarmDiscarded SwarmStatus = "discarded"
)

// Finished reports whether the assignment's agent is no longer running.
func (s SwarmStatus) Finished() bool {
	return s != SwarmPlanned && s != SwarmRunning
}

// Settled reports whether the assignment was merged or discarded, so the
// merge board has nothing left to do with it.
func (s SwarmStatus) Settled() bool {
	return s == SwarmMerged || s == SwarmDiscarded
}

// Check states, as SwarmAssignment.CheckState reports them.
const (
	SwarmCheckPass   = "pass"
	SwarmCheckFail   = "fail"
	SwarmCheckNotRun = "not run"
)

// SwarmCheck is one check command run in an assignment's worktree.
type SwarmCheck struct {
	Criterion int           `json:"criterion"`
	Command   string        `json:"command"`
	Exit      int           `json:"exit"`
	Passed    bool          `json:"passed"`
	Output    string        `json:"output,omitempty"`
	Duration  time.Duration `json:"duration"`
	TimedOut  bool          `json:"timed_out,omitempty"`
	// Skipped says why the check didn't run, for example because the
	// user hasn't approved its command. A skipped check didn't pass.
	Skipped string `json:"skipped,omitempty"`
}

// SwarmAssignment is one machine's share of a swarm: the criteria it
// works on, the branch and worktree it works in and how it went.
type SwarmAssignment struct {
	Machine      string       `json:"machine"`
	Model        string       `json:"model,omitempty"`
	Criteria     []int        `json:"criteria"`
	CriteriaText []string     `json:"criteria_text,omitempty"`
	Branch       string       `json:"branch,omitempty"`
	Worktree     string       `json:"worktree,omitempty"`
	Status       SwarmStatus  `json:"status"`
	SessionID    string       `json:"session_id,omitempty"`
	Error        string       `json:"error,omitempty"`
	Checks       []SwarmCheck `json:"checks,omitempty"`
	Cost         float64      `json:"cost"`
	Started      time.Time    `json:"started,omitzero"`
	Finished     time.Time    `json:"finished,omitzero"`
	Added        int          `json:"added"`
	Removed      int          `json:"removed"`
	Files        []string     `json:"files,omitempty"`
	Summary      string       `json:"summary,omitempty"`
	// Commit is the commit on Branch that holds the agent's work.
	Commit string `json:"commit,omitempty"`
	// ConfigHash fingerprints the worktree's git config from before the
	// agent ran, so the swarm can tell when the agent changed it.
	ConfigHash string `json:"config_hash,omitempty"`
}

// Duration is how long the assignment ran, so far when it's running.
func (a SwarmAssignment) Duration(now time.Time) time.Duration {
	if a.Started.IsZero() {
		return 0
	}
	end := a.Finished
	if end.IsZero() {
		end = now
	}
	return end.Sub(a.Started)
}

// CheckState sums up the assignment's checks: SwarmCheckPass when every
// one that ran passed, SwarmCheckFail when any didn't and
// SwarmCheckNotRun when none ran.
func (a SwarmAssignment) CheckState() string {
	ran := false
	for _, c := range a.Checks {
		if c.Skipped != "" {
			continue
		}
		ran = true
		if !c.Passed {
			return SwarmCheckFail
		}
	}
	if !ran {
		return SwarmCheckNotRun
	}
	return SwarmCheckPass
}

// Passed returns the ids of the criteria whose checks passed.
func (a SwarmAssignment) Passed() []int {
	var out []int
	for _, c := range a.Checks {
		if c.Passed && c.Skipped == "" {
			out = append(out, c.Criterion)
		}
	}
	return out
}

// SwarmRun is one swarm: a mission split across machines.
type SwarmRun struct {
	ID           string `json:"id"`
	MissionID    string `json:"mission_id"`
	MissionTitle string `json:"mission_title,omitempty"`
	Repo         string `json:"repo"`
	// Target is the branch the run's work merges into.
	Target string `json:"target,omitempty"`
	// Base is the commit every assignment's branch started from.
	Base string `json:"base"`
	// ConfigHash fingerprints the repository's git config from when the
	// run started, so merging can tell when an agent changed it.
	ConfigHash  string            `json:"config_hash,omitempty"`
	Created     time.Time         `json:"created"`
	Updated     time.Time         `json:"updated"`
	Assignments []SwarmAssignment `json:"assignments"`
}

// Assignment returns the assignment of the named machine.
func (r *SwarmRun) Assignment(machine string) *SwarmAssignment {
	for i := range r.Assignments {
		if strings.EqualFold(r.Assignments[i].Machine, machine) {
			return &r.Assignments[i]
		}
	}
	return nil
}

// Running reports whether any of the run's agents are still working.
func (r SwarmRun) Running() bool {
	for _, a := range r.Assignments {
		if !a.Status.Finished() {
			return true
		}
	}
	return false
}

// Cost is what the run's agents spent.
func (r SwarmRun) Cost() float64 {
	var sum float64
	for _, a := range r.Assignments {
		sum += a.Cost
	}
	return sum
}

// SwarmSkip is a machine a plan left out, and why.
type SwarmSkip struct {
	Machine string `json:"machine"`
	Reason  string `json:"reason"`
}

// SwarmPlan is what starting a swarm would do: which machine gets which
// criteria. The user confirms it before anything runs.
type SwarmPlan struct {
	MissionID    string            `json:"mission_id"`
	MissionTitle string            `json:"mission_title,omitempty"`
	Assignments  []SwarmAssignment `json:"assignments,omitempty"`
	Skipped      []SwarmSkip       `json:"skipped,omitempty"`
	// Open is how many unchecked criteria the mission has.
	Open int `json:"open"`
}

// Assign returns the plan as the machine to criteria map Start takes.
func (p SwarmPlan) Assign() map[string][]int {
	out := make(map[string][]int, len(p.Assignments))
	for _, a := range p.Assignments {
		out[a.Machine] = append([]int(nil), a.Criteria...)
	}
	return out
}

// SwarmFileDiff is one file an assignment changed, before and after.
type SwarmFileDiff struct {
	Path      string `json:"path"`
	Old       string `json:"old,omitempty"`
	New       string `json:"new,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ErrSwarmUnavailable is returned when the hub has no swarm engine.
var ErrSwarmUnavailable = errors.New("swarms aren't available")

// SwarmService runs swarms. The hub forwards swarm ops to it; it lives in
// its own package so control doesn't depend on agents or git.
type SwarmService interface {
	// Plan works out which machine would get which of the mission's
	// unchecked criteria. It starts nothing.
	Plan(ctx context.Context, missionID string) (SwarmPlan, error)
	// Start runs the mission's swarm. assign is the plan the user
	// confirmed; Start refuses to run anything else.
	Start(ctx context.Context, missionID string, assign map[string][]int) (SwarmRun, error)
	// Runs lists the swarms, newest first.
	Runs(ctx context.Context) ([]SwarmRun, error)
	// Merge merges a finished assignment's branch into the run's target
	// branch and ticks the criteria whose checks passed.
	Merge(ctx context.Context, runID, machine string) (SwarmRun, string, error)
	// Discard removes an assignment's worktree and deletes its branch.
	Discard(ctx context.Context, runID, machine string) (SwarmRun, error)
	// Diff returns the files an assignment changed.
	Diff(ctx context.Context, runID, machine string) ([]SwarmFileDiff, error)
	// Close stops running agents and waits for them.
	Close(ctx context.Context) error
}

// SetSwarm sets the engine that runs swarms.
func (h *Hub) SetSwarm(s SwarmService) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.swarm = s
	h.mu.Unlock()
}

func (h *Hub) swarmService() SwarmService {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.swarm
}

func (h *Hub) swarmOp(ctx context.Context, c Command) (Reply, error) {
	s := h.swarmService()
	if s == nil {
		return Reply{}, ErrSwarmUnavailable
	}
	one := func(r SwarmRun, err error) (Reply, error) {
		if err != nil {
			return Reply{}, err
		}
		return Reply{Swarm: &r}, nil
	}
	switch c.Op {
	case OpSwarmPlan:
		p, err := s.Plan(ctx, c.MissionID)
		if err != nil {
			return Reply{}, err
		}
		return Reply{Plan: &p}, nil
	case OpSwarmStart:
		if len(c.Assign) == 0 {
			return Reply{}, errors.New("confirm a swarm plan before starting it")
		}
		return one(s.Start(ctx, c.MissionID, c.Assign))
	case OpSwarmRuns:
		runs, err := s.Runs(ctx)
		if err != nil {
			return Reply{}, err
		}
		if runs == nil {
			runs = []SwarmRun{}
		}
		return Reply{Swarms: runs}, nil
	case OpSwarmMerge:
		r, msg, err := s.Merge(ctx, c.RunID, c.Machine)
		if err != nil {
			return Reply{}, err
		}
		return Reply{Swarm: &r, Message: msg}, nil
	case OpSwarmDiscard:
		return one(s.Discard(ctx, c.RunID, c.Machine))
	case OpSwarmDiff:
		d, err := s.Diff(ctx, c.RunID, c.Machine)
		if err != nil {
			return Reply{}, err
		}
		if d == nil {
			d = []SwarmFileDiff{}
		}
		return Reply{Diff: d, RunID: c.RunID, Machine: c.Machine}, nil
	}
	return Reply{}, fmt.Errorf("unknown control op %q", c.Op)
}

// RunChecksIn runs the given criteria's check commands in dir, a worktree
// of the project, without recording the results on the mission. Only
// commands the user approved in this project run; the rest come back
// skipped. Criteria without checks are left out.
func (s *Missions) RunChecksIn(ctx context.Context, id, dir string, criteria []int) ([]SwarmCheck, error) {
	m, err := s.read(id)
	if err != nil {
		return nil, err
	}
	var out []SwarmCheck
	for _, cid := range criteria {
		c := m.Criterion(cid)
		if c == nil || !c.SelfChecking() {
			continue
		}
		sc := SwarmCheck{Criterion: c.ID, Command: c.Check}
		switch {
		case HiddenChars(c.Check):
			sc.Skipped = "the command has hidden characters"
		case !s.CheckApproved(c.Check):
			sc.Skipped = "you haven't approved this check yet"
		}
		if sc.Skipped != "" {
			out = append(out, sc)
			continue
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		r := RunCheck(ctx, dir, c.Check, c.CheckTimeout(), s.blockFuncs)
		if r.Canceled {
			return out, ctx.Err()
		}
		sc.Exit, sc.Passed, sc.Output = r.Exit, r.Passed(), r.Output
		sc.Duration, sc.TimedOut = r.Duration, r.TimedOut
		out = append(out, sc)
	}
	return out, nil
}

// TickChecked ticks the criteria whose swarm checks passed, as long as
// their check command is still the one that ran. Failed and skipped
// checks leave their criteria alone. It returns the updated mission and
// the ids it ticked.
func (s *Missions) TickChecked(ctx context.Context, id string, checks []SwarmCheck) (Mission, []int, error) {
	var ticked []int
	m, err := s.Update(ctx, id, func(m *Mission) error {
		ticked = ticked[:0]
		for _, sc := range checks {
			if !sc.Passed || sc.Skipped != "" {
				continue
			}
			c := m.Criterion(sc.Criterion)
			if c == nil || c.Check != sc.Command {
				continue
			}
			c.recordCheck(CheckResult{Output: sc.Output, At: time.Now(), Duration: sc.Duration})
			ticked = append(ticked, c.ID)
		}
		return nil
	})
	return m, ticked, err
}
