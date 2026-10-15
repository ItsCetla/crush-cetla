package config

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/catwalk/pkg/embedded"
	"github.com/stretchr/testify/require"
)

func resetProviderState() {
	providerOnce = sync.Once{}
	providerList = nil
	providerErr = nil
}

// isolateCatalog points the saved provider catalog at a fresh directory and
// returns the path [Providers] reads it from.
func isolateCatalog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_DATA", dir)
	return filepath.Join(dir, "providers.json")
}

// forbidCatalogRequests points CATWALK_URL at a server that fails the test on
// any request, and clears the deprecated opt-out so the catalog is built
// exactly as it would be with automatic updates nominally enabled.
func forbidCatalogRequests(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	t.Setenv("CATWALK_URL", server.URL)

	t.Setenv("CRUSH_DISABLE_PROVIDER_AUTO_UPDATE", "")
	require.NoError(t, os.Unsetenv("CRUSH_DISABLE_PROVIDER_AUTO_UPDATE"))
}

func providerIDs(providers []catwalk.Provider) []catwalk.InferenceProvider {
	ids := make([]catwalk.InferenceProvider, len(providers))
	for i, p := range providers {
		ids[i] = p.ID
	}
	return ids
}

// TestProviders_NeverUsesNetwork guards the offline guarantee: building the
// catalog issues no HTTP request at all, even with CATWALK_URL pointing at
// a reachable server and automatic updates not disabled.
func TestProviders_NeverUsesNetwork(t *testing.T) {
	isolateCatalog(t)
	forbidCatalogRequests(t)

	resetProviderState()
	t.Cleanup(resetProviderState)

	providers, err := Providers(&Config{Options: &Options{DisableProviderAutoUpdate: false}})
	require.NoError(t, err)
	require.Len(t, providers, len(embedded.GetAll()), "the embedded catalog is used when none was saved")
}

func TestProviders_UsesSavedCatalog(t *testing.T) {
	path := isolateCatalog(t)
	forbidCatalogRequests(t)

	saved := []catwalk.Provider{
		{Name: "Saved1", ID: "s1"},
		{Name: "Saved2", ID: "s2"},
	}
	data, err := json.Marshal(saved)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	resetProviderState()
	t.Cleanup(resetProviderState)

	providers, err := Providers(&Config{Options: &Options{}})
	require.NoError(t, err)
	require.Equal(t, []catwalk.InferenceProvider{"s1", "s2"}, providerIDs(providers))
}

// TestUpdateProviders_SavesCatalogForProviders covers the explicit refresh
// path: `crush update-providers` fetches the catalog and saves it, and the
// next catalog build picks the saved copy up without another request.
func TestUpdateProviders_SavesCatalogForProviders(t *testing.T) {
	isolateCatalog(t)

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v2/providers" {
			t.Errorf("unexpected catalog path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]catwalk.Provider{{Name: "Fresh", ID: "fresh"}})
	}))
	t.Cleanup(server.Close)

	require.NoError(t, UpdateProviders(server.URL))
	require.Equal(t, int32(1), hits.Load())

	resetProviderState()
	t.Cleanup(resetProviderState)

	providers, err := Providers(&Config{Options: &Options{}})
	require.NoError(t, err)
	require.Equal(t, []catwalk.InferenceProvider{"fresh"}, providerIDs(providers))
	require.Equal(t, int32(1), hits.Load(), "building the catalog makes no request")
}

// TestProviders_MergesSourcesInPrecedenceOrder checks how catalog sources
// combine: providers are kept in source order, the earliest source wins a
// duplicate provider ID, and a source error is reported as a warning
// without dropping what that source returned.
func TestProviders_MergesSourcesInPrecedenceOrder(t *testing.T) {
	resetProviderState()
	t.Cleanup(resetProviderState)

	original := catalogSources
	t.Cleanup(func() { catalogSources = original })
	catalogSources = []catalogSource{
		{
			name: "first",
			load: func() ([]catwalk.Provider, error) {
				return []catwalk.Provider{
					{ID: "shared", Name: "From first"},
					{ID: "a", Name: "A"},
				}, nil
			},
		},
		{
			name: "second",
			load: func() ([]catwalk.Provider, error) {
				return []catwalk.Provider{
					{ID: "shared", Name: "From second"},
					{ID: "b", Name: "B"},
				}, errors.New("partly unreadable")
			},
		},
	}

	providers, err := Providers(&Config{Options: &Options{}})
	require.ErrorContains(t, err, "second provider catalog: partly unreadable")
	require.Equal(t, []catwalk.InferenceProvider{"shared", "a", "b"}, providerIDs(providers))
	require.Equal(t, "From first", providers[0].Name)
}

