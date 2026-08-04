package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
)

// --- Unexported Store methods ---

func (s *Store) header(_ context.Context, c *api.Chat) api.ChatHeader {
	return c.Header()
}

// lock returns the per-chat mutex for chatID, creating it lazily. Entries
// are never removed from the map: removing an entry races with any caller
// that already fetched the *sync.Mutex pointer, which would let two
// goroutines hold distinct mutexes for the same id and skip serialization.
// The map is bounded by the number of distinct chat ids ever accessed in
// the process lifetime (negligible memory). Uses sync.Map for lock-free
// reads on the hot path (existing chats).
func (s *Store) lock(chatID api.ChatID) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(chatID, &sync.Mutex{})
	//nolint:errcheck // LoadOrStore guarantees v is the stored *sync.Mutex.
	return v.(*sync.Mutex)
}

// --- archive.StoreAccess interface methods ---

// Lock returns the per-chat mutex for the archive package.
func (s *Store) Lock(chatID api.ChatID) *sync.Mutex { return s.lock(chatID) }

// Dir returns the store's base directory.
func (s *Store) Dir() string { return s.dir }

// Load reads a chat from the active directory (exported for archive).
func (s *Store) Load(chatID api.ChatID) (*api.Chat, error) { return s.load(chatID) }

// markDeleted records that chatID was just deleted. Mutate calls for
// the same id within tombstoneTTL will refuse to auto-create.
func (s *Store) markDeleted(chatID api.ChatID) {
	now := time.Now()
	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	s.tombstone[chatID] = now
	cutoff := now.Add(-tombstoneTTL)
	for id, t := range s.tombstone {
		if t.Before(cutoff) {
			delete(s.tombstone, id)
		}
	}
}

// isTombstoned reports whether chatID was deleted within tombstoneTTL.
func (s *Store) isTombstoned(chatID api.ChatID) bool {
	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	t, ok := s.tombstone[chatID]
	if !ok {
		return false
	}
	if time.Since(t) > tombstoneTTL {
		delete(s.tombstone, chatID)
		return false
	}
	return true
}

func (s *Store) pathFor(chatID api.ChatID) (string, error) {
	if !chatIDPattern(chatID) {
		return "", errInvalidChatID(chatID)
	}
	return filepath.Join(s.dir, string(chatID)+chatFileSuffix), nil
}

// load reads a chat file into memory. Returns nil, os.ErrNotExist if the
// file does not exist.
func (s *Store) load(chatID api.ChatID) (*api.Chat, error) {
	path, err := s.pathFor(chatID)
	if err != nil {
		return nil, err
	}
	return readChatFile(path, "chat "+string(chatID))
}

// save atomically writes a chat file. The caller holds the per-chat
// mutex, so atomicfile's own locking is unnecessary — WriteFile is the
// bare atomic temp+fsync+rename primitive. WithMkdirMode mirrors the old
// SaveBytes behavior of auto-creating the parent dir (0o700: chat files
// may carry secrets).
func (s *Store) save(chat *api.Chat) error {
	path, err := s.pathFor(api.ChatID(chat.ID))
	if err != nil {
		return err
	}
	chat.UpdatedAt = time.Now().UnixMilli()
	data, err := json.MarshalIndent(chat, "", "  ")
	if err != nil {
		return err
	}
	// WithMaxBytes mirrors the readCappedFile bound: never persist a chat
	// file the store's own read path would refuse to load.
	_, err = atomicfile.WriteFile(context.Background(), path, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(maxChatFileBytes))
	return err
}
