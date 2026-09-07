package chat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// --- Unexported Store methods ---

// lock returns the per-chat mutex for chatID, creating it lazily. Entries
// are never removed from the map: removing an entry races with any
// caller that already fetched the *sync.Mutex pointer, letting two
// goroutines hold distinct mutexes for the same id.
func (s *Store) lock(chatID vibekit.ChatID) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(chatID, &sync.Mutex{})
	//nolint:errcheck // LoadOrStore guarantees v is the stored *sync.Mutex.
	return v.(*sync.Mutex)
}

// --- archive.StoreAccess interface methods ---

// Lock returns the per-chat mutex for the archive package.
func (s *Store) Lock(chatID vibekit.ChatID) *sync.Mutex { return s.lock(chatID) }

// Dir returns the store's base directory.
func (s *Store) Dir() string { return s.dir }

// markDeleted records that chatID was just deleted. Mutate calls for
// the same id within tombstoneTTL will refuse to auto-create.
func (s *Store) markDeleted(chatID vibekit.ChatID) {
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
func (s *Store) isTombstoned(chatID vibekit.ChatID) bool {
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

func (s *Store) pathFor(chatID vibekit.ChatID) (string, error) {
	if !chatIDPattern(chatID) {
		return "", errInvalidChatID(chatID)
	}
	return filepath.Join(s.dir, string(chatID)+chatFileSuffix), nil
}

// load reads a chat file into memory. Returns nil, os.ErrNotExist if the
// file does not exist.
func (s *Store) load(chatID vibekit.ChatID) (*vibekit.Chat, error) {
	path, err := s.pathFor(chatID)
	if err != nil {
		return nil, err
	}
	return readChatFile(path, "chat "+string(chatID), s.fileCap)
}

// save stamps the chat's last-activity time and writes it to chatID's
// file. Every mutation except a draft autosave goes through here, since
// every other mutation IS activity.
func (s *Store) save(chatID vibekit.ChatID, chat *vibekit.Chat) error {
	chat.UpdatedAt = time.Now().UnixMilli()
	return s.writeChat(chatID, chat)
}

// writeChat atomically writes chat to chatID's file, leaving UpdatedAt exactly
// as the caller left it — which is what SetDraft needs. The caller holds the
// per-chat mutex, so atomicfile's own locking is unnecessary; the parent dir is
// created 0o700 because chat files may carry secrets.
//
// THE DESTINATION IS THE ARGUMENT, and the object's own id is verified against
// it: a chat whose stored id is not its filename would otherwise overwrite the
// file that id names, under the requested id's lock.
func (s *Store) writeChat(chatID vibekit.ChatID, chat *vibekit.Chat) error {
	path, err := s.pathFor(chatID)
	if err != nil {
		return err
	}
	if chat.ID != string(chatID) {
		slog.Error("chat write: refused to write a chat over another chat's file",
			"chat_id", chatID, "stored_id", chat.ID, "path", path)
		return errChatIDMismatch(chatID, chat.ID)
	}
	// Bounded copy-on-write, never in place: AppendMessage's caller keeps the
	// message it handed over and broadcasts it, and a Message copy shares its
	// ToolCalls backing array, so cutting the stored value in place would edit
	// the live one.
	data, err := json.MarshalIndent(storeChat(chat), "", "  ")
	if err != nil {
		return err
	}
	// WithMaxBytes mirrors readCappedFile's bound: never persist a chat file the
	// store's own read path would refuse to load. int64(0) is atomicfile's
	// documented "no cap", which is the same encoding chatFileCap uses.
	_, err = atomicfile.WriteFile(context.Background(), path, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(int64(s.fileCap)))
	s.logWriteOutcome(chatID, int64(len(data)), err)
	return err
}

// writeHeadroomFraction is how close to the cap a SUCCESSFUL write may land
// before it is reported. A tenth gives an operator the last 10% of a chat's
// budget to act in, and the alarm rides the write it describes rather than a
// poll nothing schedules.
const writeHeadroomFraction = 10

// logWriteOutcome reports what the cap did to this write: a refusal is an
// ERROR naming the size and the cap, because atomicfile pre-checks before
// staging its temp, so the old file survives and the TURN IS DISCARDED — the
// data-loss shape this whole bound exists to stop. A write that landed inside
// the last tenth of the cap warns while there is still room to act.
//
// Both are no-ops under an unlimited cap, where neither can happen.
func (s *Store) logWriteOutcome(chatID vibekit.ChatID, size int64, err error) {
	if s.fileCap.unlimited() {
		return
	}
	capBytes := int64(s.fileCap)
	if err != nil {
		if errors.Is(err, atomicfile.ErrFileTooLarge) {
			slog.Error("chat write: refused over the chat file cap; this turn was NOT persisted",
				"chat_id", chatID, "size_bytes", size, "cap_bytes", capBytes)
		}
		return
	}
	if headroom := capBytes - size; headroom < capBytes/writeHeadroomFraction {
		slog.Warn("chat write: this chat is near the chat file cap",
			"chat_id", chatID, "size_bytes", size, "cap_bytes", capBytes,
			"headroom_bytes", headroom)
	}
}
