package update

import (
	"os"
	"path/filepath"
	"strings"
)

// Kind is how this copy of Ta-Da was installed, which decides which release
// file to download and how to put it in place.
type Kind string

const (
	// KindDev is a development build; it never updates itself.
	KindDev Kind = "dev"
	// KindWindowsInstaller is an Inno Setup per-user install on Windows.
	KindWindowsInstaller Kind = "windows-installer"
	// KindArchive is a binary taken from a release archive.
	KindArchive Kind = "archive"
)

// Install describes the running copy.
type Install struct {
	Kind   Kind
	GOOS   string
	GOARCH string
	// Exe is the running executable with symlinks resolved.
	Exe string
}

// DetectInstall works out how the running copy was installed. exe should be
// the resolved path of the running executable ("" when unknown).
func DetectInstall(current, goos, goarch, exe string) Install {
	inst := Install{GOOS: goos, GOARCH: goarch, Exe: exe}
	switch {
	case IsDevVersion(current):
		inst.Kind = KindDev
	case goos == "windows" && exe != "" && fileExists(filepath.Join(filepath.Dir(exe), "unins000.exe")):
		inst.Kind = KindWindowsInstaller
	default:
		inst.Kind = KindArchive
	}
	return inst
}

// RunningExecutable returns the running executable's path with symlinks
// resolved.
func RunningExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// archOS maps GOOS to the title-cased name in release file names.
func archOS(goos string) string {
	switch goos {
	case "darwin":
		return "Darwin"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	}
	return ""
}

// archArch maps GOARCH to the name in release file names.
func archArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "arm64"
	}
	return ""
}

// AssetSuffix is the end of the release file name for an install, after
// "tada_<version>": "_Darwin_arm64.tar.gz", "_Windows_x86_64.zip" or
// "_Windows_x86_64_Setup.exe". It is "" when there is no such file.
func AssetSuffix(goos, goarch string, kind Kind) string {
	o, a := archOS(goos), archArch(goarch)
	if o == "" || a == "" {
		return ""
	}
	switch kind {
	case KindWindowsInstaller:
		if goos != "windows" {
			return ""
		}
		return "_" + o + "_" + a + "_Setup.exe"
	case KindArchive:
		if goos == "windows" {
			return "_" + o + "_" + a + ".zip"
		}
		return "_" + o + "_" + a + ".tar.gz"
	}
	return ""
}

// PickAsset returns the release file for an install, or nil when the
// release has none. The expected name is
// tada_<version>_<OS>_<arch><suffix>; the version comes from the tag, with
// any tada_* file of the right platform accepted as a fallback.
func PickAsset(rel *Release, goos, goarch string, kind Kind) *Asset {
	if rel == nil {
		return nil
	}
	suffix := AssetSuffix(goos, goarch, kind)
	if suffix == "" {
		return nil
	}
	want := BinaryName + "_" + rel.Version() + suffix
	for i := range rel.Assets {
		if rel.Assets[i].Name == want {
			return &rel.Assets[i]
		}
	}
	for i := range rel.Assets {
		name := rel.Assets[i].Name
		if strings.HasPrefix(name, BinaryName+"_") && strings.HasSuffix(name, suffix) {
			return &rel.Assets[i]
		}
	}
	return nil
}

// binaryName is the executable's name inside a release archive.
func binaryName(goos string) string {
	if goos == "windows" {
		return BinaryName + ".exe"
	}
	return BinaryName
}
