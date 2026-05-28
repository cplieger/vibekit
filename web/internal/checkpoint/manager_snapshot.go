// Snapshot path: AdvanceTurn, Snapshot, OldestTag, Conflicts,
// ReadBlob, tags. These methods share the Manager struct and its
// mutex but don't call the restore path — they have one reason to
// change (the snapshot/query model).

package checkpoint

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// AdvanceTurn records a fresh user turn boundary in the event log.
// Called from cmdPrompt right before the agent starts a new turn so
// the tag allocation gets a clean "N" slot. messageCount is the
// persisted message count at the moment of the turn boundary.
func (m *Manager) AdvanceTurn(ctx context.Context, messageCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.ensureLoaded(ctx); err != nil {
		return err
	}
	nextTurn := m.state.turn + 1
	ev := event{
		Kind:         kindTurnStart,
		Turn:         nextTurn,
		MessageCount: messageCount,
	}
	if err := m.log.Append(ctx, &ev); err != nil {
		return err
	}
	m.state.apply(&ev)
	return nil
}

// Snapshot captures the state of `relPath` just BEFORE an agent
// write lands on it AND the content it's about to write. The
// caller passes `newContent` so we can record the post-write SHA
// without a second disk read — that SHA feeds the cross-chat
// index's drift detector.
//
// The write itself is NOT performed here. The caller writes
// `newContent` to disk after Snapshot returns.
func (m *Manager) Snapshot(ctx context.Context, relPath string, newContent []byte, messageCount int) (Tag, error) {
	if relPath == "" {
		return Tag(""), errors.New("snapshot: empty path")
	}
	// The 5-minute blob age gate (blobGCMinAge) provides the primary
	// safety guarantee: a blob written now cannot be reaped for at
	// least 5 minutes, which is far longer than the event append takes.
	// No per-snapshot lock is needed.
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoaded(ctx); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Read the pre-write content. A read error other than
	// "file doesn't exist" is fatal; a missing file is recorded
	// as an empty beforeSHA so Restore knows to delete on
	// rollback. Pre-read size is capped at contentCap — ingesting
	// a 200 MB pre-write state would let us snapshot a file we
	// can never restore (Get refuses blobs over contentCap), and
	// would OOM vibekit's container on the way in. Over-cap paths
	// record an empty beforeSHA so the snapshot still lands;
	// Restore is a no-op on that file (nothing to roll back to)
	// and the afterSHA
	// still pins the post-write content for forward diffing.
	abs, err := m.absPath(relPath)
	if err != nil {
		return "", err
	}
	beforeSHA, err := m.readBeforeSHALocked(ctx, relPath, abs)
	if err != nil {
		return "", err
	}
	// Store the post-write content too so Diff has both endpoints
	// and the cross-chat index has a real afterSHA (the SHA the
	// file WILL have on disk once the caller's write lands). If
	// newContent is nil (caller hasn't computed it yet or it's a
	// delete), we skip the blob put and leave afterSHA empty.
	var afterSHA string
	if newContent != nil {
		afterSHA, err = m.blobs.Put(ctx, newContent)
		if err != nil {
			return "", fmt.Errorf("blob put (after): %w", err)
		}
	}

	// Allocate the tag FIRST so the conflict event (if any) and
	// the snapshot event that follows both carry the same tag.
	// The two consumers — on-disk replay via Manager.Conflicts()
	// and the live broadcast via onConf — would otherwise diverge:
	// the persisted event got an empty Tag, while the broadcast
	// carried the *previous* snapshot's latestTag. A conflict
	// belongs to the snapshot that detected the drift, not the
	// one before it, and not "no snapshot at all". allocateTag
	// is idempotent — it reads state.turn/toolsInTurn without
	// mutating; state.apply(kindSnapshot) is what advances them.
	tag := m.state.allocateTag()
	turn, tool := parseTag(tag)

	// Cross-chat conflict detection: before we emit our snapshot,
	// check whether the disk content we just observed matches
	// what any OTHER chat last left here. Drift means somebody
	// else edited it between their write and our read.
	if obs, conflict := m.index.check(string(m.chatID), relPath, beforeSHA); conflict {
		confEv := event{
			Kind:        kindConflict,
			Tag:         tag,
			Path:        relPath,
			OtherChat:   obs.chatID,
			ExpectedSHA: obs.expectedSHA,
			BeforeSHA:   beforeSHA,
			TS:          time.Now().UnixMilli(),
		}
		if err := m.log.Append(ctx, &confEv); err != nil {
			slog.Warn("checkpoint: conflict event append failed",
				"chat_id", m.chatID, "path", relPath, "error", err)
		} else {
			m.state.apply(&confEv)
			slog.Info("checkpoint: cross-chat conflict observed",
				"chat_id", m.chatID, "path", relPath,
				"expected_by", obs.chatID, "expected_sha", obs.expectedSHA,
				"actual_sha", beforeSHA)
		}
		// Always broadcast the live conflict to the UI even if
		// persistence failed. The conflict badge is best-effort
		// client-state; losing the on-disk record is the cost
		// of a transient fs error, but silently hiding the
		// drift from the user is the real bug.
		if m.onConf != nil {
			m.onConf(string(m.chatID), &ConflictPayload{
				Path:        relPath,
				OtherChat:   obs.chatID,
				ExpectedSHA: obs.expectedSHA,
				ActualSHA:   beforeSHA,
				Tag:         tag,
				TS:          confEv.TS,
			})
		}
	}

	ev := event{
		Kind:         kindSnapshot,
		Turn:         turn,
		Tool:         tool,
		Tag:          tag,
		Path:         relPath,
		BeforeSHA:    beforeSHA,
		AfterSHA:     afterSHA,
		MessageCount: messageCount,
	}
	if err := m.log.Append(ctx, &ev); err != nil {
		return "", err
	}
	m.state.apply(&ev)
	m.index.apply(string(m.chatID), &ev)
	return Tag(tag), nil
}

