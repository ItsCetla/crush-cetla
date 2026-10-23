package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

func testConfig(t *testing.T, machines config.Machines) *config.Config {
	t.Helper()
	providers := map[string]config.ProviderConfig{}
	for name, m := range machines {
		if m.Model != "" {
			providers[name] = config.ProviderConfig{ID: name, Models: []catwalk.Model{{ID: m.Model}}}
		}
	}
	return &config.Config{Machines: machines, Providers: csync.NewMapFrom(providers)}
}

func onlineServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestLeaseSlots(t *testing.T) {
	t.Parallel()

	pool := NewPool(t.TempDir())
	m := Machine{Name: "m1", MachineConfig: config.MachineConfig{Slots: 2}, Model: "x"}

	a, err := pool.TryAcquire(m)
	require.NoError(t, err)
	b, err := pool.TryAcquire(m)
	require.NoError(t, err)
	require.NotEqual(t, a.Slot, b.Slot)
	require.Equal(t, 2, pool.Busy(m))

	_, err = pool.TryAcquire(m)
	require.ErrorIs(t, err, ErrNoneFree)

	a.Release()
	a.Release()
	require.Equal(t, 1, pool.Free(m))
	c, err := pool.TryAcquire(m)
	require.NoError(t, err)
	require.Equal(t, a.Slot, c.Slot)
	b.Release()
	c.Release()
	require.Equal(t, 0, pool.Busy(m))
}

func TestAcquireAutoPicksMostFree(t *testing.T) {
	t.Parallel()

	url := onlineServer(t)
	cfg := testConfig(t, config.Machines{
		"small": {URL: url + "/v1", Model: "a", Slots: 1},
		"big":   {URL: url + "/v2", Model: "b", Slots: 3},
		"off":   {URL: url + "/v3", Model: "c", Slots: 8, Disabled: true},
		"dead":  {URL: "http://127.0.0.1:1/v1", Model: "d", Slots: 9},
	})
	pool := NewPool(t.TempDir())
	ctx := context.Background()

	var leases []*Lease
	got := map[string]int{}
	for range 4 {
		l, err := pool.Acquire(ctx, cfg, Auto)
		require.NoError(t, err)
		leases = append(leases, l)
		got[l.Machine.Name]++
	}
	require.Equal(t, map[string]int{"big": 3, "small": 1}, got)
	require.Equal(t, "big", leases[0].Machine.Name)

	_, err := pool.Acquire(ctx, cfg, "")
	require.ErrorIs(t, err, ErrNoneFree)

	for _, l := range leases {
		l.Release()
	}
}

func TestAcquireNamed(t *testing.T) {
	t.Parallel()

	url := onlineServer(t)
	cfg := testConfig(t, config.Machines{
		"m1":    {URL: url + "/v1", Model: "a", Slots: 1},
		"empty": {URL: url + "/v4"},
	})
	pool := NewPool(t.TempDir())
	ctx := context.Background()

	l, err := pool.Acquire(ctx, cfg, "M1")
	require.NoError(t, err)
	require.Equal(t, "m1", l.Machine.Name)
	defer l.Release()

	_, err = pool.Acquire(ctx, cfg, "m1")
	require.ErrorIs(t, err, ErrNoneFree)
	_, err = pool.Acquire(ctx, cfg, "nope")
	require.ErrorContains(t, err, "no machine named")
	_, err = pool.Acquire(ctx, cfg, "empty")
	require.ErrorContains(t, err, "no model")
}

func TestAcquireSkipsNamedOnlyMachine(t *testing.T) {
	t.Parallel()

	url := onlineServer(t)
	cfg := testConfig(t, config.Machines{
		"m1":   {URL: url + "/v1", Model: "a", Slots: 1},
		"mine": {URL: url + "/v2", Model: "b", Slots: 4, Use: config.MachineUseNamed},
	})
	pool := NewPool(t.TempDir())
	ctx := context.Background()

	// Automatic picks leave the by-name machine alone, however free it is.
	l, err := pool.Acquire(ctx, cfg, Auto)
	require.NoError(t, err)
	require.Equal(t, "m1", l.Machine.Name)
	defer l.Release()
	_, err = pool.Acquire(ctx, cfg, Auto)
	require.ErrorIs(t, err, ErrNoneFree)

	// Asking for it by name still works.
	named, err := pool.Acquire(ctx, cfg, "mine")
	require.NoError(t, err)
	require.Equal(t, "mine", named.Machine.Name)
	named.Release()

	require.Contains(t, Describe(Machines(cfg)[1]), ", only when asked for by name")
	got := PromptSection(cfg, "x")
	require.Contains(t, got, `A machine marked "only when asked for by name" is left out`)
}

func TestPromptSection(t *testing.T) {
	t.Parallel()

	require.Empty(t, PromptSection(testConfig(t, nil), "x"))
	require.Empty(t, PromptSection(testConfig(t, config.Machines{"off": {URL: "u", Model: "m", Disabled: true}}), "x"))

	cfg := testConfig(t, config.Machines{
		"m1": {URL: "http://a/v1", Model: "qwen", Slots: 4, Hardware: "M1 Max", Tags: []string{"code"}, Notes: "Quiet."},
		"pc": {URL: "http://b/v1", Slots: 2, Hardware: "RTX 3060 Ti"},
	})
	got := PromptSection(cfg, "the agent tool")
	require.True(t, strings.HasPrefix(got, "<machines>"))
	require.Contains(t, got, "through the agent tool")
	require.Contains(t, got, "- m1 (M1 Max): 4 slots, model qwen, good for: code. Quiet")
	require.Contains(t, got, "- pc (RTX 3060 Ti): 2 slots, no model yet")
}

func TestCheck(t *testing.T) {
	t.Parallel()

	url := onlineServer(t)
	cfg := testConfig(t, config.Machines{
		"up":   {URL: url + "/v9", Model: "a", Slots: 2},
		"down": {URL: "http://127.0.0.1:1/v9", Model: "b"},
	})
	got := NewPool(t.TempDir()).Check(context.Background(), cfg)
	require.Len(t, got, 2)
	require.Equal(t, "down", got[0].Name)
	require.False(t, got[0].Online)
	require.Contains(t, DescribeStatus(got[0]), "[offline")
	require.True(t, got[1].Online)
	require.Contains(t, DescribeStatus(got[1]), "[online, 2 of 2 free]")
}
