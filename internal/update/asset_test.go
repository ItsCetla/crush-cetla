package update

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func testRelease() *Release {
	names := []string{
		"SHA256SUMS.txt",
		"tada_0.3.0_Darwin_arm64.tar.gz",
		"tada_0.3.0_Darwin_x86_64.tar.gz",
		"tada_0.3.0_Linux_arm64.tar.gz",
		"tada_0.3.0_Linux_x86_64.tar.gz",
		"tada_0.3.0_Linux_x86_64.deb",
		"tada_0.3.0_Windows_arm64.zip",
		"tada_0.3.0_Windows_x86_64.zip",
		"tada_0.3.0_Windows_x86_64_Setup.exe",
		"tada_0.3.0_Windows_arm64_Setup.exe",
	}
	rel := &Release{TagName: "v0.3.0", HTMLURL: "https://github.com/ItsCetla/tada/releases/tag/v0.3.0"}
	for _, n := range names {
		rel.Assets = append(rel.Assets, Asset{Name: n})
	}
	return rel
}

func TestPickAsset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, goos, goarch string
		kind               Kind
		want               string
	}{
		{"darwin arm64", "darwin", "arm64", KindArchive, "tada_0.3.0_Darwin_arm64.tar.gz"},
		{"linux amd64", "linux", "amd64", KindArchive, "tada_0.3.0_Linux_x86_64.tar.gz"},
		{"windows amd64 archive", "windows", "amd64", KindArchive, "tada_0.3.0_Windows_x86_64.zip"},
		{"windows amd64 installer", "windows", "amd64", KindWindowsInstaller, "tada_0.3.0_Windows_x86_64_Setup.exe"},
		{"windows arm64 installer", "windows", "arm64", KindWindowsInstaller, "tada_0.3.0_Windows_arm64_Setup.exe"},
		{"unknown arch", "linux", "riscv64", KindArchive, ""},
		{"dev", "linux", "amd64", KindDev, ""},
		{"installer off windows", "linux", "amd64", KindWindowsInstaller, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := PickAsset(testRelease(), tt.goos, tt.goarch, tt.kind)
			if tt.want == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.want, got.Name)
		})
	}
}

func TestPickAssetFallbackName(t *testing.T) {
	t.Parallel()
	// The tag and the file version disagree (a "v0.3" tag); the platform
	// suffix still finds the file.
	rel := &Release{TagName: "v0.3", Assets: []Asset{{Name: "tada_0.3.0_Linux_x86_64.tar.gz"}}}
	got := PickAsset(rel, "linux", "amd64", KindArchive)
	require.NotNil(t, got)
	require.Equal(t, "tada_0.3.0_Linux_x86_64.tar.gz", got.Name)
}

func TestDetectInstall(t *testing.T) {
	t.Parallel()

	t.Run("dev", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, KindDev, DetectInstall("0.1.0-dev.abc1234", "linux", "amd64", "/usr/local/bin/tada").Kind)
		require.Equal(t, KindDev, DetectInstall("devel", "darwin", "arm64", "").Kind)
	})

	t.Run("archive", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, KindArchive, DetectInstall("0.2.0", "linux", "amd64", "/usr/local/bin/tada").Kind)
		require.Equal(t, KindArchive, DetectInstall("0.2.0", "windows", "amd64", filepath.Join(t.TempDir(), "tada.exe")).Kind)
	})

	t.Run("windows installer", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "unins000.exe"), []byte("x"), 0o600))
		inst := DetectInstall("0.2.0", "windows", "amd64", filepath.Join(dir, "tada.exe"))
		require.Equal(t, KindWindowsInstaller, inst.Kind)
		// The uninstaller only counts on Windows.
		require.Equal(t, KindArchive, DetectInstall("0.2.0", "linux", "amd64", filepath.Join(dir, "tada")).Kind)
	})
}
