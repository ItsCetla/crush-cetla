// Package doctor runs quick health checks on a Ta-Da install: config,
// providers, local tools and the environment. Checks are plain functions of
// the inputs they are given and of an Env, so they can be tested without
// touching the real machine.
package doctor

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Status is the outcome of a check or of one of its items.
type Status string

const (
	// StatusOK means nothing needs attention.
	StatusOK Status = "ok"
	// StatusWarn means Ta-Da works, but something is off.
	StatusWarn Status = "warn"
	// StatusFail means something is broken.
	StatusFail Status = "fail"
	// StatusSkip means the check didn't apply or wasn't run.
	StatusSkip Status = "skip"
)

// rank orders statuses from least to most severe.
func (s Status) rank() int {
	switch s {
	case StatusFail:
		return 3
	case StatusWarn:
		return 2
	case StatusOK:
		return 1
	}
	return 0
}

// Worst returns the most severe status in items, or StatusSkip when there
// are none.
func Worst(items []Item) Status {
	worst := StatusSkip
	for _, it := range items {
		if it.Status.rank() > worst.rank() {
			worst = it.Status
		}
	}
	return worst
}

// Item is one finding of a check.
type Item struct {
	Status Status `json:"status"`
	Text   string `json:"text"`
	// Hint says how to fix a finding that isn't ok.
	Hint string `json:"hint,omitempty"`
}

// Result is the outcome of one check.
type Result struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Summary string `json:"summary"`
	// Hint says how to fix the check's main problem.
	Hint  string `json:"hint,omitempty"`
	Items []Item `json:"items,omitempty"`
}

// Check is one health check.
type Check struct {
	ID   string
	Name string
	Run  func(ctx context.Context, env *Env) Result
}

// HTTPFunc sends one request without a body or credentials and returns the
// response's status code and the start of its body.
type HTTPFunc func(ctx context.Context, method, url string) (status int, body []byte, err error)

// Env is what checks may use to look at the machine.
type Env struct {
	LookPath func(file string) (string, error)
	Getenv   func(key string) string
	Stat     func(name string) (fs.FileInfo, error)
	HTTP     HTTPFunc
	// Online allows checks to contact hosts other than localhost.
	Online bool
}

// SystemEnv returns an Env backed by the real machine.
func SystemEnv(online bool) *Env {
	return &Env{
		LookPath: exec.LookPath,
		Getenv:   os.Getenv,
		Stat:     os.Stat,
		HTTP:     NewHTTP(nil),
		Online:   online,
	}
}

// maxBody caps how much of a response body an HTTPFunc reads.
const maxBody = 512 << 10

// NewHTTP returns an HTTPFunc that uses client, or a client that doesn't
// follow redirects when client is nil. It never sends credentials.
func NewHTTP(client *http.Client) HTTPFunc {
	if client == nil {
		client = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return func(ctx context.Context, method, url string) (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("User-Agent", "Tada-doctor")
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
		return res.StatusCode, body, err
	}
}

// Report is the outcome of a doctor run.
type Report struct {
	Version string   `json:"version"`
	OK      int      `json:"ok"`
	Warn    int      `json:"warn"`
	Fail    int      `json:"fail"`
	Skip    int      `json:"skip"`
	Results []Result `json:"results"`
}

// HasFail reports whether any check failed.
func (r Report) HasFail() bool { return r.Fail > 0 }

// checkTimeout bounds a single check.
const checkTimeout = 10 * time.Second

// Run runs checks concurrently and returns their results in order. A check
// that panics fails instead of taking the run down.
func Run(ctx context.Context, env *Env, checks []Check) Report {
	results := make([]Result, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Go(func() {
			results[i] = runOne(ctx, env, c)
		})
	}
	wg.Wait()

	r := Report{Results: results}
	for _, res := range results {
		switch res.Status {
		case StatusOK:
			r.OK++
		case StatusWarn:
			r.Warn++
		case StatusFail:
			r.Fail++
		default:
			r.Skip++
		}
	}
	return r
}

func runOne(ctx context.Context, env *Env, c Check) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			res = Result{
				Status:  StatusFail,
				Summary: fmt.Sprintf("The check crashed: %v", p),
				Hint:    "This is a bug in Ta-Da; please report it.",
			}
		}
		res.ID, res.Name = c.ID, c.Name
		if res.Status == "" {
			res.Status = Worst(res.Items)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	return c.Run(ctx, env)
}
