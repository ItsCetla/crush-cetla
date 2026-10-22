package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/lock"
	"github.com/charmbracelet/crush/internal/shell"
)

// ApprovalsFileName is the file, in Ta-Da's global control directory,
// that records which check commands the user allowed in each project.
const ApprovalsFileName = "approved_checks.json"

// approvalsFile is the on-disk shape of the approvals file: check commands
// keyed by the project directory they run in.
type approvalsFile struct {
	Projects map[string][]string `json:"projects"`
}

// SetApprovals sets the file that records approved check commands. It
// belongs outside the project, normally in Ta-Da's global data directory,
// so a mission file that came with a repository can't approve its own
// commands. With no file, approvals last only as long as the process.
func (s *Missions) SetApprovals(path string) { s.approvalsPath = path }

// ApprovalsPath is the file set with SetApprovals, or "".
func (s *Missions) ApprovalsPath() string { return s.approvalsPath }

// SetBlockFuncs sets the deny-list every check runs under, normally the
// bash tool's, so a check can't run what the bash tool refuses.
func (s *Missions) SetBlockFuncs(fns []shell.BlockFunc) { s.blockFuncs = fns }

func (s *Missions) projectKey() string {
	dir := s.WorkDir()
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Clean(dir)
}

// ApproveChecks records that the user allowed commands to run as checks
// in this project, so they can later run without asking: after a turn,
// or when the agent verifies its mission.
func (s *Missions) ApproveChecks(ctx context.Context, commands ...string) error {
	var add []string
	for _, c := range commands {
		if c = strings.TrimSpace(c); c != "" {
			add = append(add, c)
		}
	}
	if len(add) == 0 {
		return nil
	}
	key := s.projectKey()
	if s.approvalsPath == "" {
		s.approvedMu.Lock()
		defer s.approvedMu.Unlock()
		if s.approved == nil {
			s.approved = make(map[string]bool)
		}
		for _, c := range add {
			s.approved[key+"\x00"+c] = true
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.approvalsPath), 0o700); err != nil {
		return fmt.Errorf("create approvals dir: %w", err)
	}
	release, err := lock.File(ctx, s.approvalsPath+".lock")
	if err != nil {
		return err
	}
	defer release()
	f, err := s.readApprovals()
	if err != nil {
		return err
	}
	list := f.Projects[key]
	for _, c := range add {
		if !slices.Contains(list, c) {
			list = append(list, c)
		}
	}
	f.Projects[key] = list
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.approvalsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.approvalsPath)
}

// CheckApproved reports whether the user allowed command to run as a
// check in this project.
func (s *Missions) CheckApproved(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	key := s.projectKey()
	if s.approvalsPath == "" {
		s.approvedMu.Lock()
		defer s.approvedMu.Unlock()
		return s.approved[key+"\x00"+command]
	}
	f, err := s.readApprovals()
	if err != nil {
		return false
	}
	return slices.Contains(f.Projects[key], command)
}

// UnapprovedChecks returns the criteria Verify would run with o whose
// commands the user hasn't approved in this project.
func (s *Missions) UnapprovedChecks(m Mission, o VerifyOptions) []Criterion {
	var out []Criterion
	for _, c := range m.Criteria {
		if !o.wants(c) {
			continue
		}
		if !s.CheckApproved(c.Check) {
			out = append(out, c)
		}
	}
	return out
}

func (s *Missions) readApprovals() (approvalsFile, error) {
	f := approvalsFile{Projects: map[string][]string{}}
	data, err := os.ReadFile(s.approvalsPath)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return approvalsFile{Projects: map[string][]string{}}, fmt.Errorf("read approvals: %w", err)
	}
	if f.Projects == nil {
		f.Projects = map[string][]string{}
	}
	return f, nil
}

// UnapprovedMessage tells the agent why checks didn't run and how to get
// them approved.
func UnapprovedMessage(cs []Criterion) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%d (`%s`)", c.ID, c.Check))
	}
	return "Not run, because the user hasn't approved these checks in this project: " +
		strings.Join(parts, ", ") +
		". Set each one again with set_check, which asks the user, or ask the user to run it from the cockpit."
}
