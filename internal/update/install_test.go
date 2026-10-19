package update

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstall(t *testing.T) {
	t.Parallel()

	t.Run("archive replaces the executable", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		archive := makeTarGz(t, []archiveEntry{
			{name: "tada_0.3.0_Linux_x86_64/", dir: true},
			{name: "tada_0.3.0_Linux_x86_64/tada", body: "tada 0.3.0"},
		})
		rel := gh.setRelease("v0.3.0", map[string][]byte{"tada_0.3.0_Linux_x86_64.tar.gz": archive}, false)

		exe := filepath.Join(t.TempDir(), "tada")
		require.NoError(t, os.WriteFile(exe, []byte("tada 0.2.0"), 0o755))

		inst := Install{Kind: KindArchive, GOOS: "linux", GOARCH: "amd64", Exe: exe}
		res, err := gh.updater("0.2.0").Install(t.Context(), rel, inst)
		require.NoError(t, err)
		require.Equal(t, "0.3.0", res.Version)
		require.Equal(t, exe, res.Path)
		require.False(t, res.Background)
		got, err := os.ReadFile(exe)
		require.NoError(t, err)
		require.Equal(t, "tada 0.3.0", string(got))
	})

	t.Run("archive with a bad checksum leaves the executable alone", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		archive := makeTarGz(t, []archiveEntry{{name: "tada_0.3.0_Linux_x86_64/tada", body: "tada 0.3.0"}})
		rel := gh.setRelease("v0.3.0", map[string][]byte{"tada_0.3.0_Linux_x86_64.tar.gz": archive}, true)
		rel.Assets[0].Digest = "sha256:" + sha256Hex([]byte("other"))

		exe := filepath.Join(t.TempDir(), "tada")
		require.NoError(t, os.WriteFile(exe, []byte("tada 0.2.0"), 0o755))
		_, err := gh.updater("0.2.0").Install(t.Context(), rel, Install{Kind: KindArchive, GOOS: "linux", GOARCH: "amd64", Exe: exe})
		require.ErrorIs(t, err, ErrChecksumMismatch)
		got, err := os.ReadFile(exe)
		require.NoError(t, err)
		require.Equal(t, "tada 0.2.0", string(got))
	})

	t.Run("windows installer starts setup detached", func(t *testing.T) {
		t.Parallel()
		gh := newFakeGitHub(t)
		rel := gh.setRelease("v0.3.0", map[string][]byte{
			"tada_0.3.0_Windows_x86_64_Setup.exe": []byte("setup"),
			"tada_0.3.0_Windows_x86_64.zip":       []byte("zip"),
		}, true)

		var gotName string
		var gotArgs []string
		u := gh.updater("0.2.0")
		u.StartDetached = func(name string, args []string) error {
			gotName, gotArgs = name, args
			return nil
		}
		res, err := u.Install(t.Context(), rel, Install{Kind: KindWindowsInstaller, GOOS: "windows", GOARCH: "amd64"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(res.Path)) })
		require.True(t, res.Background)
		require.Equal(t, "tada_0.3.0_Windows_x86_64_Setup.exe", filepath.Base(gotName))
		require.Equal(t, gotName, res.Path)
		require.Equal(t, []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/CLOSEAPPLICATIONS"}, gotArgs)
		data, err := os.ReadFile(gotName)
		require.NoError(t, err)
		require.Equal(t, "setup", string(data))
	})

	t.Run("dev builds don't update", func(t *testing.T) {
		t.Parallel()
		u := &Updater{Current: "0.1.0-dev.abc1234"}
		_, err := u.Install(t.Context(), testRelease(), Install{Kind: KindDev})
		require.ErrorIs(t, err, ErrDevBuild)
	})

	t.Run("missing platform", func(t *testing.T) {
		t.Parallel()
		u := &Updater{Current: "0.2.0"}
		_, err := u.Install(t.Context(), testRelease(), Install{Kind: KindArchive, GOOS: "freebsd", GOARCH: "amd64", Exe: "/x/tada"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "no download")
	})
}
