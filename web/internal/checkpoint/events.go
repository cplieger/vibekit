// Per-chat event log. Append-only JSONL at
// <configDir>/snapshots/chats/<chatID>/events.jsonl. Every checkpoint
// operation (turn boundary, file snapshot, restore) produces one line;
// the whole chat state is recoverable by replaying the file.
//
// Why JSONL instead of git:
//
//   - Append-only writes are atomic without lock coordination. No
//     .lock files, no two-phase commit, no "git index corrupt" edge
//     cases when a crash hits mid-operation.
//   - Replays are linear and cheap (< 10 MB per chat even after
//     thousands of snapshots).
//   - Cross-chat isolation is automatic — chats don't share the
//     event log, only the blob store.
//   - No external process (git) in the hot path.
//
// File format: one JSON object per line. Unknown fields on read are
// ignored so we can add fields forward-compat. Integer epoch millis
// for timestamps; the human-readable form is a client concern.
//
// State derivation: replay() loads the full log and returns a state;
// subsequent events are folded in via state.apply() so callers
// avoid re-replaying the full log on every mutation.

package checkpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// eventKind discriminates the union of event record shapes.
type eventKind string

// ErrLogCorrupt signals that the event log is in a corrupt or
// tampered state (e.g. symlink replacement). Distinct from a
// generic error so callers can classify the failure category.
var ErrLogCorrupt = errors.New("event log corrupt")

const (
	kindTurnStart        eventKind = "turn_start"        // marks the beginning of a user turn
	kindSnapshot         eventKind = "snapshot"          // one file was captured before a write
	kindRestore          eventKind = "restore"           // workspace rolled back to a prior tag (legacy single-event marker)
	kindRestoreStarted   eventKind = "restore_started"   // restore journal open; phase 2 about to run
	kindRestoreCommitted eventKind = "restore_committed" // restore journal closed; all renames succeeded
	// Emitted when a cross-chat conflict is detected: another chat
	// edited a file between our snapshots. Records which chat and
	// which afterSHA we expected so the UI can surface it.
	kindConflict eventKind = "conflict_detected"
)

// validKinds is the single source of truth for known event kinds.
// Adding a new kind to the const block above AND this map is the
// only step needed; valid() is a map lookup.
var validKinds = map[eventKind]struct{}{
	kindTurnStart:        {},
	kindSnapshot:         {},
	kindRestore:          {},
	kindRestoreStarted:   {},
	kindRestoreCommitted: {},
	kindConflict:         {},
}

// valid reports whether k is a known event kind. Used at the Append
// boundary to reject invalid kinds at write time rather than
// discovering them at replay time (where the damage is permanent).
func (k eventKind) valid() bool {
	_, ok := validKinds[k]
	return ok
}

// event is the on-disk representation. One per line, serialized via
// json.Marshal (no pretty-printing — we want one line per event).
// Unused fields for the record's Kind stay zero-valued and are
// omitted via the `omitempty` tag so the JSONL stays compact.
//
// V stamps every newly-written event so future schema changes can
// be diagnosed at replay time. v=0 means "legacy event written
// before versioning"; readers tolerate it. Bump currentEventVersion
// only for breaking changes (renamed/retyped fields); purely
// additive fields stay at v=1 because the unknown-fields-ignored
// read policy already makes them forward-compatible.
type event struct {
	Kind         eventKind `json:"type"`
	Tag          string    `json:"tag,omitempty"`
	Path         string    `json:"path,omitempty"`
	BeforeSHA    string    `json:"before_sha,omitempty"`
	AfterSHA     string    `json:"after_sha,omitempty"`
	Description  string    `json:"description,omitempty"`
	OtherChat    string    `json:"other_chat,omitempty"`
	ExpectedSHA  string    `json:"expected_sha,omitempty"`
	TS           int64     `json:"ts"`
	Turn         int       `json:"turn,omitempty"`
	Tool         int       `json:"tool,omitempty"`
	MessageCount int       `json:"message_count,omitempty"`
	V            int       `json:"v"`
}

