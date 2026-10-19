// Package update implements Ta-Da's in-app updates from GitHub Releases.
//
// Network egress: the only hosts this package ever contacts are
// api.github.com (to read the latest release of ItsCetla/tada) and, only
// when the user asks to install an update, the release asset download on
// github.com, which redirects to GitHub's asset storage
// (objects.githubusercontent.com / release-assets.githubusercontent.com).
// Nothing else leaves the computer: no telemetry, no other update servers.
// Requests and redirects to any other host are refused.
//
// The background check runs at most every six hours, never blocks startup
// and stays silent on failure. A download only starts when the user runs
// `tada update` (or picks it in the command palette). Every download is
// checked against the SHA-256 GitHub lists for the asset, or the release's
// SHA256SUMS.txt; without a checksum the update is refused. The update is
// installed the way this copy was installed:
//
//	dev                a development build: never updates itself.
//	windows-installer  Inno Setup per-user install: run the new Setup silently.
//	archive            a binary from a release archive: swap the executable.
package update

const (
	// Repo is the GitHub repository releases are read from.
	Repo = "ItsCetla/tada"
	// LatestReleaseURL is the GitHub API endpoint for the latest release.
	LatestReleaseURL = "https://api.github.com/repos/" + Repo + "/releases/latest"
	// ReleasesPage is where people can download a release by hand.
	ReleasesPage = "https://github.com/" + Repo + "/releases"
	// AppName is the display name used in messages.
	AppName = "Ta-Da"
	// BinaryName is the command name, without the .exe suffix.
	BinaryName = "tada"
	// StateFile is the name of the update state file in the global data dir.
	StateFile = "update.json"
)
