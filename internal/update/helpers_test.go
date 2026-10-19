package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeGitHub is an httptest stand-in for the releases API and its asset
// downloads. Tests never touch the real GitHub.
type fakeGitHub struct {
	srv *httptest.Server

	mu       sync.Mutex
	release  *Release // nil answers 404
	status   int      // non-zero overrides the latest-release status
	files    map[string][]byte
	checks   atomic.Int32
	lastHead http.Header
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{files: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/ItsCetla/tada/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		f.checks.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastHead = r.Header.Clone()
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if f.release == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.release)
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		data, ok := f.files[strings.TrimPrefix(r.URL.Path, "/download/")]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// updater returns an Updater for current that talks to the fake server.
func (f *fakeGitHub) updater(current string) *Updater {
	return &Updater{
		Current: current,
		HTTP:    f.srv.Client(),
		APIURL:  f.srv.URL + "/repos/ItsCetla/tada/releases/latest",
	}
}

// setRelease publishes a release with the given files. withDigest puts
// GitHub's digest on each asset; otherwise a SHA256SUMS.txt is added.
func (f *fakeGitHub) setRelease(tag string, files map[string][]byte, withDigest bool) *Release {
	f.mu.Lock()
	defer f.mu.Unlock()
	rel := &Release{TagName: tag, HTMLURL: "https://github.com/ItsCetla/tada/releases/tag/" + tag}
	var sums strings.Builder
	for name, data := range files {
		f.files[name] = data
		a := Asset{Name: name, BrowserDownloadURL: f.srv.URL + "/download/" + name, Size: int64(len(data))}
		if withDigest {
			a.Digest = "sha256:" + sha256Hex(data)
		}
		sums.WriteString(sha256Hex(data) + "  " + name + "\n")
		rel.Assets = append(rel.Assets, a)
	}
	if !withDigest {
		f.files[SumsFile] = []byte(sums.String())
		rel.Assets = append(rel.Assets, Asset{Name: SumsFile, BrowserDownloadURL: f.srv.URL + "/download/" + SumsFile})
	}
	f.release = rel
	return rel
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type archiveEntry struct {
	name string
	body string
	dir  bool
	link string // symlink target (tar only)
}

func makeTarGz(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		switch {
		case e.dir:
			hdr.Typeflag, hdr.Size = tar.TypeDir, 0
		case e.link != "":
			hdr.Typeflag, hdr.Size, hdr.Linkname = tar.TypeSymlink, 0, e.link
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if hdr.Typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func makeZip(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		if e.dir {
			_, err := zw.Create(e.name)
			require.NoError(t, err)
			continue
		}
		w, err := zw.Create(e.name)
		require.NoError(t, err)
		_, err = w.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
