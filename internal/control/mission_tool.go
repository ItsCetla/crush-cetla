package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MissionToolName is the name of the agent's mission tool.
const MissionToolName = "mission"

// MissionToolDescription tells the model what the mission tool does.
const MissionToolDescription = `Track this session's mission: a goal with a checklist of done criteria that outlives the session. Actions:
- get (default): show the session's mission.
- list: list missions in this project.
- create: start a mission with a title, a goal and criteria; the session is attached to it. A criterion written as "text :: command" is self-checking, for example "Tests pass :: go test ./...".
- attach: attach this session to an existing mission by mission_id.
- add_criterion: add a done criterion (text), optionally with a check command (check).
- set_check: give criterion id a check command (check), or clear it with an empty check.
- check: mark a criterion done by its id, with a note saying how you verified it (text). Only check what you have verified. A self-checking criterion runs its command instead and is ticked only if the command exits 0; if it fails you get the output.
- verify: run the check commands (all of them, or only criterion id) and record the results.
- uncheck: mark a criterion not done by its id.
- note: record a decision, finding or blocker in the mission journal (text).
- complete: finish the mission with a short summary (text). Every criterion must be done first.
Checks run in the project directory, one at a time, each limited to timeout_seconds (5 minutes by default). Give a criterion a check whenever a command can verify it: a test, build or lint run, or a grep. Setting a check asks the user's permission the way running a command does. Open checks also rerun after each of your turns.
Don't create a mission for a small one-off task; use one when the user asks for it or the work spans several sessions.`

// MissionParams are the mission tool's arguments.
type MissionParams struct {
	Action         string   `json:"action,omitempty" description:"get (default), list, create, attach, add_criterion, set_check, check, verify, uncheck, note or complete" jsonschema:"get (default), list, create, attach, add_criterion, set_check, check, verify, uncheck, note or complete"`
	MissionID      string   `json:"mission_id,omitempty" description:"The mission to attach to (attach only)" jsonschema:"The mission to attach to (attach only)"`
	Title          string   `json:"title,omitempty" description:"The mission title (create only)" jsonschema:"The mission title (create only)"`
	Goal           string   `json:"goal,omitempty" description:"What success looks like (create only)" jsonschema:"What success looks like (create only)"`
	Criteria       []string `json:"criteria,omitempty" description:"Done criteria (create only); text :: command makes one self-checking" jsonschema:"Done criteria (create only); text :: command makes one self-checking"`
	ID             int      `json:"id,omitempty" description:"The criterion id (set_check, check, verify and uncheck)" jsonschema:"The criterion id (set_check, check, verify and uncheck)"`
	Text           string   `json:"text,omitempty" description:"The criterion, note, verification or summary text" jsonschema:"The criterion, note, verification or summary text"`
	Check          string   `json:"check,omitempty" description:"A shell command that exits 0 when the criterion is met (add_criterion and set_check)" jsonschema:"A shell command that exits 0 when the criterion is met (add_criterion and set_check)"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty" description:"How long the check may run in seconds (add_criterion and set_check; default 300)" jsonschema:"How long the check may run in seconds (add_criterion and set_check; default 300)"`
}

