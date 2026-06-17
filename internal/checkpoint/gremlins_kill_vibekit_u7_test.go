package checkpoint

// Mutant-killing tests for unit vibekit-u7 (internal/checkpoint).
// Test-only file. Every identifier is prefixed gk_vibekit_u7_ /
// Test_gk_vibekit_u7_ so it never collides with the sibling u6 unit
// that shares this package.
//
// Technique: most living mutants here are CONDITIONALS_NEGATION on an
// `err != nil` (or `Len == cap`) guard whose ONLY in-branch effect is
// a fire-and-forget slog.Warn — the function's return value and state
// mutations are identical on both sides of the branch. The negation
// flips WHICH path logs, so the kill is "on the success path, assert
// the failure-warn was NOT emitted." This mirrors the established
// gk_vibekit_u6_* slog-capture style in this package.
//
// Equivalent mutants (documented, no test — same dispositions in the
// UNIT RESULT report):
//   - state.go:116:12 CONDITIONALS_BOUNDARY (`e.Turn > s.turn` -> `>=`):
//     the two operators differ only at e.Turn == s.turn, and the sole
//     in-branch statement `s.turn = e.Turn` is then a no-op (assigns
//     s.turn to itself). No state, return, or log differs. equivalent.
//   - state_query.go:193:9 CONDITIONALS_BOUNDARY (compareTags `at < bt`
//     -> `<=`): line 193 is reached only inside `if at != bt`, so the
//     equality case `<=` would add is excluded and `<`/`<=` return
//     identically for every reachable input. equivalent.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// --- shared helpers ------------------------------------------------

// gk_vibekit_u7_logCapture is a slog.Handler that records every
// record's Message so tests can assert presence/absence of a warn.
type gk_vibekit_u7_logCapture struct {
	mu   *sync.Mutex
	msgs *[]string
}

func (h gk_vibekit_u7_logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h gk_vibekit_u7_logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.msgs = append(*h.msgs, r.Message)
	return nil
}

func (h gk_vibekit_u7_logCapture) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h gk_vibekit_u7_logCapture) WithGroup(_ string) slog.Handler      { return h }

