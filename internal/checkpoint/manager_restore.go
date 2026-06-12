// Restore path: RestorePreview, Restore, CheckoutFile, Cleanup, and
// the two-phase journal logic (restoreLocked, stageRestoreLocked,
// applyStagesLocked, logRestoreStarted/Committed). These methods
// share the Manager struct and its mutex but don't call the snapshot
// path — they have one reason to change (the restore/rollback model).

package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"
)

// commitRename atomically renames an externally-staged temp file into place
// and fsyncs the parent directory. The atomicfile library no longer exposes a
// bare-rename primitive (it owns temp creation), and the restore/staging flow
// produces the temp itself, so this mirrors the old atomicfile.Commit
// semantics locally: a rename failure is fatal (the data did not land), but a
// parent-dir fsync failure means the file is in place yet not guaranteed
// durable across an immediate crash — log at Warn and return nil rather than
// failing the commit.
func commitRename(tmp, final string) error {
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(final))
	if err != nil {
		slog.Warn("checkpoint: parent-dir open for fsync failed; rename not durable",
			"path", final, "error", err)
		return nil
	}
	defer d.Close()
	if syncErr := d.Sync(); syncErr != nil {
		slog.Warn("checkpoint: parent-dir fsync failed; rename not durable",
			"path", final, "error", syncErr)
	}
	return nil
}

// RestorePreview returns the list of files that would be touched by
// a Restore(tag) call. Used by the client to warn the user before
// the destructive action so they know whether in-flight editor
// buffers will be clobbered. Read-only; no log mutations.
//
// NOTE: the preview is a point-in-time snapshot of state at call
// time. If the agent snapshots another file between preview and
// Restore, that file will be touched by Restore but will not
// appear in the preview. Today the client accepts that window;
// tighter race closure (e.g. passing the expected file set back
// to Restore and rejecting the call if it changed) is deferred
// until a concrete user problem motivates it.
func (m *Manager) RestorePreview(ctx context.Context, tag Tag) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadAndValidateTag(ctx, string(tag)); err != nil {
		return nil, err
	}
	return m.state.filesTouchedAtOrAfter(string(tag)), nil
}

// Restore rolls the workspace back to the state captured at `tag`.
// Only files snapshotted at tags after `tag` are touched — every
// other file in the workspace is preserved. Returns the message
// count watermark recorded at `tag` so the caller can truncate the
// chat transcript in sync.
//
// Journaled two-phase commit:
//
//  1. Stage every target file to a `.vibekit-restore` sibling
//     (phase 1). Nothing destructive yet; any stage failure
//     aborts with a clean workspace.
//  2. Append `restore_started` so the log records "we are about
//     to mutate". fsync guarantees this survives a crash.
//  3. Phase 2: execute every rename / delete. A crash between
//     renames leaves the workspace half-restored, but the
//     journal tells us what to finish on next load.
//  4. Append `restore_committed` on success.
//
// Recovery (in ensureLoaded): if we see a `restore_started`
// without a matching `restore_committed` on replay, re-run the
// restore to that tag before accepting any new writes. Crashes
// during recovery are themselves idempotent — running the commit
// twice is harmless (renames either succeed or error with
// "already at target state").
func (m *Manager) Restore(ctx context.Context, tag Tag) (int, error) {
	m.mu.Lock()
	if err := m.loadAndValidateTag(ctx, string(tag)); err != nil {
		m.mu.Unlock()
		return 0, err
	}
	msgCount, touched, err := m.collectRestoreInfoLocked(string(tag))
	m.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if len(touched) == 0 {
		// Nothing to revert. Close any dangling journal.
		m.mu.Lock()
		defer m.mu.Unlock()
		return msgCount, m.logRestoreCommittedLocked(ctx, string(tag), msgCount)
	}

	// Phase 1: stage blob reads without the lock.
	stages, err := m.stageBlobReads(ctx, touched, string(tag))
	if err != nil {
		return 0, err
	}

	// Re-acquire lock for journal + phase 2.
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.logRestoreStartedLocked(ctx, string(tag), msgCount); err != nil {
		m.cleanupStages(stages)
		return 0, fmt.Errorf("restore: journal open: %w", err)
	}
	if err := m.applyStagesLocked(stages); err != nil {
		return 0, err
	}
	if err := m.logRestoreCommittedLocked(ctx, string(tag), msgCount); err != nil {
		slog.Warn("checkpoint: restore_committed append failed",
			"chat_id", m.chatID, "tag", tag, "error", err)
	}
	return msgCount, nil
}

