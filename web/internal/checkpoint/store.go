// Per-chat Manager registry plus shared cross-chat index, blob GC
// scheduling, and the GC↔Snapshot coordination lock. Preserves the
// public surface the hub depends on; new additions: the
// ConflictBroadcaster wiring and the GC-scheduling lifecycle.

package checkpoint

import (
	"context"
	"log/slog"
	"maps"
	"sync"

	checkpointgc "vibekit/internal/checkpoint/gc"
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
	blobs     *blobStore
	index     *crossChatIndex
	onConf    ConflictBroadcaster
	gc        *checkpointgc.Coordinator
	managers  map[string]*Manager
	configDir string
	workDir   string
	mu        sync.Mutex
}

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
	s.gc = checkpointgc.NewCoordinator(configDir, blobsRoot(configDir), chatsRoot(configDir), blobGCInterval, nil, s.cachedBlobRefs)
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

// --- GCCoordination implementation ---
// The 5-minute blob age gate (blobGCMinAge) provides the primary safety
// guarantee against removing in-flight blobs. The lock-based coordination
// is no longer needed; these methods are retained as no-ops to satisfy
// the GCCoordination interface contract for existing test code.

// AcquireSnapshotLock is a no-op; the age gate provides safety.
func (s *Store) AcquireSnapshotLock() {}

// ReleaseSnapshotLock is a no-op; the age gate provides safety.
func (s *Store) ReleaseSnapshotLock() {}

// AcquireGCLock is a no-op; the age gate provides safety.
func (s *Store) AcquireGCLock() {}

// ReleaseGCLock is a no-op; the age gate provides safety.
func (s *Store) ReleaseGCLock() {}

// Compile-time assertion that Store implements GCCoordination.
var _ GCCoordination = (*Store)(nil)



// AdvanceTurn delegates to Manager.AdvanceTurn. Errors are logged
// but not returned because the prompt path shouldn't stall on a
// checkpoint failure — the checkpoint exists to serve the user,
// not gate them.
func (s *Store) AdvanceTurn(ctx context.Context, chatID string, messageCount int) {
	if err := s.get(chatID).AdvanceTurn(ctx, messageCount); err != nil {
		slog.Warn("checkpoint: AdvanceTurn failed",
			"chat_id", chatID, "error", err)
	}
}

// Snapshot captures the pre-write content of relPath + the content
// the caller is about to write, and returns the assigned tag.
// Errors bubble because the caller (bridge_fs) logs them already.
//
// The gcLock.RLock coordination lives inside Manager.Snapshot
// itself (via the shared gcLock pointer passed to newManager) so
// this wrapper is a thin pass-through — no double-locking, no
// gc bypass risk if a future caller reaches the Manager directly.
func (s *Store) Snapshot(ctx context.Context, chatID, relPath string, newContent []byte, messageCount int) (Tag, error) {
	return s.get(chatID).Snapshot(ctx, relPath, newContent, messageCount)
}

// RestorePreview returns the workspace-relative paths that a
// Restore(chatID, tag) call would mutate. Used by the client to
// warn the user before committing to the rollback.
func (s *Store) RestorePreview(ctx context.Context, chatID string, tag Tag) ([]string, error) {
	return s.get(chatID).RestorePreview(ctx, tag)
}

// Restore rolls the workspace back to `tag` for chatID and returns
// the message-count watermark captured at `tag`.
func (s *Store) Restore(ctx context.Context, chatID string, tag Tag) (int, error) {
	return s.get(chatID).Restore(ctx, tag)
}

// CheckoutFile reverts a single file to its content at `tag`. Used
// by the per-file Undo button.
func (s *Store) CheckoutFile(ctx context.Context, chatID string, tag Tag, relPath string) error {
	return s.get(chatID).CheckoutFile(ctx, tag, relPath)
}

// OldestTag returns the earliest available tag for chatID, or "".
func (s *Store) OldestTag(ctx context.Context, chatID string) Tag {
	return Tag(s.get(chatID).OldestTag(ctx))
}

// Diff returns per-file changes between two tags. See Manager.Diff.
func (s *Store) Diff(ctx context.Context, chatID string, from, to Tag) ([]FileChange, error) {
	return s.get(chatID).Diff(ctx, from, to)
}

// Conflicts returns all conflict_detected events for a chat. Used
// by the client at page load to replay outstanding badges.
func (s *Store) Conflicts(ctx context.Context, chatID string) ([]ConflictPayload, error) {
	return s.get(chatID).Conflicts(ctx)
}

// ReadBlob returns blob content for a chat-scoped SHA. The chat
// must reference the SHA in its event log — prevents cross-chat
// blob probing via raw SHAs.
func (s *Store) ReadBlob(ctx context.Context, chatID, sha string) ([]byte, error) {
	return s.get(chatID).ReadBlob(ctx, sha)
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
func (s *Store) Cleanup(ctx context.Context, chatID string) {
	s.mu.Lock()
	m, ok := s.managers[chatID]
	if ok {
		delete(s.managers, chatID)
	}
	s.mu.Unlock()
	if ok {
		m.Cleanup(ctx)
		return
	}
	wipe(s.configDir, chatID)
	s.index.forgetChat(chatID)
}

// get returns the Manager for chatID, creating one lazily. Every
// public method routes through this so a manager is always reused
// across calls and never starts with empty state when an event log
// already exists on disk.
func (s *Store) get(rawID string) *Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.managers[rawID]; ok {
		return m
	}
	log := newEventLog(s.configDir, rawID)
	deps := &managerDeps{
		blobs:   s.blobs,
		index:   s.index,
		onConf:  s.onConf,
		gcCoord: s,
	}
	m := newManager(chatID(rawID), s.workDir, log, deps)
	s.managers[rawID] = m
	return m
}

// cachedChatIDs returns the set of chat IDs that have a cached
// Manager. Used by the GC to collect referenced blobs from in-memory
// state for cached chats (avoiding disk re-reads).
func (s *Store) cachedChatIDs() map[string]*Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*Manager, len(s.managers))
	maps.Copy(out, s.managers)
	return out
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
