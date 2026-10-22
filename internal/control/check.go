package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/lock"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/x/ansi"
	"mvdan.cc/sh/v3/interp"
)

// DefaultCheckTimeout bounds a criterion's check when it doesn't set its
// own timeout.
const DefaultCheckTimeout = 5 * time.Minute

// checkOutputLimit is how much of a check's output is kept: the tail,
// where test runners and compilers put the part worth reading.
const checkOutputLimit = 2048

// CheckTimedOut is the exit code recorded for a check that ran past its
// timeout and was killed.
const CheckTimedOut = -1

// Who ticked a criterion.
const (
	// CheckedByCheck means the criterion's check command passed.
	CheckedByCheck = "check"
	// CheckedByAgent means the agent ticked it by hand.
	CheckedByAgent = "agent"
	// CheckedByUser means the person ticked it by hand.
	CheckedByUser = "user"
)

// ErrCheckRunning is returned when a mission's checks are already running
// and the caller asked not to wait.
var ErrCheckRunning = errors.New("checks are already running for this mission")

// CheckResult is the outcome of one check run.
type CheckResult struct {
	// Exit is the command's exit code, or CheckTimedOut.
	Exit int
	// Output is the tail of the combined stdout and stderr, without ANSI
	// escapes.
	Output string
	// At is when the run finished.
	At       time.Time
	Duration time.Duration
	TimedOut bool
	// Canceled is set when the caller's context ended the run; such a run
	// says nothing about the criterion and isn't recorded.
	Canceled bool
}

// Passed reports whether the check succeeded.
func (r CheckResult) Passed() bool { return r.Exit == 0 && !r.TimedOut && !r.Canceled }

// ParseCriterion splits "text :: command" into the criterion text and its
// check command. Without a " :: " separator the whole string is the text
// and the criterion is checked by hand. A bare "::" inside a word (as in
// "pkg::Type") is left alone.
func ParseCriterion(s string) (text, check string) {
	s = strings.TrimSpace(s)
	text, check, ok := strings.Cut(s, " :: ")
	if !ok {
		return s, ""
	}
	text, check = strings.TrimSpace(text), strings.TrimSpace(check)
	if text == "" {
		text = check
	}
	return text, check
}

// RunCheck runs a check command in dir and returns its outcome. A timeout
// of zero or less means DefaultCheckTimeout. It never returns an error:
// a command that can't start is reported as a failed run whose output
// says why. block is the deny-list the command runs under; nil allows
// everything.
func RunCheck(ctx context.Context, dir, command string, timeout time.Duration, block []shell.BlockFunc) CheckResult {
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &tailBuffer{limit: checkOutputLimit}
	start := time.Now()
	err := shell.Run(runCtx, shell.RunOptions{
		Command:    command,
		Cwd:        dir,
		Env:        os.Environ(),
		Stdout:     out,
		Stderr:     out,
		BlockFuncs: block,
	})
	r := CheckResult{At: time.Now(), Duration: time.Since(start)}
	var status interp.ExitStatus
	switch {
	case err == nil:
	case ctx.Err() != nil:
		r.Canceled = true
		r.Exit = shell.ExitCode(err)
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		r.TimedOut = true
		r.Exit = CheckTimedOut
		fmt.Fprintf(out, "\n(timed out after %s)", timeout.Round(time.Second))
	case errors.As(err, &status):
		r.Exit = int(status)
	default:
		r.Exit = shell.ExitCode(err)
		fmt.Fprintf(out, "\n%v", err)
	}
	r.Output = out.String()
	return r
}

// tailBuffer keeps the last limit bytes written to it. Stdout and stderr
// share one, so writes are serialized.
type tailBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	// Compact only now and then so a chatty command doesn't copy on
	// every write.
	if len(t.buf) > 4*t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-2*t.limit:]...)
		t.truncated = true
	}
	return len(p), nil
}

// String returns the kept tail with ANSI escapes stripped, starting on a
// whole line when it was cut.
func (t *tailBuffer) String() string {
	t.mu.Lock()
	s := ansi.Strip(string(t.buf))
	truncated := t.truncated
	t.mu.Unlock()

	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if len(s) > t.limit {
		s = s[len(s)-t.limit:]
		truncated = true
	}
	if truncated {
		for s != "" && !utf8.RuneStart(s[0]) {
			s = s[1:]
		}
		if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
			s = s[i+1:]
		}
		s = "…\n" + s
	}
	return s
}

