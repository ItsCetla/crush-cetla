package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckInterval is the least time between two background checks.
const CheckInterval = 6 * time.Hour

// NoCheckEnv disables the background update check when set to anything but
// "", "0" or "false". An explicit `tada update` still works.
const NoCheckEnv = "TADA_NO_UPDATE_CHECK"

// BackgroundCheckDisabledByEnv reports whether NoCheckEnv turns the
// background check off.
func BackgroundCheckDisabledByEnv() bool {
	v := strings.TrimSpace(os.Getenv(NoCheckEnv))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// State is what update.json remembers between runs.
type State struct {
	LastChecked time.Time `json:"lastChecked"`
	// Latest is the latest release's version at the last check.
	Latest string `json:"latest,omitempty"`
	// URL is the latest release's page.
	URL string `json:"url,omitempty"`
	// NotifiedVersion is the last version the TUI announced.
	NotifiedVersion string `json:"notifiedVersion,omitempty"`
}

// LoadState reads the state file. A missing or unreadable file gives the
// zero State.
func LoadState(path string) State {
	var s State
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}
	}
	return s
}

// SaveState writes the state file atomically.
func SaveState(path string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-*.json")
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// DueForCheck reports whether the last check is at least CheckInterval old
// (or in the future, after a clock change).
func (s State) DueForCheck(now time.Time) bool {
	return s.LastChecked.IsZero() || now.Sub(s.LastChecked) >= CheckInterval || s.LastChecked.After(now)
}

// Record stores a check's result.
func (s *State) Record(res CheckResult, now time.Time) {
	s.LastChecked = now
	s.Latest = res.Latest
	s.URL = res.URL
}

// Available returns the newer version the last check found, if any.
func (s State) Available(current string) (string, bool) {
	if s.Latest == "" || CompareVersions(s.Latest, current) <= 0 {
		return "", false
	}
	return s.Latest, true
}

// PendingNotice returns the newer version that hasn't been announced yet.
func (s State) PendingNotice(current string) (string, bool) {
	v, ok := s.Available(current)
	if !ok || s.NotifiedVersion == v {
		return "", false
	}
	return v, true
}

// Notice is the one-line message announcing version.
func Notice(version string) string {
	return fmt.Sprintf("%s %s is out. Run `%s update` (or use the command palette) to install it.", AppName, version, BinaryName)
}

// Background runs the background check: it checks GitHub when the last
// check is older than CheckInterval, then returns the notice for a newer
// version that hasn't been announced yet and marks it announced. It returns
// "" when there is nothing to say, including on any failure. Dev builds
// never check.
func (u *Updater) Background(ctx context.Context, statePath string, now time.Time) string {
	if IsDevVersion(u.Current) {
		return ""
	}
	s := LoadState(statePath)
	if s.DueForCheck(now) {
		res, err := u.Check(ctx)
		if err != nil {
			// Stay quiet; the cached result (if any) still counts.
			return u.markNotified(statePath, s)
		}
		s.Record(res, now)
		if err := SaveState(statePath, s); err != nil {
			return ""
		}
	}
	return u.markNotified(statePath, s)
}

func (u *Updater) markNotified(statePath string, s State) string {
	v, ok := s.PendingNotice(u.Current)
	if !ok {
		return ""
	}
	s.NotifiedVersion = v
	if err := SaveState(statePath, s); err != nil {
		return ""
	}
	return Notice(v)
}