// CheckoutFile reverts a single file to its content at `tag`. Used
// by the per-file Undo button on tool-call cards. Does not touch
// other files, does not truncate the transcript. Two-phase (stage
// + rename) so a crash mid-write can't leave a corrupt file.
func (m *Manager) CheckoutFile(ctx context.Context, tag Tag, relPath string) error {
	if relPath == "" {
		return errors.New("checkout: empty path")
	}
	// Phase 0: snapshot state under lock.
	m.mu.Lock()
	if err := m.loadAndValidateTag(ctx, string(tag)); err != nil {
		m.mu.Unlock()
		return err
	}
	sha, existed := m.state.contentAtOrBeforeTag(relPath, string(tag))
	abs, err := m.absPath(relPath)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()

	// Phase 1: blob I/O without the lock.
	if !existed {
		if rmErr := os.Remove(abs); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("checkout: remove %s: %w", relPath, rmErr)
		}
		return nil
	}
	data, err := m.blobs.Get(ctx, sha)
	if err != nil {
		return fmt.Errorf("checkout: get blob %s: %w", sha, err)
	}
	parentDir := filepath.Dir(abs)
	if mkErr := os.MkdirAll(parentDir, 0o755); mkErr != nil {
		return fmt.Errorf("checkout: mkdir: %w", mkErr)
	}
	// Random-suffix tmp via os.CreateTemp closes the symlink-TOCTOU
	// that a deterministic ".vibekit-restore" sibling would open
	// (see restoreLocked for the full rationale).
	tmpFile, tErr := os.CreateTemp(parentDir, filepath.Base(abs)+RestoreStageSuffix)
	if tErr != nil {
		return fmt.Errorf("checkout: create temp for %s: %w", relPath, tErr)
	}
	tmp := tmpFile.Name()
	if _, wErr := tmpFile.Write(data); wErr != nil {
		tmpFile.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("checkout: stage %s: %w", relPath, wErr)
	}
	if sErr := tmpFile.Sync(); sErr != nil {
		tmpFile.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("checkout: fsync stage %s: %w", relPath, sErr)
	}
	if cErr := tmpFile.Close(); cErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("checkout: close stage %s: %w", relPath, cErr)
	}
	if rnErr := commitRename(tmp, abs); rnErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("checkout: commit %s: %w", relPath, rnErr)
	}
	return nil
}

// Cleanup removes this chat's event log + parent directory and
// forgets every cross-chat index entry owned by this chat. Blobs
// are NOT touched here — the blob GC pass reaps any orphans
// asynchronously.
//
// Stale-reference semantics: after Cleanup, any caller holding a
// *Manager reference obtained from a prior Store.get on this
// chatID will see an empty state on the next method call (the log
// is gone, so ensureLoaded replays as a fresh chat — no error).
// Callers that need "was this chat deleted?" semantics should
// re-fetch through Store.get, not reuse stale Manager references.
// In practice the hub always goes through Store.get and never
// holds Manager pointers across Cleanup.
func (m *Manager) Cleanup(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if err := m.log.Wipe(); err != nil {
		slog.Warn("checkpoint: cleanup wipe failed",
			"chat_id", m.chatID, "error", err)
	}
	m.index.forgetChat(m.chatID)
	// m.log is retained with the same path on purpose. After
	// Wipe() the events.jsonl and parent dir are gone, but
	// m.loaded=false forces the next method call through
	// ensureLoaded → m.log.Read(), which treats a missing file
	// as an empty event stream (os.IsNotExist is handled
	// cleanly) and rebuilds m.state from scratch via
	// replay(nil). A caller that reuses this Manager after
	// Cleanup therefore sees a clean slate rather than an
	// error. The hub never does this in practice (it re-
	// fetches through Store.get), but the property keeps
	// the lifecycle simple.
	m.loadResult.Store(nil)
}

