package fleet

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	probeTimeout = 1500 * time.Millisecond
	probeTTL     = 20 * time.Second
)

type probeResult struct {
	err error
	at  time.Time
}

var (
	probeMu    sync.Mutex
	probeCache = map[string]probeResult{}
)

// Probe checks that m's model server answers, caching the answer for a
// short while so picking a machine stays fast.
func Probe(ctx context.Context, m Machine) error {
	key := m.URL
	probeMu.Lock()
	if r, ok := probeCache[key]; ok && time.Since(r.at) < probeTTL {
		probeMu.Unlock()
		return r.err
	}
	probeMu.Unlock()

	err := probe(ctx, m)
	probeMu.Lock()
	probeCache[key] = probeResult{err: err, at: time.Now()}
	probeMu.Unlock()
	return err
}

// ForgetProbes drops cached probe results, so the next check is live.
func ForgetProbes() {
	probeMu.Lock()
	clear(probeCache)
	probeMu.Unlock()
}

func probe(ctx context.Context, m Machine) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	url := strings.TrimRight(m.URL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	setAuth(req, m)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("server answered %s", resp.Status)
	}
	return nil
}