const currentEventVersion = 1

// eventLog is the per-chat writer + reader. Single writer, many
// readers — safe for concurrent Read/Append calls. Append holds the
// mutex only long enough to fsync the line; replay reads take a fresh
// fd so they don't block writers.
type eventLog struct {
	path string
	mu   sync.Mutex
}

// newEventLog builds an eventLog for chatID. The directory is
// created on first Append so an Open-for-replay on a never-written
// chat doesn't create an empty dir.
func newEventLog(configDir, chatID string) *eventLog {
	return &eventLog{
		path: chatLogPath(configDir, chatID),
	}
}

// Append writes one event. The l.mu mutex serialises in-process
// writes so concurrent appenders never interleave partial lines
// (PIPE_BUF atomicity is a pipe guarantee, not a regular-file one
// on POSIX — the mutex, not the kernel, is what protects us).
// fsync guarantees durability before return; parent-dir sync on the
// first Append persists the newly-created file's inode entry so a
// power loss right after a chat's first snapshot doesn't lose the
// events.jsonl even though Sync reported success.
func (l *eventLog) Append(ctx context.Context, ev *event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ev.Kind.valid() {
		return fmt.Errorf("invalid event kind: %q", ev.Kind)
	}
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	if ev.V == 0 {
		ev.V = currentEventVersion
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	parent := filepath.Dir(l.path)
	if mkErr := os.MkdirAll(parent, 0o700); mkErr != nil {
		return fmt.Errorf("mkdir event log: %w", errors.Join(ErrTransient, mkErr))
	}
	// O_CREATE here may create events.jsonl on first Append for a
	// chat. We sync the parent dir post-write to persist the new
	// inode entry.
	existed, err := isRegularFile(l.path)
	if err != nil {
		return fmt.Errorf("stat event log: %w", err)
	}
	f, openErr := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if openErr != nil {
		return fmt.Errorf("open event log: %w", errors.Join(ErrTransient, openErr))
	}
	// close errors post-Sync are genuinely ignorable; _ = f.Close
	// makes the intent explicit (vs defer f.Close() silently
	// discarding).
	defer func() { _ = f.Close() }()
	if err := ensureNewlineTerminated(f); err != nil {
		return err
	}
	if _, wErr := f.Write(line); wErr != nil {
		return fmt.Errorf("append event: %w", wErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// fsync to guarantee durability before Append returns.
	// Without this, a power loss within seconds of Append can
	// lose the event even though the kernel accepted the write
	// (page cache, not disk). The cost is a few ms per snapshot
	// on SSDs; worth the durability claim.
	if syncErr := f.Sync(); syncErr != nil {
		return fmt.Errorf("fsync event log: %w", syncErr)
	}
	if !existed {
		// First append to this chat's log: persist the newly-
		// created inode entry so the events.jsonl file itself
		// survives power loss, not just its data pages.
		syncDir(parent)
	}
	return nil
}

// isRegularFile reports whether path is an existing regular file.
// Distinct from os.IsNotExist because we use the result to decide
// whether to fsync the parent directory (no sync needed for pre-
// existing files). Uses Lstat and rejects symlinks so a planted
// symlink at events.jsonl can't redirect event writes outside the
// chat-scoped dir (mirrors the non-regular-file rejection in
// blobStore.Get and gc.sweepFanout). Returns an error on any non-
// IsNotExist stat failure so the caller doesn't proceed in an
// unknown filesystem state.
func isRegularFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%w: %s", ErrLogCorrupt, path)
	}
	return info.Mode().IsRegular(), nil
}

// ensureNewlineTerminated guarantees the next append won't glue
// onto a partial line from a crashed prior process. Cheap O(1)
// stat+ReadAt on the tail byte; when the file is empty or already
// ends in '\n' it's a no-op.
func ensureNewlineTerminated(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat event log: %w", err)
	}
	if info.Size() == 0 {
		return nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
		// ReadAt failure on the tail byte is unusual but
		// non-fatal — the subsequent append just loses the
		// defensive newline. Skip silently; the
		// json.Unmarshal skip path tolerates a glued line.
		return nil
	}
	if last[0] == '\n' {
		return nil
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("append newline guard: %w", err)
	}
	return nil
}