// SelfChecking reports whether the criterion has a check command.
func (c Criterion) SelfChecking() bool { return strings.TrimSpace(c.Check) != "" }

// CheckTimeout is how long the criterion's check may run.
func (c Criterion) CheckTimeout() time.Duration {
	if c.TimeoutSeconds > 0 {
		return time.Duration(c.TimeoutSeconds) * time.Second
	}
	return DefaultCheckTimeout
}

// Ran reports whether the criterion's check has run at least once.
func (c Criterion) Ran() bool { return !c.LastRunAt.IsZero() }

// FirstOutputLine is the first non-empty line of the last check's output,
// skipping the truncation marker.
func (c Criterion) FirstOutputLine() string {
	for line := range strings.SplitSeq(c.LastOutput, "\n") {
		if line = strings.TrimSpace(line); line != "" && line != "…" {
			return line
		}
	}
	return ""
}

// recordCheck stores a check run on the criterion. A pass ticks it; a
// failure unticks it, since the command is what the criterion means.
func (c *Criterion) recordCheck(r CheckResult) {
	c.LastRunAt, c.LastExit, c.LastOutput = r.At, r.Exit, r.Output
	if r.Passed() {
		if !c.Done {
			c.Done, c.DoneAt = true, r.At
		}
		c.CheckedBy = CheckedByCheck
		return
	}
	c.Done, c.DoneAt, c.CheckedBy = false, time.Time{}, ""
}

// VerifyOptions picks which of a mission's checks Verify runs.
type VerifyOptions struct {
	// Criterion limits the run to one criterion; zero runs every check.
	Criterion int
	// OpenOnly skips criteria that are already ticked.
	OpenOnly bool
	// NoWait returns ErrCheckRunning instead of waiting when the
	// mission's checks are already running.
	NoWait bool
	// ApprovedOnly skips checks whose commands the user hasn't approved
	// in this project.
	ApprovedOnly bool
	// Allow lists commands the user was just shown and asked to run, as
	// in the cockpit. They run even when not yet approved, and running
	// them approves them. A criterion whose command changed since then
	// doesn't match, so it's skipped.
	Allow []string
}

// allows reports whether the user asked to run command in this run.
func (o VerifyOptions) allows(command string) bool {
	return slices.Contains(o.Allow, strings.TrimSpace(command))
}

// wants reports whether a run with these options includes c, ignoring
// approval.
func (o VerifyOptions) wants(c Criterion) bool {
	if !c.SelfChecking() || (o.Criterion != 0 && c.ID != o.Criterion) {
		return false
	}
	return !o.OpenOnly || !c.Done
}

// SetWorkDir sets the directory checks run in, normally the project's
// working directory. Call it before the store is used.
func (s *Missions) SetWorkDir(dir string) { s.workDir = dir }

