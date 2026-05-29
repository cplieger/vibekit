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
	// Phase 1: validate and resolve path under lock.
	m.mu.Lock()
	if err := m.ensureLoaded(ctx); err != nil {
		m.mu.Unlock()
		return "", err
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return "", err
	}
	abs, err := m.absPath(relPath)
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	m.mu.Unlock()

	// Phase 2: blob I/O without the lock.
	beforeSHA, err := m.readBeforeSHALocked(ctx, relPath, abs)
	if err != nil {
		return "", err
	}
	var afterSHA string
	if newContent != nil {
		afterSHA, err = m.blobs.Put(ctx, newContent)
		if err != nil {
			return "", fmt.Errorf("blob put (after): %w", err)
		}
	}

	// Phase 3: re-acquire lock for tag allocation, conflict check, event append.
	m.mu.Lock()
	defer m.mu.Unlock()

	tag := m.state.allocateTag()
	turn, tool := parseTag(tag)

	// Cross-chat conflict detection.
	if obs, conflict := m.index.check(m.chatID, relPath, beforeSHA); conflict {
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
		if m.onConf != nil {
			m.onConf(m.chatID, &ConflictPayload{
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
	m.index.apply(m.chatID, &ev)
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
	return m.state.conflicts.Slice(), nil
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
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
