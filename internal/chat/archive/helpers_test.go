package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// fakeStore is a minimal StoreAccess for purge tests. Only Dir and
// Lock carry behavior the purge subsystem under test depends on; the
// rest are inert stubs that record the calls a test wants to assert on.
type fakeStore struct {
	dir         string
	mu          sync.Mutex
	locks       map[vibekit.ChatID]*sync.Mutex
	markedDel   []vibekit.ChatID
	clearedTomb []vibekit.ChatID
	// loadResult, when non-nil, makes Load succeed with this chat
	// (default: Load returns an error).
	loadResult *vibekit.Chat
}

func newFakeStore(dir string) *fakeStore {
	return &fakeStore{dir: dir, locks: make(map[vibekit.ChatID]*sync.Mutex)}
}

func (f *fakeStore) Dir() string { return f.dir }

// Lock returns a stable per-chat mutex so the purge code's
// lock/unlock pairing behaves like the real store.
func (f *fakeStore) Lock(chatID vibekit.ChatID) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.locks[chatID]
	if !ok {
		m = &sync.Mutex{}
		f.locks[chatID] = m
	}
	return m
}

func (f *fakeStore) PathFor(chatID vibekit.ChatID) (string, error) {
	return filepath.Join(f.dir, string(chatID)+".json"), nil
}

func (f *fakeStore) Load(vibekit.ChatID) (*vibekit.Chat, error) {
	if f.loadResult != nil {
		return f.loadResult, nil
	}
	return nil, errors.New("fakeStore: Load not implemented")
}

func (f *fakeStore) Header(context.Context, *vibekit.Chat) vibekit.ChatHeader {
	return vibekit.ChatHeader{}
}

func (f *fakeStore) MarkDeleted(chatID vibekit.ChatID) {
	f.mu.Lock()
	f.markedDel = append(f.markedDel, chatID)
	f.mu.Unlock()
}

func (f *fakeStore) ClearTombstone(chatID vibekit.ChatID) {
	f.mu.Lock()
	f.clearedTomb = append(f.clearedTomb, chatID)
	f.mu.Unlock()
}

// purgeRecorder collects chat IDs passed to an onPurge
// callback. Safe for concurrent use (Purge runs callbacks from worker
// goroutines).
type purgeRecorder struct {
	mu     sync.Mutex
	ids    []vibekit.ChatID
	chains map[vibekit.ChatID][]string
}

// recordPurge satisfies WithOnPurge, keeping the session chain the purge
// handed over so tests can assert the chat's sessions were offered for reaping.
func (r *purgeRecorder) recordPurge(id vibekit.ChatID, sessionChain []string) {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	if r.chains == nil {
		r.chains = map[vibekit.ChatID][]string{}
	}
	r.chains[id] = sessionChain
	r.mu.Unlock()
}

// chainFor returns the session chain recorded for a purged chat.
func (r *purgeRecorder) chainFor(id vibekit.ChatID) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chains[id]
}

func (r *purgeRecorder) sorted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return idsToSortedStrings(r.ids)
}

func idsToSortedStrings(ids []vibekit.ChatID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	slices.Sort(out)
	return out
}

// newPurgeTestService builds a Service backed by a fakeStore with a
// fresh temp store dir.
func newPurgeTestService(t *testing.T, opts ...Option) (*Service, *fakeStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := newFakeStore(dir)
	// Chats no longer move, so the purge scans the MAIN chat directory. The
	// third return is that directory; tests seed into it directly.
	return New(store, opts...), store, dir
}

// writeAgedChat writes a chat file whose MTIME is `age` in the past and
// returns its path. age=0 means "now".
//
// It writes no UpdatedAt, so purgeReferenceTime falls through to the mtime —
// which is what makes these age assertions readable. The UpdatedAt path has its
// own test.
func writeAgedChat(t *testing.T, dir, id string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, id+".json")
	if err := os.WriteFile(p, []byte(`{"id":"`+id+`"}`), 0o600); err != nil {
		t.Fatalf("write aged chat %s: %v", id, err)
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
