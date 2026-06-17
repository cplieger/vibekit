package checkpoint

// Mutant-killing tests for unit vibekit-u6 (internal/checkpoint).
// Test-only file. Every identifier is prefixed gk_vibekit_u6_ /
// Test_gk_vibekit_u6_ so it never collides with a sibling unit that
// shares this package.
//
// Equivalent / infeasible mutants (documented, no test — see the
// UNIT RESULT report for the same dispositions):
//   - diff.go:39:7  CONDITIONALS_BOUNDARY (n > lcsCellCap): boundary
//     only differs at n == 16,777,216 lines; that input is infeasible
//     under the process mem limit. hard-skip.
//   - diff.go:39:25 CONDITIONALS_BOUNDARY (m > lcsCellCap): same, at
//     m == 16,777,216 lines. hard-skip.
//   - diff.go:56:8  INCREMENT_DECREMENT (iter++): the only use of iter
//     is `iter & 0xFFFF == 0`, and (-N)&0xFFFF == 0 iff N ≡ 0 (mod
//     65536), exactly the same iterations as N&0xFFFF==0. The ctx-check
//     cadence and the function result are identical for ++ and --.
//     equivalent.
//   - events.go:287:30 ARITHMETIC_BASE (64*1024 scanner buf): the value
//     is only the bufio.Scanner *initial* buffer capacity; the scanner
//     grows up to eventLogMaxLine regardless, and the max token size is
//     max(eventLogMaxLine, cap(buf)) which is dominated by
//     eventLogMaxLine for cap 0/65536/1088/64. Read's output is
//     identical. equivalent.
//   - manager_diff.go:59:33 CONDITIONALS_BOUNDARY (compareTags > 0):
//     differs from >=0 only when compareTags == 0, which for two valid
//     existing tags only happens when from == to (or two strings that
//     are compareTags-equal). Swapping compareTags-equal tags is a
//     no-op, and every downstream lookup (filesTouchedBetween,
//     contentAtTag, contentAtOrBeforeTag) is compareTags-based, so the
//     Diff result is invariant under the swap. equivalent.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- shared helpers ------------------------------------------------

// gk_vibekit_u6_logCapture is a slog.Handler that records every
// record message it is handed. Enabled at all levels so Debug logs
// (syncDir) are captured too.
type gk_vibekit_u6_logCapture struct {
	mu   *sync.Mutex
	msgs *[]string
}

func (h gk_vibekit_u6_logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h gk_vibekit_u6_logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	*h.msgs = append(*h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}

func (h gk_vibekit_u6_logCapture) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h gk_vibekit_u6_logCapture) WithGroup(_ string) slog.Handler      { return h }

