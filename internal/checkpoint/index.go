// Cross-chat file index.
//
// Tracks "last observed afterSHA for path P by chat C" globally across
// every chat in the vibekit process. Used by the conflict detector:
// when chat A is about to snapshot path P with a before_sha X, we look
// up the most recent (chat, afterSHA) for P. If the recorded sha is
// from a different chat and doesn't equal X, another chat edited P
// out of band — we emit a conflict_detected event and surface it to
// the UI.
//
// Rebuilt at startup by scanning every chat's event log. Kept
// in-memory thereafter, updated live on every Snapshot. No on-disk
// persistence: it's a derived cache, so we accept "rebuild on boot"
// as the simplicity tax. A fully crashed vibekit restarting on a
// cold disk replays a few hundred events across every chat in
// ~milliseconds.
//
// The index is NOT per-file on disk. It's a single map[path] →
// last observation, keyed by the absolute workspace-relative path
// that both chats would agree on.

package checkpoint

import (
	"sync"
)

// crossChatIndex maps workspace-relative path → most recent
// observation of that path across every chat. Observations are
// ordered by event timestamp (within a chat, TS is monotonic;
// across chats, we rely on wall-clock ordering which is good
// enough for a drift detector — the false-positive failure mode
// is a spurious conflict event, not corruption).
type crossChatIndex struct {
	entries map[string]crossChatObs
	byChat  map[string]map[string]struct{} // chatID → paths owned, set for O(1) add/remove
	mu      sync.RWMutex
}

// crossChatObs records one chat's most recent observation of a
// path. ExpectedSHA is the SHA the disk content would have if no
// one else has edited it since — i.e. the before_sha of this
// chat's most recent snapshot event for the path, because we
// don't record after_sha at write time (see Manager.Snapshot).
//
// Rationale for using beforeSHA: in the current design, if chat
// A snapshotted the file at SHA X and then wrote new content Y,
// we DON'T record Y. But when chat A snapshots again later, it
// reads disk and records the current beforeSHA — which SHOULD be
// Y if no one else touched it. That becomes our expected value.
// The next snapshot from chat A sees the current disk sha; if
// it differs from ExpectedSHA, drift occurred.
type crossChatObs struct {
	chatID      string
	expectedSHA string
	ts          int64
}

// newCrossChatIndex builds an empty index. Hub wires one per
// process; every Manager shares the same pointer.
func newCrossChatIndex() *crossChatIndex {
	return &crossChatIndex{
		entries: map[string]crossChatObs{},
		byChat:  map[string]map[string]struct{}{},
	}
}

// rebuild removed: the eager cross-chat scan was replaced by
// per-Manager lazy population via Manager.ensureLoaded, which
// applies each chat's snapshot events at first access. Chat IDs
// are validated by the chat store before Manager construction,
// so the directory-traversal defense previously implemented here
// is no longer reachable.

// apply folds one event into the index. Called from Manager.Snapshot
// and Manager.ensureLoaded (live and replay updates). Uses write-lock
// semantics.
func (idx *crossChatIndex) apply(chatID string, ev *event) {
	// Only snapshot events update the index. Turn starts,
	// restores, and conflict records are bookkeeping.
	if ev.Kind != kindSnapshot || ev.Path == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx._applyLocked(chatID, ev)
}

// _applyLocked does the work with the caller holding idx.mu. Split
// out so rebuild can batch-apply without locking on every entry.
//
// Drift-detection model: we track what chat X last LEFT on disk
// (afterSHA), so a subsequent chat Y's beforeSHA can be compared
// against it. If Y's beforeSHA equals X's afterSHA, no drift —
// Y is reading exactly what X wrote. If they differ, something
// else (another chat, a manual edit, a tool call that didn't go
// through our bridge_fs) mutated the file in between.
//
// Ordering: most-recent-by-ts wins. When two chats record exactly
// the same ts (rare; events are millisecond-granular and cross-
// chat ties only happen at a ms boundary), the incumbent wins —
// i.e. the older observation keeps the slot, the newcomer drops.
// Same-chat updates always overwrite the incumbent regardless of
// ts so a chat's own latest observation is authoritative for its
// own path.
func (idx *crossChatIndex) _applyLocked(chatID string, ev *event) {
	existing, ok := idx.entries[ev.Path]
	if ok && ev.TS <= existing.ts && existing.chatID != chatID {
		return
	}
	// Only record when we have an actual afterSHA — the SHA of
	// what this chat wrote to disk. beforeSHA alone is useless
	// for drift detection because it's "what someone else left",
	// not "what we're about to leave".
	if ev.AfterSHA == "" {
		return
	}

	// Maintain byChat reverse index: remove path from previous owner.
	if ok && existing.chatID != chatID {
		idx.removeByChatEntry(existing.chatID, ev.Path)
	}

	// Add path to new owner's set (only if not already owned by this chat).
	if !ok || existing.chatID != chatID {
		if idx.byChat[chatID] == nil {
			idx.byChat[chatID] = map[string]struct{}{}
		}
		idx.byChat[chatID][ev.Path] = struct{}{}
	}

	idx.entries[ev.Path] = crossChatObs{
		chatID:      chatID,
		expectedSHA: ev.AfterSHA,
		ts:          ev.TS,
	}
}

// check returns the other-chat observation if a drift is detected.
// Drift means: some OTHER chat previously recorded a specific
// afterSHA for this path, and the current disk SHA (what chat
// chatID is about to snapshot as its beforeSHA) doesn't match.
// Returns the observation + true when we should emit a
// conflict_detected event, zero-value + false otherwise.
//
// Same-chat observations never produce a conflict — they're just
// our own history. A drift in the current chat's own history
// means the user edited a file outside the chat; that's user
// intent, not a conflict.
func (idx *crossChatIndex) check(chatID, path, currentSHA string) (crossChatObs, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	existing, ok := idx.entries[path]
	if !ok {
		return crossChatObs{}, false
	}
	if existing.chatID == chatID {
		return crossChatObs{}, false
	}
	if existing.expectedSHA == "" {
		return crossChatObs{}, false
	}
	if existing.expectedSHA == currentSHA {
		return crossChatObs{}, false
	}
	return existing, true
}

// forgetChat evicts every entry owned by chatID. Called from
// Store.Cleanup so a deleted chat's entries don't linger and
// generate false-positive conflicts if a new chat later re-uses
// similar paths.
func (idx *crossChatIndex) forgetChat(chatID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for path := range idx.byChat[chatID] {
		if obs, ok := idx.entries[path]; ok && obs.chatID == chatID {
			delete(idx.entries, path)
		}
	}
	delete(idx.byChat, chatID)
}

// removeByChatEntry removes a single path from a chat's byChat set.
// Caller must hold idx.mu.
func (idx *crossChatIndex) removeByChatEntry(chatID, path string) {
	if s := idx.byChat[chatID]; s != nil {
		delete(s, path)
		if len(s) == 0 {
			delete(idx.byChat, chatID)
		}
	}
}
