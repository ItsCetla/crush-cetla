package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testAsset = "tada_0.3.0_Linux_x86_64.tar.gz"

func TestParseSums(t *testing.T) {
	t.Parallel()
	a := strings.Repeat("a", 64)
	b := strings.Repeat("B", 64)
	sums := a + "  tada_0.3.0_Darwin_arm64.tar.gz\n" + b + " *" + testAsset + "\nnot a line\n"
	require.Equal(t, a, ParseSums(sums, "tada_0.3.0_Darwin_arm64.tar.gz"))
	require.Equal(t, strings.ToLower(b), ParseSums(sums, testAsset))
	require.Empty(t, ParseSums(sums, "tada_0.3.0_Windows_x86_64.zip"))
	require.Empty(t, ParseSums("xyz  "+testAsset, testAsset))
}

func TestDigestSHA256(t *testing.T) {
	t.Parallel()
	sum := strings.Repeat("c", 64)
	require.Equal(t, sum, digestSHA256("sha256:"+sum))
	require.Equal(t, sum, digestSHA256("SHA256:"+strings.ToUpper(sum)))
	require.Empty(t, digestSHA256(""))
	require.Empty(t, digestSHA256("sha512:"+sum))
	require.Empty(t, digestSHA256("sha256:short"))
}

func TestDownload(t *testing.T) {
	t.Parallel()
	payload := []byte("pretend this is an archive")

	t.Run("verified by digest", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		rel := gh.setRelease("v0.3.0", map[string][]byte{testAsset: payload}, true)
		dir := t.TempDir()
		path, err := gh.updater("0.2.0").Download(t.Context(), rel, PickAsset(rel, "linux", "amd64", KindArchive), dir)
		require.NoError(t, err)
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, payload, got)
	})

	t.Run("verified by SHA256SUMS", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		rel := gh.setRelease("v0.3.0", map[string][]byte{testAsset: payload}, false)
		path, err := gh.updater("0.2.0").Download(t.Context(), rel, PickAsset(rel, "linux", "amd64", KindArchive), t.TempDir())
		require.NoError(t, err)
		require.FileExists(t, path)
	})

	t.Run("digest mismatch deletes the file", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		rel := gh.setRelease("v0.3.0", map[string][]byte{testAsset: payload}, true)
		asset := PickAsset(rel, "linux", "amd64", KindArchive)
		asset.Digest = "sha256:" + sha256Hex([]byte("something else"))
		dir := t.TempDir()
		_, err := gh.updater("0.2.0").Download(t.Context(), rel, asset, dir)
		require.ErrorIs(t, err, ErrChecksumMismatch)
		require.NoFileExists(t, filepath.Join(dir, testAsset))
	})

	t.Run("SHA256SUMS mismatch deletes the file", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		rel := gh.setRelease("v0.3.0", map[string][]byte{testAsset: payload}, false)
		gh.mu.Lock()
		gh.files[testAsset] = []byte("tampered")
		gh.mu.Unlock()
		dir := t.TempDir()
		_, err := gh.updater("0.2.0").Download(t.Context(), rel, PickAsset(rel, "linux", "amd64", KindArchive), dir)
		require.ErrorIs(t, err, ErrChecksumMismatch)
		require.NoFileExists(t, filepath.Join(dir, testAsset))
	})

	t.Run("no checksum is refused", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		rel := gh.setRelease("v0.3.0", map[string][]byte{testAsset: payload}, true)
		asset := PickAsset(rel, "linux", "amd64", KindArchive)
		asset.Digest = ""
		dir := t.TempDir()
		_, err := gh.updater("0.2.0").Download(t.Context(), rel, asset, dir)
		require.ErrorIs(t, err, ErrNoChecksum)
		require.NoFileExists(t, filepath.Join(dir, testAsset))
	})

	t.Run("SHA256SUMS without the file is refused", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		rel := gh.setRelease("v0.3.0", map[string][]byte{testAsset: payload}, false)
		gh.mu.Lock()
		gh.files[SumsFile] = []byte(strings.Repeat("d", 64) + "  other.tar.gz\n")
		gh.mu.Unlock()
		_, err := gh.updater("0.2.0").Download(t.Context(), rel, PickAsset(rel, "linux", "amd64", KindArchive), t.TempDir())
		require.ErrorIs(t, err, ErrNoChecksum)
	})
}
