// Per-chat Manager registry plus shared cross-chat index, blob GC
// scheduling, and the GC↔Snapshot coordination lock. Preserves the
// public surface the hub depends on; new additions: the
// ConflictBroadcaster wiring and the GC-scheduling lifecycle.

package checkpoint

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
	checkpointgc "github.com/cplieger/vibekit/internal/checkpoint/gc"
	"golang.org/x/sync/singleflight"
)

// Store is the hub's entry point. One Store per vibekit process;
// internally it hands out one Manager per chatID and keeps the
// shared blob store + cross-chat index alive.
//
// The 5-minute blob age gate (blobGCMinAge) provides the primary safety
// guarantee against removing in-flight blobs: a blob written now cannot
// be reaped for at least 5 minutes, which is far longer than the event
// append takes. No per-sweep lock is needed.
type Store struct {
	createSF  singleflight.Group
	blobs     *blobStore
	index     *crossChatIndex
	onConf    ConflictBroadcaster
	gc        *checkpointgc.Coordinator
	managers  map[string]*Manager
	configDir string
	workDir   string
	mu        sync.Mutex
	warmOnce  sync.Once
}

// Compile-time interface assertion.
var _ api.CheckpointService = (*Store)(nil)

// NewStore builds a Store rooted at configDir with the given work
// tree. All chats share the same work tree, the same blob store,
// and the same cross-chat index. `onConf` is called every time a
// Snapshot detects drift; pass nil to disable broadcasting (tests).
// The cross-chat index starts empty and is populated lazily as
// Managers are accessed — each Manager's ensureLoaded applies its
// events to the shared index after replay, halving startup I/O
// compared to the prior eager-rebuild approach.
func NewStore(configDir, workDir string, onConf ConflictBroadcaster) *Store {
	s := &Store{
		configDir: configDir,
		workDir:   workDir,
		blobs:     newBlobStore(configDir),
		index:     newCrossChatIndex(),
		onConf:    onConf,
		managers:  make(map[string]*Manager),
	}
	s.gc = checkpointgc.NewCoordinator(configDir, blobsRoot(configDir), chatsRoot(configDir), FileEvents, blobGCInterval, nil, s.cachedBlobRefs)
	return s
}

// StartBackgroundTasks kicks off the periodic blob GC. Runs one
// immediate sweep, then every blobGCInterval. Idempotent — a
// second call while a gcLoop is already running is a no-op, so
// defensive double-calls from lifecycle refactors don't fork a
// second ticker and double the scheduled work. Split out from
// NewStore so unit tests can construct a Store without
// accidentally starting a goroutine. The context is used for
// cancellation of in-flight GC I/O on shutdown.
func (s *Store) StartBackgroundTasks(ctx context.Context) {
	s.gc.Start(ctx)
}

// Stop halts the background GC goroutine and waits for it to
// finish. Called from Hub.Shutdown. Safe to call even if
// StartBackgroundTasks was never invoked.
func (s *Store) Stop() {
	s.gc.Stop()
}

// AdvanceTurn delegates to Manager.AdvanceTurn (pass-through — logic
// lives in Manager). Errors are logged but not returned because the
// prompt path shouldn't stall on a checkpoint failure — the checkpoint
// exists to serve the user, not gate them.
func (s *Store) AdvanceTurn(ctx context.Context, chatID api.ChatID, messageCount int) {
	if err := s.get(string(chatID)).AdvanceTurn(ctx, messageCount); err != nil {
		slog.Warn("checkpoint: AdvanceTurn failed",
			"chat_id", chatID, "error", err)
	}
}

// Snapshot captures the pre-write content of relPath (pass-through —
// logic lives in Manager). The gcLock.RLock coordination lives inside
// Manager.Snapshot itself so this wrapper is a thin pass-through.
func (s *Store) Snapshot(ctx context.Context, chatID api.ChatID, relPath string, newContent []byte, messageCount int) (Tag, error) {
	return s.get(string(chatID)).Snapshot(ctx, relPath, newContent, messageCount)
}

// RestorePreview returns paths a Restore would mutate (pass-through —
// logic lives in Manager).
func (s *Store) RestorePreview(ctx context.Context, chatID api.ChatID, tag Tag) ([]string, error) {
	return s.get(string(chatID)).RestorePreview(ctx, tag)
}

// Restore rolls the workspace back to `tag` (pass-through — logic
// lives in Manager).
func (s *Store) Restore(ctx context.Context, chatID api.ChatID, tag Tag) (int, error) {
	return s.get(string(chatID)).Restore(ctx, tag)
}

// OldestTag returns the earliest available tag for chatID, or ""
// (pass-through — logic lives in Manager).
func (s *Store) OldestTag(ctx context.Context, chatID api.ChatID) Tag {
	return Tag(s.get(string(chatID)).OldestTag(ctx))
}

// Diff returns per-file changes between two tags (pass-through —
// logic lives in Manager).
func (s *Store) Diff(ctx context.Context, chatID api.ChatID, from, to Tag) ([]FileChange, error) {
	return s.get(string(chatID)).Diff(ctx, from, to)
}

