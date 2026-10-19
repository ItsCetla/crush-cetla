package version

import (
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
)

// Build-time parameters set via -ldflags.

var (
	Version = "devel"
	Commit  = "unknown"
	// BuildID is a unique identifier for this build. For release builds it
	// equals Commit; for development builds (go run / go build without
	// ldflags) it is derived from the executable's modification time, which
	// changes on every recompilation.
	BuildID = ""
)

// A user may install tada using `go install github.com/charmbracelet/crush@latest`.
// without -ldflags, in which case the version above is unset. As a workaround
// we use the embedded build version that *is* set when using `go install` (and
// is only set for `go install` and not for `go build`).
func init() {
	info, ok := debug.ReadBuildInfo()
	if ok {
		// Newer Go toolchains also stamp a VCS pseudo-version into
		// plain `go build` binaries, so only fall back to it when no
		// version was passed through -ldflags.
		mainVersion := info.Main.Version
		if Version == "devel" && mainVersion != "" && mainVersion != "(devel)" {
			Version = mainVersion
		}
	}

	// Derive BuildID when not set via ldflags.
	if BuildID == "" {
		BuildID = deriveBuildID()
	}
}

// deriveBuildID uses the running executable's modification time as a unique
// build fingerprint. This changes on every recompilation (including `go run`),
// making it reliable for detecting stale servers during development.
func deriveBuildID() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return "unknown"
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 36)
}

// pseudoVersion matches Go's VCS pseudo-versions, e.g.
// v0.1.1-0.20261019163516-ef7bc645ab12 (optionally +dirty).
var pseudoVersion = regexp.MustCompile(`^v?[0-9.]+-(?:[0-9a-z.]+\.)?0\.[0-9]{14}-([0-9a-f]{12})(\+dirty)?$`)

// Display returns a short, human-friendly version for the UI. Release
// versions are returned as-is; Go pseudo-versions from plain `go build`
// become "dev+<short commit>". Use [Version] for anything compared or sent
// over the wire.
func Display() string {
	return display(Version)
}

func display(v string) string {
	m := pseudoVersion.FindStringSubmatch(v)
	if m == nil {
		return v
	}
	s := "dev+" + m[1][:7]
	if m[2] != "" {
		s += "-dirty"
	}
	return s
}
