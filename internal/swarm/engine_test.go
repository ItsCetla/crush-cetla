package swarm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/worktree"
	"github.com/stretchr/testify/require"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
}

// runGit runs git in dir, isolated from the user's and the system's
// config, and returns its trimmed output.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.CommandContext(context.Background(), "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return strings.TrimSpace(string(out))
}

// newRepo creates a repository with one commit on main. Identity and
// signing are set in the repository so the engine's own commands, which
// don't use runGit's environment, work too.
func newRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	repo := filepath.Join(tmp, "app")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	commitFile(t, repo, "README.md", "hello\n", "initial commit")
	return repo
}

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", msg)
}

// harness is an engine over a temp repository with fake machines and a
// fake agent.
type harness struct {
	repo     string
	missions *control.Missions
	store    *Store
	engine   *Engine
	mission  control.Mission

	mu   sync.Mutex
	jobs []AgentJob
	// write maps a machine to the files its agent writes, relative to
	// the worktree.
	write map[string]map[string]string
	// fail makes a machine's agent return an error.
	fail map[string]bool
	// act runs in a machine's worktree after its agent writes its files.
	act map[string]func(dir string)
	// ready is what the engine's Ready hook returns.
	ready error
	// denyChecks makes the engine's ApproveChecks hook say no.
	denyChecks bool
	// approvals are the check approvals the engine asked for.
	approvals []CheckApproval
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := t.Context()
	h := &harness{
		repo:  newRepo(t),
		write: map[string]map[string]string{},
		fail:  map[string]bool{},
		act:   map[string]func(string){},
	}

	var err error
	h.missions, err = control.NewMissions(filepath.Join(t.TempDir(), "missions"))
	require.NoError(t, err)
	h.missions.SetWorkDir(h.repo)
	h.missions.SetApprovals(filepath.Join(t.TempDir(), control.ApprovalsFileName))

	h.mission, err = h.missions.Create(ctx, "Add files", "Two files exist.", []string{"a exists", "b exists", "docs read well"}, "")
	require.NoError(t, err)
	h.mission, err = h.missions.Update(ctx, h.mission.ID, func(m *control.Mission) error {
		m.Criterion(1).Check = "test -f a.txt"
		m.Criterion(2).Check = "test -f b.txt"
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, h.missions.ApproveChecks(ctx, "test -f a.txt", "test -f b.txt"))

	h.store, err = NewStore(StoreDir(t.TempDir()))
	require.NoError(t, err)
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	h.engine, err = NewEngine(Options{
		Missions: h.missions,
		Store:    h.store,
		WorkDir:  h.repo,
		Worktree: []worktree.Option{worktree.WithDir(wtDir)},
		Machines: func(context.Context) []fleet.Status {
			return []fleet.Status{readyMachine("alpha"), readyMachine("beta")}
		},
		Acquire: func(_ context.Context, name string) (*fleet.Lease, error) {
			return &fleet.Lease{Machine: readyMachine(name).Machine}, nil
		},
		Agent: h.agent,
		Ready: func() error { return h.ready },
		ApproveChecks: func(_ context.Context, req CheckApproval) (bool, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.approvals = append(h.approvals, req)
			return !h.denyChecks, nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.engine.Close(context.Background()) })
	return h
}

func (h *harness) agent(_ context.Context, job AgentJob) (AgentResult, error) {
	h.mu.Lock()
	h.jobs = append(h.jobs, job)
	files := h.write[job.Machine.Name]
	fail := h.fail[job.Machine.Name]
	act := h.act[job.Machine.Name]
	h.mu.Unlock()

	job.Session("session-" + job.Machine.Name)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(job.WorkDir, name), []byte(content), 0o644); err != nil {
			return AgentResult{}, err
		}
	}
	if act != nil {
		act(job.WorkDir)
	}
	if fail {
		return AgentResult{SessionID: "session-" + job.Machine.Name}, errors.New("the model gave up")
	}
	return AgentResult{SessionID: "session-" + job.Machine.Name, Cost: 0.25, Text: "Wrote the files."}, nil
}

