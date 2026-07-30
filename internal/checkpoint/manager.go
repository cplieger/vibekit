// Per-chat checkpoint Manager, content-addressed edition.
//
// File-level, content-addressed checkpoints inspired by the Kiro
// IDE's `session-file-snapshots` design. Invariants we guarantee:
//
//   - Per-FILE snapshots, not per-WORKSPACE. Restoring a chat's
//     checkpoint only touches files that chat's agent edited.
//     Unrelated user edits (other chats' agents, editor buffers,
//     shell side-effects) survive every Restore.
//   - Global, deduplicated blob store shared across chats.
//   - Append-only fsync'd JSONL event log per chat — durable
//     after Append returns.
//   - Tag grammar `N` / `N.K`; client is unaware of the rewrite.
//   - Message-count watermark per snapshot → transcript and file
//     rollback stay coupled.
//   - Two-phase atomic restore with a journal: `restore_started`
//     before phase 2, `restore_committed` after. A crash between
//     the two reruns the commit on next load.
//   - Cross-chat conflict detection via a shared index: when we
//     see disk content that doesn't match what another chat left
//     there, a `conflict_detected` event is emitted and an
//     optional broadcast callback fires so clients can surface
//     the drift inline on the affected tool call.

package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/cplieger/pathinside"
	chktypes "github.com/cplieger/vibekit/internal/checkpoint/types"
	"golang.org/x/sync/singleflight"
)

// ErrPathEscape signals that a workspace-relative path resolves
// outside the workdir boundary. Distinct from a generic error so
// callers can classify the failure without string matching.
var ErrPathEscape = errors.New("path escapes workdir")

// ErrTransient signals a retryable I/O failure (disk full, permission
// denied temporarily, NFS stale handle). Callers can use
// errors.Is(err, ErrTransient) to distinguish "retry later" from
// permanent failures (ErrPathEscape, ErrTagNotFound, ErrLogCorrupt).
var ErrTransient = errors.New("transient checkpoint error")

// ConflictBroadcaster is the hook the Manager calls to fan conflict
// events out to SSE subscribers. Takes the payload by pointer so the
// hot path doesn't copy ~90 bytes per call.
type ConflictBroadcaster func(chatID string, payload *ConflictPayload)

// RestoreStageSuffix is the temp-file pattern used by both
// CheckoutFile and stageRestoreLocked for two-phase atomic writes.
// Single constant so cleanup tooling or tests that glob for orphaned
// staging files can reference it instead of hardcoding the pattern.
const RestoreStageSuffix = ".vibekit-restore-*"

// ConflictPayload is re-exported from the types sub-package for
// backward compatibility within this package.
type ConflictPayload = chktypes.ConflictPayload

// managerDeps bundles the shared infrastructure that every Manager
// in a Store receives. These are identical for every Manager the
// Store creates; only chatID, workDir, and log vary per chat.
type managerDeps struct {
	blobs  *blobStore
	index  *crossChatIndex
	onConf ConflictBroadcaster
}

// loadOutcome holds the result of the one-shot event log replay.
type loadOutcome struct {
	state *state
	err   error
}

// Manager owns one chat's checkpoint log and drives all snapshot /
// restore / diff operations on behalf of that chat. Safe for
// concurrent use — every entry point serializes on m.mu.
type Manager struct {
	loadSF singleflight.Group
	*managerDeps
	log        *eventLog
	state      *state
	loadResult atomic.Pointer[loadOutcome]
	chatID     string
	workDir    string
	mu         sync.Mutex
}

// newManager builds a Manager instance. Callers go through Store.get;
// this constructor isn't exported because the Store owns registry
// semantics (one Manager per chat, lazy init). gcLock is a shared
// pointer to the Store's RWMutex so every caller of Manager.Snapshot
// takes the same read lock that runGCOnce blocks against — even
// future refactors that hand *Manager out to callers that bypass
// Store.Snapshot inherit the coordination.
func newManager(id, workDir string, log *eventLog, deps *managerDeps) *Manager {
	return &Manager{
		managerDeps: deps,
		chatID:      id,
		workDir:     workDir,
		log:         log,
		state:       newState(),
	}
}

