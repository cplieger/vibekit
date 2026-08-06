package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v2"
)

// FileName is the store's file, beside mcp.json in the config dir.
const FileName = "schedules.json"

// ErrNotFound means no schedule owns the given id.
var ErrNotFound = errors.New("schedule not found")

// Entry is one scheduled workflow. Source is the recipe launch key that
// LaunchRun takes; it is re-validated at launch time rather than trusted here,
// because it looks like a path.
type Entry struct {
	// Anchor is what NextRun measures from: the last fire (or skip), falling
	// back to creation so a new schedule does not immediately fire for every
	// slot since the epoch.
	Anchor time.Time `json:"anchor"`
	// LastRunAt / LastResult are for display only; the run's own record is the
	// durable history (see vibekit-acp.md "Workflow runs on the wire").
	LastRunAt  time.Time `json:"last_run_at,omitzero"`
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Name       string    `json:"name,omitempty"`
	LastResult string    `json:"last_result,omitempty"`
	Spec       Spec      `json:"spec"`
	Enabled    bool      `json:"enabled"`
}

// Store persists schedules in one 0600 JSON file, rewritten atomically. Same
// shape as internal/mcp's store: the whole set is small, so a full rewrite per
// mutation is simpler and safer than incremental edits.
type Store struct {
	entries map[string]Entry
	path    string
	mu      sync.Mutex
}

// NewStore opens (or starts) the store at <dir>/schedules.json. A malformed
// file is a hard error rather than a silent reset: dropping the user's
// schedules without telling them is worse than refusing to start the runner.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName), entries: map[string]Entry{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	var list []Entry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	for i := range list {
		s.entries[list[i].ID] = list[i]
	}
	return s, nil
}

// List returns every schedule, ordered by id so the UI and tests see a stable
// sequence out of the map.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

func (s *Store) sortedLocked() []Entry {
	out := make([]Entry, 0, len(s.entries))
	for id := range s.entries {
		out = append(out, s.entries[id])
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Put inserts or replaces a schedule, validating the spec first so the runner
// never reads one it cannot compute.
func (s *Store) Put(ctx context.Context, e *Entry) error {
	if e.ID == "" {
		return errors.New("schedule id is required")
	}
	if e.Source == "" {
		return errors.New("schedule source is required")
	}
	if err := e.Spec.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.entries[e.ID]; ok {
		// Preserve run history across an edit; the client does not send it.
		e.LastRunAt, e.LastResult = prev.LastRunAt, prev.LastResult
		if e.Anchor.IsZero() {
			e.Anchor = prev.Anchor
		}
	}
	if e.Anchor.IsZero() {
		e.Anchor = time.Now()
	}
	s.entries[e.ID] = *e
	return s.persistLocked(ctx)
}

// Delete removes a schedule. Deleting one that is gone is not an error.
func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return nil
	}
	delete(s.entries, id)
	return s.persistLocked(ctx)
}

// recordFire advances a schedule's anchor after a fire or a skip. The anchor is
// set to the DUE time rather than now, so a schedule cannot drift later by the
// tick's own latency.
func (s *Store) recordFire(ctx context.Context, id string, due time.Time, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return ErrNotFound
	}
	e.Anchor = due
	e.LastRunAt = due
	e.LastResult = result
	s.entries[id] = e
	return s.persistLocked(ctx)
}

// skipTo advances the anchor WITHOUT recording a run, for a slot missed while
// the container was down.
func (s *Store) skipTo(ctx context.Context, id string, to time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return ErrNotFound
	}
	e.Anchor = to
	s.entries[id] = e
	return s.persistLocked(ctx)
}

// RecordOutcome overwrites a schedule's last result AFTER its run has started.
//
// Separate from recordFire because the interesting outcomes arrive late: a
// scheduled run that parks on an unanswered permission is denied minutes after
// it launched, and without this the row would still read "started" while the
// schedule silently failed the same way every night.
//
// Deliberately does NOT touch the anchor: the slot already fired, and moving the
// anchor here would shift the next run by however long the failure took.
func (s *Store) RecordOutcome(ctx context.Context, id, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return ErrNotFound
	}
	e.LastResult = result
	s.entries[id] = e
	return s.persistLocked(ctx)
}

func (s *Store) persistLocked(ctx context.Context) error {
	data, err := json.MarshalIndent(s.sortedLocked(), "", "  ")
	if err != nil {
		return fmt.Errorf("encode schedules: %w", err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