// start runs the swarm and waits for every agent to finish.
func (h *harness) start(t *testing.T, assign map[string][]int) control.SwarmRun {
	t.Helper()
	run, err := h.engine.Start(t.Context(), h.mission.ID, assign)
	require.NoError(t, err)
	h.engine.Wait()
	got, err := h.store.Get(run.ID)
	require.NoError(t, err)
	return got
}

func TestEngineRunsEachMachineInItsOwnWorktree(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.write["alpha"] = map[string]string{"a.txt": "a\n"}
	h.write["beta"] = map[string]string{"b.txt": "b\n"}

	run := h.start(t, map[string][]int{"alpha": {1, 3}, "beta": {2}})
	require.Equal(t, "main", run.Target)
	require.False(t, run.Running())
	require.InDelta(t, 0.5, run.Cost(), 1e-9)

	alpha, beta := run.Assignment("alpha"), run.Assignment("beta")
	require.NotNil(t, alpha)
	require.NotNil(t, beta)
	for _, a := range []*control.SwarmAssignment{alpha, beta} {
		require.Equal(t, control.SwarmDone, a.Status, a.Error)
		require.NotEmpty(t, a.Worktree)
		require.NotEqual(t, h.repo, a.Worktree)
		require.DirExists(t, a.Worktree)
		require.Equal(t, "session-"+a.Machine, a.SessionID)
		require.NotEmpty(t, a.Commit)
		require.NotEqual(t, run.Base, a.Commit)
		require.Equal(t, 1, a.Added)
		require.Equal(t, "Wrote the files.", a.Summary)
	}
	require.NotEqual(t, alpha.Worktree, beta.Worktree)
	require.Equal(t, "swarm/add-files/alpha", alpha.Branch)
	require.Equal(t, []string{"a.txt"}, alpha.Files)

	// Each agent was told to stay in its own worktree.
	require.Len(t, h.jobs, 2)
	for _, j := range h.jobs {
		require.Contains(t, j.Prompt, j.WorkDir)
	}

	// Only the assignment's own criteria with checks ran.
	require.Len(t, alpha.Checks, 1)
	require.Equal(t, 1, alpha.Checks[0].Criterion)
	require.True(t, alpha.Checks[0].Passed)
	require.Equal(t, control.SwarmCheckPass, alpha.CheckState())
	require.Len(t, beta.Checks, 1)
	require.True(t, beta.Checks[0].Passed)

	// Each machine's checks were asked about before they ran.
	require.Len(t, h.approvals, 2)
	for _, req := range h.approvals {
		require.Equal(t, "session-"+req.Machine, req.SessionID)
		require.Len(t, req.Checks, 1)
	}

	// The project checkout is untouched until a merge.
	require.NoFileExists(t, filepath.Join(h.repo, "a.txt"))
	require.Empty(t, runGit(t, h.repo, "status", "--porcelain"))

	diff, err := h.engine.Diff(ctx, run.ID, "alpha")
	require.NoError(t, err)
	require.Equal(t, []control.SwarmFileDiff{{Path: "a.txt", New: "a\n"}}, diff)
}

func TestEngineMergeTicksPassedCriteria(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.write["alpha"] = map[string]string{"a.txt": "a\n"}
	h.write["beta"] = map[string]string{"b.txt": "b\n"}
	run := h.start(t, map[string][]int{"alpha": {1, 3}, "beta": {2}})
	alpha := *run.Assignment("alpha")

	merged, msg, err := h.engine.Merge(ctx, run.ID[:15], "alpha")
	require.NoError(t, err)
	require.Contains(t, msg, "Merged swarm/add-files/alpha into main")
	require.Contains(t, msg, "ticked criterion 1")
	require.Equal(t, control.SwarmMerged, merged.Assignment("alpha").Status)

	require.FileExists(t, filepath.Join(h.repo, "a.txt"))
	require.NoDirExists(t, alpha.Worktree)
	require.Empty(t, runGit(t, h.repo, "branch", "--list", alpha.Branch))
	require.Empty(t, runGit(t, h.repo, "status", "--porcelain"))

	m, err := h.missions.Get(h.mission.ID)
	require.NoError(t, err)
	require.True(t, m.Criterion(1).Done, "its check passed")
	require.False(t, m.Criterion(2).Done, "beta isn't merged")
	require.False(t, m.Criterion(3).Done, "it has no check")

	_, _, err = h.engine.Merge(ctx, run.ID, "alpha")
	require.ErrorContains(t, err, "already merged")
}