// eventLogMaxLine bounds a single scanned line. 32 MiB is orders of
// magnitude above anything a healthy event can produce but gives
// headroom for future event fields (inline conflict diffs, large
// description payloads). A line that exceeds this is treated as
// malformed (see Read) so a pathological single line doesn't
// permanently wedge the chat's replay.
const eventLogMaxLine = 32 << 20

// warnedLargeLogs tracks which event logs have already triggered
// the "event log is large" warn, so a hot chat doesn't flood Loki
// with one warn per Read call. Keyed by absolute path.
var warnedLargeLogs sync.Map

// Read replays the event log into an []event slice. Skips blank
// lines and malformed entries (logged at warn). Returns nil on a
// missing file — that's the "fresh chat" case. A single oversized
// line (bufio.ErrTooLong) is treated like any other malformed line:
// logged and skipped so the rest of the log still replays.
//
// Log size note: events.jsonl is append-only and grows O(snapshot
// count). A chat with 10k+ snapshots will have a multi-MB log that
// takes ~50ms to replay on boot. We don't compact today — every
// snapshot's beforeSHA contributes to the Restore/Diff content
// model, so truncation would lose rollback history. The warn below
// fires at most once per process per chat so long-lived heavy
// chats don't drown Loki in duplicate breadcrumbs.
func (l *eventLog) Read() ([]event, error) {
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open event log: %w", err)
	}
	defer f.Close()
	var out []event
	sc := bufio.NewScanner(f)
	// 32 MiB ceiling per line — far above any healthy event
	// shape, but generous enough that future fields can grow.
	// When a line crosses this we fall through to the ErrTooLong
	// handler below instead of aborting the entire replay.
	sc.Buffer(make([]byte, 0, 64*1024), eventLogMaxLine)
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(b, &ev); err != nil {
			// Don't fail the whole replay on one malformed
			// line — a partial write from a crashed process
			// shouldn't make the chat unrecoverable. Log at
			// warn so operators can tell "one-off corruption"
			// from "systemic schema drift".
			slog.Warn("checkpoint: skipped malformed event line",
				"path", l.path, "bytes", len(b), "error", err)
			continue
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			// A single 32 MiB+ line is indistinguishable
			// from corruption in practice. Preserve the
			// partial replay and let the caller proceed
			// rather than abort the whole chat. Escalated
			// to Error so Loki alerting picks up the
			// truncation — the tail of this log is
			// silently dropped on every subsequent
			// process restart until the oversized line
			// is removed, so operators need a loud signal.
			slog.Error("checkpoint: event log truncated at oversized line",
				"path", l.path, "cap", eventLogMaxLine,
				"events_recovered", len(out))
			return out, nil
		}
		return nil, fmt.Errorf("scan event log: %w", err)
	}
	// Log-growth heuristic: warn once per path when an event log
	// crosses 10k entries so operators have a breadcrumb if
	// replays start feeling slow. Prior implementation logged on
	// every Read call which was called multiple times per hour
	// on hot chats and spammed Loki with duplicate alerts.
	if len(out) > 10000 {
		if _, seen := warnedLargeLogs.LoadOrStore(l.path, struct{}{}); !seen {
			slog.Warn("checkpoint event log is large",
				"path", l.path, "events", len(out))
		}
	}
	return out, nil
}

// Wipe removes the event log file and its parent directory. Called
// from Manager.Cleanup on chat delete/archive/purge. Also clears
// the "log is large" warn latch for the path so a re-created chat
// with the same id doesn't silently skip the warn.
func (l *eventLog) Wipe() error {
	warnedLargeLogs.Delete(l.path)
	if err := os.RemoveAll(filepath.Dir(l.path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
