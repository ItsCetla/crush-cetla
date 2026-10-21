package claudecode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"
)

// LiveSession is an interactive Claude Code session running on this
// machine, as the CLI describes it in ~/.claude/sessions/<pid>.json.
type LiveSession struct {
	PID       int
	SessionID string
	Cwd       string
	Name      string
	// Status is "idle", "busy" or "waiting" (for the user).
	Status        string
	UpdatedAt     time.Time
	StatusUpdated time.Time
}

// liveSessionFile is the part of a session file Ta-Da reads.
type liveSessionFile struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	UpdatedAt       int64  `json:"updatedAt"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

// LiveSessions lists the interactive Claude Code sessions whose process
// is still running, most recently updated first. It reads only the
// sessions directory's *.json files, never anything else in it.
func LiveSessions() []LiveSession {
	var out []LiveSession
	seen := map[int]bool{}
	for _, dir := range configDirs() {
		files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*.json"))
		for _, f := range files {
			s, ok := readLiveSession(f)
			if !ok || seen[s.PID] {
				continue
			}
			seen[s.PID] = true
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func readLiveSession(path string) (LiveSession, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return LiveSession{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LiveSession{}, false
	}
	var f liveSessionFile
	if json.Unmarshal(data, &f) != nil || f.Kind != "interactive" || f.PID <= 0 || !processAlive(f.PID) {
		return LiveSession{}, false
	}
	return LiveSession{
		PID:           f.PID,
		SessionID:     f.SessionID,
		Cwd:           f.Cwd,
		Name:          f.Name,
		Status:        f.Status,
		UpdatedAt:     fromMillis(f.UpdatedAt),
		StatusUpdated: fromMillis(f.StatusUpdatedAt),
	}, true
}

func fromMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// processAlive reports whether a process with this pid exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer p.Release() //nolint:errcheck
	if runtime.GOOS == "windows" {
		// FindProcess opens the process there, so finding it is enough.
		return true
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
