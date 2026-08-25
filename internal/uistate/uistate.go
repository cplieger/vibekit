// Package uistate persists the workspace's UI arrangement so every device shows
// the same one.
//
// # Why this exists at all
//
// It used to live entirely in the browser's localStorage, on the reasoning that
// an arrangement is a per-viewer preference. That reasoning was wrong in the way
// the sibling app already learned: web-terminal-ui shipped a `wt-tab-order`
// localStorage key on exactly that argument, the user reported the arrangement
// not travelling between devices, and the fix was to DELETE the local copy
// rather than sync it both ways. Its steering doc carries the standing rule —
// "Do not reintroduce a local arrangement as an offline fallback: two sources of
// truth for one ordering is how the original bug got its per-load reshuffle."
//
// So this store is the single source of truth, and the client keeps no
// authoritative copy. One field is deliberately NOT here — see State.
//
// # Shape
//
// One 0600 JSON file, rewritten atomically, same as internal/schedule and
// internal/mcp: the whole document is a few kilobytes, so a full rewrite per
// mutation is simpler and safer than incremental edits.
//
// # Concurrency
//
// A monotonic Revision, checked on write. This is the one place vibekit needs an
// optimistic-concurrency token, and it is worth saying why the sibling app does
// not: web-terminal-engine's tab order is a permutation of LIVE, server-minted
// sessions, so requiring the body to name that set exactly is itself the
// concurrency control and it rejects revisions outright. Here the document is an
// arbitrary blob with no live set behind it, so nothing about its content can
// detect a stale writer, and a blind last-write-wins would let a phone that
// loaded an hour ago erase every tab opened since.
package uistate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/cplieger/atomicfile/v3"
)

// FileName is the store's file, beside the chats directory in the config dir.
const FileName = "ui-state.json"

// MaxBytes caps a decoded document. The arrangement is small and the fields are
// bounded below; this is the outer wall against a client that sends a megabyte
// of dismissed-banner keys.
const MaxBytes = 256 * 1024

// ErrStale means the writer's Revision is not the current one: another device
// changed the arrangement in between. The caller re-reads and re-applies.
var ErrStale = errors.New("ui state revision is stale")

// State is the whole synced arrangement.
//
// Every field here is a property of the WORKSPACE, so it travels. Two things are
// deliberately absent because they are properties of the VIEWER:
//
//   - `active_view` — which tab this screen is looking at. A phone must not move
//     the desktop's active tab.
//   - `shell_open` — whether the terminal panel is showing. The shell's CONTENT
//     already travels without help: it is one global PTY on the server
//     (`/api/shell/ws`) that survives reconnects and replays its screen and
//     scrollback, so a second device sees the same terminal whether or not the
//     panel happened to be open when it arrived. The panel can start closed.
//   - `shell_h` — how tall that panel is dragged. A length is the one thing here
//     whose right value genuinely depends on the screen in front of you: 700px
//     is two thirds of a laptop and the whole of a phone. It is per-device, and
//     losing it is not a loss worth engineering against.
//
// The client keeps both in its own localStorage and this store never sees them.
//
// Bounds are enforced in Sanitize rather than trusted from the wire, because
// this document is written by a browser and read by the boot path.
type State struct {
	// TurnFolds is per-chat, per-turn fold overrides: chat id -> the turn's
	// opening message id -> open. Absent means "follow the automatic rule",
	// which is why the value is a bool rather than a set of open ids — a turn
	// the reader deliberately FOLDED has to outrank the two-newest rule too.
	TurnFolds map[string]map[string]bool `json:"turn_folds,omitempty"`
	// FBPath is the file browser's directory.
	FBPath string `json:"fb_path,omitempty"`
	// Theme is "dark", "light" or "system". Empty means no choice recorded.
	//
	// "system" is a real stored CHOICE, not the absence of one: it means the
	// user asked to follow the OS, and dropping it is what made Auto
	// unreachable after one toggle click.
	Theme string `json:"theme,omitempty"`
	// TabOrder is every open tab, in display order. Singleton ids
	// (__settings__, __git__, __docs__, __history__, __files__), editor tabs
	// (editor:<path>), run tabs (run:<id>) and chat ids all travel here — the
	// user asked for the whole strip, not only its chats.
	TabOrder []string `json:"tab_order,omitempty"`
	// PinnedTabs are the tabs sorted ahead of every unpinned one.
	PinnedTabs []string `json:"pinned_tabs,omitempty"`
	// EditorFiles are the paths open in editor tabs.
	EditorFiles []string `json:"editor_files,omitempty"`
	// DismissedBanners are the banner keys the user has dismissed.
	DismissedBanners []string `json:"dismissed_banners,omitempty"`
}

// Document is what the wire carries: the state plus the revision a writer must
// echo back.
type Document struct {
	State
	// Revision increments on every accepted write. 0 is a store nobody has
	// written yet, and a writer may send 0 to mean "I am the first" — which the
	// revision check then rejects if it is not true.
	Revision uint64 `json:"revision"`
}

// Store owns the file. Every read returns a deep COPY: handing out the stored
// maps and slices would let a caller reorder the store's own state through the
// value it was given.
type Store struct {
	path     string
	state    State
	mu       sync.Mutex
	revision uint64
}

// NewStore opens (or starts) the store at <dir>/ui-state.json.
//
// A malformed file WARNS AND STARTS EMPTY rather than failing, which is the
// opposite of internal/schedule's choice and deliberately so: a schedule is work
// the user asked to happen and losing it silently is a real loss, while a tab
// arrangement is re-derivable by opening the tabs again. Refusing to start would
// take the whole app down over cosmetic state, and invariant 6 says a broken
// state must be able to heal itself.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName)}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return s, fmt.Errorf("parse %s (starting empty): %w", s.path, err)
	}
	s.state = Sanitize(&doc.State)
	s.revision = doc.Revision
	return s, nil
}

// Get returns the current document.
func (s *Store) Get() Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Document{State: cloneState(&s.state), Revision: s.revision}
}

// Put replaces the state when rev matches the current revision, and returns the
// document that resulted.
//
// The revision is checked BEFORE the write and the new one is returned, so a
// caller never has to re-read to learn what to send next. A mismatch is
// ErrStale and nothing is written.
func (s *Store) Put(ctx context.Context, next *State, rev uint64) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rev != s.revision {
		return Document{State: cloneState(&s.state), Revision: s.revision}, ErrStale
	}
	s.state = Sanitize(next)
	s.revision++
	if err := s.persistLocked(ctx); err != nil {
		// Roll the revision back: the caller is about to be told this failed,
		// and leaving it bumped would make every other device's next write stale
		// for a change that never landed.
		s.revision--
		return Document{}, err
	}
	return Document{State: cloneState(&s.state), Revision: s.revision}, nil
}

func (s *Store) persistLocked(ctx context.Context) error {
	data, err := json.MarshalIndent(Document{State: s.state, Revision: s.revision}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ui state: %w", err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700),
		atomicfile.WithMaxBytes(MaxBytes)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
