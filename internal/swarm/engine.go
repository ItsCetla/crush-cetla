package swarm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/lock"
	"github.com/charmbracelet/crush/internal/worktree"
)

// Diff limits keep the merge board responsive on large changes.
const (
	maxDiffFiles = 30
	maxDiffBytes = 64 << 10
)

// stoppedEarly is the error an assignment gets when Ta-Da stopped while
// its agent was working.
const stoppedEarly = "Ta-Da stopped before this finished"

// AgentJob is the work handed to one machine's agent.
type AgentJob struct {
	// Machine is the machine the agent runs on, as leased.
	Machine fleet.Machine
	// WorkDir is the assignment's worktree. The agent must not touch
	// anything outside it.
	WorkDir string
	// Prompt says what to build.
	Prompt string
	// Title names the agent's session.
	Title string
	// Session, when set, is called with the agent's session id as soon as
	// the session exists, so the run can point at it while it works.
	Session func(id string)
}

// AgentResult is what a machine's agent did.
type AgentResult struct {
	SessionID string
	Cost      float64
	Text      string
}

// AgentFunc runs a machine's agent to completion.
type AgentFunc func(ctx context.Context, job AgentJob) (AgentResult, error)

// Options configures an Engine.
type Options struct {
	// Missions holds the missions swarms work on.
	Missions *control.Missions
	// Store keeps the runs.
	Store *Store
	// WorkDir is the project directory. Swarms branch from the branch
	// checked out there and merge back into it.
	WorkDir string
	// Worktree says where assignment worktrees go.
	Worktree []worktree.Option
	// Machines reports every configured machine and its free slots.
	Machines func(ctx context.Context) []fleet.Status
	// Acquire leases a slot on the named machine. It must not wait for a
	// slot to free up.
	Acquire func(ctx context.Context, name string) (*fleet.Lease, error)
	// Agent runs a machine's agent.
	Agent AgentFunc
	// Ready, when set, is asked before a swarm starts and refuses it
	// with an error, for example while auto mode would approve the
	// agents' tool calls without asking.
	Ready func() error
	// ApproveChecks, when set, is asked before a machine's checks run on
	// its work. Checks run the code the agent wrote, so nothing runs
	// unless it says yes. When it is nil, checks don't run.
	ApproveChecks func(ctx context.Context, req CheckApproval) (bool, error)
}

// CheckApproval asks to run a mission's checks on one machine's work.
type CheckApproval struct {
	Machine   string
	WorkDir   string
	SessionID string
	// Checks are the checks that would run. None of them is skipped.
	Checks []control.SwarmCheck
}

// checksNotAllowed is the skip reason for checks the user didn't let
// run on an agent's work.
const checksNotAllowed = "you didn't let the checks run on this machine's work"

// Engine runs swarms. It is safe for concurrent use.
type Engine struct {
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// mu serializes the operations that change the repository, so two
	// merges can't race in the same checkout.
	mu     sync.Mutex
	closed bool
}

var _ control.SwarmService = (*Engine)(nil)

