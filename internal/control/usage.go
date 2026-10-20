package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/lock"
)

// Pricing says where a usage row's cost comes from.
type Pricing string

const (
	// PricingList is computed from the provider catalog's list prices.
	PricingList Pricing = "list"
	// PricingBilled is the cost the provider reported for the call.
	PricingBilled Pricing = "billed"
	// PricingEstimate is Claude Code's own estimate for API-key billing.
	PricingEstimate Pricing = "estimate"
	// PricingFlat is a flat-rate plan or subscription. The cost, when
	// known, is what the call would cost at list price; it isn't counted
	// toward budgets.
	PricingFlat Pricing = "flat"
	// PricingUnpriced has no known price. It's never counted as $0.
	PricingUnpriced Pricing = "unpriced"
)

// Counted reports whether rows with this pricing count toward budgets.
func (p Pricing) Counted() bool {
	return p == PricingList || p == PricingBilled || p == PricingEstimate
}

// Usage is one model call, or one Claude Code turn.
type Usage struct {
	At         time.Time `json:"at"`
	Project    string    `json:"project,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Model      string    `json:"model,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Input      int64     `json:"input,omitempty"`
	Output     int64     `json:"output,omitempty"`
	CacheRead  int64     `json:"cache_read,omitempty"`
	CacheWrite int64     `json:"cache_write,omitempty"`
	Cost       float64   `json:"cost,omitempty"`
	Pricing    Pricing   `json:"pricing"`
}

// Tokens is the total of every token bucket.
func (u Usage) Tokens() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// Ledger is an append-only log of usage, one JSON line per row and one
// file per month. It lives in the global data directory so budgets cover
// every project, and several Ta-Da processes can write to it at once.
type Ledger struct {
	dir string

	mu     sync.Mutex
	months map[string]*monthCache
}

type monthCache struct {
	offset int64
	rows   []Usage
}

// NewLedger opens (and creates) a ledger in dir.
func NewLedger(dir string) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create usage dir: %w", err)
	}
	return &Ledger{dir: dir, months: make(map[string]*monthCache)}, nil
}

func monthKey(t time.Time) string { return t.Format("2006-01") }

func (l *Ledger) path(month string) string {
	return filepath.Join(l.dir, "usage-"+month+".jsonl")
}

// Add appends a row.
func (l *Ledger) Add(ctx context.Context, u Usage) error {
	if u.At.IsZero() {
		u.At = time.Now()
	}
	if u.Pricing == "" {
		u.Pricing = PricingUnpriced
	}
	line, err := json.Marshal(u)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	release, err := lock.File(ctx, filepath.Join(l.dir, "usage.lock"))
	if err != nil {
		return err
	}
	defer release()
	f, err := os.OpenFile(l.path(monthKey(u.At)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Since returns every row at or after t, oldest first.
func (l *Ledger) Since(t time.Time) ([]Usage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Usage
	now := time.Now()
	for m := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()); !m.After(now); m = m.AddDate(0, 1, 0) {
		rows, err := l.load(monthKey(m))
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !r.At.Before(t) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// load reads the rows of a month, parsing only what was appended since
// the last call.
func (l *Ledger) load(month string) ([]Usage, error) {
	c := l.months[month]
	if c == nil {
		c = &monthCache{}
		l.months[month] = c
	}
	f, err := os.Open(l.path(month))
	if os.IsNotExist(err) {
		return c.rows, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() < c.offset {
		// The file was replaced; start over.
		c.offset, c.rows = 0, nil
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			c.offset += int64(len(line))
			var u Usage
			if json.Unmarshal(line, &u) == nil {
				c.rows = append(c.rows, u)
			}
		}
		if err != nil {
			// A line without a newline is still being written; it's read
			// next time.
			break
		}
	}
	return c.rows, nil
}
