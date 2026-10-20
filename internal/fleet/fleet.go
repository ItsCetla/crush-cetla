// Package fleet hands work to the computers the user listed as machines.
// Each machine runs a fixed number of agents at once (its slots). A slot
// is a file lock under the Ta-Da data directory, so every Ta-Da process on
// this computer shares the same count, and a crashed process frees its
// slot automatically.
package fleet

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/lock"
)

// Auto picks the best free machine. Main means "don't use a machine".
const (
	Auto = "auto"
	Main = "main"
)

// ErrNoneFree is returned when no machine has a free slot.
var ErrNoneFree = errors.New("no machine has a free slot")

// Machine is a configured machine plus the model its agents use.
type Machine struct {
	Name string
	config.MachineConfig
	// Model is the model agents on the machine run. It is empty when the
	// machine was offline at startup and lists no model.
	Model string
}

// Usable reports whether agents can run on the machine.
func (m Machine) Usable() bool {
	return !m.Disabled && m.URL != "" && m.Model != ""
}

// Machines returns the configured machines in name order.
func Machines(cfg *config.Config) []Machine {
	if cfg == nil {
		return nil
	}
	out := make([]Machine, 0, len(cfg.Machines))
	for _, name := range cfg.Machines.Names() {
		out = append(out, Machine{
			Name:          name,
			MachineConfig: cfg.Machines[name],
			Model:         cfg.MachineModel(name),
		})
	}
	return out
}

// Lookup returns the named machine.
func Lookup(cfg *config.Config, name string) (Machine, bool) {
	for _, m := range Machines(cfg) {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return Machine{}, false
}

// Pool tracks slot leases in a directory shared by every Ta-Da process.
type Pool struct {
	dir string
}

// NewPool returns a pool that keeps its locks in dir.
func NewPool(dir string) *Pool {
	return &Pool{dir: dir}
}

var (
	defaultPool     *Pool
	defaultPoolOnce sync.Once
)

// Default returns the pool in the Ta-Da data directory.
func Default() *Pool {
	defaultPoolOnce.Do(func() {
		defaultPool = NewPool(filepath.Join(filepath.Dir(config.GlobalConfigData()), "fleet"))
	})
	return defaultPool
}

// Lease holds one slot on a machine until Release is called.
type Lease struct {
	Machine Machine
	Slot    int

	once    sync.Once
	release func()
}

// Release frees the slot. It is safe to call more than once.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.release != nil {
			l.release()
		}
	})
}

func (p *Pool) slotPath(machine string, slot int) string {
	return filepath.Join(p.dir, machine, fmt.Sprintf("slot-%d.lock", slot))
}

// TryAcquire takes a free slot on m, or returns ErrNoneFree.
func (p *Pool) TryAcquire(m Machine) (*Lease, error) {
	if err := os.MkdirAll(filepath.Join(p.dir, m.Name), 0o700); err != nil {
		return nil, fmt.Errorf("create fleet directory: %w", err)
	}
	for slot := range m.SlotCount() {
		release, err := lock.TryFile(p.slotPath(m.Name, slot))
		if errors.Is(err, lock.ErrContended) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &Lease{Machine: m, Slot: slot, release: release}, nil
	}
	return nil, fmt.Errorf("%s: %w", m.Name, ErrNoneFree)
}

// Busy returns how many of m's slots are taken right now.
func (p *Pool) Busy(m Machine) int {
	busy := 0
	for slot := range m.SlotCount() {
		path := p.slotPath(m.Name, slot)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		release, err := lock.TryFile(path)
		if err != nil {
			busy++
			continue
		}
		release()
	}
	return busy
}

// Free returns how many of m's slots are open right now.
func (p *Pool) Free(m Machine) int {
	return max(m.SlotCount()-p.Busy(m), 0)
}

// Acquire leases a slot. An empty name or "auto" picks the reachable
// machine with the most free slots; any other name must be a machine.
// It never waits: when nothing is free it returns ErrNoneFree.
func (p *Pool) Acquire(ctx context.Context, cfg *config.Config, name string) (*Lease, error) {
	if name != "" && !strings.EqualFold(name, Auto) {
		m, ok := Lookup(cfg, name)
		if !ok {
			return nil, fmt.Errorf("no machine named %q", name)
		}
		if !m.Usable() {
			return nil, fmt.Errorf("machine %q is disabled or has no model", m.Name)
		}
		if err := Probe(ctx, m); err != nil {
			return nil, fmt.Errorf("machine %q is not reachable: %w", m.Name, err)
		}
		return p.TryAcquire(m)
	}

	type candidate struct {
		m    Machine
		free int
	}
	var candidates []candidate
	for _, m := range Machines(cfg) {
		if !m.Usable() {
			continue
		}
		if free := p.Free(m); free > 0 {
			candidates = append(candidates, candidate{m, free})
		}
	}
	// Most free slots first, then most slots overall, then by name.
	slices.SortStableFunc(candidates, func(a, b candidate) int {
		return cmp.Or(
			cmp.Compare(b.free, a.free),
			cmp.Compare(b.m.SlotCount(), a.m.SlotCount()),
			strings.Compare(a.m.Name, b.m.Name),
		)
	})
	for _, c := range candidates {
		if Probe(ctx, c.m) != nil {
			continue
		}
		if lease, err := p.TryAcquire(c.m); err == nil {
			return lease, nil
		}
	}
	return nil, ErrNoneFree
}
