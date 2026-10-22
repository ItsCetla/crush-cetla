package swarm

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/lock"
)

// ErrRunNotFound is returned for a swarm run that doesn't exist.
var ErrRunNotFound = errors.New("swarm run not found")

// StoreDir is where swarm runs live under Ta-Da's control directory.
func StoreDir(controlDir string) string {
	return filepath.Join(controlDir, "swarms")
}

// Store keeps swarm runs as JSON files, one per run. Writes go to a
// temporary file that is renamed into place, so a crash never leaves a
// half-written run, and a file lock serializes them across processes.
type Store struct {
	dir string
}

// NewStore opens (and creates) a store in dir.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create swarm dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir is the directory the runs are stored in.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

// lockPath is the lock an assignment's agent holds while it runs, so
// another process can tell a live run from one Ta-Da quit in the middle
// of.
func (s *Store) lockPath(id, machine string) string {
	return filepath.Join(s.dir, id+"."+machine+".lock")
}

func validRunID(id string) bool {
	if id == "" || len(id) > 40 {
		return false
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func newRunID(now time.Time) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

func (s *Store) locked(ctx context.Context, fn func() error) error {
	release, err := lock.File(ctx, filepath.Join(s.dir, "swarms.lock"))
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// Get returns the run with the given id.
func (s *Store) Get(id string) (control.SwarmRun, error) {
	if !validRunID(id) {
		return control.SwarmRun{}, ErrRunNotFound
	}
	data, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return control.SwarmRun{}, ErrRunNotFound
	}
	if err != nil {
		return control.SwarmRun{}, err
	}
	var r control.SwarmRun
	if err := json.Unmarshal(data, &r); err != nil {
		return control.SwarmRun{}, fmt.Errorf("read swarm run %s: %w", id, err)
	}
	return r, nil
}

func (s *Store) write(r control.SwarmRun) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, r.ID+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path(r.ID)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Save writes a run, replacing any with the same id.
func (s *Store) Save(ctx context.Context, r control.SwarmRun) error {
	if !validRunID(r.ID) {
		return fmt.Errorf("invalid swarm run id %q", r.ID)
	}
	return s.locked(ctx, func() error {
		r.Updated = time.Now()
		return s.write(r)
	})
}

// Update reads a run, applies fn and writes it back, under the store's
// lock. An error from fn leaves the run unchanged.
func (s *Store) Update(ctx context.Context, id string, fn func(*control.SwarmRun) error) (control.SwarmRun, error) {
	var out control.SwarmRun
	err := s.locked(ctx, func() error {
		r, err := s.Get(id)
		if err != nil {
			return err
		}
		if err := fn(&r); err != nil {
			return err
		}
		r.Updated = time.Now()
		if err := s.write(r); err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}

// List returns every run, newest first.
func (s *Store) List() ([]control.SwarmRun, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []control.SwarmRun
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		r, err := s.Get(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b control.SwarmRun) int {
		return cmp.Or(b.Created.Compare(a.Created), cmp.Compare(b.ID, a.ID))
	})
	return out, nil
}

// Find returns the run whose id is id or starts with it, so the CLI can
// take a short prefix.
func (s *Store) Find(id string) (control.SwarmRun, error) {
	if r, err := s.Get(id); err == nil {
		return r, nil
	}
	runs, err := s.List()
	if err != nil {
		return control.SwarmRun{}, err
	}
	var match []control.SwarmRun
	for _, r := range runs {
		if id != "" && strings.HasPrefix(r.ID, id) {
			match = append(match, r)
		}
	}
	switch len(match) {
	case 0:
		return control.SwarmRun{}, ErrRunNotFound
	case 1:
		return match[0], nil
	}
	return control.SwarmRun{}, fmt.Errorf("%q matches %d swarm runs; give more of the id", id, len(match))
}