// NewEngine returns an engine with the given options.
func NewEngine(opts Options) (*Engine, error) {
	switch {
	case opts.Missions == nil:
		return nil, errors.New("swarm: no missions")
	case opts.Store == nil:
		return nil, errors.New("swarm: no store")
	case opts.Machines == nil || opts.Acquire == nil:
		return nil, errors.New("swarm: no machines")
	case opts.Agent == nil:
		return nil, errors.New("swarm: no agent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{opts: opts, ctx: ctx, cancel: cancel}, nil
}

// Plan works out which machine would get which of the mission's
// unchecked criteria.
func (e *Engine) Plan(ctx context.Context, missionID string) (control.SwarmPlan, error) {
	m, err := e.opts.Missions.Get(missionID)
	if err != nil {
		return control.SwarmPlan{}, err
	}
	return Plan(m, e.opts.Machines(ctx)), nil
}

// lease is one machine's slot and run lock while its agent works.
type lease struct {
	slot    *fleet.Lease
	release func()
}

func (l lease) free() {
	l.slot.Release()
	if l.release != nil {
		l.release()
	}
}

// Start runs the mission's swarm with the confirmed assignment of
// criteria to machines. Every machine must still be ready and every
// criterion still unchecked, or nothing starts.
func (e *Engine) Start(ctx context.Context, missionID string, assign map[string][]int) (control.SwarmRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return control.SwarmRun{}, errors.New("Ta-Da is shutting down")
	}
	if e.opts.Ready != nil {
		if err := e.opts.Ready(); err != nil {
			return control.SwarmRun{}, err
		}
	}
	m, err := e.opts.Missions.Get(missionID)
	if err != nil {
		return control.SwarmRun{}, err
	}
	machines, err := e.checkAssign(ctx, m, assign)
	if err != nil {
		return control.SwarmRun{}, err
	}

	repo, err := worktree.Root(ctx, e.opts.WorkDir)
	if err != nil {
		return control.SwarmRun{}, err
	}
	target, err := currentBranch(ctx, repo)
	if err != nil {
		return control.SwarmRun{}, err
	}
	base, err := headCommit(ctx, repo)
	if err != nil {
		return control.SwarmRun{}, fmt.Errorf("the repository has no commits to branch from: %w", err)
	}
	config, err := configHash(ctx, repo)
	if err != nil {
		return control.SwarmRun{}, fmt.Errorf("fingerprint the git config: %w", err)
	}
	for _, name := range machines {
		if b := BranchName(m, name); branchExists(ctx, repo, b) {
			return control.SwarmRun{}, fmt.Errorf("branch %s already exists; merge or discard the last swarm on this mission first", b)
		}
	}

	now := time.Now()
	run := control.SwarmRun{
		ID:           newRunID(now),
		MissionID:    m.ID,
		MissionTitle: m.Title,
		Repo:         repo,
		Target:       target,
		Base:         base,
		ConfigHash:   config,
		Created:      now,
	}

	// Lease every slot before creating anything: a swarm that can only
	// get some of its machines would quietly drop criteria.
	leases := make(map[string]lease, len(machines))
	freeAll := func() {
		for _, l := range leases {
			l.free()
		}
	}
	for _, name := range machines {
		slot, err := e.opts.Acquire(ctx, name)
		if err != nil {
			freeAll()
			if errors.Is(err, fleet.ErrNoneFree) {
				return control.SwarmRun{}, fmt.Errorf("%s isn't free anymore; plan the swarm again", name)
			}
			return control.SwarmRun{}, fmt.Errorf("lease %s: %w", name, err)
		}
		release, err := lock.TryFile(e.opts.Store.lockPath(run.ID, strings.ToLower(name)))
		if err != nil {
			slot.Release()
			freeAll()
			return control.SwarmRun{}, fmt.Errorf("lock %s: %w", name, err)
		}
		leases[name] = lease{slot: slot, release: release}
	}

	for _, name := range machines {
		l := leases[name]
		a := control.SwarmAssignment{
			Machine: name,
			Model:   l.slot.Machine.Model,
			Branch:  BranchName(m, name),
			Status:  control.SwarmRunning,
			Started: now,
		}
		for _, id := range assign[name] {
			a.Criteria = append(a.Criteria, id)
			a.CriteriaText = append(a.CriteriaText, m.Criterion(id).Text)
		}
		wt, err := worktree.Create(ctx, repo, a.Branch, base, e.opts.Worktree...)
		if err == nil {
			a.Worktree = wt.Path
			if a.ConfigHash, err = configHash(ctx, wt.Path); err != nil {
				err = fmt.Errorf("fingerprint the git config: %w", err)
			}
		}
		if err != nil {
			a.Status, a.Error, a.Finished = control.SwarmFailed, err.Error(), now
			l.free()
			delete(leases, name)
		}
		run.Assignments = append(run.Assignments, a)
	}

	if err := e.opts.Store.Save(ctx, run); err != nil {
		freeAll()
		for _, a := range run.Assignments {
			e.cleanup(context.WithoutCancel(ctx), run.Repo, a)
		}
		return control.SwarmRun{}, err
	}

	for i := range run.Assignments {
		a := run.Assignments[i]
		l, ok := leases[a.Machine]
		if !ok {
			continue
		}
		e.wg.Add(1)
		go e.work(run, a, l, m)
	}
	return run, nil
}

// checkAssign makes sure assign is a plan that can run: known, ready
// machines, each with unchecked criteria nobody else has. It returns the
// machines in name order.
func (e *Engine) checkAssign(ctx context.Context, m control.Mission, assign map[string][]int) ([]string, error) {
	if len(assign) == 0 {
		return nil, errors.New("the swarm has no machines")
	}
	statuses := make(map[string]fleet.Status)
	for _, s := range e.opts.Machines(ctx) {
		statuses[s.Name] = s
	}
	seen := make(map[int]string)
	names := make([]string, 0, len(assign))
	for name, ids := range assign {
		s, ok := statuses[name]
		if !ok {
			return nil, fmt.Errorf("no machine is named %q", name)
		}
		if reason := notReady(s); reason != "" {
			return nil, fmt.Errorf("%s %s; plan the swarm again", name, reason)
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("%s has no criteria to work on", name)
		}
		for _, id := range ids {
			c := m.Criterion(id)
			switch {
			case c == nil:
				return nil, fmt.Errorf("mission %s has no criterion %d", m.ID, id)
			case c.Done:
				return nil, fmt.Errorf("criterion %d is already checked off", id)
			case seen[id] != "":
				return nil, fmt.Errorf("criterion %d is assigned to both %s and %s", id, seen[id], name)
			}
			seen[id] = name
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// checks runs the assignment's checks on its worktree once the user
// allows it. Checks that need no approval to be skipped come back skipped
// without asking.
func (e *Engine) checks(ctx context.Context, missionID string, a control.SwarmAssignment) ([]control.SwarmCheck, error) {
	planned, err := e.opts.Missions.PlanChecksIn(missionID, a.Criteria)
	if err != nil {
		return nil, err
	}
	var runnable []control.SwarmCheck
	for _, c := range planned {
		if c.Skipped == "" {
			runnable = append(runnable, c)
		}
	}
	if len(runnable) == 0 {
		return planned, nil
	}
	ok := false
	if e.opts.ApproveChecks != nil {
		ok, err = e.opts.ApproveChecks(ctx, CheckApproval{
			Machine:   a.Machine,
			WorkDir:   a.Worktree,
			SessionID: a.SessionID,
			Checks:    runnable,
		})
		if err != nil && ctx.Err() == nil {
			slog.Warn("Failed to ask about swarm checks", "machine", a.Machine, "error", err)
		}
	}
	if !ok {
		for i := range planned {
			if planned[i].Skipped == "" {
				planned[i].Skipped = checksNotAllowed
			}
		}
		return planned, nil
	}
	return e.opts.Missions.RunPlannedChecks(ctx, missionID, a.Worktree, planned)
}

// work runs one assignment's agent, commits what it did, runs the checks
// and records the outcome.
func (e *Engine) work(run control.SwarmRun, a control.SwarmAssignment, l lease, m control.Mission) {
	defer e.wg.Done()
	defer l.free()
	ctx := e.ctx
	// Recording the outcome must happen even when Ta-Da is stopping.
	after := context.WithoutCancel(ctx)

	res, agentErr := e.opts.Agent(ctx, AgentJob{
		Machine: l.slot.Machine,
		WorkDir: a.Worktree,
		Prompt:  Prompt(m, a),
		Title:   fmt.Sprintf("Swarm: %s (%s)", m.Title, a.Machine),
		Session: func(id string) {
			_, err := e.opts.Store.Update(after, run.ID, func(r *control.SwarmRun) error {
				if p := r.Assignment(a.Machine); p != nil {
					p.SessionID = id
				}
				return nil
			})
			if err != nil {
				slog.Warn("Failed to record swarm session", "run", run.ID, "machine", a.Machine, "error", err)
			}
		},
	})
	a.SessionID = cmp.Or(res.SessionID, a.SessionID)
	a.Cost, a.Summary = res.Cost, summarize(res.Text)

	// Commit what the agent did even when it failed, so its work can be
	// looked at before it's discarded.
	// Committing runs the filters the config names, so it waits until
	// the config is known to be the one from before the agent ran.
	commitCtx, cancel := context.WithTimeout(after, time.Minute)
	var commit string
	commitErr := verifyConfig(commitCtx, a.Worktree, a.ConfigHash)
	if commitErr == nil {
		commit, _, commitErr = commitAll(commitCtx, a.Worktree, fmt.Sprintf("swarm(%s): %s", a.Machine, m.Title))
	}
	cancel()
	if commitErr == nil {
		a.Commit = commit
		a.Added, a.Removed, a.Files, _ = numstat(after, a.Worktree, run.Base, commit)
	}

	var checkErr error
	if agentErr == nil && commitErr == nil && ctx.Err() == nil {
		a.Checks, checkErr = e.checks(ctx, run.MissionID, a)
	}

	a.Finished = time.Now()
	switch {
	case ctx.Err() != nil:
		a.Status, a.Error = control.SwarmFailed, stoppedEarly
	case errors.Is(commitErr, errConfigChanged):
		a.Status, a.Error = control.SwarmFailed, "the git config changed while the agent worked, so Ta-Da didn't commit its work; check the config, then discard it"
	case agentErr != nil:
		a.Status, a.Error = control.SwarmFailed, agentErr.Error()
	case commitErr != nil:
		a.Status, a.Error = control.SwarmFailed, "commit the agent's work: "+commitErr.Error()
	case checkErr != nil:
		a.Status, a.Error = control.SwarmFailed, "run checks: "+checkErr.Error()
	default:
		a.Status = control.SwarmDone
	}
	_, err := e.opts.Store.Update(after, run.ID, func(r *control.SwarmRun) error {
		p := r.Assignment(a.Machine)
		if p == nil {
			return fmt.Errorf("assignment %s is gone", a.Machine)
		}
		*p = a
		return nil
	})
	if err != nil {
		slog.Error("Failed to record swarm result", "run", run.ID, "machine", a.Machine, "error", err)
	}
}

// summarize keeps the first lines of the agent's final answer.
func summarize(text string) string {
	text = strings.TrimSpace(text)
	const limit = 600
	if len(text) <= limit {
		return text
	}
	cut := strings.LastIndexAny(text[:limit], " \n")
	if cut < limit/2 {
		cut = limit
	}
	return strings.TrimSpace(text[:cut]) + "…"
}

// Runs lists the swarms, newest first. An assignment left running by a
// Ta-Da that has since quit is marked failed.
func (e *Engine) Runs(ctx context.Context) ([]control.SwarmRun, error) {
	runs, err := e.opts.Store.List()
	if err != nil {
		return nil, err
	}
	for i, r := range runs {
		if !r.Running() {
			continue
		}
		if fixed, ok := e.recover(ctx, r); ok {
			runs[i] = fixed
		}
	}
	return runs, nil
}

// get returns a run with abandoned assignments marked failed.
func (e *Engine) get(ctx context.Context, id string) (control.SwarmRun, error) {
	r, err := e.opts.Store.Find(id)
	if err != nil {
		return r, err
	}
	if r.Running() {
		if fixed, ok := e.recover(ctx, r); ok {
			r = fixed
		}
	}
	return r, nil
}

// recover marks the run's running assignments whose agents no process is
// holding the lock for as failed.
func (e *Engine) recover(ctx context.Context, r control.SwarmRun) (control.SwarmRun, bool) {
	var stale []string
	for _, a := range r.Assignments {
		if a.Status.Finished() {
			continue
		}
		release, err := lock.TryFile(e.opts.Store.lockPath(r.ID, strings.ToLower(a.Machine)))
		if err != nil {
			continue
		}
		release()
		stale = append(stale, a.Machine)
	}
	if len(stale) == 0 {
		return r, false
	}
	fixed, err := e.opts.Store.Update(ctx, r.ID, func(r *control.SwarmRun) error {
		for _, name := range stale {
			if a := r.Assignment(name); a != nil && !a.Status.Finished() {
				a.Status, a.Error, a.Finished = control.SwarmFailed, stoppedEarly, time.Now()
			}
		}
		return nil
	})
	if err != nil {
		slog.Warn("Failed to mark abandoned swarm agents", "run", r.ID, "error", err)
		return r, false
	}
	return fixed, true
}

func (e *Engine) assignment(ctx context.Context, runID, machine string) (control.SwarmRun, control.SwarmAssignment, error) {
	r, err := e.get(ctx, runID)
	if err != nil {
		return r, control.SwarmAssignment{}, err
	}
	a := r.Assignment(machine)
	if a == nil {
		return r, control.SwarmAssignment{}, fmt.Errorf("swarm %s has no machine %q", r.ID, machine)
	}
	return r, *a, nil
}

// Merge merges a finished assignment's branch into the branch checked out
// in the run's repository and ticks the criteria whose checks passed. It
// refuses when that checkout has uncommitted changes, and a merge that
// conflicts is aborted, leaving the checkout as it was.
func (e *Engine) Merge(ctx context.Context, runID, machine string) (control.SwarmRun, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, a, err := e.assignment(ctx, runID, machine)
	if err != nil {
		return r, "", err
	}
	switch a.Status {
	case control.SwarmDone:
	case control.SwarmPlanned, control.SwarmRunning:
		return r, "", fmt.Errorf("%s is still working", a.Machine)
	case control.SwarmFailed:
		return r, "", fmt.Errorf("%s didn't finish, so there's nothing to merge; discard it", a.Machine)
	default:
		return r, "", fmt.Errorf("%s was already %s", a.Machine, a.Status)
	}
	if a.Commit == "" {
		return r, "", fmt.Errorf("%s has no recorded commit to merge; discard it", a.Machine)
	}
	if !branchExists(ctx, r.Repo, a.Branch) {
		return r, "", fmt.Errorf("branch %s is gone", a.Branch)
	}
	// Merging runs the merge drivers and filters the config names, and
	// even git status can run filters.
	if err := verifyConfig(ctx, r.Repo, r.ConfigHash); err != nil {
		if errors.Is(err, errConfigChanged) {
			return r, "", fmt.Errorf("%w, so Ta-Da won't merge %s; check the config, then discard it", err, a.Branch)
		}
		return r, "", err
	}
	// Merge the commit the swarm recorded and checked, not whatever the
	// branch points at now.
	tip, err := git(ctx, r.Repo, "rev-parse", "--verify", "-q", "refs/heads/"+a.Branch+"^{commit}")
	if err != nil {
		return r, "", fmt.Errorf("read branch %s: %w", a.Branch, err)
	}
	if tip != a.Commit {
		return r, "", fmt.Errorf("branch %s moved after %s finished, so Ta-Da won't merge it; discard it", a.Branch, a.Machine)
	}
	if mergeInProgress(ctx, r.Repo) {
		return r, "", errors.New("the checkout is in the middle of a merge; finish or abort it first")
	}
	dirty, err := dirtyPaths(ctx, r.Repo)
	if err != nil {
		return r, "", err
	}
	if len(dirty) > 0 {
		return r, "", fmt.Errorf("%s has uncommitted changes; commit or stash them before merging", r.Repo)
	}
	into, err := currentBranch(ctx, r.Repo)
	if err != nil {
		return r, "", err
	}
	if into == a.Branch {
		return r, "", fmt.Errorf("%s is checked out; check out the branch to merge into", a.Branch)
	}

	var args []string
	if !hasIdentity(ctx, r.Repo) {
		args = append(args, "-c", "user.name=Ta-Da swarm", "-c", "user.email=swarm@tada.invalid")
	}
	msg := fmt.Sprintf("Merge %s (swarm on %s)", a.Branch, a.Machine)
	args = append(args, "merge", "--no-ff", "--no-edit", "--no-verify", "-m", msg, a.Commit)
	if _, mergeErr := git(ctx, r.Repo, args...); mergeErr != nil {
		if !mergeInProgress(ctx, r.Repo) {
			return r, "", mergeErr
		}
		paths := conflictedPaths(ctx, r.Repo)
		if _, err := git(context.WithoutCancel(ctx), r.Repo, "merge", "--abort"); err != nil {
			return r, "", fmt.Errorf("merging %s conflicted and aborting the merge failed too; run git merge --abort in %s: %w", a.Branch, r.Repo, err)
		}
		where := ""
		if len(paths) > 0 {
			where = " in " + strings.Join(paths, ", ")
		}
		return r, "", fmt.Errorf("merging %s into %s conflicts%s; the merge was aborted and %s is unchanged", a.Branch, into, where, into)
	}

	// The merge happened: from here on, problems are reported but don't
	// undo it.
	after := context.WithoutCancel(ctx)
	var ticked []int
	var notes []string
	if len(a.Passed()) > 0 {
		if _, ticked, err = e.opts.Missions.TickChecked(after, r.MissionID, a.Checks); err != nil {
			notes = append(notes, "couldn't tick the criteria: "+err.Error())
		}
	}
	if err := e.removeWorktree(after, r.Repo, a.Worktree); err != nil {
		notes = append(notes, "couldn't remove the worktree: "+err.Error())
	}
	if _, err := git(after, r.Repo, "branch", "-d", a.Branch); err != nil {
		notes = append(notes, "couldn't delete the branch: "+err.Error())
	}
	r, err = e.opts.Store.Update(after, r.ID, func(r *control.SwarmRun) error {
		if p := r.Assignment(a.Machine); p != nil {
			p.Status = control.SwarmMerged
		}
		return nil
	})
	if err != nil {
		notes = append(notes, "couldn't record the merge: "+err.Error())
	}

	out := fmt.Sprintf("Merged %s into %s", a.Branch, into)
	if len(ticked) > 0 {
		out += "; ticked " + plural(len(ticked), "criterion", "criteria") + " " + describeCriteria(ticked)
	} else {
		out += "; no checks passed, so no criteria were ticked"
	}
	if len(notes) > 0 {
		out += " (" + strings.Join(notes, "; ") + ")"
	}
	return r, out + ".", nil
}

// Discard removes an assignment's worktree and deletes its branch.
func (e *Engine) Discard(ctx context.Context, runID, machine string) (control.SwarmRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, a, err := e.assignment(ctx, runID, machine)
	if err != nil {
		return r, err
	}
	switch {
	case !a.Status.Finished():
		return r, fmt.Errorf("%s is still working", a.Machine)
	case a.Status.Settled():
		return r, fmt.Errorf("%s was already %s", a.Machine, a.Status)
	}
	if err := e.removeWorktree(ctx, r.Repo, a.Worktree); err != nil {
		return r, err
	}
	if branchExists(ctx, r.Repo, a.Branch) {
		if _, err := git(ctx, r.Repo, "branch", "-D", a.Branch); err != nil {
			return r, err
		}
	}
	return e.opts.Store.Update(context.WithoutCancel(ctx), r.ID, func(r *control.SwarmRun) error {
		if p := r.Assignment(a.Machine); p != nil {
			p.Status = control.SwarmDiscarded
		}
		return nil
	})
}

// removeWorktree force-removes an assignment's worktree. A worktree whose
// directory is already gone is pruned instead.
func (e *Engine) removeWorktree(ctx context.Context, repo, path string) error {
	if path == "" {
		return nil
	}
	err := worktree.Remove(ctx, worktree.Worktree{Path: path, Repo: repo}, true)
	if err == nil {
		return nil
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		_, pruneErr := git(ctx, repo, "worktree", "prune")
		return pruneErr
	}
	return err
}

// cleanup undoes an assignment's worktree and branch after a failed
// start.
func (e *Engine) cleanup(ctx context.Context, repo string, a control.SwarmAssignment) {
	if a.Worktree == "" {
		return
	}
	if err := e.removeWorktree(ctx, repo, a.Worktree); err != nil {
		slog.Warn("Failed to remove swarm worktree", "path", a.Worktree, "error", err)
		return
	}
	if _, err := git(ctx, repo, "branch", "-D", a.Branch); err != nil {
		slog.Warn("Failed to delete swarm branch", "branch", a.Branch, "error", err)
	}
}

// Diff returns the files a finished assignment changed, before and after.
func (e *Engine) Diff(ctx context.Context, runID, machine string) ([]control.SwarmFileDiff, error) {
	r, a, err := e.assignment(ctx, runID, machine)
	if err != nil {
		return nil, err
	}
	if !a.Status.Finished() {
		return nil, fmt.Errorf("%s is still working", a.Machine)
	}
	if a.Commit == "" || a.Commit == r.Base {
		return nil, nil
	}
	if err := verifyConfig(ctx, r.Repo, r.ConfigHash); err != nil {
		if errors.Is(err, errConfigChanged) {
			return nil, fmt.Errorf("%w, so Ta-Da won't show %s's changes; check the config, then discard them", err, a.Machine)
		}
		return nil, err
	}
	args := append(append([]string{"diff", "--numstat", "--no-renames"}, noExternalDiff...), r.Base, a.Commit, "--")
	out, err := git(ctx, r.Repo, args...)
	if err != nil {
		return nil, err
	}
	var diffs []control.SwarmFileDiff
	for line := range strings.SplitSeq(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		if len(diffs) == maxDiffFiles {
			diffs = append(diffs, control.SwarmFileDiff{Path: "…", Truncated: true})
			break
		}
		d := control.SwarmFileDiff{Path: parts[2], Binary: parts[0] == "-"}
		if !d.Binary {
			var cutOld, cutNew bool
			d.Old, cutOld = e.blob(ctx, r.Repo, r.Base, d.Path)
			d.New, cutNew = e.blob(ctx, r.Repo, a.Commit, d.Path)
			d.Truncated = cutOld || cutNew
		}
		diffs = append(diffs, d)
	}
	return diffs, nil
}

// blob returns a file's contents at a commit, cut to maxDiffBytes, or ""
// when the file doesn't exist there.
func (e *Engine) blob(ctx context.Context, repo, commit, path string) (string, bool) {
	out, err := gitRaw(ctx, repo, "cat-file", "blob", commit+":"+path)
	if err != nil {
		return "", false
	}
	if len(out) > maxDiffBytes {
		return out[:maxDiffBytes], true
	}
	return out, false
}

// Close stops the running agents and waits for them to record how far
// they got.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.cancel()
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until every running agent has finished. Tests use it.
func (e *Engine) Wait() { e.wg.Wait() }

// Prompt is what a machine's agent is asked to do.
func Prompt(m control.Mission, a control.SwarmAssignment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are one of several agents working on the mission %q at the same time, each on its own machine.\n\n", m.Title)
	if g := strings.TrimSpace(m.Goal); g != "" {
		fmt.Fprintf(&b, "Mission goal:\n%s\n\n", g)
	}
	b.WriteString("Your share is these acceptance criteria. Make each of them true:\n")
	for _, id := range a.Criteria {
		c := m.Criterion(id)
		if c == nil {
			continue
		}
		fmt.Fprintf(&b, "%d. %s\n", c.ID, c.Text)
		if c.SelfChecking() {
			fmt.Fprintf(&b, "   It is checked by running: %s\n", c.Check)
		}
	}
	var others []string
	for _, c := range m.Criteria {
		if !c.Done && !slices.Contains(a.Criteria, c.ID) {
			others = append(others, fmt.Sprintf("%d. %s", c.ID, c.Text))
		}
	}
	if len(others) > 0 {
		b.WriteString("\nOther agents are handling these, so leave them alone:\n")
		b.WriteString(strings.Join(others, "\n"))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nYou work in %s, a separate checkout on the branch %s. ", a.Worktree, a.Branch)
	b.WriteString("Only read and change files inside it. Don't commit, switch branches or touch other checkouts: ")
	b.WriteString("Ta-Da commits your work when you finish and runs the checks itself. ")
	b.WriteString("When you're done, reply with a short summary of what you changed.\n")
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