// Checks lists the check commands the call would set, so the caller can
// ask permission for them before the tool runs: once set, a check runs
// without asking again.
func (p MissionParams) Checks() []string {
	var out []string
	add := func(c string) {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	switch strings.ToLower(strings.TrimSpace(p.Action)) {
	case "create":
		for _, c := range p.Criteria {
			_, check := ParseCriterion(c)
			add(check)
		}
	case "add_criterion":
		if strings.TrimSpace(p.Check) != "" {
			add(p.Check)
		} else {
			_, check := ParseCriterion(p.Text)
			add(check)
		}
	case "set_check":
		add(p.Check)
	}
	return out
}

// RunMissionTool carries out a mission tool call for a session and
// returns the text the model sees. Notes it writes are signed "claude".
//
// It never approves check commands: the caller does that, and only after
// the user granted the permission prompt for p.Checks(). Checks it runs
// (verify, check) must already be approved, or they're skipped.
func RunMissionTool(ctx context.Context, ms *Missions, sessionID string, p MissionParams) (string, error) {
	return runMissionTool(ctx, ms, sessionID, p)
}

func runMissionTool(ctx context.Context, ms *Missions, sessionID string, p MissionParams) (string, error) {
	if ms == nil {
		return "", errors.New("missions aren't available")
	}
	action := strings.ToLower(strings.TrimSpace(p.Action))
	text := strings.TrimSpace(p.Text)
	timeout := max(p.TimeoutSeconds, 0)

	switch action {
	case "list":
		all, err := ms.List()
		if err != nil {
			return "", err
		}
		if len(all) == 0 {
			return "No missions in this project yet.", nil
		}
		var b strings.Builder
		for _, m := range all {
			done, total := m.Progress()
			fmt.Fprintf(&b, "%s  %s  %s (%d/%d)\n", m.ID, m.Status, m.Title, done, total)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	case "create":
		if sessionID == "" {
			return "", errors.New("no session to attach the mission to")
		}
		m, err := ms.Create(ctx, p.Title, p.Goal, p.Criteria, sessionID)
		if err != nil {
			return "", err
		}
		return "Created the mission and attached this session.\n" + PromptContext(m), nil
	case "attach":
		if strings.TrimSpace(p.MissionID) == "" {
			return "", errors.New("attach needs a mission_id")
		}
		m, err := ms.Attach(ctx, strings.TrimSpace(p.MissionID), sessionID)
		if err != nil {
			return "", err
		}
		return "Attached this session.\n" + PromptContext(m), nil
	}

	cur, ok := ms.ForSession(sessionID)
	if !ok {
		if action == "" || action == "get" {
			return "This session has no mission. Create one with action \"create\" if the user wants one.", nil
		}
		return "", errors.New("this session has no mission; create or attach one first")
	}

	update := func(fn func(*Mission) error) (string, error) {
		m, err := ms.Update(ctx, cur.ID, fn)
		if err != nil {
			return "", err
		}
		return PromptContext(m), nil
	}
	criterion := func(m *Mission) (*Criterion, error) {
		c := m.Criterion(p.ID)
		if c == nil {
			return nil, fmt.Errorf("mission %s has no criterion %d", m.ID, p.ID)
		}
		return c, nil
	}

	switch action {
	case "", "get":
		return PromptContext(cur), nil
	case "add_criterion":
		if text == "" {
			return "", errors.New("add_criterion needs text")
		}
		return update(func(m *Mission) error {
			var id int
			if strings.TrimSpace(p.Check) != "" {
				id = m.AddCriterionCheck(text, p.Check)
			} else {
				id = m.AddCriterion(text)
			}
			if c := m.Criterion(id); c != nil && c.SelfChecking() {
				c.TimeoutSeconds = timeout
			}
			return nil
		})
	case "set_check":
		return update(func(m *Mission) error {
			c, err := criterion(m)
			if err != nil {
				return err
			}
			c.Check = strings.TrimSpace(p.Check)
			c.TimeoutSeconds = 0
			if c.Check != "" {
				c.TimeoutSeconds = timeout
			}
			c.LastRunAt, c.LastExit, c.LastOutput = time.Time{}, 0, ""
			if c.CheckedBy == CheckedByCheck {
				c.Done, c.DoneAt, c.CheckedBy = false, time.Time{}, ""
			}
			return nil
		})
	case "check":
		if c := cur.Criterion(p.ID); c != nil && c.SelfChecking() {
			return checkByCommand(ctx, ms, cur.ID, p.ID, text)
		}
		if text == "" {
			return "", errors.New("check needs text saying how you verified the criterion")
		}
		return update(func(m *Mission) error {
			c, err := criterion(m)
			if err != nil {
				return err
			}
			c.Done, c.DoneAt, c.Note, c.CheckedBy = true, time.Now(), text, CheckedByAgent
			return nil
		})
	case "verify":
		opts := VerifyOptions{Criterion: p.ID, ApprovedOnly: true}
		skipped := ms.UnapprovedChecks(cur, opts)
		m, ran, err := ms.Verify(ctx, cur.ID, opts)
		if err != nil {
			return "", err
		}
		if len(ran) == 0 {
			if len(skipped) > 0 {
				return "", errors.New(UnapprovedMessage(skipped))
			}
			return "", errors.New("this mission has no self-checking criteria; add one with add_criterion or set_check")
		}
		var b strings.Builder
		b.WriteString(CheckSummary(m, ran))
		if len(skipped) > 0 {
			b.WriteString("\n\n" + UnapprovedMessage(skipped))
		}
		for _, id := range ran {
			if c := m.Criterion(id); c != nil && c.LastExit != 0 {
				b.WriteString("\n\n" + CheckFailure(*c))
			}
		}
		b.WriteString("\n\n" + PromptContext(m))
		return b.String(), nil
	case "uncheck":
		return update(func(m *Mission) error {
			c, err := criterion(m)
			if err != nil {
				return err
			}
			c.Done, c.DoneAt, c.Note, c.CheckedBy = false, time.Time{}, "", ""
			if text != "" {
				m.AddNote(fmt.Sprintf("Reopened %d: %s", c.ID, text), "claude")
			}
			return nil
		})
	case "note":
		if text == "" {
			return "", errors.New("note needs text")
		}
		return update(func(m *Mission) error {
			m.AddNote(text, "claude")
			return nil
		})
	case "complete":
		if text == "" {
			return "", errors.New("complete needs a short summary in text")
		}
		return update(func(m *Mission) error {
			var open []string
			for _, c := range m.Criteria {
				if !c.Done {
					open = append(open, fmt.Sprintf("%d. %s", c.ID, c.Text))
				}
			}
			if len(open) > 0 {
				return fmt.Errorf("these criteria aren't done yet: %s", strings.Join(open, "; "))
			}
			m.Summary = text
			m.SetStatus(MissionDone)
			return nil
		})
	}
	return "", fmt.Errorf("unknown mission action %q", p.Action)
}

// checkByCommand runs a self-checking criterion's command for the agent.
// The criterion is ticked only if the command passes; a failure comes
// back as an error carrying the output, so the agent can fix it.
func checkByCommand(ctx context.Context, ms *Missions, missionID string, id int, note string) (string, error) {
	opts := VerifyOptions{Criterion: id, ApprovedOnly: true}
	cur, err := ms.read(missionID)
	if err != nil {
		return "", err
	}
	if skipped := ms.UnapprovedChecks(cur, opts); len(skipped) > 0 {
		return "", errors.New(UnapprovedMessage(skipped))
	}
	m, _, err := ms.Verify(ctx, missionID, opts)
	if err != nil {
		return "", err
	}
	c := m.Criterion(id)
	if c == nil {
		return "", fmt.Errorf("mission %s has no criterion %d", missionID, id)
	}
	if !c.Done {
		return "", errors.New(CheckFailure(*c))
	}
	if note != "" {
		m, err = ms.Update(ctx, missionID, func(m *Mission) error {
			if c := m.Criterion(id); c != nil {
				c.Note = note
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("The check passed, so criterion %d is ticked.\n", id) + PromptContext(m), nil
}