// gk_vibekit_u6_captureLogs installs a capturing handler as the slog
// default for the test and returns a predicate reporting whether any
// captured message contains substr. Restored on cleanup. Safe because
// no test in this package uses t.Parallel().
func gk_vibekit_u6_captureLogs(t *testing.T) func(substr string) bool {
	t.Helper()
	var mu sync.Mutex
	var msgs []string
	h := gk_vibekit_u6_logCapture{mu: &mu, msgs: &msgs}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
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

// gk_vibekit_u6_writeLog writes raw JSONL content to an event log's
// on-disk path (creating the parent dir).
func gk_vibekit_u6_writeLog(t *testing.T, l *eventLog, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(l.path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

// gk_vibekit_u6_jsonl returns n copies of a minimal valid event line.
func gk_vibekit_u6_jsonl(n int) string {
	return strings.Repeat(`{"type":"snapshot","ts":1,"v":1}`+"\n", n)
}

// gk_vibekit_u6_lines builds a byte buffer of n identical "x" lines.
func gk_vibekit_u6_lines(n int) []byte {
	return []byte(strings.Repeat("x\n", n))
}

// === blobs.go =====================================================

// blobs.go:181:17 CONDITIONALS_BOUNDARY — `info.Size() > contentCap`.
// A blob of EXACTLY contentCap bytes must be readable with `>` but is
// rejected with `>=`.
func Test_gk_vibekit_u6_BlobGetSizeBoundary(t *testing.T) {
	ctx := context.Background()
	b := newBlobStore(t.TempDir())
	data := make([]byte, contentCap) // exactly at the cap (16 MiB)
	h, err := b.Put(ctx, data)
	if err != nil {
		t.Fatalf("Put(contentCap bytes): %v", err)
	}
	got, err := b.Get(ctx, h)
	if err != nil {
		t.Fatalf("Get(blob of exactly contentCap bytes) = err %v, want nil (size==cap is NOT over the cap)", err)
	}
	if len(got) != contentCap {
		t.Errorf("Get(cap-sized blob) returned %d bytes, want %d", len(got), contentCap)
	}
}

// blobs.go:197:12 CONDITIONALS_NEGATION — `actual != hash` (integrity
// check). A valid blob (content hashes to its name) must NOT emit the
// integrity-failure Error log; `==` would log it on every clean read.
func Test_gk_vibekit_u6_BlobIntegrityLogsOnlyOnMismatch(t *testing.T) {
	has := gk_vibekit_u6_captureLogs(t)
	ctx := context.Background()
	b := newBlobStore(t.TempDir())
	h, err := b.Put(ctx, []byte("valid-and-self-consistent"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := b.Get(ctx, h); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if has("integrity check FAILED") {
		t.Errorf("Get(valid blob) emitted an integrity-FAILED log; original code logs only when actual != hash")
	}
}

// blobs.go:241:9 CONDITIONALS_NEGATION — syncDir's `if err != nil`
// after os.Open. On a non-existent dir the original returns early
// (Open fails) with no Debug log; the `err == nil` mutation falls
// through to Sync on a nil fd, which emits the "dir sync failed"
// Debug log.
func Test_gk_vibekit_u6_SyncDirSkipsWhenOpenFails(t *testing.T) {
	has := gk_vibekit_u6_captureLogs(t)
	syncDir(filepath.Join(t.TempDir(), "definitely-does-not-exist"))
	if has("dir sync failed") {
		t.Errorf("syncDir(missing dir) emitted 'dir sync failed'; original returns before Sync when Open fails")
	}
}

// blobs.go:244:28 CONDITIONALS_NEGATION — syncDir's `if serr != nil`
// after d.Sync(). On a valid dir whose fsync succeeds, the original
// emits no Debug log; the `serr == nil` mutation logs on success.
func Test_gk_vibekit_u6_SyncDirNoLogOnSuccessfulSync(t *testing.T) {
	dir := t.TempDir()
	// Precondition: directory fsync must succeed on this filesystem,
	// otherwise the original would also log and the assertion is moot.
	probe, err := os.Open(dir)
	if err != nil {
		t.Skipf("cannot open temp dir: %v", err)
	}
	syncErr := probe.Sync()
	_ = probe.Close()
	if syncErr != nil {
		t.Skipf("directory fsync unsupported on this fs: %v", syncErr)
	}

	has := gk_vibekit_u6_captureLogs(t)
	syncDir(dir)
	if has("dir sync failed") {
		t.Errorf("syncDir(valid dir, fsync ok) emitted 'dir sync failed'; original logs only when Sync errors")
	}
}

// === diff.go ======================================================

// diff.go:39:49 ARITHMETIC_BASE — `int64(n)*int64(m) > lcsCellCap`.
// With n==m==5000 the product (25,000,000) exceeds the cap, so the
// original short-circuits to "everything changed" = (m, n). Any other
// operator (/, +, -, %) collapses to <= cap, taking the LCS path which
// for identical content yields (0, 0).
func Test_gk_vibekit_u6_CountLineDeltaProductGateMultiplies(t *testing.T) {
	const lines = 5000 // 5000*5000 = 25,000,000 > lcsCellCap (16,777,216)
	buf := gk_vibekit_u6_lines(lines)
	added, removed := countLineDelta(context.Background(), buf, buf)
	if added != lines || removed != lines {
		t.Errorf("countLineDelta(5000 lines, identical) = (%d,%d), want (%d,%d): product gate uses multiplication so n*m exceeds the cap and returns (m,n)",
			added, removed, lines, lines)
	}
}

// diff.go:39:59 CONDITIONALS_BOUNDARY — `int64(n)*int64(m) > lcsCellCap`.
// With n==m==4096 the product equals lcsCellCap exactly. The strict `>`
// does NOT trip the gate (proceeds to LCS → (0,0) for identical input);
// `>=` would trip it and return (m,n)=(4096,4096).
func Test_gk_vibekit_u6_CountLineDeltaProductGateStrictGreater(t *testing.T) {
	const lines = 4096 // 4096*4096 = 16,777,216 == lcsCellCap exactly
	buf := gk_vibekit_u6_lines(lines)
	added, removed := countLineDelta(context.Background(), buf, buf)
	if added != 0 || removed != 0 {
		t.Errorf("countLineDelta(4096 identical lines) = (%d,%d), want (0,0): product==cap is NOT > cap, so the LCS path runs and finds zero changes",
			added, removed)
	}
}

// diff.go:57:19 CONDITIONALS_NEGATION — `if iter&0xFFFF == 0`. The
// inner ctx check fires every 65536 iterations. With a small input
// (loop < 65536 iters) and a cancelled context, the original NEVER
// reaches a check and completes normally (→ (0,0) for identical
// content). The `!= 0` mutation checks ctx on iteration 1, sees the
// cancellation, and bails early returning (m,n).
func Test_gk_vibekit_u6_CountLineDeltaCtxCheckCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	buf := gk_vibekit_u6_lines(3) // 3x3 = 9 iters, far below 65536
	added, removed := countLineDelta(ctx, buf, buf)
	if added != 0 || removed != 0 {
		t.Errorf("countLineDelta(cancelled ctx, 3 identical lines) = (%d,%d), want (0,0): the ctx check only fires at iter&0xFFFF==0, never for a 9-iteration loop, so the LCS completes",
			added, removed)
	}
}

// === events.go ====================================================

// events.go:174:27 CONDITIONALS_NEGATION — Append's leading
// `if err := ctx.Err(); err != nil`. With a live (uncancelled)
// context the event MUST be written and re-readable. The `err == nil`
// mutation returns nil early and writes nothing.
func Test_gk_vibekit_u6_AppendWritesWhenCtxLive(t *testing.T) {
	ctx := context.Background()
	l := newEventLog(t.TempDir(), "live")
	if err := l.Append(ctx, &event{Kind: kindSnapshot, Tag: "0.0", Path: "f"}); err != nil {
		t.Fatalf("Append(live ctx): %v", err)
	}
	got, err := l.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("after Append(live ctx) Read returned %d events, want 1: original writes when ctx.Err()==nil; the mutation would no-op", len(got))
	}
}

// events.go:307:14 ARITHMETIC_BASE — `if len(out)%1000 == 0`. The `%`
// makes the ctx check fire at multiples of 1000. With a cancelled
// context and 1500 events, the original returns at len==1000 with a
// non-nil error. Mutating `%` to `*` makes `len(out)*1000 == 0` never
// true (len>=1), so the ctx check is skipped and Read finishes with a
// nil error.
func Test_gk_vibekit_u6_ReadCtxCheckUsesModulo(t *testing.T) {
	l := newEventLog(t.TempDir(), "big")
	gk_vibekit_u6_writeLog(t, l, gk_vibekit_u6_jsonl(1500))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.Read(ctx)
	if err == nil {
		t.Errorf("Read(cancelled ctx, 1500 events) err = nil, want non-nil: original checks ctx at len%%1000==0 (fires at 1000) and returns the cancellation")
	}
}

// events.go:307:20 CONDITIONALS_NEGATION — `if len(out)%1000 == 0`.
// With a cancelled context and a SMALL log (5 events), the original
// never hits a 1000-multiple so it never checks ctx and returns all 5
// events with nil error. The `!= 0` mutation checks ctx on the very
// first event and bails.
func Test_gk_vibekit_u6_ReadCtxCheckOnlyAtMultiples(t *testing.T) {
	l := newEventLog(t.TempDir(), "small")
	gk_vibekit_u6_writeLog(t, l, gk_vibekit_u6_jsonl(5))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := l.Read(ctx)
	if err != nil {
		t.Fatalf("Read(cancelled ctx, 5 events) err = %v, want nil: original checks ctx only at len%%1000==0, never for 5 events", err)
	}
	if len(got) != 5 {
		t.Errorf("Read returned %d events, want 5", len(got))
	}
}

// events.go:336:14 (CONDITIONALS_NEGATION and CONDITIONALS_BOUNDARY) —
// `if len(out) > 10000`. At exactly 10000 events the original does NOT
// warn (so the path is not latched in warnedLargeLogs); both `>=` and
// `<=` would warn at 10000.
func Test_gk_vibekit_u6_LargeLogWarnNotAtBoundary(t *testing.T) {
	l := newEventLog(t.TempDir(), "exactly10k")
	gk_vibekit_u6_writeLog(t, l, gk_vibekit_u6_jsonl(10000))
	if _, err := l.Read(context.Background()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, latched := warnedLargeLogs.Load(l.path); latched {
		t.Errorf("Read(10000 events) latched the large-log warn; original warns only when len > 10000 (10000 is not > 10000)")
	}
	warnedLargeLogs.Delete(l.path)
}

// events.go:336:14 CONDITIONALS_NEGATION (`>` → `<=`) — at 10001 events
// the original DOES warn (latches the path). The `<=` mutation would
// not warn for 10001.
func Test_gk_vibekit_u6_LargeLogWarnAboveBoundary(t *testing.T) {
	l := newEventLog(t.TempDir(), "above10k")
	gk_vibekit_u6_writeLog(t, l, gk_vibekit_u6_jsonl(10001))
	if _, err := l.Read(context.Background()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, latched := warnedLargeLogs.Load(l.path); !latched {
		t.Errorf("Read(10001 events) did NOT latch the large-log warn; original warns when len > 10000")
	}
	warnedLargeLogs.Delete(l.path)
}

// events.go:351:52 CONDITIONALS_NEGATION — Wipe's
// `if err := os.RemoveAll(...); err != nil && !os.IsNotExist(err)`.
// When RemoveAll fails with a real (non-IsNotExist) error, the original
// returns it; the `err == nil` mutation short-circuits the && and
// swallows the error (returns nil). We force a RemoveAll failure by
// rooting the chat dir under a regular file (ENOTDIR, not IsNotExist).
func Test_gk_vibekit_u6_WipePropagatesRemoveError(t *testing.T) {
	base := t.TempDir()
	regular := filepath.Join(base, "afile")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatalf("write regular file: %v", err)
	}
	// Dir(l.path) = <base>/afile/chatdir; RemoveAll on it returns
	// ENOTDIR because <base>/afile is not a directory.
	l := &eventLog{path: filepath.Join(regular, "chatdir", "events.jsonl")}
	if err := l.Wipe(); err == nil {
		t.Errorf("Wipe() with an un-removable dir returned nil; original returns the RemoveAll error when it is not IsNotExist")
	}
}

// === index.go =====================================================

// index.go:108:17 CONDITIONALS_BOUNDARY — `ev.TS <= existing.ts`. On a
// timestamp tie between two different chats the incumbent keeps the
// slot (`<=` is true → return). The `<` mutation would let the
// newcomer overwrite.
func Test_gk_vibekit_u6_IndexTieKeepsIncumbent(t *testing.T) {
	idx := newCrossChatIndex()
	idx.apply("A", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "aaa", TS: 100})
	idx.apply("B", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "bbb", TS: 100}) // same ts, other chat
	if got := idx.entries["P"].chatID; got != "A" {
		t.Errorf("entries[P].chatID = %q, want %q: on a ts tie (ev.TS <= existing.ts) the incumbent chat keeps the slot", got, "A")
	}
}

// index.go:108:51 CONDITIONALS_NEGATION — `existing.chatID != chatID`.
// A same-chat update always overwrites the incumbent regardless of ts
// (the != short-circuits the early return). The `==` mutation would
// drop a same-chat update whose ts is <= the existing one.
func Test_gk_vibekit_u6_IndexSameChatAlwaysOverwrites(t *testing.T) {
	idx := newCrossChatIndex()
	idx.apply("A", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "first", TS: 100})
	idx.apply("A", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "second", TS: 50}) // same chat, OLDER ts
	if got := idx.entries["P"].expectedSHA; got != "second" {
		t.Errorf("entries[P].expectedSHA = %q, want %q: same-chat updates overwrite regardless of ts because chatID == chatID makes the != guard false", got, "second")
	}
}

