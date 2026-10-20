package control

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/charmbracelet/crush/internal/lock"
	"github.com/charmbracelet/crush/internal/permission"
)

const (
	// keepActions is how many actions the log keeps on disk and in memory.
	keepActions = 1000
	// trimSlack is how far past keepActions the file may grow before it's
	// rewritten.
	trimSlack = 500
)

// ActionLog persists the permission service's action log for a project,
// so it survives restarts.
type ActionLog struct {
	path string

	mu      sync.Mutex
	actions []permission.Action
	lines   int
}

// OpenActionLog loads the log at path, creating its directory.
func OpenActionLog(path string) (*ActionLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &ActionLog{path: path}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var a permission.Action
		if json.Unmarshal(sc.Bytes(), &a) != nil {
			continue
		}
		l.lines++
		l.actions = append(l.actions, a)
		if len(l.actions) > keepActions {
			l.actions = l.actions[len(l.actions)-keepActions:]
		}
	}
	return l, nil
}

// Add appends an action. It's safe to use as the permission service's
// recorder.
func (l *ActionLog) Add(a permission.Action) {
	line, err := json.Marshal(a)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.actions = append(l.actions, a)
	if len(l.actions) > keepActions {
		l.actions = l.actions[len(l.actions)-keepActions:]
	}
	release, err := lock.File(context.Background(), l.path+".lock")
	if err != nil {
		return
	}
	defer release()
	if l.lines >= keepActions+trimSlack {
		l.rewrite()
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err == nil {
		l.lines++
	}
}

// rewrite replaces the file with the in-memory actions. The caller holds
// the lock.
func (l *ActionLog) rewrite() {
	tmp := l.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	for _, a := range l.actions {
		line, err := json.Marshal(a)
		if err != nil {
			continue
		}
		_, _ = w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	f.Close()
	if os.Rename(tmp, l.path) == nil {
		l.lines = len(l.actions)
	}
}

// Recent returns up to limit actions, newest first. When sessionID isn't
// empty only that session's actions (and session-less events) are
// returned. A limit of 0 returns all of them.
func (l *ActionLog) Recent(sessionID string, limit int) []permission.Action {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []permission.Action
	for i := len(l.actions) - 1; i >= 0; i-- {
		a := l.actions[i]
		if sessionID != "" && a.SessionID != "" && a.SessionID != sessionID {
			continue
		}
		out = append(out, a)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Count returns how many logged actions belong to the session.
func (l *ActionLog) Count(sessionID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, a := range l.actions {
		if a.SessionID == sessionID && a.Decision != permission.DecisionEvent {
			n++
		}
	}
	return n
}
