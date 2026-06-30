package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// fakeStore is a minimal StoreAccess for archive tests. Only Dir and
// Lock carry behavior the archive subsystem under test depends on; the
// rest are inert stubs that record the calls a test wants to assert on.
type fakeStore struct {
	dir         string
	mu          sync.Mutex
	locks       map[api.ChatID]*sync.Mutex
	markedDel   []api.ChatID
	clearedTomb []api.ChatID
	// loadResult, when non-nil, makes Load succeed with this chat
	// (default: Load returns an error). bc, when non-nil, is returned
	// by Broadcast (default: nil, i.e. no broadcaster).
	loadResult *api.Chat
	bc         api.Broadcaster
}

func newFakeStore(dir string) *fakeStore {
	return &fakeStore{dir: dir, locks: make(map[api.ChatID]*sync.Mutex)}
}

func (f *fakeStore) Dir() string { return f.dir }

// Lock returns a stable per-chat mutex so the archive code's
// lock/unlock pairing behaves like the real store.
func (f *fakeStore) Lock(chatID api.ChatID) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.locks[chatID]
	if !ok {
		m = &sync.Mutex{}
		f.locks[chatID] = m
	}
	return m
}

func (f *fakeStore) PathFor(chatID api.ChatID) (string, error) {
	return filepath.Join(f.dir, string(chatID)+".json"), nil
}

func (f *fakeStore) Load(api.ChatID) (*api.Chat, error) {
	if f.loadResult != nil {
		return f.loadResult, nil
	}
	return nil, errors.New("fakeStore: Load not implemented")
}

func (f *fakeStore) Header(context.Context, *api.Chat) api.ChatHeader {
	return api.ChatHeader{}
}

func (f *fakeStore) MarkDeleted(chatID api.ChatID) {
	f.mu.Lock()
	f.markedDel = append(f.markedDel, chatID)
	f.mu.Unlock()
}

func (f *fakeStore) ClearTombstone(chatID api.ChatID) {
	f.mu.Lock()
	f.clearedTomb = append(f.clearedTomb, chatID)
	f.mu.Unlock()
}

func (f *fakeStore) Broadcast() api.Broadcaster { return f.bc }

func (f *fakeStore) OldestCheckpoint() func(context.Context, api.ChatID) string { return nil }

func (f *fakeStore) markedDeleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return idsToSortedStrings(f.markedDel)
}

func (f *fakeStore) clearedTombstones() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return idsToSortedStrings(f.clearedTomb)
}

// purgeRecorder collects chat IDs passed to an onPurge / onArchive
// callback. Safe for concurrent use (Purge runs callbacks from worker
// goroutines).
type purgeRecorder struct {
	mu  sync.Mutex
	ids []api.ChatID
}

func (r *purgeRecorder) record(id api.ChatID) {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
}

func (r *purgeRecorder) sorted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return idsToSortedStrings(r.ids)
}

func idsToSortedStrings(ids []api.ChatID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	sort.Strings(out)
	return out
}

// newArchiveTestService builds a Service backed by a fakeStore with a
// fresh temp store dir and an existing (empty) archive subdirectory.
func newArchiveTestService(t *testing.T, opts ...Option) (*Service, *fakeStore, string) {
	t.Helper()
	dir := t.TempDir()
	archiveDir := filepath.Join(dir, Subdir)
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		t.Fatalf("mkdir archive dir: %v", err)
	}
	store := newFakeStore(dir)
	return New(store, opts...), store, archiveDir
}

// writeArchivedChat writes an archived chat file aged `age` in the past
// and returns its path. age=0 means "now".
func writeArchivedChat(t *testing.T, archiveDir, id string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(archiveDir, id+".json")
	if err := os.WriteFile(p, []byte(`{"id":"`+id+`"}`), 0o600); err != nil {
		t.Fatalf("write archived chat %s: %v", id, err)
	}
	if age != 0 {
		mt := time.Now().Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatalf("chtimes %s: %v", id, err)
		}
	}
	return p
}

// exists reports whether a path is present on disk.
func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	t.Fatalf("stat %s: %v", path, err)
	return false
}

// recordingBroadcaster is an api.Broadcaster that records every event it
// receives so a test can assert which SSE events a Service operation
// emitted. Safe for concurrent use.
type recordingBroadcaster struct {
	mu     sync.Mutex
	events []api.ServerEvent
}

func (b *recordingBroadcaster) Broadcast(_ context.Context, evt api.ServerEvent) {
	b.mu.Lock()
	b.events = append(b.events, evt)
	b.mu.Unlock()
}

// hasType reports whether an event of type t was recorded.
func (b *recordingBroadcaster) hasType(t api.EventType) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.events {
		if b.events[i].Type == t {
			return true
		}
	}
	return false
}

// recordedTypes returns the recorded event types in receipt order (for
// failure messages).
func (b *recordingBroadcaster) recordedTypes() []api.EventType {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]api.EventType, len(b.events))
	for i := range b.events {
		out[i] = b.events[i].Type
	}
	return out
}