// index.go:187:32 CONDITIONALS_NEGATION — removeByChatEntry's
// `if s := idx.byChat[chatID]; s != nil`. When a path's ownership
// transfers from chat A to chat B, A's byChat set must lose the path
// (and, being empty, be dropped). The `s == nil` mutation skips the
// removal for a non-nil set, leaving the path owned by both chats.
func Test_gk_vibekit_u6_IndexTransferRemovesOldOwner(t *testing.T) {
	idx := newCrossChatIndex()
	idx.apply("A", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "aaa", TS: 100})
	idx.apply("B", &event{Kind: kindSnapshot, Path: "P", AfterSHA: "bbb", TS: 200}) // newer ts, other chat
	if got := idx.entries["P"].chatID; got != "B" {
		t.Fatalf("entries[P].chatID = %q, want B (transfer precondition)", got)
	}
	if n := len(idx.byChat["A"]); n != 0 {
		t.Errorf("byChat[A] has %d paths after transfer to B, want 0: removeByChatEntry must drop the path from the prior owner's non-nil set", n)
	}
}

// === manager.go ===================================================

// manager.go:125:14 CONDITIONALS_NEGATION — ensureLoaded fast path
// `if res.err != nil`. A cached load outcome carrying an error must be
// returned. The `res.err == nil` mutation always returns nil from the
// fast path, hiding the cached error.
func Test_gk_vibekit_u6_EnsureLoadedFastPathPropagatesErr(t *testing.T) {
	m, _ := newTestManager(t, "c")
	sentinel := errors.New("gk_vibekit_u6 cached load failure")
	m.loadResult.Store(&loadOutcome{err: sentinel})
	m.mu.Lock()
	err := m.ensureLoaded(context.Background())
	m.mu.Unlock()
	if !errors.Is(err, sentinel) {
		t.Errorf("ensureLoaded fast path = %v, want the cached error: original returns res.err when res.err != nil", err)
	}
}