// Conflicts returns all conflict_detected events for a chat
// (pass-through — logic lives in Manager).
func (s *Store) Conflicts(ctx context.Context, chatID api.ChatID) ([]ConflictPayload, error) {
	return s.get(string(chatID)).Conflicts(ctx)
}

// ReadBlob returns blob content for a chat-scoped SHA (pass-through —
// logic lives in Manager).
func (s *Store) ReadBlob(ctx context.Context, chatID api.ChatID, sha string) ([]byte, error) {
	return s.get(string(chatID)).ReadBlob(ctx, sha)
}

// OwnerOf returns the chat whose agent most recently wrote relPath —
// the owner of the path's checkpoint lineage — or ok=false when no
// chat tracks it. The editor-save capture uses this to route a manual
// save into the one timeline whose restores and undos consult the
// path. The first call warms the cross-chat index from disk: managers
// load lazily, so a cold process would otherwise report "no owner"
// for everything until chats happened to be accessed.
func (s *Store) OwnerOf(ctx context.Context, relPath string) (api.ChatID, bool) {
	s.warmOnce.Do(func() { s.warmIndex(ctx) })
	id, ok := s.index.ownerOf(relPath)
	return api.ChatID(id), ok
}

// warmIndex loads every chat manager found on disk so each event log
// populates the shared cross-chat index. Per-chat failures are logged
// and skipped (a corrupt log costs that one chat's ownership data,
// nothing else). Replaying every chat is milliseconds of work (see
// the crossChatIndex package doc); runs once per process.
func (s *Store) warmIndex(ctx context.Context) {
	entries, err := os.ReadDir(chatsRoot(s.configDir))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("checkpoint: index warm scan failed", "error", err)
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if lerr := s.get(e.Name()).load(ctx); lerr != nil {
			slog.Warn("checkpoint: index warm load failed",
				"chat_id", e.Name(), "error", lerr)
		}
	}
}

// Cleanup removes chatID's event log + parent dir and evicts the
// manager from cache. Index entries owned by the chat are forgotten
// inside Manager.Cleanup. Blobs survive — the GC pass reaps orphans.
//
// Serializes with runGCOnce via gcLock: a chat delete walks the
// same event logs runBlobGC collects from, so wiping a chat mid-
// sweep could drop its blob references from the referenced-set
// and let the sweep reap blobs that were live until microseconds
// ago. Both Cleanup and Snapshot take gcLock.RLock so neither can
// interleave with the GC's exclusive Lock() sweep — Cleanup mutates
// the referenced-blob set from the GC's perspective just as
// Snapshot does, only in the opposite direction.
func (s *Store) Cleanup(ctx context.Context, chatID api.ChatID) {
	id := string(chatID)
	s.mu.Lock()
	m, ok := s.managers[id]
	if ok {
		delete(s.managers, id)
	}
	s.mu.Unlock()
	if ok {
		m.Cleanup(ctx)
		return
	}
	wipe(s.configDir, id)
	s.index.forgetChat(id)
}

// get returns the Manager for chatID, creating one lazily via
// singleflight so concurrent first-access calls for the same chatID
// coalesce, and callers for different chatIDs don't block each other.
func (s *Store) get(rawID string) *Manager {
	// Fast path: map hit under short lock.
	s.mu.Lock()
	if m, ok := s.managers[rawID]; ok {
		s.mu.Unlock()
		return m
	}
	s.mu.Unlock()

	// Slow path: create via singleflight keyed by chatID.
	v, _, _ := s.createSF.Do(rawID, func() (any, error) {
		// Double-check under lock.
		s.mu.Lock()
		if m, ok := s.managers[rawID]; ok {
			s.mu.Unlock()
			return m, nil
		}
		s.mu.Unlock()

		log := newEventLog(s.configDir, rawID)
		deps := &managerDeps{
			blobs:  s.blobs,
			index:  s.index,
			onConf: s.onConf,
		}
		m := newManager(rawID, s.workDir, log, deps)

		s.mu.Lock()
		s.managers[rawID] = m
		s.mu.Unlock()
		return m, nil
	})
	mgr, _ := v.(*Manager)
	return mgr
}

// cachedBlobRefs returns the cached managers as gc.BlobRefer
// interface values for the GC coordinator.
func (s *Store) cachedBlobRefs() map[string]checkpointgc.BlobRefer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]checkpointgc.BlobRefer, len(s.managers))
	for k, v := range s.managers {
		out[k] = v
	}
	return out
}

// wipe removes a chat's event log without going through a Manager.
// Used by Store.Cleanup as the fallback when no Manager is cached
// (orphan cleanup / archive purge). Idempotent; package-local
// because the *Store type is the package's public surface.
func wipe(configDir, chatID string) {
	log := newEventLog(configDir, chatID)
	if err := log.Wipe(); err != nil {
		slog.Warn("checkpoint: wipe failed",
			"chat_id", chatID, "error", err)
	}
}