// WorkDir is the directory checks run in. It falls back to the process's
// working directory when none was set.
func (s *Missions) WorkDir() string {
	if s.workDir != "" {
		return s.workDir
	}
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

func (s *Missions) checkLockPath(id string) string {
	return filepath.Join(s.dir, id+".check.lock")
}

// errCheckChanged skips recording a run whose criterion was removed or
// given a different command while it ran.
var errCheckChanged = errors.New("check changed while it ran")

// Verify runs a mission's checks one at a time in the store's working
// directory and records each result: a pass ticks the criterion, a
// failure unticks it. A file lock keeps runs of the same mission from
// overlapping, across processes too. It returns the updated mission and
// the ids of the criteria whose checks ran.
func (s *Missions) Verify(ctx context.Context, id string, o VerifyOptions) (Mission, []int, error) {
	m, err := s.read(id)
	if err != nil {
		return Mission{}, nil, err
	}
	if o.Criterion != 0 {
		c := m.Criterion(o.Criterion)
		if c == nil {
			return m, nil, fmt.Errorf("mission %s has no criterion %d", m.ID, o.Criterion)
		}
		if !c.SelfChecking() {
			return m, nil, fmt.Errorf("criterion %d has no check command", c.ID)
		}
	}

	var release func()
	if o.NoWait {
		release, err = lock.TryFile(s.checkLockPath(id))
		if errors.Is(err, lock.ErrContended) {
			err = ErrCheckRunning
		}
	} else {
		release, err = lock.File(ctx, s.checkLockPath(id))
	}
	if err != nil {
		return m, nil, err
	}
	defer release()

	// Read again under the check lock: a run that just finished may have
	// ticked criteria this one would otherwise repeat.
	if m, err = s.read(id); err != nil {
		return Mission{}, nil, err
	}
	type job struct {
		id      int
		check   string
		timeout time.Duration
	}
	var jobs []job
	var approve []string
	for _, c := range m.Criteria {
		// A check with hidden characters never runs: what the user saw
		// may not be what it does.
		if !o.wants(c) || HiddenChars(c.Check) {
			continue
		}
		if o.allows(c.Check) {
			approve = append(approve, c.Check)
		} else if o.ApprovedOnly && !s.CheckApproved(c.Check) {
			continue
		}
		jobs = append(jobs, job{id: c.ID, check: c.Check, timeout: c.CheckTimeout()})
	}
	// Approve exactly the commands the user asked for, as read under the
	// check lock, so a change made after they looked can't slip in.
	if err := s.ApproveChecks(ctx, approve...); err != nil {
		return m, nil, err
	}

	dir := s.WorkDir()
	var ran []int
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		r := RunCheck(ctx, dir, j.check, j.timeout, s.blockFuncs)
		if r.Canceled {
			break
		}
		updated, err := s.Update(ctx, id, func(m *Mission) error {
			c := m.Criterion(j.id)
			if c == nil || c.Check != j.check {
				return errCheckChanged
			}
			c.recordCheck(r)
			return nil
		})
		switch {
		case errors.Is(err, errCheckChanged):
			continue
		case err != nil:
			return m, ran, err
		}
		m = updated
		ran = append(ran, j.id)
	}
	return m, ran, ctx.Err()
}

// HasOpenChecks reports whether any unticked criterion has a check.
func (m Mission) HasOpenChecks() bool {
	for _, c := range m.Criteria {
		if !c.Done && c.SelfChecking() {
			return true
		}
	}
	return false
}

// CheckSummary describes the results of the given criteria's latest
// runs in a sentence, with the first failure's output.
func CheckSummary(m Mission, ran []int) string {
	if len(ran) == 0 {
		return "No checks ran."
	}
	var passed int
	var failed []Criterion
	for _, id := range ran {
		c := m.Criterion(id)
		if c == nil {
			continue
		}
		if c.LastExit == 0 {
			passed++
		} else {
			failed = append(failed, *c)
		}
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return fmt.Sprintf(many, n)
	}
	if len(failed) == 0 {
		return plural(passed, "The check passed.", "All %d checks passed.")
	}
	f := failed[0]
	msg := fmt.Sprintf("%s failed: %d. %s (%s)", plural(len(failed), "1 check", "%d checks"), f.ID, f.Text, ExitLabel(f.LastExit))
	if line := f.FirstOutputLine(); line != "" {
		msg += ": " + line
	}
	return msg
}

// ExitLabel names a recorded exit code for people.
func ExitLabel(exit int) string {
	if exit == CheckTimedOut {
		return "timed out"
	}
	return fmt.Sprintf("exit %d", exit)
}

// CheckFailure is what the agent sees when a criterion's check fails: the
// command, how it ended and the tail of its output.
func CheckFailure(c Criterion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Criterion %d's check failed (%s), so it was left unticked.\n", c.ID, ExitLabel(c.LastExit))
	fmt.Fprintf(&b, "Command: %s\n", c.Check)
	if c.LastOutput != "" {
		fmt.Fprintf(&b, "Output (tail):\n%s\n", c.LastOutput)
	}
	b.WriteString("Fix the problem, then check it again.")
	return b.String()
}

// ErrHiddenChars is returned when a check command holds characters a
// person can't see, such as escape codes, line breaks or direction
// marks, so what's shown could differ from what runs.
var ErrHiddenChars = errors.New("check command has hidden characters")

// HiddenChars reports whether command is anything but one line of
// visible text. Plain spaces are allowed; tabs, line breaks, escape and
// other control or format characters are not.
func HiddenChars(command string) bool {
	if !utf8.ValidString(command) {
		return true
	}
	for _, r := range command {
		if r != ' ' && !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}