// manager.go:176:59 CONDITIONALS_NEGATION — ensureLoaded recovery
// `if _, recErr := m.restoreLocked(...); recErr != nil`. A dangling
// restore_started for a tag that has no snapshot makes restoreLocked
// fail with ErrTagNotFound; the original propagates it, the
// `recErr == nil` mutation swallows it (returns nil).
func Test_gk_vibekit_u6_EnsureLoadedRecoveryPropagatesErr(t *testing.T) {
	cfg := t.TempDir()
	work := t.TempDir()
	l := newEventLog(cfg, "rec")
	// restore_started for tag "9" with no matching snapshot → pending
	// restore on replay → recovery restoreLocked("9") → ErrTagNotFound.
	gk_vibekit_u6_writeLog(t, l, `{"type":"restore_started","tag":"9","ts":1,"v":1}`+"\n")
	m := newManager("rec", work, l, &managerDeps{blobs: newBlobStore(cfg), index: newCrossChatIndex()})
	m.mu.Lock()
	err := m.ensureLoaded(context.Background())
	m.mu.Unlock()
	if !errors.Is(err, ErrTagNotFound) {
		t.Errorf("ensureLoaded with dangling restore_started for a missing tag = %v, want ErrTagNotFound: original returns the recovery error", err)
	}
}