// --- Internals ---

// ensureLoaded replays the event log into state via singleflight so
// concurrent callers coalesce on a single replay. After replay, if
// a dangling restore_started is detected, recovery runs under m.mu.
//
// Callers must hold m.mu.
// INVARIANT: returns with m.mu held.
func (m *Manager) ensureLoaded(ctx context.Context) error {
	// Fast path: already loaded. Still re-check the restore journal: an
	// in-process failed phase-2 restore leaves state.pendingRestore set,
	// and returning early here would let subsequent checkpoint ops run
	// against a half-restored workspace until a restart. The recovery
	// call is a no-op field check when no restore is pending.
	if res := m.loadResult.Load(); res != nil {
		if res.err != nil {
			return res.err
		}
		return m.recoverPendingRestoreLocked(ctx)
	}

	// Slow path: release m.mu, perform replay via singleflight so
	// concurrent callers coalesce on a single replay.
	m.mu.Unlock()
	result, err, _ := m.loadSF.Do("load", func() (any, error) {
		return m.loadOnce(ctx), nil
	})
	// Re-acquire m.mu before returning (INVARIANT).
	m.mu.Lock()

	if err != nil {
		return err
	}
	outcome, _ := result.(*loadOutcome)
	if outcome.err != nil {
		return outcome.err
	}
	// Apply the loaded state if not yet set on this Manager.
	if m.state.turn == 0 && len(m.state.orderedTags) == 0 && outcome.state != nil {
		m.state = outcome.state
	}

	// Recovery: if a dangling restore_started exists, re-run.
	return m.recoverPendingRestoreLocked(ctx)
}

// loadOnce performs the one-shot event-log replay shared by all
// concurrent ensureLoaded callers via singleflight. It reads the log,
// builds the derived state, and applies every event to the shared
// cross-chat index, memoizing the outcome in m.loadResult so the fast
// path can skip the replay. Always returns non-nil; a read failure is
// carried in loadOutcome.err rather than returned, so the singleflight
// wrapper never reports an error.
func (m *Manager) loadOnce(ctx context.Context) *loadOutcome {
	// Another goroutine may have completed the load between the
	// caller's fast-path miss and this call.
	if res := m.loadResult.Load(); res != nil {
		return res
	}
	events, readErr := m.log.Read(ctx)
	if readErr != nil {
		outcome := &loadOutcome{err: readErr}
		m.loadResult.Store(outcome)
		return outcome
	}
	st := replay(events)
	if m.index != nil {
		for i := range events {
			m.index.apply(m.chatID, &events[i])
		}
	}
	outcome := &loadOutcome{state: st}
	m.loadResult.Store(outcome)
	return outcome
}

// recoverPendingRestoreLocked re-runs an interrupted restore when the
// replayed state carries a dangling restore_started (no matching
// restore_committed). Idempotent: already-renamed files are no-ops, so
// a crash during recovery is safe to repeat. Caller must hold m.mu;
// returns with m.mu held.
func (m *Manager) recoverPendingRestoreLocked(ctx context.Context) error {
	if m.state.pendingRestore == "" {
		return nil
	}
	tag := m.state.pendingRestore
	slog.Info("checkpoint: recovering interrupted restore",
		"chat_id", m.chatID, "tag", tag)
	if _, recErr := m.restoreLocked(ctx, tag, true); recErr != nil {
		slog.Error("checkpoint: restore recovery failed",
			"chat_id", m.chatID, "tag", tag, "error", recErr)
		return recErr
	}
	return nil
}

// load forces the event-log replay (ensureLoaded) without performing
// any other operation, so the shared cross-chat index picks up this
// chat's events. Used by Store.warmIndex on the first ownership
// lookup; harmless on an already-loaded Manager (fast path).
func (m *Manager) load(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureLoaded(ctx)
}