func TestEngineDiscardRemovesWorktreeAndBranch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.write["beta"] = map[string]string{"b.txt": "b\n"}
	run := h.start(t, map[string][]int{"beta": {2}})
	beta := *run.Assignment("beta")
	require.DirExists(t, beta.Worktree)

	got, err := h.engine.Discard(ctx, run.ID, "beta")
	require.NoError(t, err)
	require.Equal(t, control.SwarmDiscarded, got.Assignment("beta").Status)
	require.NoDirExists(t, beta.Worktree)
	require.Empty(t, runGit(t, h.repo, "branch", "--list", beta.Branch))
	require.NoFileExists(t, filepath.Join(h.repo, "b.txt"))

	m, err := h.missions.Get(h.mission.ID)
	require.NoError(t, err)
	require.False(t, m.Criterion(2).Done)

	_, err = h.engine.Discard(ctx, run.ID, "beta")
	require.ErrorContains(t, err, "already discarded")
	_, _, err = h.engine.Merge(ctx, run.ID, "beta")
	require.ErrorContains(t, err, "already discarded")
}

func TestEngineMergeConflictAborts(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.write["alpha"] = map[string]string{"README.md": "from alpha\n", "a.txt": "a\n"}
	run := h.start(t, map[string][]int{"alpha": {1}})
	alpha := *run.Assignment("alpha")

	// The project moves on in a way that conflicts with alpha's work.
	commitFile(t, h.repo, "README.md", "from main\n", "change readme")
	head := runGit(t, h.repo, "rev-parse", "HEAD")

	_, _, err := h.engine.Merge(ctx, run.ID, "alpha")
	require.ErrorContains(t, err, "conflicts in README.md")
	require.ErrorContains(t, err, "the merge was aborted")

	require.Equal(t, head, runGit(t, h.repo, "rev-parse", "HEAD"))
	require.Empty(t, runGit(t, h.repo, "status", "--porcelain"))
	readme, err := os.ReadFile(filepath.Join(h.repo, "README.md"))
	require.NoError(t, err)
	require.Equal(t, "from main\n", string(readme))

	// The assignment is left as it was, so it can still be discarded.
	got, err := h.store.Get(run.ID)
	require.NoError(t, err)
	require.Equal(t, control.SwarmDone, got.Assignment("alpha").Status)
	require.DirExists(t, alpha.Worktree)
	m, err := h.missions.Get(h.mission.ID)
	require.NoError(t, err)
	require.False(t, m.Criterion(1).Done)
}

func TestEngineMergeRefusesDirtyCheckout(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write["alpha"] = map[string]string{"a.txt": "a\n"}
	run := h.start(t, map[string][]int{"alpha": {1}})

	require.NoError(t, os.WriteFile(filepath.Join(h.repo, "README.md"), []byte("edited\n"), 0o644))
	_, _, err := h.engine.Merge(t.Context(), run.ID, "alpha")
	require.ErrorContains(t, err, "uncommitted changes")
	require.NoFileExists(t, filepath.Join(h.repo, "a.txt"))
}

func TestEngineSkipsUnapprovedChecks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	_, err := h.missions.Update(ctx, h.mission.ID, func(m *control.Mission) error {
		m.Criterion(1).Check = "test -f a.txt && touch ran"
		return nil
	})
	require.NoError(t, err)
	h.write["alpha"] = map[string]string{"a.txt": "a\n"}
	run := h.start(t, map[string][]int{"alpha": {1}})
	alpha := run.Assignment("alpha")

	require.Len(t, alpha.Checks, 1)
	require.NotEmpty(t, alpha.Checks[0].Skipped)
	require.False(t, alpha.Checks[0].Passed)
	require.NoFileExists(t, filepath.Join(alpha.Worktree, "ran"))

	_, msg, err := h.engine.Merge(ctx, run.ID, "alpha")
	require.NoError(t, err)
	require.Contains(t, msg, "no checks passed")
	m, err := h.missions.Get(h.mission.ID)
	require.NoError(t, err)
	require.False(t, m.Criterion(1).Done)
}

