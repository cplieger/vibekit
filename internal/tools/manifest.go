package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v2"
)

// store owns tools.json (user intent) and tools-state.json (machine
// state). It is the ONLY writer of both files; every read-modify-write
// runs under mu. Files are re-read on each access so an out-of-band
// hand edit of the manifest is picked up on the next operation.
type store struct {
	manifestPath string
	statePath    string
	mu           sync.Mutex
}

func newStore(configDir string) *store {
	return &store{
		manifestPath: filepath.Join(configDir, "tools.json"),
		statePath:    filepath.Join(configDir, "tools-state.json"),
	}
}

// initFiles backs up a retired v1 manifest (any JSON without
// "version": 2) and seeds an empty v2 manifest in its place. Called
// once at engine start.
func (s *store) initFiles() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManifestLocked()
	if err == nil && m.Version == ManifestVersion {
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Unparseable or old-shape file: preserve it for the user.
		backup := s.manifestPath + ".v1.bak"
		if renameErr := os.Rename(s.manifestPath, backup); renameErr == nil {
			slog.Info("tools: retired v1 manifest backed up", "backup", backup)
		}
	}
	if m != nil && m.Version != 0 && m.Version != ManifestVersion {
		backup := s.manifestPath + ".v" + strconv.Itoa(m.Version) + ".bak"
		if renameErr := os.Rename(s.manifestPath, backup); renameErr == nil {
			slog.Info("tools: old manifest backed up", "backup", backup)
		}
	}
	return s.writeManifestLocked(&Manifest{Version: ManifestVersion, Tools: map[string]Tool{}})
}

// readManifestLocked parses tools.json. A v1 file (sections at the top
// level, no matching version) yields an error so initFiles can back it
// up. Caller holds mu.
func (s *store) readManifestLocked() (*Manifest, error) {
	data, err := os.ReadFile(s.manifestPath)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.manifestPath, err)
	}
	if m.Version != ManifestVersion {
		return &m, fmt.Errorf("manifest version %d (want %d)", m.Version, ManifestVersion)
	}
	if m.Tools == nil {
		m.Tools = map[string]Tool{}
	}
	return &m, nil
}

func (s *store) writeManifestLocked(m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	_, err = atomicfile.WriteFile(context.Background(), s.manifestPath, append(data, '\n'),
		atomicfile.WithMode(0o644), atomicfile.WithMkdirMode(0o755))
	return err
}

// Manifest returns a deep-enough copy of the current manifest (safe for
// concurrent readers; Tool values are copied by value, maps/slices
// inside are not mutated by callers).
func (s *store) Manifest() (*Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManifestLocked()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Manifest{Version: ManifestVersion, Tools: map[string]Tool{}}, nil
		}
		return nil, err
	}
	return m, nil
}

// MutateManifest applies fn to the parsed manifest and persists the
// result atomically, all under the store lock.
func (s *store) MutateManifest(fn func(*Manifest) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManifestLocked()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		m = &Manifest{Version: ManifestVersion, Tools: map[string]Tool{}}
	}
	if err := fn(m); err != nil {
		return err
	}
	return s.writeManifestLocked(m)
}

// State returns the current machine state (missing file = empty state).
func (s *store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readStateLocked()
}

func (s *store) readStateLocked() State {
	st := State{Tools: map[string]ToolStatus{}}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		slog.Warn("tools: state file unreadable, resetting", "error", err)
		return State{Tools: map[string]ToolStatus{}}
	}
	if st.Tools == nil {
		st.Tools = map[string]ToolStatus{}
	}
	return st
}

// MutateState applies fn to the machine state and persists it. State
// write failures are logged, not fatal — state is reconstructible.
func (s *store) MutateState(fn func(*State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.readStateLocked()
	fn(&st)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		slog.Error("tools: marshal state", "error", err)
		return
	}
	if _, err := atomicfile.WriteFile(context.Background(), s.statePath, append(data, '\n'),
		atomicfile.WithMode(0o644), atomicfile.WithMkdirMode(0o755)); err != nil {
		slog.Error("tools: write state", "error", err)
	}
}

// setToolStatus records a status update for one tool.
func (s *store) setToolStatus(name string, fn func(*ToolStatus)) {
	s.MutateState(func(st *State) {
		cur := st.Tools[name]
		fn(&cur)
		cur.UpdatedAt = time.Now().UTC()
		st.Tools[name] = cur
	})
}

// dropToolStatus removes a tool's machine state entirely (uninstall).
func (s *store) dropToolStatus(name string) {
	s.MutateState(func(st *State) {
		delete(st.Tools, name)
	})
}
