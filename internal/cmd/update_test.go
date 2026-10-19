package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/update"
	"github.com/stretchr/testify/require"
)

// fakeReleaseServer serves a latest release with one Linux archive holding
// a "tada" binary. tag "" answers 404.
func fakeReleaseServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "tada " + tag
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "tada_x/tada", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			if tag == "" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(update.Release{
				TagName: tag,
				HTMLURL: "https://github.com/ItsCetla/tada/releases/tag/" + tag,
				Assets: []update.Asset{{
					Name:               "tada_" + strings.TrimPrefix(tag, "v") + "_Linux_x86_64.tar.gz",
					BrowserDownloadURL: srv.URL + "/asset",
					Digest:             "sha256:" + hex.EncodeToString(sum[:]),
				}},
			})
		case "/asset":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestUpdateRun(t *testing.T, srv *httptest.Server, current string) (updateRun, *bytes.Buffer, string) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "tada")
	require.NoError(t, os.WriteFile(exe, []byte("tada old"), 0o755))
	var out bytes.Buffer
	return updateRun{
		u:         &update.Updater{Current: current, HTTP: srv.Client(), APIURL: srv.URL + "/latest"},
		inst:      update.Install{Kind: update.KindArchive, GOOS: "linux", GOARCH: "amd64", Exe: exe},
		out:       &out,
		in:        strings.NewReader(""),
		statePath: filepath.Join(t.TempDir(), update.StateFile),
	}, &out, exe
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestRunUpdate(t *testing.T) {
	t.Parallel()

	t.Run("check only reports", func(t *testing.T) {
		t.Parallel()
		r, out, exe := newTestUpdateRun(t, fakeReleaseServer(t, "v0.3.0"), "0.2.0")
		r.checkOnly = true
		require.NoError(t, runUpdate(t.Context(), r))
		require.Contains(t, out.String(), "Ta-Da 0.2.0 → 0.3.0")
		require.Equal(t, "tada old", readFile(t, exe))
		require.Equal(t, "0.3.0", update.LoadState(r.statePath).Latest)
	})

	t.Run("yes installs", func(t *testing.T) {
		t.Parallel()
		r, out, exe := newTestUpdateRun(t, fakeReleaseServer(t, "v0.3.0"), "0.2.0")
		r.yes = true
		require.NoError(t, runUpdate(t.Context(), r))
		require.Contains(t, out.String(), "Updated Ta-Da to 0.3.0")
		require.Equal(t, "tada v0.3.0", readFile(t, exe))
	})

	t.Run("tty prompt yes installs", func(t *testing.T) {
		t.Parallel()
		r, out, exe := newTestUpdateRun(t, fakeReleaseServer(t, "v0.3.0"), "0.2.0")
		r.tty = true
		r.in = strings.NewReader("y\n")
		require.NoError(t, runUpdate(t.Context(), r))
		require.Contains(t, out.String(), "[y/N]")
		require.Equal(t, "tada v0.3.0", readFile(t, exe))
	})

	t.Run("tty prompt default is no", func(t *testing.T) {
		t.Parallel()
		r, out, exe := newTestUpdateRun(t, fakeReleaseServer(t, "v0.3.0"), "0.2.0")
		r.tty = true
		r.in = strings.NewReader("\n")
		require.NoError(t, runUpdate(t.Context(), r))
		require.Contains(t, out.String(), "Not updated.")
		require.Equal(t, "tada old", readFile(t, exe))
	})

	t.Run("no tty without yes only reports", func(t *testing.T) {
		t.Parallel()
		r, out, exe := newTestUpdateRun(t, fakeReleaseServer(t, "v0.3.0"), "0.2.0")
		require.NoError(t, runUpdate(t.Context(), r))
		require.Contains(t, out.String(), "tada update --yes")
		require.Equal(t, "tada old", readFile(t, exe))
	})

	t.Run("up to date", func(t *testing.T) {
		t.Parallel()
		r, out, _ := newTestUpdateRun(t, fakeReleaseServer(t, "v0.3.0"), "0.3.0")
		r.yes = true
		require.NoError(t, runUpdate(t.Context(), r))
		require.Contains(t, out.String(), "up to date")
	})

	t.Run("no release yet", func(t *testing.T) {
		t.Parallel()
		r, out, _ := newTestUpdateRun(t, fakeReleaseServer(t, ""), "0.3.0")
		require.NoError(t, runUpdate(t.Context(), r))
		require.Contains(t, out.String(), "No Ta-Da release")
	})

	t.Run("dev build", func(t *testing.T) {
		t.Parallel()
		r, out, _ := newTestUpdateRun(t, fakeReleaseServer(t, "v0.3.0"), "0.1.0-dev.abc1234")
		r.inst.Kind = update.KindDev
		r.yes = true
		require.NoError(t, runUpdate(t.Context(), r))
		require.Contains(t, out.String(), "development build")
		require.Contains(t, out.String(), update.ReleasesPage)
	})
}