func TestEngineFailedAgentKeepsWorkForReview(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.write["alpha"] = map[string]string{"a.txt": "half done\n"}
	h.fail["alpha"] = true
	run := h.start(t, map[string][]int{"alpha": {1}})
	alpha := run.Assignment("alpha")

	require.Equal(t, control.SwarmFailed, alpha.Status)
	require.Equal(t, "the model gave up", alpha.Error)
	require.Empty(t, alpha.Checks, "checks don't run after a failure")
	require.NotEqual(t, run.Base, alpha.Commit, "the work is committed for review")

	_, _, err := h.engine.Merge(ctx, run.ID, "alpha")
	require.ErrorContains(t, err, "nothing to merge")
	_, err = h.engine.Discard(ctx, run.ID, "alpha")
	require.NoError(t, err)
}

func TestEngineStartRefusals(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)

	h.ready = errors.New("auto mode is on")
	_, err := h.engine.Start(ctx, h.mission.ID, map[string][]int{"alpha": {1}})
	require.ErrorContains(t, err, "auto mode is on")
	h.ready = nil

	for name, tc := range map[string]struct {
		assign map[string][]int
		want   string
	}{
		"empty":         {map[string][]int{}, "no machines"},
		"unknown":       {map[string][]int{"zulu": {1}}, `no machine is named "zulu"`},
		"no criteria":   {map[string][]int{"alpha": {}}, "no criteria"},
		"bad criterion": {map[string][]int{"alpha": {9}}, "no criterion 9"},
		"shared":        {map[string][]int{"alpha": {1}, "beta": {1}}, "assigned to both"},
	} {
		_, err := h.engine.Start(ctx, h.mission.ID, tc.assign)
		require.ErrorContains(t, err, tc.want, name)
	}

	// Nothing was created by the refused starts.
	runs, err := h.store.List()
	require.NoError(t, err)
	require.Empty(t, runs)
	require.Empty(t, runGit(t, h.repo, "branch", "--list", "swarm/*"))
	require.Empty(t, h.jobs)

	// A branch left from an earlier swarm blocks a new one.
	h.write["alpha"] = map[string]string{"a.txt": "a\n"}
	h.start(t, map[string][]int{"alpha": {1}})
	_, err = h.engine.Start(ctx, h.mission.ID, map[string][]int{"alpha": {1}})
	require.ErrorContains(t, err, "already exists")
}

func TestEngineConfigChangedByAgentFails(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.write["alpha"] = map[string]string{"a.txt": "a\n", ".gitattributes": "* filter=evil\n"}
	h.act["alpha"] = func(dir string) {
		runGit(t, dir, "config", "filter.evil.clean", "touch pwned; cat")
	}
	run := h.start(t, map[string][]int{"alpha": {1}})
	alpha := run.Assignment("alpha")

	require.Equal(t, control.SwarmFailed, alpha.Status)
	require.Contains(t, alpha.Error, "git config changed while the agent worked")
	require.Empty(t, alpha.Commit, "nothing was committed")
	require.Empty(t, alpha.Checks)
	require.NoFileExists(t, filepath.Join(alpha.Worktree, "pwned"), "the filter never ran")
	require.Equal(t, run.Base, runGit(t, h.repo, "rev-parse", alpha.Branch))

	_, _, err := h.engine.Merge(ctx, run.ID, "alpha")
	require.ErrorContains(t, err, "nothing to merge")
	_, err = h.engine.Discard(ctx, run.ID, "alpha")
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(h.repo, "pwned"))
}

