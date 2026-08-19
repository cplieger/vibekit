package runlease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v3"
)

// FileName is the store's file, beside schedules.json in the config dir.
//
// A SIBLING file rather than a second record type inside schedules.json, which
// is a bare JSON array and so has nowhere to put a version. Two independent
// subsystems behind one parse would also mean a malformed file disabling both,
// and the config dir already holds exactly this pattern: one single-purpose 0600
// file per subsystem, each with its own store.
const FileName = "runs.json"

// Version is the on-disk format version, and the reason the file's top-level
// value is an OBJECT rather than the array schedules.json uses. This is the one
// genuinely irreversible part of the lease work, so it carries a version from
// the first write rather than acquiring one after a shape has to change.
const Version = 1

// ErrNotFound means no lease owns the given workflow id.
var ErrNotFound = errors.New("run lease not found")

// file is the on-disk shape.
type file struct {
	Leases  []Lease `json:"leases"`
	Version int     `json:"version"`
}

// Store persists run leases in one 0600 JSON file, rewritten atomically.
//
// The set is bounded by the single-run rule (one live run per recipe), so a full
// rewrite per mutation is simpler and safer than incremental edits — the same
// reasoning as internal/schedule's and internal/mcp's stores.
//
// THE 0600 IS VERIFIED, NOT MERELY REQUESTED, and it is verified by the WRITE
// rather than by a pass afterwards. A mode argument is normally a request — open(2)
// puts it through umask and an inheritable ACL can store something wider — which
// is why this repo has filemode.EnforceFile for the objects it chmods by name.
// atomicfile.WriteFile does not need it: finalizeTempFile runs
// atomicfile.EnforceMode on the OPEN TEMP DESCRIPTOR (fchmod then fstat, one
// handle) and FAILS the write with ErrModeNotStored rather than publishing a wider
// file, and the rename then publishes that same verified inode. So the mode on disk
// is a fact by the time this returns, and TestStore_WritesA0600File pins it.
//
// Do not add a second EnforceFile on s.path after the write: it would re-verify
// what the temp handle already established, by NAME, which is the weaker of the two
// checks. The one thing genuinely not covered is a mode widened by something else
// BETWEEN two writes, which is out of a write's reach and low-consequence here —
// this file holds workflow names, ids and timestamps, no credentials, unlike
// mcp-secrets.json whose 0600 is its whole protection and which therefore enforces
// on LOAD as well.
//
// The ZERO VALUE is usable: an empty path persists nothing, which is what a test
// wants and what a hub built without the durable store falls back to. Nothing
// here reaches back into its caller, so a caller may hold its own lock across a
// call without a lock-order question.
type Store struct {
	leases map[string]Lease
	path   string
	mu     sync.Mutex
}

// NewMemory returns an in-memory store that persists nothing. For tests, and for
// a hub constructed without a config dir.
func NewMemory() *Store { return &Store{leases: map[string]Lease{}} }

// NewStore opens (or starts) the store at <dir>/runs.json.
//
// It ALWAYS returns a usable store; the error is diagnostic. That is the
// opposite of schedule.NewStore, which treats a malformed file as fatal, and the
// difference is deliberate: a schedule is the user's configuration and dropping
// it silently is worse than refusing to start the runner, while a lease is
// DERIVED state about runs KAS owns. Refusing to open it would leave the caller
// with no lease registry at all, which means no run gets a wall clock — strictly
// worse than starting empty and letting the next launch mint fresh leases.
//
// An unrecognised version is discarded for the same reason a malformed file is:
// a record written by another build may carry semantics this one cannot honour,
// and acting on half-understood leases is how a live run gets cancelled. The
// next mutation rewrites the file at this build's version.
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
		// A deadline read from disk is a fact about a process that no longer
		// exists: nothing was executing across the restart, because KAS
		// reconciles a dead owner's run to paused. Parking every loaded lease is
		// what keeps the bound on EXECUTING time — the run's next start re-arms
		// it with a full budget.
		l.Deadline = time.Time{}
		s.leases[l.WorkflowID] = l
	}
	return s, nil
}

// List returns every lease, ordered by workflow id so the file and the tests see
// a stable sequence out of the map.
func (s *Store) List() []Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

func (s *Store) sortedLocked() []Lease {
	out := make([]Lease, 0, len(s.leases))
	for id := range s.leases {
		out = append(out, s.leases[id])
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].WorkflowID < out[j-1].WorkflowID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
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

// Put grants a lease, replacing any lease already held for the run.
//
// A persist failure is reported but the lease is KEPT in memory: the run is on
// the wire either way, and forgetting it would leave it unbounded in this
// process as well as absent from the next one.
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

// Release forgets a run's lease. Releasing one that is gone is not an error —
// both the terminal frame and the cancel path release, and neither knows which
// arrived first.
func (s *Store) Release(ctx context.Context, workflowID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.leases[workflowID]; !ok {
		return nil
	}
	delete(s.leases, workflowID)
	return s.persistLocked(ctx)
}

// SetDeadline re-stamps a lease's deadline, or parks it with the zero time.
//
// This is the mutation that makes the bound one on EXECUTING time: every start
// re-arms, every pause parks.
//
// THE ERROR REPORTS DURABILITY, NOT THE MUTATION. Whenever the lease exists the
// in-memory deadline IS the one asked for by the time this returns, and only the
// persist can still fail — same posture as Put, and for the same reason: the run
// is executing either way, so the in-process envelope must survive a disk error.
// A caller that treats a non-nil error as "the deadline was not set" would arm no
// timer against a lease that reads bounded, and the arm's own idempotence check
// makes that permanent. ErrNotFound is the one error that means nothing was
// stored, and it is the only one a caller may read that way.
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