// OldestTag is the earliest available tag, or "" if no snapshots
// exist yet. O(1) via the sorted index in state.
func (m *Manager) OldestTag(ctx context.Context) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoaded(ctx); err != nil {
		return ""
	}
	return m.state.oldestTag()
}

// Conflicts returns every conflict_detected event recorded for this
// chat in chronological order. O(1) copy from the ring buffer
// maintained by state.apply — no log re-read.
func (m *Manager) Conflicts(ctx context.Context) ([]ConflictPayload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	return m.state.conflicts.slice(), nil
}

// ReadBlob returns the content of a blob for this chat. Exposed so
// the client can fetch "what another chat left on disk" without
// going through the filesystem. SHA is validated as hex to prevent
// path traversal; only blobs referenced by this chat's event log
// are returned — we refuse SHAs we don't recognize so chat A
// can't probe chat B's private blobs.
func (m *Manager) ReadBlob(ctx context.Context, sha string) ([]byte, error) {
	if sha == "" || !isHexHash(sha) {
		return nil, errors.New("invalid blob sha")
	}
	// Normalise to lowercase so the referencesBlob map lookup
	// (case-sensitive, lowercase keys from hashOf) and the
	// on-disk path (also lowercase) stay in lock-step. Otherwise
	// an uppercase variant could pass isHexHash, miss the
	// referencesBlob guard, and on case-folded filesystems
	// (macOS HFS+, SMB) resolve to a shared lowercase blob path.
	sha = strings.ToLower(sha)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	if !m.state.referencesBlob(sha) {
		return nil, ErrBlobNotFound
	}
	return m.blobs.Get(ctx, sha)
}

// ReferencedBlobs returns a copy of all blob SHAs referenced by
// this chat's event log. Used by the GC to collect the referenced
// set from in-memory state instead of re-reading disk. Thread-safe.
func (m *Manager) ReferencedBlobs(ctx context.Context) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoaded(ctx); err != nil {
		return nil
	}
	out := make([]string, 0, len(m.state.blobRefs))
	for sha := range m.state.blobRefs {
		out = append(out, sha)
	}
	return out
}

// isHexHash validates that a hash string looks like a 64-char hex
// SHA-256. Used at the ReadBlob entrypoint to reject malformed
// input before it reaches the filesystem.
func isHexHash(s string) bool {
	if s == "" {
		return true // empty = "no blob" marker
	}
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