// restoreLocked is the core restore implementation. Callers must
// hold m.mu. `recovering=true` skips re-journaling the
// restore_started event (we're re-running from a dangling one) so
// repeated crashes don't bloat the log with orphan started markers.
// Kept separate so recovery (also under m.mu) can reuse it without
// the public-entrypoint wrapper.
func (m *Manager) restoreLocked(ctx context.Context, tag string, recovering bool) (int, error) {
	msgCount, ok := m.state.tags[tag]
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrTagNotFound, tag)
	}
	touched := m.state.filesTouchedAtOrAfter(tag)
	if len(touched) == 0 {
		return msgCount, m.logRestoreCommittedLocked(ctx, tag, msgCount)
	}

	// Release m.mu for blob I/O (same pattern as public Restore).
	m.mu.Unlock()
	stages, err := m.stageBlobReads(ctx, touched, tag)
	m.mu.Lock()
	if err != nil {
		return 0, err
	}

	// Journal open (skip during recovery — already open).
	if !recovering {
		if err := m.logRestoreStartedLocked(ctx, tag, msgCount); err != nil {
			m.cleanupStages(stages)
			return 0, fmt.Errorf("restore: journal open: %w", err)
		}
	}

	// Phase 2: commit.
	if err := m.applyStagesLocked(stages); err != nil {
		return 0, err
	}
	if err := m.logRestoreCommittedLocked(ctx, tag, msgCount); err != nil {
		slog.Warn("checkpoint: restore_committed append failed",
			"chat_id", m.chatID, "tag", tag, "error", err)
	}
	return msgCount, nil
}

// collectRestoreInfoLocked snapshots the state needed for a restore
// under m.mu. Returns msgCount, touched files, and any error.
// Caller must hold m.mu.
func (m *Manager) collectRestoreInfoLocked(tag string) (msgCount int, restoredPaths []string, err error) {
	count, ok := m.state.tags[tag]
	if !ok {
		return 0, nil, fmt.Errorf("%w: %q", ErrTagNotFound, tag)
	}
	msgCount = count
	restoredPaths = m.state.filesTouchedAtOrAfter(tag)
	return msgCount, restoredPaths, nil
}

// restorePlan holds the pre-computed info for one file in a restore.
type restorePlan struct {
	path    string
	abs     string
	sha     string
	existed bool
}

// collectRestorePlans builds the plan for each file under m.mu.
// Caller must hold m.mu.
func (m *Manager) collectRestorePlans(touched []string, tag string) ([]restorePlan, error) {
	plans := make([]restorePlan, 0, len(touched))
	for _, path := range touched {
		sha, existed := m.state.contentAtOrBeforeTag(path, tag)
		abs, pathErr := m.absPath(path)
		if pathErr != nil {
			return nil, fmt.Errorf("restore: resolve %s: %w", path, pathErr)
		}
		plans = append(plans, restorePlan{path: path, abs: abs, sha: sha, existed: existed})
	}
	return plans, nil
}

