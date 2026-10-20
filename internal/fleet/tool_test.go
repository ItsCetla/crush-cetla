package fleet

import (
	"context"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// fakeStore keeps machines per scope, plus ones from a tadarc that the
// tool can't delete.
type fakeStore struct {
	t      *testing.T
	scopes map[config.Scope]config.Machines
	rc     config.Machines
}

func newFakeStore(t *testing.T) *fakeStore {
	return &fakeStore{t: t, scopes: map[config.Scope]config.Machines{
		config.ScopeGlobal:    {},
		config.ScopeWorkspace: {},
	}, rc: config.Machines{}}
}

func (s *fakeStore) Config() *config.Config {
	merged := config.Machines{}
	for _, src := range []config.Machines{s.rc, s.scopes[config.ScopeGlobal], s.scopes[config.ScopeWorkspace]} {
		for name, m := range src {
			if old, ok := merged[name]; ok && m.URL == "" {
				old.Disabled = m.Disabled
				m = old
			}
			merged[name] = m
		}
	}
	return testConfig(s.t, merged)
}

func (s *fakeStore) KnownProviders() []catwalk.Provider {
	return []catwalk.Provider{{ID: "openai"}}
}

func (s *fakeStore) HasConfigField(scope config.Scope, key string) bool {
	_, ok := s.scopes[scope][strings.TrimPrefix(key, "machines.")]
	return ok
}

func (s *fakeStore) SetConfigField(scope config.Scope, key string, value any) error {
	name := strings.TrimPrefix(key, "machines.")
	if n, ok := strings.CutSuffix(name, ".disabled"); ok {
		m := s.scopes[scope][n]
		m.Disabled = value.(bool)
		s.scopes[scope][n] = m
		return nil
	}
	s.scopes[scope][name] = value.(config.MachineConfig)
	return nil
}

func (s *fakeStore) RemoveConfigField(scope config.Scope, key string) error {
	delete(s.scopes[scope], strings.TrimPrefix(key, "machines."))
	return nil
}

func TestToolAddUpdateRemove(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t)
	ctx := context.Background()
	pool := NewPool(t.TempDir())
	url := onlineServer(t)

	out, err := RunTool(ctx, store, pool, Params{})
	require.NoError(t, err)
	require.Contains(t, out, "No machines configured")

	out, err = RunTool(ctx, store, pool, Params{
		Action: "add", Name: "m1", URL: url + "/v1/", Type: "Ollama",
		Model: "qwen", Slots: 4, Hardware: "M1 Max",
	})
	require.NoError(t, err)
	require.Contains(t, out, "Saved m1")
	require.Contains(t, out, "online and ready")
	saved := store.scopes[config.ScopeGlobal]["m1"]
	require.Equal(t, url+"/v1", saved.URL)
	require.Equal(t, catwalk.Type("ollama"), saved.Type)
	require.Equal(t, 4, saved.Slots)

	_, err = RunTool(ctx, store, pool, Params{Action: "add", Name: "M1", URL: url})
	require.ErrorContains(t, err, "already exists")

	out, err = RunTool(ctx, store, pool, Params{Action: "update", Name: "m1", Slots: 2})
	require.NoError(t, err)
	require.Contains(t, out, "Updated m1")
	saved = store.scopes[config.ScopeGlobal]["m1"]
	require.Equal(t, 2, saved.Slots)
	require.Equal(t, "M1 Max", saved.Hardware, "update keeps fields it wasn't given")

	out, err = RunTool(ctx, store, pool, Params{Action: "check"})
	require.NoError(t, err)
	require.Contains(t, out, "online, 2 of 2 free")

	out, err = RunTool(ctx, store, pool, Params{Action: "rm", Name: "m1"})
	require.NoError(t, err)
	require.Equal(t, "Removed m1.", out)
	require.Empty(t, store.scopes[config.ScopeGlobal])
}

func TestToolRemoveDisablesTadarcMachine(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t)
	store.rc["pc"] = config.MachineConfig{URL: "http://127.0.0.1:1/v1", Model: "x"}

	out, err := RunTool(context.Background(), store, NewPool(t.TempDir()), Params{Action: "remove", Name: "pc"})
	require.NoError(t, err)
	require.Contains(t, out, "Disabled pc")
	require.True(t, store.scopes[config.ScopeGlobal]["pc"].Disabled)
}

func TestToolRejectsBadInput(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t)
	ctx := context.Background()
	pool := NewPool(t.TempDir())
	for _, tc := range []struct {
		p    Params
		want string
	}{
		{Params{Action: "add", Name: "openai", URL: "http://x"}, "built-in provider"},
		{Params{Action: "add", Name: "auto", URL: "http://x"}, ""},
		{Params{Action: "add", Name: "m1"}, "needs the url"},
		{Params{Action: "add", Name: "m1", URL: "192.168.1.2:11434"}, "must start with http"},
		{Params{Action: "add", Name: "m1", URL: "http://x", Slots: -1}, "negative"},
		{Params{Action: "update", Name: "nope"}, "no machine named"},
		{Params{Action: "explode"}, "unknown action"},
	} {
		_, err := RunTool(ctx, store, pool, tc.p)
		require.Error(t, err, tc.p)
		require.ErrorContains(t, err, tc.want)
	}
	require.Empty(t, store.scopes[config.ScopeGlobal])
}

func TestParamsChanges(t *testing.T) {
	t.Parallel()

	for _, a := range []string{"add", "update", "remove", "rm", "delete", " Remove "} {
		require.True(t, Params{Action: a}.Changes(), a)
	}
	for _, a := range []string{"", "list", "check"} {
		require.False(t, Params{Action: a}.Changes(), a)
	}
	require.Equal(t, "(set)", Params{APIKey: "secret"}.Redacted().APIKey)
	require.Contains(t, DescribeChange(Params{Action: "add", Name: "m1", URL: "http://x"}), "send code to this URL")
}
