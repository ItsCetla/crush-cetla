package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// llamaServer serves a llama.cpp-style /v1/models and /props.
func llamaServer(t *testing.T, props string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"bonsai-8b"}]}`))
		case "/props":
			if props == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(props))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

func TestParseModels(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"a", "b"}, parseModels([]byte(`{"data":[{"id":"a"},{"id":""},{"id":"b"}]}`)))
	require.Equal(t, []string{"qwen3:8b", "x"}, parseModels([]byte(`{"models":[{"model":"qwen3:8b"},{"name":"x"}]}`)))
	require.Nil(t, parseModels([]byte(`not json`)))
	require.Nil(t, parseModels([]byte(`{}`)))
}

func TestParseProps(t *testing.T) {
	t.Parallel()

	slots, ctx := parseProps([]byte(`{"total_slots":4,"default_generation_settings":{"n_ctx":16384}}`))
	require.Equal(t, 4, slots)
	require.EqualValues(t, 16384, ctx)

	slots, ctx = parseProps([]byte(`{"total_slots":2,"n_ctx":32768}`))
	require.Equal(t, 2, slots)
	require.EqualValues(t, 32768, ctx)

	slots, ctx = parseProps([]byte(`{"total_slots":-1}`))
	require.Zero(t, slots)
	require.Zero(t, ctx)

	slots, ctx = parseProps([]byte(`oops`))
	require.Zero(t, slots)
	require.Zero(t, ctx)
}

func TestServerRoot(t *testing.T) {
	t.Parallel()

	require.Equal(t, "http://h:8080", serverRoot("http://h:8080/v1"))
	require.Equal(t, "http://h:8080", serverRoot("http://h:8080/v1/"))
	require.Equal(t, "http://h:8080", serverRoot("http://h:8080"))
}

// Not parallel: the machines tool tests clear the shared probe cache.
func TestInspectReadsSlots(t *testing.T) {
	url := llamaServer(t, `{"total_slots":4,"default_generation_settings":{"n_ctx":16384}}`)
	m := Machine{Name: "slate", MachineConfig: config.MachineConfig{URL: url, Slots: 1}}
	live, err := Inspect(context.Background(), m)
	require.NoError(t, err)
	require.Equal(t, []string{"bonsai-8b"}, live.Models)
	require.Equal(t, 4, live.Slots)
	require.EqualValues(t, 16384, live.ContextWindow)

	// Machines picks up the live answer: four agents, not the one in
	// settings, and the server's model when none is set.
	cfg := testConfig(t, config.Machines{"slate": {URL: url, Slots: 1}})
	got := Machines(cfg)
	require.Len(t, got, 1)
	require.Equal(t, 4, got[0].SlotCount())
	require.Equal(t, 1, got[0].MachineConfig.SlotCount())
	require.Equal(t, "bonsai-8b", got[0].Model)
	require.EqualValues(t, 16384, got[0].ContextWindow)
	require.True(t, got[0].Usable())
}

// Not parallel: the machines tool tests clear the shared probe cache.
func TestInspectWithoutProps(t *testing.T) {
	// Ollama and LM Studio don't serve /props: the settings decide.
	url := llamaServer(t, "")
	cfg := testConfig(t, config.Machines{"pc": {URL: url, Slots: 2, Model: "qwen"}})
	Refresh(context.Background(), cfg)
	got := Machines(cfg)
	require.Len(t, got, 1)
	require.Zero(t, got[0].Live.Slots)
	require.Equal(t, 2, got[0].SlotCount())
	require.Equal(t, "qwen", got[0].Model)
}

// Not parallel: the machines tool tests clear the shared probe cache.
func TestReadyCountsOnlineAgents(t *testing.T) {
	four := llamaServer(t, `{"total_slots":4,"default_generation_settings":{"n_ctx":16384}}`)
	two := llamaServer(t, `{"total_slots":2,"default_generation_settings":{"n_ctx":32768}}`)
	cfg := testConfig(t, config.Machines{
		"slate": {URL: four},
		"pc":    {URL: two},
		"down":  {URL: "http://127.0.0.1:1/v1", Model: "m", Slots: 3},
		"off":   {URL: four, Model: "m", Disabled: true},
	})
	statuses := NewPool(t.TempDir()).Check(context.Background(), cfg)
	machines, agents := Ready(statuses)
	require.Equal(t, 2, machines)
	require.Equal(t, 6, agents)

	machines, agents, checked := CachedReady(cfg)
	require.True(t, checked)
	require.Equal(t, 2, machines)
	require.Equal(t, 6, agents)

	got := PromptSection(cfg, "the agent tool")
	require.Contains(t, got, "The user has 2 other computers running local models, ready to use: 6 agents at once in total.")
	require.Contains(t, got, "The user wants them used.")
	require.Contains(t, got, "in parallel")
	require.Contains(t, got, "- down: 3 slots, model m (offline right now)")
	require.Contains(t, got, "- slate: 4 slots, model bonsai-8b")
	require.True(t, strings.HasSuffix(got, "</machines>"))
}

func TestCachedReadyBeforeCheck(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, config.Machines{"never": {URL: "http://never-checked.invalid/v1", Model: "m"}})
	_, _, checked := CachedReady(cfg)
	require.False(t, checked)
}
