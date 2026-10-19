package update

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheck(t *testing.T) {
	t.Parallel()

	t.Run("newer", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		gh.setRelease("v0.3.0", nil, true)
		res, err := gh.updater("0.2.0").Check(t.Context())
		require.NoError(t, err)
		require.True(t, res.Newer)
		require.Equal(t, "0.3.0", res.Latest)
		require.Equal(t, "0.2.0", res.Current)
		require.Equal(t, "https://github.com/ItsCetla/tada/releases/tag/v0.3.0", res.URL)
		require.NotNil(t, res.Release)

		gh.mu.Lock()
		defer gh.mu.Unlock()
		require.Equal(t, "application/vnd.github+json", gh.lastHead.Get("Accept"))
		require.Equal(t, "2022-11-28", gh.lastHead.Get("X-GitHub-Api-Version"))
		require.Equal(t, "Tada/0.2.0", gh.lastHead.Get("User-Agent"))
	})

	t.Run("same", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		gh.setRelease("v0.3.0", nil, true)
		res, err := gh.updater("v0.3.0").Check(t.Context())
		require.NoError(t, err)
		require.False(t, res.Newer)
		require.Equal(t, "0.3.0", res.Latest)
	})

	t.Run("prerelease of the same number is older", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		gh.setRelease("v0.3.0", nil, true)
		res, err := gh.updater("0.3.0-rc.1").Check(t.Context())
		require.NoError(t, err)
		require.True(t, res.Newer)
	})

	t.Run("no release yet", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		res, err := gh.updater("0.2.0").Check(t.Context())
		require.NoError(t, err)
		require.False(t, res.Newer)
		require.Empty(t, res.Latest)
		require.Nil(t, res.Release)
		require.Equal(t, ReleasesPage, res.URL)
	})

	t.Run("server error", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		gh.status = http.StatusInternalServerError
		_, err := gh.updater("0.2.0").Check(t.Context())
		require.Error(t, err)
	})
}

func TestNewOnlyTalksToGitHub(t *testing.T) {
	t.Parallel()
	u := New("0.2.0")
	require.Equal(t, LatestReleaseURL, u.APIURL)
	for _, ok := range []string{
		"https://api.github.com/repos/ItsCetla/tada/releases/latest",
		"https://github.com/ItsCetla/tada/releases/download/v0.3.0/x.tar.gz",
		"https://objects.githubusercontent.com/github-production-release-asset/1",
		"https://release-assets.githubusercontent.com/github-production-release-asset/1",
	} {
		target, err := url.Parse(ok)
		require.NoError(t, err)
		require.NoError(t, u.allowed(target), ok)
	}
	for _, bad := range []string{
		"http://api.github.com/repos/ItsCetla/tada/releases/latest",
		"https://example.com/tada.tar.gz",
		"https://github.com.evil.example/x",
	} {
		target, err := url.Parse(bad)
		require.NoError(t, err)
		require.Error(t, u.allowed(target), bad)
	}
	// A redirect to another host is refused as well.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/x", nil)
	require.NoError(t, err)
	require.Error(t, u.HTTP.CheckRedirect(req, nil))
}
