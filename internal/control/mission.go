package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/lock"
)

// MissionStatus is where a mission stands.
type MissionStatus string

const (
	MissionActive    MissionStatus = "active"
	MissionPaused    MissionStatus = "paused"
	MissionDone      MissionStatus = "done"
	MissionAbandoned MissionStatus = "abandoned"
)

// Valid reports whether s is a known status.
func (s MissionStatus) Valid() bool {
	switch s {
	case MissionActive, MissionPaused, MissionDone, MissionAbandoned:
		return true
	}
	return false
}

// Open reports whether the mission is still being worked on.
func (s MissionStatus) Open() bool { return s == MissionActive || s == MissionPaused }

// Criterion is one item on a mission's checklist.
type Criterion struct {
	ID     int       `json:"id"`
	Text   string    `json:"text"`
	Done   bool      `json:"done"`
	DoneAt time.Time `json:"done_at,omitzero"`
	Note   string    `json:"note,omitempty"`
}

// Note is a timestamped entry in a mission's journal.
type Note struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
	By   string    `json:"by,omitempty"`
}

// Mission is a goal that outlives a single session: a title, a goal, a
// checklist of done criteria and a journal.
type Mission struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Goal      string        `json:"goal,omitempty"`
	Status    MissionStatus `json:"status"`
	Criteria  []Criterion   `json:"criteria,omitempty"`
	Notes     []Note        `json:"notes,omitempty"`
	Sessions  []string      `json:"sessions,omitempty"`
	Summary   string        `json:"summary,omitempty"`
	Created   time.Time     `json:"created"`
	Updated   time.Time     `json:"updated"`
	Completed time.Time     `json:"completed,omitzero"`
}

// Progress returns how many criteria are done out of how many.
func (m Mission) Progress() (done, total int) {
	for _, c := range m.Criteria {
		if c.Done {
			done++
		}
	}
	return done, len(m.Criteria)
}

// Criterion returns the criterion with the given id.
func (m *Mission) Criterion(id int) *Criterion {
	for i := range m.Criteria {
		if m.Criteria[i].ID == id {
			return &m.Criteria[i]
		}
	}
	return nil
}

// AddCriterion appends a checklist item and returns its id.
func (m *Mission) AddCriterion(text string) int {
	id := 1
	for _, c := range m.Criteria {
		if c.ID >= id {
			id = c.ID + 1
		}
	}
	m.Criteria = append(m.Criteria, Criterion{ID: id, Text: strings.TrimSpace(text)})
	return id
}

// AddNote appends a journal entry.
func (m *Mission) AddNote(text, by string) {
	m.Notes = append(m.Notes, Note{At: time.Now(), Text: strings.TrimSpace(text), By: by})
}

// SetStatus changes the status and keeps Completed in step.
func (m *Mission) SetStatus(s MissionStatus) {
	m.Status = s
	if s.Open() {
		m.Completed = time.Time{}
	} else if m.Completed.IsZero() {
		m.Completed = time.Now()
	}
}

// ErrMissionNotFound is returned for an unknown mission id.
var ErrMissionNotFound = errors.New("mission not found")

// Missions stores a project's missions as one JSON file each, so a Claude
// Code MCP subprocess can use them while Ta-Da has the project database
// locked.
type Missions struct {
	dir string
}

// NewMissions opens (and creates) a mission store in dir.
func NewMissions(dir string) (*Missions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create missions dir: %w", err)
	}
	return &Missions{dir: dir}, nil
}

// Dir is the directory the missions are stored in.
func (s *Missions) Dir() string { return s.dir }

func (s *Missions) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Missions) locked(ctx context.Context, fn func() error) error {
	release, err := lock.File(ctx, filepath.Join(s.dir, "missions.lock"))
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func validID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func (s *Missions) read(id string) (Mission, error) {
	if !validID(id) {
		return Mission{}, ErrMissionNotFound
	}
	data, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return Mission{}, ErrMissionNotFound
	}
	if err != nil {
		return Mission{}, err
	}
	var m Mission
	if err := json.Unmarshal(data, &m); err != nil {
		return Mission{}, fmt.Errorf("read mission %s: %w", id, err)
	}
	return m, nil
}

func (s *Missions) write(m Mission) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(m.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(m.ID))
}

func (s *Missions) list() ([]Mission, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []Mission
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		m, err := s.read(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// List returns every mission: open ones first, then by last update,
// newest first.
func (s *Missions) List() ([]Mission, error) {
	out, err := s.list()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if oi, oj := out[i].Status.Open(), out[j].Status.Open(); oi != oj {
			return oi
		}
		return out[i].Updated.After(out[j].Updated)
	})
	return out, nil
}

// Get returns one mission.
func (s *Missions) Get(id string) (Mission, error) { return s.read(id) }