// manager.go:266:17 CONDITIONALS_BOUNDARY — readBeforeSHALocked's
// `if info.Size() > contentCap`. A pre-write file of EXACTLY contentCap
// bytes is read and stored as a blob (non-empty sha) with `>`; `>=`
// would skip it and return "".
func Test_gk_vibekit_u6_ReadBeforeSHASizeBoundary(t *testing.T) {
	m, work := newTestManager(t, "c")
	abs := filepath.Join(work, "big.txt")
	if err := os.WriteFile(abs, make([]byte, contentCap), 0o600); err != nil {
		t.Fatalf("write cap-sized file: %v", err)
	}
	sha, err := m.readBeforeSHALocked(context.Background(), "big.txt", abs)
	if err != nil {
		t.Fatalf("readBeforeSHALocked: %v", err)
	}
	if sha == "" {
		t.Errorf("readBeforeSHALocked(file of exactly contentCap bytes) = \"\", want a non-empty sha: size==cap is NOT over the cap")
	}
}

// === manager_diff.go ==============================================

// manager_diff.go:81:38 CONDITIONALS_NEGATION — Diff's skip guard
// `if fromSHA == toSHA && fromExisted == toExisted`. A file that is
// unchanged between two tags (same SHA, existed at both) is skipped by
// the original, so Diff returns it nowhere. The `fromExisted !=
// toExisted` mutation breaks the skip and emits the unchanged file as
// a (spurious) FileChange.
func Test_gk_vibekit_u6_DiffSkipsUnchangedFile(t *testing.T) {
	cfg := t.TempDir()
	work := t.TempDir()
	l := newEventLog(cfg, "c")
	// Two snapshots of "f" sharing the same beforeSHA. Diff("1","2")
	// computes fromSHA = beforeSHA@1 = "sha_x", toSHA = beforeSHA@2 =
	// "sha_x", both existed → unchanged → skipped.
	gk_vibekit_u6_writeLog(t, l,
		`{"type":"snapshot","tag":"1","path":"f","before_sha":"sha_x","after_sha":"sha_y","turn":1,"ts":1,"v":1}`+"\n"+
			`{"type":"snapshot","tag":"2","path":"f","before_sha":"sha_x","after_sha":"sha_z","turn":2,"ts":2,"v":1}`+"\n")
	m := newManager("c", work, l, &managerDeps{blobs: newBlobStore(cfg), index: newCrossChatIndex()})
	res, err := m.Diff(context.Background(), "1", "2")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("Diff over an unchanged file returned %d changes, want 0: the file has the same SHA and existed at both endpoints so it must be skipped", len(res))
	}
}

// === manager_restore.go ===========================================

// manager_restore.go:29:39 CONDITIONALS_NEGATION — commitRename's
// `if err := os.Rename(tmp, final); err != nil`. A failing rename
// (source does not exist) must be returned; the `err == nil` mutation
// swallows it and returns nil.
func Test_gk_vibekit_u6_CommitRenamePropagatesError(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "does-not-exist")
	final := filepath.Join(t.TempDir(), "final")
	if err := commitRename(tmp, final); err == nil {
		t.Errorf("commitRename(missing tmp, final) = nil, want the rename error: original returns err when Rename fails")
	}
}
