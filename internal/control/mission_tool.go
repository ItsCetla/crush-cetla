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
- create: start a mission with a title, a goal and criteria; the session is attached to it.
- attach: attach this session to an existing mission by mission_id.
- add_criterion: add a done criterion (text).
- check: mark a criterion done by its id, with a note saying how you verified it (text). Only check what you have verified.
- uncheck: mark a criterion not done by its id.
- note: record a decision, finding or blocker in the mission journal (text).
- complete: finish the mission with a short summary (text). Every criterion must be done first.
Don't create a mission for a small one-off task; use one when the user asks for it or the work spans several sessions.`

// MissionParams are the mission tool's arguments.
type MissionParams struct {
	Action    string   `json:"action,omitempty" description:"get (default), list, create, attach, add_criterion, check, uncheck, note or complete" jsonschema:"get (default), list, create, attach, add_criterion, check, uncheck, note or complete"`
	MissionID string   `json:"mission_id,omitempty" description:"The mission to attach to (attach only)" jsonschema:"The mission to attach to (attach only)"`
	Title     string   `json:"title,omitempty" description:"The mission title (create only)" jsonschema:"The mission title (create only)"`
	Goal      string   `json:"goal,omitempty" description:"What success looks like (create only)" jsonschema:"What success looks like (create only)"`
	Criteria  []string `json:"criteria,omitempty" description:"Done criteria (create only)" jsonschema:"Done criteria (create only)"`
	ID        int      `json:"id,omitempty" description:"The criterion id (check and uncheck)" jsonschema:"The criterion id (check and uncheck)"`
	Text      string   `json:"text,omitempty" description:"The criterion, note, verification or summary text" jsonschema:"The criterion, note, verification or summary text"`
}

// RunMissionTool carries out a mission tool call for a session and
// returns the text the model sees. Notes it writes are signed "claude".
func RunMissionTool(ctx context.Context, ms *Missions, sessionID string, p MissionParams) (string, error) {
	if ms == nil {
		return "", errors.New("missions aren't available")
	}
	action := strings.ToLower(strings.TrimSpace(p.Action))
	text := strings.TrimSpace(p.Text)

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
			m.AddCriterion(text)
			return nil
		})
	case "check":
		if text == "" {
			return "", errors.New("check needs text saying how you verified the criterion")
		}
		return update(func(m *Mission) error {
			c, err := criterion(m)
			if err != nil {
				return err
			}
			c.Done, c.DoneAt, c.Note = true, time.Now(), text
			return nil
		})
	case "uncheck":
		return update(func(m *Mission) error {
			c, err := criterion(m)
			if err != nil {
				return err
			}
			c.Done, c.DoneAt, c.Note = false, time.Time{}, ""
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