// TestProviders_HonorsDisableDefaultProviders makes sure no catalog
// provider is offered when default providers are disabled.
func TestProviders_HonorsDisableDefaultProviders(t *testing.T) {
	isolateCatalog(t)

	resetProviderState()
	t.Cleanup(resetProviderState)

	providers, err := Providers(&Config{
		Options: &Options{DisableDefaultProviders: true},
	})
	require.NoError(t, err)
	require.Empty(t, providers)
}

func TestCache_StoreAndGet(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := tmpDir + "/test.json"

	cache := newCache[[]catwalk.Provider](cachePath)

	providers := []catwalk.Provider{
		{Name: "Provider1", ID: "p1"},
		{Name: "Provider2", ID: "p2"},
	}

	// Store.
	err := cache.Store(providers)
	require.NoError(t, err)

	// Get.
	result, err := cache.Get()
	require.NoError(t, err)
	require.Len(t, result, 2)
	require.Equal(t, "Provider1", result[0].Name)
}

func TestCache_GetNonExistent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := tmpDir + "/nonexistent.json"

	cache := newCache[[]catwalk.Provider](cachePath)

	_, err := cache.Get()
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to read provider cache file")
}

func TestCache_GetInvalidJSON(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := tmpDir + "/invalid.json"

	require.NoError(t, os.WriteFile(cachePath, []byte("invalid json"), 0o644))

	cache := newCache[[]catwalk.Provider](cachePath)

	_, err := cache.Get()
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to unmarshal provider data from cache")
}

func TestCachePathFor(t *testing.T) {
	tests := []struct {
		name            string
		crushGlobalData string
		xdgDataHome     string
		expected        string
	}{
		{
			name:            "with CRUSH_GLOBAL_DATA",
			crushGlobalData: "/scratch/data",
			expected:        "/scratch/data/providers.json",
		},
		{
			name:            "CRUSH_GLOBAL_DATA takes priority over XDG_DATA_HOME",
			crushGlobalData: "/scratch/data",
			xdgDataHome:     "/custom/data",
			expected:        "/scratch/data/providers.json",
		},
		{
			name:        "with XDG_DATA_HOME",
			xdgDataHome: "/custom/data",
			expected:    "/custom/data/crush/providers.json",
		},
		{
			name:     "without either",
			expected: "", // Will use platform-specific default.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CRUSH_GLOBAL_DATA", tt.crushGlobalData)
			t.Setenv("XDG_DATA_HOME", tt.xdgDataHome)

			result := cachePathFor("providers")
			if tt.expected != "" {
				require.Equal(t, tt.expected, filepath.ToSlash(result))
			} else {
				require.Contains(t, result, "crush")
				require.Contains(t, result, "providers.json")
			}
		})
	}
}

// TestCacheStore_ReplacesFileInsteadOfRewritingIt guards the property that
// a running Crush instance depends on while `crush update-providers`
// replaces the catalog: the file is swapped into place as a finished file,
// never truncated and refilled underneath a reader that is already reading
// it. A reader that loses that race cannot parse the catalog and silently
// falls back to the bundled copy.
func TestCacheStore_ReplacesFileInsteadOfRewritingIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	c := newCache[[]catwalk.Provider](path)

	require.NoError(t, c.Store([]catwalk.Provider{{ID: "first", Name: "First"}}))
	before, err := os.Stat(path)
	require.NoError(t, err)

	require.NoError(t, c.Store([]catwalk.Provider{{ID: "second", Name: "Second"}}))
	after, err := os.Stat(path)
	require.NoError(t, err)

	// os.Stat on Windows resolves file identity lazily by reopening the path,
	// so both stats describe whichever file the path points at by the time
	// they are compared and SameFile cannot observe the replacement. The
	// write path is shared, so asserting this on the other platforms covers
	// it. The checks below still run everywhere.
	if runtime.GOOS != "windows" {
		require.False(t, os.SameFile(before, after),
			"the cache should be replaced by a rename, not rewritten in place")
	}

	// The new contents are complete and no temporary files are left behind.
	got, err := c.Get()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, catwalk.InferenceProvider("second"), got[0].ID)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the cache file should remain")
	require.Equal(t, "providers.json", entries[0].Name())
}