// ForSession returns the mission the session is attached to.
func (s *Missions) ForSession(sessionID string) (Mission, bool) {
	if sessionID == "" {
		return Mission{}, false
	}
	all, err := s.List()
	if err != nil {
		return Mission{}, false
	}
	for _, m := range all {
		if slices.Contains(m.Sessions, sessionID) {
			return m, true
		}
	}
	return Mission{}, false
}

func newMissionID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Create makes a new active mission. When sessionID isn't empty the
// session is attached to it.
func (s *Missions) Create(ctx context.Context, title, goal string, criteria []string, sessionID string) (Mission, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Mission{}, errors.New("a mission needs a title")
	}
	now := time.Now()
	m := Mission{
		Title:   title,
		Goal:    strings.TrimSpace(goal),
		Status:  MissionActive,
		Created: now,
		Updated: now,
	}
	for _, c := range criteria {
		if strings.TrimSpace(c) != "" {
			m.AddCriterion(c)
		}
	}
	err := s.locked(ctx, func() error {
		for range 10 {
			m.ID = newMissionID()
			if _, err := os.Stat(s.path(m.ID)); os.IsNotExist(err) {
				break
			}
		}
		if sessionID != "" {
			if err := s.detachLocked(sessionID, ""); err != nil {
				return err
			}
			m.Sessions = []string{sessionID}
		}
		return s.write(m)
	})
	return m, err
}

// Update applies fn to a mission under the store lock and saves it.
func (s *Missions) Update(ctx context.Context, id string, fn func(*Mission) error) (Mission, error) {
	var m Mission
	err := s.locked(ctx, func() error {
		var err error
		m, err = s.read(id)
		if err != nil {
			return err
		}
		if err := fn(&m); err != nil {
			return err
		}
		m.Updated = time.Now()
		return s.write(m)
	})
	return m, err
}

// Attach links a session to a mission, detaching it from any other.
func (s *Missions) Attach(ctx context.Context, id, sessionID string) (Mission, error) {
	if sessionID == "" {
		return Mission{}, errors.New("no session to attach")
	}
	var m Mission
	err := s.locked(ctx, func() error {
		var err error
		m, err = s.read(id)
		if err != nil {
			return err
		}
		if err := s.detachLocked(sessionID, id); err != nil {
			return err
		}
		if !slices.Contains(m.Sessions, sessionID) {
			m.Sessions = append(m.Sessions, sessionID)
			m.Updated = time.Now()
			return s.write(m)
		}
		return nil
	})
	return m, err
}

// Detach unlinks a session from whichever mission it's attached to.
func (s *Missions) Detach(ctx context.Context, sessionID string) error {
	return s.locked(ctx, func() error { return s.detachLocked(sessionID, "") })
}

func (s *Missions) detachLocked(sessionID, except string) error {
	all, err := s.list()
	if err != nil {
		return err
	}
	for _, m := range all {
		if m.ID == except || !slices.Contains(m.Sessions, sessionID) {
			continue
		}
		m.Sessions = slices.DeleteFunc(m.Sessions, func(s string) bool { return s == sessionID })
		m.Updated = time.Now()
		if err := s.write(m); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes a mission.
func (s *Missions) Delete(ctx context.Context, id string) error {
	return s.locked(ctx, func() error {
		if _, err := s.read(id); err != nil {
			return err
		}
		return os.Remove(s.path(id))
	})
}

// PromptContext is the mission block for the agent's system prompt. It
// leaves out timestamps and notes so it only changes when the checklist
// or status does, which keeps the prompt cache warm.
func PromptContext(m Mission) string {
	var b strings.Builder
	done, total := m.Progress()
	b.WriteString("<mission>\n")
	fmt.Fprintf(&b, "This session is working on a mission (id %s, %s).\n", m.ID, m.Status)
	fmt.Fprintf(&b, "Title: %s\n", m.Title)
	if m.Goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n", m.Goal)
	}
	if total > 0 {
		fmt.Fprintf(&b, "Done criteria (%d of %d done):\n", done, total)
		for _, c := range m.Criteria {
			mark := " "
			if c.Done {
				mark = "x"
			}
			fmt.Fprintf(&b, "- [%s] %d. %s\n", mark, c.ID, c.Text)
		}
	} else {
		b.WriteString("It has no done criteria yet. Propose a few and add them with the mission tool.\n")
	}
	b.WriteString("Use the mission tool to check a criterion off only once you've verified it, ")
	b.WriteString("with a note saying how you checked. Record decisions and blockers as notes. ")
	b.WriteString("When every criterion is done, call it complete with a short summary. ")
	b.WriteString("Don't mark anything done you haven't verified.\n")
	if m.Status == MissionPaused {
		b.WriteString("The mission is paused: only work on it if the user asks.\n")
	}
	b.WriteString("</mission>")
	return b.String()
}
