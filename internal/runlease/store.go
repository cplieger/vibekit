package runlease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v3"
)

// FileName is the store's file, beside schedules.json in the config dir. A SIBLING
// file rather than a record inside schedules.json, which is a bare JSON array with
// nowhere to put a version, and two subsystems behind one parse would mean a
// malformed file disabling both.
const FileName = "runs.json"

// Version is the on-disk format version, and why the file's top-level value is an
// OBJECT rather than the array schedules.json uses.
const Version = 1

// ErrNotFound means no lease owns the given workflow id.
var ErrNotFound = errors.New("run lease not found")

// file is the on-disk shape.
type file struct {
	Leases  []Lease `json:"leases"`
	Version int     `json:"version"`
}

// Store persists run leases in one 0600 JSON file, rewritten atomically: the set is
// bounded by the single-run rule, so a full rewrite per mutation is simpler than
// incremental edits. The ZERO VALUE is usable, an empty path persisting nothing, and
// nothing here reaches back into its caller.
//
// The 0600 is verified by the WRITE rather than by a pass afterwards — atomicfile
// fchmods and fstats the open temp descriptor and fails rather than publishing a
// wider file — so do not add a second EnforceFile on s.path, which checks by NAME.
type Store struct {
	leases map[string]Lease
	path   string
	mu     sync.Mutex
}

// NewMemory returns an in-memory store that persists nothing.
func NewMemory() *Store { return &Store{leases: map[string]Lease{}} }

// NewStore opens (or starts) the store at <dir>/runs.json.
//
// It ALWAYS returns a usable store; the error is diagnostic. The opposite of
// schedule.NewStore, deliberately: a schedule is the user's configuration, while a
// lease is DERIVED state, and refusing to open it would leave no lease registry at
// all, so no run would get a wall clock. An unrecognised version is discarded for
// the same reason a malformed file is — acting on half-understood leases is how a
// live run gets cancelled — and the next mutation rewrites at this build's version.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName), leases: map[string]Lease{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read %s: %w", s.path, err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return s, fmt.Errorf("parse %s: %w", s.path, err)
	}
	if f.Version != Version {
		return s, fmt.Errorf("%s: unsupported version %d (this build writes %d)", s.path, f.Version, Version)
	}
	for i := range f.Leases {
		l := f.Leases[i]
		if l.WorkflowID == "" || !l.Origin.Valid() {
			continue
		}
		// A deadline read from disk describes a process that no longer exists, so
		// parking it keeps the bound on EXECUTING time; the next start re-arms.
		l.Deadline = time.Time{}
		s.leases[l.WorkflowID] = l
	}
	return s, nil
}

// List returns every lease, ordered by workflow id so the file is stable.
func (s *Store) List() []Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

func (s *Store) sortedLocked() []Lease {
	out := make([]Lease, 0, len(s.leases))
	for _, id := range slices.Sorted(maps.Keys(s.leases)) {
		out = append(out, s.leases[id])
	}
	return out
}

// Get returns one lease.
func (s *Store) Get(workflowID string) (Lease, bool) {
	if workflowID == "" {
		return Lease{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[workflowID]
	return l, ok
}

// Put grants a lease, replacing any lease already held for the run. A persist
// failure is reported but the lease is KEPT in memory: the run is on the wire either
// way, and forgetting it would leave it unbounded here as well as absent next time.
func (s *Store) Put(ctx context.Context, l *Lease) error {
	if l.WorkflowID == "" {
		return errors.New("lease workflow id is required")
	}
	if !l.Origin.Valid() {
		return fmt.Errorf("lease origin %q is not one of scheduled/manual/agent", l.Origin)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leases[l.WorkflowID] = *l
	return s.persistLocked(ctx)
}

// Release forgets a run's lease. Releasing one that is gone is not an error: both
// the terminal frame and the cancel path release, and neither knows which was first.
func (s *Store) Release(ctx context.Context, workflowID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.leases[workflowID]; !ok {
		return nil
	}
	delete(s.leases, workflowID)
	return s.persistLocked(ctx)
}

// SetDeadline re-stamps a lease's deadline, or parks it with the zero time, which is
// what makes the bound one on EXECUTING time: every start re-arms, every pause parks.
//
// THE ERROR REPORTS DURABILITY, NOT THE MUTATION: whenever the lease exists the
// in-memory deadline IS the one asked for by the time this returns. A caller treating
// a non-nil error as "the deadline was not set" would arm no timer against a lease
// that reads bounded, and the arm's own idempotence check makes that permanent.
// ErrNotFound is the one error meaning nothing was stored.
func (s *Store) SetDeadline(ctx context.Context, workflowID string, deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[workflowID]
	if !ok {
		return ErrNotFound
	}
	if l.Deadline.Equal(deadline) {
		return nil
	}
	l.Deadline = deadline
	s.leases[workflowID] = l
	return s.persistLocked(ctx)
}

// MarkTabOffered records that the run's tab has been offered, so nothing offers it
// twice. Already-marked is a no-op, not an error: the offer is retried on each step's
// frame, so the repeat is the normal case.
//
// The error reports DURABILITY, not the mutation, exactly as SetDeadline's does;
// ErrNotFound is the one error meaning nothing was stored.
func (s *Store) MarkTabOffered(ctx context.Context, workflowID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[workflowID]
	if !ok {
		return ErrNotFound
	}
	if l.TabOffered {
		return nil
	}
	l.TabOffered = true
	s.leases[workflowID] = l
	return s.persistLocked(ctx)
}

func (s *Store) persistLocked(ctx context.Context) error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(file{Version: Version, Leases: s.sortedLocked()}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run leases: %w", err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
