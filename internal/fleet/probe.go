package fleet

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"charm.land/catwalk/pkg/catwalk"

	"github.com/charmbracelet/crush/internal/netlog"
)

const (
	probeTimeout = 1500 * time.Millisecond
	probeTTL     = 20 * time.Second
)

// Live is what a machine's model server reports about itself. Fields the
// server doesn't report are zero.
type Live struct {
	// Models are the model IDs the server lists.
	Models []string
	// Slots is how many requests the server runs at once (llama.cpp's
	// --parallel).
	Slots int
	// ContextWindow is the context each slot gets.
	ContextWindow int64
}

type probeResult struct {
	live Live
	err  error
	at   time.Time
}

var (
	probeMu    sync.Mutex
	probeCache = map[string]probeResult{}
)

// Probe checks that m's model server answers, caching the answer for a
// short while so picking a machine stays fast.
func Probe(ctx context.Context, m Machine) error {
	_, err := Inspect(ctx, m)
	return err
}

// Inspect checks that m's model server answers and reads what it reports
// about itself: its models and, for llama.cpp servers, how many agents
// it runs at once and their context. Answers are cached for a short
// while.
func Inspect(ctx context.Context, m Machine) (Live, error) {
	key := m.URL
	probeMu.Lock()
	prev, ok := probeCache[key]
	probeMu.Unlock()
	if ok && time.Since(prev.at) < probeTTL {
		return prev.live, prev.err
	}

	live, err := inspect(ctx, m)
	if err != nil {
		// Keep what the server said last time, so an offline machine
		// still shows its usual slots.
		live = prev.live
	}
	probeMu.Lock()
	probeCache[key] = probeResult{live: live, err: err, at: time.Now()}
	probeMu.Unlock()
	return live, err
}

// lastLive returns what m's server reported the last time it was
// checked, without contacting it.
func lastLive(m Machine) Live {
	probeMu.Lock()
	defer probeMu.Unlock()
	return probeCache[m.URL].live
}

// ForgetProbes drops cached probe results, so the next check is live.
func ForgetProbes() {
	probeMu.Lock()
	clear(probeCache)
	probeMu.Unlock()
}

func inspect(ctx context.Context, m Machine) (Live, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var live Live
	data, err := get(ctx, m, strings.TrimRight(m.URL, "/")+"/models")
	if err != nil {
		return live, err
	}
	live.Models = parseModels(data)
	if reportsProps(m.Type) {
		if data, err := get(ctx, m, serverRoot(m.URL)+"/props"); err == nil {
			live.Slots, live.ContextWindow = parseProps(data)
		}
	}
	return live, nil
}

func get(ctx context.Context, m Machine, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	setAuth(req, m)
	resp, err := machineClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server answered %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// reportsProps reports whether a server of type t may be llama.cpp,
// which describes its slots at /props. Plain OpenAI-compatible servers
// are often llama.cpp too; others just answer 404.
func reportsProps(t catwalk.Type) bool {
	switch t {
	case "", catwalk.TypeOpenAICompat, "llamacpp":
		return true
	}
	return false
}

// serverRoot strips the OpenAI path from a base URL:
// http://host:8080/v1 -> http://host:8080.
func serverRoot(url string) string {
	url = strings.TrimRight(url, "/")
	return strings.TrimSuffix(url, "/v1")
}

// parseModels reads model IDs from an OpenAI-style list ({"data":[{"id"}]})
// or llama.cpp's and Ollama's {"models":[{"model"|"name"}]}.
func parseModels(data []byte) []string {
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Model string `json:"model"`
			Name  string `json:"name"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	var out []string
	for _, d := range list.Data {
		if d.ID != "" {
			out = append(out, d.ID)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, m := range list.Models {
		if id := cmp.Or(m.Model, m.Name); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// parseProps reads llama.cpp's slot count and per-slot context from its
// /props answer.
func parseProps(data []byte) (slots int, contextWindow int64) {
	var props struct {
		TotalSlots int   `json:"total_slots"`
		NCtx       int64 `json:"n_ctx"`
		Defaults   struct {
			NCtx int64 `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if json.Unmarshal(data, &props) != nil {
		return 0, 0
	}
	return max(props.TotalSlots, 0), max(cmp.Or(props.Defaults.NCtx, props.NCtx), 0)
}

// machineClient carries requests to other Ta-Da machines and counts them
// in the network ledger for the privacy receipt.
var machineClient = &http.Client{Transport: netlog.Wrap(nil, netlog.PurposeMachine)}
