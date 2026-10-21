// Package limits reads AI plan usage (the 5 hour window and the weekly
// limit) from a local JSON file that some other tool writes. Ta-Da never
// fetches these numbers itself. The file format matches the "limit rings"
// file other agent hosts read, so one writer can feed them all:
//
//	{"claude": {"name": "Claude", "_ts": 1760000000, "used5h": 42.5,
//	            "reset5h": 1760010000, "usedWeek": 18, "resetWeek": 1760400000}}
package limits

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// StaleAfter is how old a reading can be before it shows as stale.
const StaleAfter = 15 * time.Minute

// WarnAt is the percent from which a window is drawn as a warning.
const WarnAt = 80.0

// Window is one usage window. Known is false when the number or its reset
// time is missing, or the reset time has already passed.
type Window struct {
	Known bool      `json:"known"`
	Used  float64   `json:"used,omitempty"`
	Reset time.Time `json:"reset,omitzero"`
}

// Warn reports whether the window is at or above WarnAt.
func (w Window) Warn() bool { return w.Known && w.Used >= WarnAt }

// Full reports whether the window is used up.
func (w Window) Full() bool { return w.Known && w.Used >= 100 }

// Provider is one provider's reading.
type Provider struct {
	Key   string    `json:"key"`
	Name  string    `json:"name"`
	Tier  string    `json:"tier,omitempty"`
	At    time.Time `json:"at,omitzero"`
	Stale bool      `json:"stale"`
	Five  Window    `json:"five_hour"`
	Week  Window    `json:"week"`
}

type rawProvider struct {
	Name      *string  `json:"name"`
	TS        *float64 `json:"_ts"`
	OK        *bool    `json:"ok"`
	Stale     *bool    `json:"stale"`
	Used5h    *float64 `json:"used5h"`
	Reset5h   *float64 `json:"reset5h"`
	UsedWeek  *float64 `json:"usedWeek"`
	ResetWeek *float64 `json:"resetWeek"`
	Tier      *string  `json:"tier"`
}

// Parse reads the file contents. Keys that start with "_" and values that
// are not provider objects are skipped. Providers come back sorted by key.
func Parse(data []byte, now time.Time) ([]Provider, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		if !strings.HasPrefix(k, "_") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var out []Provider
	for _, k := range keys {
		raw := top[k]
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var r rawProvider
		if err := json.Unmarshal(raw, &r); err != nil {
			continue
		}
		if r.TS == nil && r.Used5h == nil && r.UsedWeek == nil {
			continue
		}
		p := Provider{Key: k, Name: k}
		if r.Name != nil && strings.TrimSpace(*r.Name) != "" {
			p.Name = strings.TrimSpace(*r.Name)
		}
		if r.Tier != nil {
			p.Tier = *r.Tier
		}
		if r.TS != nil {
			p.At = epoch(*r.TS)
		}
		p.Stale = r.TS == nil || now.Sub(p.At) > StaleAfter ||
			(r.OK != nil && !*r.OK) || (r.Stale != nil && *r.Stale)
		p.Five = window(r.Used5h, r.Reset5h, now)
		p.Week = window(r.UsedWeek, r.ResetWeek, now)
		out = append(out, p)
	}
	return out, nil
}

func window(used, reset *float64, now time.Time) Window {
	if used == nil || reset == nil {
		return Window{}
	}
	at := epoch(*reset)
	if !at.After(now) {
		return Window{}
	}
	u := *used
	u = max(0, min(100, u))
	return Window{Known: true, Used: u, Reset: at}
}

func epoch(s float64) time.Time {
	sec := int64(s)
	return time.Unix(sec, int64((s-float64(sec))*1e9))
}

// Reader caches the file by modification time so polling it is cheap.
type Reader struct {
	Path string

	mu    sync.Mutex
	mod   time.Time
	size  int64
	data  []byte
	ready bool
}

// maxSize keeps a wrong path (say, a log file) from being read whole.
const maxSize = 1 << 20

// Read returns the providers in the file. A missing file returns no
// providers and no error, so the section just stays hidden until the
// writer creates it.
func (r *Reader) Read(now time.Time) ([]Provider, error) {
	if r == nil || r.Path == "" {
		return nil, nil
	}
	st, err := os.Stat(r.Path)
	if errors.Is(err, os.ErrNotExist) {
		r.mu.Lock()
		r.ready, r.data = false, nil
		r.mu.Unlock()
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("limit rings path is a directory")
	}
	if st.Size() > maxSize {
		return nil, errors.New("limit rings file is too large")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready || !st.ModTime().Equal(r.mod) || st.Size() != r.size {
		data, err := os.ReadFile(r.Path)
		if err != nil {
			return nil, err
		}
		r.data, r.mod, r.size, r.ready = data, st.ModTime(), st.Size(), true
	}
	// Parse every time: staleness and passed resets depend on now.
	return Parse(r.data, now)
}

// Ring returns a ring glyph for the window: empty to full in quarters.
func (w Window) Ring() string {
	switch {
	case !w.Known:
		return "◌"
	case w.Used >= 100:
		return "●"
	case w.Used >= 62.5:
		return "◕"
	case w.Used >= 37.5:
		return "◑"
	case w.Used >= 12.5:
		return "◔"
	default:
		return "○"
	}
}

// Percent returns "42%", or "—" when the window is unknown.
func (w Window) Percent() string {
	if !w.Known {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", w.Used)
}

// ResetLabel says when the window resets: a clock time within a day, the
// weekday and time within a week, else the date.
func (w Window) ResetLabel(now time.Time) string {
	if !w.Known {
		return ""
	}
	at := w.Reset.In(now.Location())
	switch d := at.Sub(now); {
	case d < 24*time.Hour:
		return at.Format("3:04 PM")
	case d < 6*24*time.Hour:
		return at.Format("Mon 3:04 PM")
	default:
		return at.Format("Jan 2")
	}
}