// loadAndValidateTag performs the common three-step prelude shared by
// RestorePreview, Restore, CheckoutFile, and Diff: ensure the event
// log is loaded, check for context cancellation, and verify the tag
// exists. Callers must hold m.mu.
func (m *Manager) loadAndValidateTag(ctx context.Context, tag string) error {
	if err := m.ensureLoaded(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := m.state.tags[tag]; !ok {
		return fmt.Errorf("%w: %q", ErrTagNotFound, tag)
	}
	return nil
}

// absPath turns a workspace-relative path into the on-disk absolute
// path, refusing anything that would resolve outside workDir. Two
// threat models the refusal closes:
//
//  1. Corrupt log: Restore reads paths straight from events.jsonl,
//     so a tampered entry with ".." or "" could produce a path
//     outside (or equal to) m.workDir.
//  2. Empty-string footgun: filepath.Rel(m.workDir, m.workDir)
//     returns ".", not an error, so without the explicit rel=="."
//     check an empty relPath would silently resolve to the workdir
//     root and callers would try to read/write/rename the workspace
//     itself at the next disk op.
//
// The escape half is pathinside.RelEscapes, which is separator-precise:
// a name whose first segment merely BEGINS with two dots ("..extras")
// is a name, not a traversal. The rel=="." equality rejection stays
// HERE rather than moving into the library call, because pathinside's
// containment predicates deliberately treat the root as inside (a tree
// includes its own root) and this function must EXCLUDE it — reason (2)
// above. pathinside.Inside would therefore be the wrong function at
// this site; the library's own doc prescribes the explicit equality
// test a root-excluding caller composes on top of RelEscapes.
//
// NOTE: this function does NOT evaluate symlinks. Callers that
// perform destructive filesystem operations (write, rename, remove)
// must defend against agent-planted symlink escapes separately —
// today the staged tmp path uses os.CreateTemp with a random
// suffix, which prevents pre-planted symlinks at deterministic
// staging paths. Parent-dir symlink traversal via os.MkdirAll is a
// known residual gap; closing it requires migrating restore +
// checkout to os.Root (Go 1.24+) so Rename/MkdirAll/Remove refuse
// to traverse symlinks that escape workDir.
func (m *Manager) absPath(relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, relPath)
	}
	clean := filepath.Clean(filepath.Join(m.workDir, relPath))
	rel, err := filepath.Rel(m.workDir, clean)
	if err != nil || rel == "." || pathinside.RelEscapes(rel) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, relPath)
	}
	return clean, nil
}

// safeGetBlob reads a blob or returns nil. Diff line-counting treats
// missing blobs as empty content so a blob GC'd between the
// snapshot and the diff request doesn't wedge the UI.
func (m *Manager) safeGetBlob(ctx context.Context, sha string) []byte {
	if sha == "" {
		return nil
	}
	data, err := m.blobs.Get(ctx, sha)
	if err != nil {
		return nil
	}
	return data
}

// readBeforeSHALocked reads the pre-write content at abs, stores it
// as a blob, and returns its SHA. Returns ("", nil) for missing
// files (expected — first snapshot of a new path), over-cap files
// (logged Warn; the snapshot still lands without a rollback
// target), and the race where the file disappears between Stat and
// ReadFile. Separated from Snapshot so the multi-branch
// stat/cap/read/error bookkeeping doesn't push Snapshot's
// cyclomatic complexity past gocyclo's threshold.
func (m *Manager) readBeforeSHALocked(ctx context.Context, relPath, abs string) (string, error) {
	info, statErr := os.Stat(abs)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return "", nil
		}
		return "", fmt.Errorf("stat pre-snapshot content: %w", statErr)
	}
	if info.Size() > contentCap {
		slog.Warn("checkpoint: pre-write content exceeds cap, skipping beforeSHA",
			"chat_id", m.chatID, "path", relPath,
			"size", info.Size(), "cap", contentCap)
		return "", nil
	}
	data, rErr := os.ReadFile(abs)
	if rErr != nil {
		if os.IsNotExist(rErr) {
			// Race: file existed at Stat, gone at ReadFile.
			// Treat as "did not exist" for rollback purposes.
			return "", nil
		}
		return "", fmt.Errorf("read pre-snapshot content: %w", rErr)
	}
	sha, putErr := m.blobs.Put(ctx, data)
	if putErr != nil {
		return "", fmt.Errorf("blob put (before): %w", putErr)
	}
	return sha, nil
}