// stageBlobReads performs blob I/O and temp-file staging WITHOUT
// holding m.mu. This is the unlock-before-I/O pattern that prevents
// blocking other Manager methods during restore's disk reads.
// Uses bounded-concurrency errgroup (limit 8) matching the Diff
// method's pattern for parallel blob reads.
func (m *Manager) stageBlobReads(ctx context.Context, touched []string, tag string) ([]restoreStage, error) {
	// Collect plans under lock.
	m.mu.Lock()
	plans, err := m.collectRestorePlans(touched, tag)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}

	// Perform blob reads and staging without the lock, concurrently.
	stages := make([]restoreStage, len(plans))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, p := range plans {
		stages[i] = restoreStage{path: p.path, abs: p.abs, existed: p.existed}
		if !p.existed {
			continue
		}
		g.Go(func() error {
			data, getErr := m.blobs.Get(gctx, p.sha)
			if getErr != nil {
				return fmt.Errorf("restore: get blob %s for %s: %w", p.sha, p.path, getErr)
			}
			parentDir := filepath.Dir(p.abs)
			if mkErr := os.MkdirAll(parentDir, 0o755); mkErr != nil {
				return fmt.Errorf("restore: mkdir for %s: %w", p.path, mkErr)
			}
			tmpFile, tErr := os.CreateTemp(parentDir, filepath.Base(p.abs)+RestoreStageSuffix)
			if tErr != nil {
				return fmt.Errorf("restore: create temp for %s: %w", p.path, tErr)
			}
			if _, wErr := tmpFile.Write(data); wErr != nil {
				tmpFile.Close()
				_ = os.Remove(tmpFile.Name())
				return fmt.Errorf("restore: stage %s: %w", p.path, wErr)
			}
			if sErr := tmpFile.Sync(); sErr != nil {
				tmpFile.Close()
				_ = os.Remove(tmpFile.Name())
				return fmt.Errorf("restore: fsync stage %s: %w", p.path, sErr)
			}
			if cErr := tmpFile.Close(); cErr != nil {
				_ = os.Remove(tmpFile.Name())
				return fmt.Errorf("restore: close stage %s: %w", p.path, cErr)
			}
			stages[i].tmp = tmpFile.Name()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		m.cleanupStages(stages)
		return nil, err
	}
	return stages, nil
}

// applyStagesLocked executes phase 2 of Restore. Callers hold m.mu.
// On any rename/delete failure we log the offending path and
// clean up every still-staged sibling for files we haven't
// renamed yet, so repeated recovery attempts don't accumulate
// orphan .vibekit-restore-* siblings alongside the workspace
// files they were meant to replace.
func (m *Manager) applyStagesLocked(stages []restoreStage) error {
	for i, st := range stages {
		if !st.existed {
			if rmErr := os.Remove(st.abs); rmErr != nil && !os.IsNotExist(rmErr) {
				slog.Warn("checkpoint: restore phase-2 remove failed",
					"chat_id", m.chatID, "path", st.path, "error", rmErr)
				m.cleanupStages(stages[i+1:])
				return fmt.Errorf("restore: remove %s: %w", st.path, rmErr)
			}
			continue
		}
		if renErr := commitRename(st.tmp, st.abs); renErr != nil {
			slog.Warn("checkpoint: restore phase-2 rename failed",
				"chat_id", m.chatID, "path", st.path, "error", renErr)
			m.cleanupStages(stages[i:])
			return fmt.Errorf("restore: rename %s: %w", st.path, renErr)
		}
	}
	return nil
}

// logRestoreStartedLocked / logRestoreCommittedLocked: journal
// helpers. Callers hold m.mu.
func (m *Manager) logRestoreStartedLocked(ctx context.Context, tag string, msgCount int) error {
	ev := event{Kind: kindRestoreStarted, Tag: tag, MessageCount: msgCount}
	if err := m.log.Append(ctx, &ev); err != nil {
		return err
	}
	m.state.apply(&ev)
	return nil
}

func (m *Manager) logRestoreCommittedLocked(ctx context.Context, tag string, msgCount int) error {
	ev := event{Kind: kindRestoreCommitted, Tag: tag, MessageCount: msgCount}
	if err := m.log.Append(ctx, &ev); err != nil {
		return err
	}
	m.state.apply(&ev)
	return nil
}

// cleanupStages removes any staged restore files from a
// failed restore attempt. Best-effort; logs at warn level when a
// stage sibling can't be removed so operators see the breadcrumb
// if staged files accumulate over time (stuck permissions, full
// filesystem, read-only mount).
func (m *Manager) cleanupStages(stages []restoreStage) {
	for _, st := range stages {
		if st.tmp == "" {
			continue
		}
		if err := os.Remove(st.tmp); err != nil && !os.IsNotExist(err) {
			slog.Warn("checkpoint: stage cleanup failed",
				"chat_id", m.chatID, "path", st.path,
				"tmp", st.tmp, "error", err)
		}
	}
}

// restoreStage is a staged filesystem operation for two-phase
// restore. One entry per file we intend to revert.
type restoreStage struct {
	path    string // workspace-relative
	abs     string // absolute
	tmp     string // absolute path of staged content ("" means delete instead)
	existed bool   // whether the file existed at the restore tag
}