// gk_vibekit_u7_captureLogs installs a capturing handler as the slog
// default for the duration of the test and returns a predicate that
// reports whether any captured message contains substr. Restores the
// previous default on cleanup. Tests using it must not call
// t.Parallel (slog default is process-global).
func gk_vibekit_u7_captureLogs(t *testing.T) func(substr string) bool {
	t.Helper()
	var mu sync.Mutex
	var msgs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(gk_vibekit_u7_logCapture{mu: &mu, msgs: &msgs}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func(substr string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range msgs {
			if strings.Contains(m, substr) {
				return true
			}
		}
		return false
	}
}

// === manager_restore.go ===========================================

// commitRename success path must NOT emit either durability warn.
// Kills manager_restore.go:33:9 (the `err != nil` guard after
// os.Open(dir); a `err == nil` mutant warns "parent-dir open for
// fsync failed" and returns early when the open SUCCEEDS) and
// manager_restore.go:39:34 (the `syncErr != nil` guard after
// d.Sync(); a `syncErr == nil` mutant warns "parent-dir fsync
// failed" when the sync SUCCEEDS).
func Test_gk_vibekit_u7_CommitRenameNoSpuriousDurabilityWarn(t *testing.T) {
	dir := t.TempDir()
	// Precondition: directory fsync must succeed on this fs, else
	// the ORIGINAL legitimately logs "parent-dir fsync failed" and
	// the 39:34 assertion is moot.
	probe, err := os.Open(dir)
	if err != nil {
		t.Skipf("cannot open temp dir: %v", err)
	}
	syncErr := probe.Sync()
	_ = probe.Close()
	if syncErr != nil {
		t.Skipf("directory fsync unsupported on this fs: %v", syncErr)
	}

	tmp := filepath.Join(dir, "staged.tmp")
	final := filepath.Join(dir, "final.txt")
	if err := os.WriteFile(tmp, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	has := gk_vibekit_u7_captureLogs(t)
	if err := commitRename(tmp, final); err != nil {
		t.Fatalf("commitRename(success path) = %v, want nil", err)
	}
	if has("parent-dir open for fsync failed") {
		t.Errorf("commitRename success path logged 'parent-dir open for fsync failed'; a negated open err-check warns when Open succeeds (manager_restore.go:33)")
	}
	if has("parent-dir fsync failed") {
		t.Errorf("commitRename success path logged 'parent-dir fsync failed'; a negated sync err-check warns when Sync succeeds (manager_restore.go:39)")
	}
	if _, err := os.Stat(final); err != nil {
		t.Errorf("commitRename did not move file into place: %v", err)
	}
}

// restoreLocked on the happy path must return the real watermark,
// clear the pending-restore marker, and NOT log a commit-failure.
// Kills manager_restore.go:269:45 (the `applyStagesLocked() err !=
// nil` guard; a `err == nil` mutant short-circuits to `return 0, nil`
// on SUCCESS, dropping the watermark and skipping the commit) and
// manager_restore.go:272:65 (the committed-append `err != nil` guard;
// a `err == nil` mutant warns "restore_committed append failed" on
// success).
func Test_gk_vibekit_u7_RestoreLockedCommitsAndReturnsWatermark(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "rl")
	f := filepath.Join(work, "f.txt")
	if err := os.WriteFile(f, []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	tag1, err := m.Snapshot(ctx, "f.txt", nil, 7) // watermark = 7
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(f, []byte("v1"), 0o600)

	has := gk_vibekit_u7_captureLogs(t)
	m.mu.Lock()
	n, rErr := m.restoreLocked(ctx, string(tag1), false)
	pr := m.state.pendingRestore
	m.mu.Unlock()

	if rErr != nil {
		t.Fatalf("restoreLocked = %v, want nil", rErr)
	}
	if n != 7 {
		t.Errorf("restoreLocked watermark = %d, want 7; a negated applyStagesLocked err-check returns (0,nil) on success (manager_restore.go:269)", n)
	}
	if pr != "" {
		t.Errorf("pendingRestore = %q, want cleared; a negated applyStagesLocked err-check skips the commit (manager_restore.go:269)", pr)
	}
	if has("restore_committed append failed") {
		t.Errorf("successful restoreLocked logged 'restore_committed append failed'; a negated committed-append check warns on success (manager_restore.go:272)")
	}
	if got, _ := os.ReadFile(f); string(got) != "v0" {
		t.Errorf("file after restoreLocked = %q, want v0", got)
	}
}

// A successful public Restore (the touched-files path) must NOT log a
// commit-failure. Kills manager_restore.go:125:73 (the committed-
// append `err != nil` guard inside Restore; a `err == nil` mutant
// warns "restore_committed append failed" on success).
func Test_gk_vibekit_u7_RestoreSuccessNoCommittedAppendWarn(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "rs")
	f := filepath.Join(work, "f.txt")
	if err := os.WriteFile(f, []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	tag1, err := m.Snapshot(ctx, "f.txt", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(f, []byte("v1"), 0o600)

	has := gk_vibekit_u7_captureLogs(t)
	mc, err := m.Restore(ctx, tag1)
	if err != nil {
		t.Fatalf("Restore = %v, want nil", err)
	}
	if mc != 3 {
		t.Errorf("Restore watermark = %d, want 3", mc)
	}
	if has("restore_committed append failed") {
		t.Errorf("successful Restore logged 'restore_committed append failed'; a negated committed-append check warns on success (manager_restore.go:125)")
	}
}

// logRestoreStartedLocked must apply the event into state after a
// successful append. Kills manager_restore.go:407:40 (the append
// `err != nil` guard; a `err == nil` mutant takes the branch on
// SUCCESS, returning before m.state.apply runs, so pendingRestore is
// never set).
func Test_gk_vibekit_u7_LogRestoreStartedAppliesEventOnSuccess(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestManager(t, "lrs")
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	err := m.logRestoreStartedLocked(ctx, "5", 7)
	pr := m.state.pendingRestore
	m.mu.Unlock()

	if err != nil {
		t.Fatalf("logRestoreStartedLocked = %v, want nil", err)
	}
	if pr != "5" {
		t.Errorf("pendingRestore = %q, want %q; a negated append err-check skips m.state.apply on success (manager_restore.go:407)", pr, "5")
	}
}

// logRestoreCommittedLocked must apply the event into state after a
// successful append. Kills manager_restore.go:416:40 (the append
// `err != nil` guard; a `err == nil` mutant returns before
// m.state.apply runs on SUCCESS, so latestTag is never set and
// pendingRestore is never cleared).
func Test_gk_vibekit_u7_LogRestoreCommittedAppliesEventOnSuccess(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestManager(t, "lrc")
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	m.state.pendingRestore = "9" // simulate an open restore journal
	err := m.logRestoreCommittedLocked(ctx, "5", 7)
	lt := m.state.latestTag
	pr := m.state.pendingRestore
	m.mu.Unlock()

	if err != nil {
		t.Fatalf("logRestoreCommittedLocked = %v, want nil", err)
	}
	if lt != "5" {
		t.Errorf("latestTag = %q, want %q; a negated append err-check skips m.state.apply on success (manager_restore.go:416)", lt, "5")
	}
	if pr != "" {
		t.Errorf("pendingRestore = %q, want cleared after restore_committed apply (manager_restore.go:416)", pr)
	}
}

// cleanupStages removing an existing staged file must NOT log a
// cleanup-failure. Kills manager_restore.go:433:36 (the `err != nil`
// guard before `&& !os.IsNotExist(err)`; a `err == nil` mutant warns
// "stage cleanup failed" when the Remove SUCCEEDS).
func Test_gk_vibekit_u7_CleanupStagesNoWarnOnSuccessfulRemove(t *testing.T) {
	m, work := newTestManager(t, "cs")
	tmp1 := filepath.Join(work, "f1.vibekit-restore-aaa")
	if err := os.WriteFile(tmp1, []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}

	has := gk_vibekit_u7_captureLogs(t)
	m.cleanupStages([]restoreStage{
		{path: "f1.txt", abs: filepath.Join(work, "f1.txt"), tmp: tmp1, existed: true},
	})
	if has("stage cleanup failed") {
		t.Errorf("cleanupStages logged 'stage cleanup failed' on a successful Remove; a negated err-check warns when Remove succeeds (manager_restore.go:433)")
	}
	if _, err := os.Stat(tmp1); !os.IsNotExist(err) {
		t.Errorf("staged temp not removed: stat err = %v", err)
	}
}

// Cleanup of a manager whose log exists must NOT log a wipe-failure.
// Kills manager_restore.go:217:30 (the `m.log.Wipe() err != nil`
// guard; a `err == nil` mutant warns "cleanup wipe failed" when Wipe
// SUCCEEDS).
func Test_gk_vibekit_u7_CleanupNoSpuriousWipeWarn(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestManager(t, "cl")
	if err := m.AdvanceTurn(ctx, 1); err != nil { // create the log so Wipe succeeds
		t.Fatal(err)
	}

	has := gk_vibekit_u7_captureLogs(t)
	m.Cleanup(ctx)
	if has("cleanup wipe failed") {
		t.Errorf("Cleanup logged 'cleanup wipe failed' on a successful wipe; a negated err-check warns on success (manager_restore.go:217)")
	}
}

// === manager_snapshot.go ==========================================

// ReferencedBlobs must return the chat's blob SHAs once the log is
// loaded. Kills manager_snapshot.go:201:37 (the `ensureLoaded() err
// != nil` guard; a `err == nil` mutant returns nil on SUCCESS).
func Test_gk_vibekit_u7_ReferencedBlobsReturnsRefsWhenLoaded(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "rb")
	f := filepath.Join(work, "f.txt")
	if err := os.WriteFile(f, []byte("before-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "f.txt", []byte("after-content"), 2); err != nil {
		t.Fatal(err)
	}

	refs := m.ReferencedBlobs(ctx)
	if len(refs) != 2 {
		t.Errorf("ReferencedBlobs() returned %d SHAs, want 2 (beforeSHA + afterSHA); a negated ensureLoaded check returns nil on success (manager_snapshot.go:201)", len(refs))
	}
}

// === state.go =====================================================

// applySnapshot must advance s.turn when the event's turn exceeds the
// current turn. Kills state.go:116:12 CONDITIONALS_NEGATION
// (`e.Turn > s.turn` -> `e.Turn <= s.turn`): on a fresh state
// (turn 0) an event with Turn 2 advances to 2 under the original but
// leaves turn at 0 under the negation.
func Test_gk_vibekit_u7_ApplySnapshotAdvancesTurn(t *testing.T) {
	s := newState()
	s.apply(&event{Kind: kindSnapshot, Turn: 2, Tool: 0, Tag: "2", Path: "f"})
	if s.turn != 2 {
		t.Errorf("state.turn = %d, want 2; a negated 'e.Turn > s.turn' guard leaves turn unadvanced (state.go:116)", s.turn)
	}
}

// applyConflict on a non-full ring must NOT log the ring-full warn.
// Kills state.go:181:23 CONDITIONALS_NEGATION (`Len() ==
// maxInMemoryConflicts` -> `!=`): with an empty ring (Len 0) the
// original stays silent while the negation logs "conflict ring full".
func Test_gk_vibekit_u7_ApplyConflictNoRingFullWarnBelowCap(t *testing.T) {
	s := newState()
	has := gk_vibekit_u7_captureLogs(t)
	s.apply(&event{Kind: kindConflict, Path: "f", Tag: "1", BeforeSHA: "b", OtherChat: "o", ExpectedSHA: "e", TS: 1})
	if has("conflict ring full") {
		t.Errorf("applyConflict logged 'conflict ring full' with an empty ring; a negated Len==cap guard warns below cap (state.go:181)")
	}
	if got := s.conflicts.Len(); got != 1 {
		t.Errorf("conflicts.Len() = %d, want 1 (the append must run regardless of the warn)", got)
	}
}

// === state_query.go ===============================================

// contentAtOrBeforeTag's no-exact-match fallback must return the
// afterSHA of the nearest prior snapshot. Kills state_query.go:101:10
// CONDITIONALS_BOUNDARY (`best >= 0` -> `best > 0`): with the only
// prior snapshot at index 0, the original returns its afterSHA while
// `> 0` drops to ("",false). Also kills state_query.go:101:41
// CONDITIONALS_NEGATION (`afterSHA != ""` -> `afterSHA == ""`):
// case 1 (non-empty afterSHA) makes the original return content while
// the negation falls through; case 2 (empty afterSHA) makes the
// negation falsely claim content.
func Test_gk_vibekit_u7_ContentAtOrBeforeTagFallback(t *testing.T) {
	// Case 1: single prior snapshot at index 0 (tag "1"), query a
	// later tag "2" with no exact match -> best=0 -> fallback.
	s := newState()
	s.apply(&event{Kind: kindSnapshot, Turn: 1, Tool: 0, Tag: "1", Path: "f", BeforeSHA: "b1", AfterSHA: "a1"})
	gotSHA, ok := s.contentAtOrBeforeTag("f", "2")
	if !ok || gotSHA != "a1" {
		t.Errorf("contentAtOrBeforeTag(f,2) = (%q,%v), want (a1,true); a '>' boundary (101:10) or negated afterSHA check (101:41) drops the prior-snapshot fallback", gotSHA, ok)
	}

	// Case 2: best=0 but afterSHA is empty -> original ("",false);
	// the negated afterSHA!="" check would falsely return ("",true).
	s2 := newState()
	s2.apply(&event{Kind: kindSnapshot, Turn: 1, Tool: 0, Tag: "1", Path: "g", BeforeSHA: "b2", AfterSHA: ""})
	_, ok2 := s2.contentAtOrBeforeTag("g", "2")
	if ok2 {
		t.Errorf("contentAtOrBeforeTag(g,2) ok = true, want false; an empty afterSHA must not be reported as content (state_query.go:101:41)")
	}
}

// atoiSafe with a leading zero must not stop early. Kills
// state_query.go:230:8 CONDITIONALS_BOUNDARY (`n < 0` -> `n <= 0`):
// for "05"/"007" the accumulator is 0 after the leading '0', so the
// original keeps parsing while `n <= 0` returns 0 mid-parse.
func Test_gk_vibekit_u7_AtoiSafeLeadingZero(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"05", 5},
		{"007", 7},
		{"10", 10},
		{"0", 0},
	}
	for _, tc := range cases {
		if got := atoiSafe(tc.in); got != tc.want {
			t.Errorf("atoiSafe(%q) = %d, want %d; a '<=' overflow boundary returns 0 once n==0 mid-parse (state_query.go:230)", tc.in, got, tc.want)
		}
	}
}

// === store.go =====================================================

// Store.AdvanceTurn on a successful underlying call must NOT log a
// failure. Kills store.go:86:70 (the delegated-call `err != nil`
// guard; a `err == nil` mutant warns "AdvanceTurn failed" on
// success).
func Test_gk_vibekit_u7_StoreAdvanceTurnNoWarnOnSuccess(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	s := NewStore(cfg, work, nil)

	has := gk_vibekit_u7_captureLogs(t)
	s.AdvanceTurn(ctx, api.ChatID("chat-a"), 1)
	if has("AdvanceTurn failed") {
		t.Errorf("Store.AdvanceTurn logged 'AdvanceTurn failed' on success; a negated err-check warns when the delegated call succeeds (store.go:86)")
	}
}

// wipe() of an existing log must NOT log a failure. Kills
// store.go:226:28 (the `log.Wipe() err != nil` guard; a `err == nil`
// mutant warns "wipe failed" when Wipe SUCCEEDS).
func Test_gk_vibekit_u7_WipeNoSpuriousFailureWarn(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	log := newEventLog(cfg, "wp")
	deps := &managerDeps{blobs: blobs, index: newCrossChatIndex()}
	m := newManager("wp", work, log, deps)
	if err := m.AdvanceTurn(ctx, 1); err != nil { // create the log on disk
		t.Fatal(err)
	}

	has := gk_vibekit_u7_captureLogs(t)
	wipe(cfg, "wp")
	if has("checkpoint: wipe failed") {
		t.Errorf("wipe() logged 'wipe failed' on a successful wipe; a negated err-check warns on success (store.go:226)")
	}
}