func TestEngineRefusesChangedConfig(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.write["alpha"] = map[string]string{"a.txt": "a\n"}
	run := h.start(t, map[string][]int{"alpha": {1}})
	require.Equal(t, control.SwarmDone, run.Assignment("alpha").Status)
	require.NotEmpty(t, run.ConfigHash)
	head := runGit(t, h.repo, "rev-parse", "HEAD")

	// Something adds a merge driver after the agents finished.
	runGit(t, h.repo, "config", "merge.evil.driver", "touch pwned")
	_, _, err := h.engine.Merge(ctx, run.ID, "alpha")
	require.ErrorContains(t, err, "git config changed")
	require.ErrorContains(t, err, "won't merge")
	_, err = h.engine.Diff(ctx, run.ID, "alpha")
	require.ErrorContains(t, err, "git config changed")
	require.Equal(t, head, runGit(t, h.repo, "rev-parse", "HEAD"))

	// So do new attributes.
	runGit(t, h.repo, "config", "--unset", "merge.evil.driver")
	info := filepath.Join(h.repo, ".git", "info")
	require.NoError(t, os.MkdirAll(info, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(info, "attributes"), []byte("* merge=evil\n"), 0o644))
	_, err = h.engine.Diff(ctx, run.ID, "alpha")
	require.ErrorContains(t, err, "git config changed")
	require.NoError(t, os.Remove(filepath.Join(info, "attributes")))

	// Once the config is back as it was, and with only branch settings
	// changed, the merge goes ahead.
	runGit(t, h.repo, "config", "branch.main.description", "the main line")
	_, err = h.engine.Diff(ctx, run.ID, "alpha")
	require.NoError(t, err)
	_, _, err = h.engine.Merge(ctx, run.ID, "alpha")
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(h.repo, "pwned"))
}

func TestEngineRefusesRunWithoutConfigHash(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.write["alpha"] = map[string]string{"a.txt": "a\n"}
	run := h.start(t, map[string][]int{"alpha": {1}})
	_, err := h.store.Update(ctx, run.ID, func(r *control.SwarmRun) error {
		r.ConfigHash = ""
		return nil
	})
	require.NoError(t, err)

	_, _, err = h.engine.Merge(ctx, run.ID, "alpha")
	require.ErrorContains(t, err, "git config changed")
	require.NoFileExists(t, filepath.Join(h.repo, "a.txt"))
}

func TestEngineMergesTheRecordedCommit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newHarness(t)
	h.write["alpha"] = map[string]string{"a.txt": "a\n"}
	run := h.start(t, map[string][]int{"alpha": {1}})
	alpha := *run.Assignment("alpha")
	head := runGit(t, h.repo, "rev-parse", "HEAD")

	// Something commits on the branch after the swarm recorded its work.
	commitFile(t, alpha.Worktree, "late.txt", "late\n", "sneak in")
	_, _, err := h.engine.Merge(ctx, run.ID, "alpha")
	require.ErrorContains(t, err, "moved after alpha finished")
	require.Equal(t, head, runGit(t, h.repo, "rev-parse", "HEAD"))
	require.NoFileExists(t, filepath.Join(h.repo, "late.txt"))

	// Back at the recorded commit, the merge takes exactly that.
	runGit(t, alpha.Worktree, "reset", "--hard", alpha.Commit)
	_, _, err = h.engine.Merge(ctx, run.ID, "alpha")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(h.repo, "a.txt"))
	require.Equal(t, alpha.Commit, runGit(t, h.repo, "rev-parse", "HEAD^2"))
}

func TestEngineSkipsChecksTheUserDenies(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.denyChecks = true
	h.write["alpha"] = map[string]string{"a.txt": "a\n"}
	run := h.start(t, map[string][]int{"alpha": {1}})
	alpha := *run.Assignment("alpha")

	require.Equal(t, control.SwarmDone, alpha.Status)
	require.Len(t, alpha.Checks, 1)
	require.False(t, alpha.Checks[0].Passed)
	require.Equal(t, checksNotAllowed, alpha.Checks[0].Skipped)
}
