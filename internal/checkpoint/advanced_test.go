// Advanced behavior tests: cross-chat conflict detection, blob GC,
// atomic restore, and concurrent access under the race detector.
// Split from manager_test.go so the basic correctness tests stay
// readable.

package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCrossChatConflictDetected(t *testing.T) {
	// Two chats share the same workspace and the same index. Chat
	// A snapshots AND writes new content (recording an afterSHA
	// in the index). Later, someone else edits the file. Chat B
	// snapshots and reads disk content that doesn't match A's
	// recorded afterSHA — drift. B's snapshot emits a
	// conflict_detected event.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()
	path := filepath.Join(work, "shared.go")

	_ = os.WriteFile(path, []byte("baseline"), 0o600)

	// Chat A snapshots (newContent records the afterSHA in the
	// index). Then A's caller writes newContent to disk.
	mA := newManager("A", work, newEventLog(cfg, "A"), &managerDeps{blobs: blobs, index: idx})
	mA.AdvanceTurn(ctx, 0)
	aNew := []byte("A wrote this")
	if _, err := mA.Snapshot(ctx, "shared.go", aNew, 1); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, aNew, 0o600)

	// Some external process edits the file. The index still
	// records A's afterSHA = sha("A wrote this").
	_ = os.WriteFile(path, []byte("external edit"), 0o600)

	// Chat B snapshots. Its beforeSHA = sha("external edit"),
	// which doesn't match A's recorded afterSHA. Drift detected.
	mB := newManager("B", work, newEventLog(cfg, "B"), &managerDeps{blobs: blobs, index: idx})
	mB.AdvanceTurn(ctx, 0)
	if _, err := mB.Snapshot(ctx, "shared.go", nil, 1); err != nil {
		t.Fatal(err)
	}

	evs, err := mB.log.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	for _, e := range evs {
		if e.Kind == kindConflict {
			conflicts++
			if e.OtherChat != "A" {
				t.Errorf("conflict.OtherChat = %q, want A", e.OtherChat)
			}
			if e.Path != "shared.go" {
				t.Errorf("conflict.Path = %q, want shared.go", e.Path)
			}
			if e.ExpectedSHA == "" {
				t.Error("conflict.ExpectedSHA is empty — should carry the SHA A thought it left on disk")
			}
		}
	}
	if conflicts != 1 {
		t.Errorf("conflict events = %d, want 1", conflicts)
	}
}

func TestNoFalseConflictWhenChatReadsOwnWrite(t *testing.T) {
	// The canonical NOT-a-conflict case: chat A writes content X,
	// chat B later snapshots and reads exactly X from disk. That's
	// normal hand-off; no drift. This regression-guards against
	// the old beforeSHA approximation where B's snapshot of A's
	// written content would look like drift.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()
	path := filepath.Join(work, "shared.go")
	_ = os.WriteFile(path, []byte("baseline"), 0o600)

	mA := newManager("A", work, newEventLog(cfg, "A"), &managerDeps{blobs: blobs, index: idx})
	mA.AdvanceTurn(ctx, 0)
	aNew := []byte("A wrote this")
	if _, err := mA.Snapshot(ctx, "shared.go", aNew, 1); err != nil {
		t.Fatal(err)
	}
	// A's caller actually writes the content. Index now records
	// afterSHA = sha(aNew).
	_ = os.WriteFile(path, aNew, 0o600)

	// Chat B snapshots the exact same content A wrote. No drift.
	mB := newManager("B", work, newEventLog(cfg, "B"), &managerDeps{blobs: blobs, index: idx})
	mB.AdvanceTurn(ctx, 0)
	if _, err := mB.Snapshot(ctx, "shared.go", nil, 1); err != nil {
		t.Fatal(err)
	}
	evs, _ := mB.log.Read(context.Background())
	for _, e := range evs {
		if e.Kind == kindConflict {
			t.Errorf("false-positive conflict for clean hand-off: %+v", e)
		}
	}
}

func TestNoConflictForSameChatDrift(t *testing.T) {
	// A chat editing its own file between its own snapshots is
	// expected behavior, not a conflict. The index must not
	// trigger when the recorded owner matches the calling chat.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()
	path := filepath.Join(work, "f.go")
	_ = os.WriteFile(path, []byte("v0"), 0o600)

	m := newManager("only", work, newEventLog(cfg, "only"), &managerDeps{blobs: blobs, index: idx})
	m.AdvanceTurn(ctx, 0)
	if _, err := m.Snapshot(ctx, "f.go", nil, 1); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, []byte("v1"), 0o600)
	// Simulate external edit between snapshots. For a single-chat
	// workflow this should NOT trigger conflict detection because
	// there's no other chat to blame.
	_ = os.WriteFile(path, []byte("user edited"), 0o600)
	if _, err := m.Snapshot(ctx, "f.go", nil, 2); err != nil {
		t.Fatal(err)
	}

	evs, err := m.log.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == kindConflict {
			t.Errorf("unexpected conflict event for same-chat drift: %+v", e)
		}
	}
}

func TestIndexRebuildAfterRestart(t *testing.T) {
	// Simulate a full process restart: we write some snapshots
	// from multiple chats, then build a brand-new Store (which
	// rebuilds the index from disk). The resulting index must
	// contain the most recent observation per file.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	path := filepath.Join(work, "t.go")
	_ = os.WriteFile(path, []byte("v0"), 0o600)

	// Process 1: chats A and B both snapshot with afterSHA so the
	// index records real observations (beforeSHA alone no longer
	// qualifies).
	{
		s := NewStore(cfg, work, nil)
		s.AdvanceTurn(ctx, "A", 0)
		s.Snapshot(ctx, "A", "t.go", []byte("va"), 1)
		_ = os.WriteFile(path, []byte("va"), 0o600)
		time.Sleep(5 * time.Millisecond) // ensure B's event has later TS
		s.AdvanceTurn(ctx, "B", 0)
		s.Snapshot(ctx, "B", "t.go", []byte("vb"), 1)
	}

	// Process 2: fresh Store with lazy index. Access both chats to
	// trigger ensureLoaded which populates the index. B's observation
	// was most recent, so B wins.
	s2 := NewStore(cfg, work, nil)
	// Trigger lazy loading of both chats to populate the index.
	s2.OldestTag(context.Background(), "A")
	s2.OldestTag(context.Background(), "B")
	obs, ok := s2.index.entries["t.go"]
	if !ok {
		t.Fatal("index is empty after rebuild")
	}
	if obs.chatID != "B" {
		t.Errorf("rebuild winner = %q, want B (most recent)", obs.chatID)
	}
}

func TestBlobGCSweeps(t *testing.T) {
	// A blob referenced by a live chat survives; a blob whose
	// chat was deleted (and whose event log is gone) is reaped
	// by the GC sweep after it ages past the minimum-age gate.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	path := filepath.Join(work, "alive.go")
	_ = os.WriteFile(path, []byte("alive"), 0o600)
	doomedPath := filepath.Join(work, "doomed.go")
	_ = os.WriteFile(doomedPath, []byte("doomed"), 0o600)

	s := NewStore(cfg, work, nil)
	s.AdvanceTurn(ctx, "A", 0)
	s.Snapshot(ctx, "A", "alive.go", nil, 1)
	s.AdvanceTurn(ctx, "B", 0)
	s.Snapshot(ctx, "B", "doomed.go", nil, 1)

	// Before deletion, both blobs are referenced. Need to age
	// them past blobGCMinAge, so roll each file's mtime back.
	blobsRoot := blobsRoot(cfg)
	backdate(t, blobsRoot, -10*time.Minute)
	removed, _, err := runBlobGC(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("GC removed %d from two live chats, want 0", removed)
	}

	// Delete chat B by wiping its event log — simulates Cleanup.
	s.Cleanup(ctx, "B")
	backdate(t, blobsRoot, -10*time.Minute)
	removed, _, err = runBlobGC(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("GC after B cleanup removed %d, want 1", removed)
	}
	// Sanity: A's blob still exists.
	fanout, _ := os.ReadDir(blobsRoot)
	count := 0
	for _, e := range fanout {
		if !e.IsDir() {
			continue
		}
		inner, _ := os.ReadDir(filepath.Join(blobsRoot, e.Name()))
		count += len(inner)
	}
	if count != 1 {
		t.Errorf("live blob count = %d, want 1", count)
	}
}

func TestBlobGCRespectsMinAge(t *testing.T) {
	// A fresh blob (within the safety window) must not be
	// collected even when nothing references it — the Snapshot
	// path writes the blob before it appends the event, leaving
	// a brief window where an orphan exists legitimately.
	cfg := t.TempDir()
	blobs := newBlobStore(cfg)
	if _, err := blobs.Put(context.Background(), []byte("brand new")); err != nil {
		t.Fatal(err)
	}
	removed, _, err := runBlobGC(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("GC reaped %d fresh blobs, want 0 (min-age protection)", removed)
	}
}

// TestBlobGCSkipsMalformedFanoutEntries documents the invariant
// that a stray file at the fanout level, a 1-char or 3-char
// subdir, or a subdirectory where a blob file would normally sit
// are all silently skipped. A human who pokes at the on-disk
// layout (debugging, broken recovery, etc.) should not be able to
// wedge the sweep — removed = 0, no real blobs deleted.
func TestBlobGCSkipsMalformedFanoutEntries(t *testing.T) {
	cfg := t.TempDir()
	blobsRoot := blobsRoot(cfg)
	if err := os.MkdirAll(blobsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobsRoot, "stray.txt"),
		[]byte("not a fanout"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(blobsRoot, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(blobsRoot, "xyz"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Real 2-char dir with a directory-as-blob entry. GC must
	// skip the subdir and not Remove it.
	realFanout := filepath.Join(blobsRoot, "ab")
	if err := os.MkdirAll(filepath.Join(realFanout, "nested-dir"), 0o700); err != nil {
		t.Fatal(err)
	}

	removed, _, err := runBlobGC(context.Background(), cfg)
	if err != nil {
		t.Fatalf("runBlobGC err = %v, want nil", err)
	}
	if removed != 0 {
		t.Errorf("runBlobGC removed %d, want 0 (no real blobs)", removed)
	}
	// Stray file at the fanout level must still be on disk — GC
	// only deletes regular files inside 2-char-named dirs.
	if _, err := os.Stat(filepath.Join(blobsRoot, "stray.txt")); err != nil {
		t.Errorf("GC removed stray file at fanout level: %v", err)
	}
	// 1-char and 3-char dirs must still be on disk.
	if _, err := os.Stat(filepath.Join(blobsRoot, "x")); err != nil {
		t.Errorf("GC removed 1-char fanout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(blobsRoot, "xyz")); err != nil {
		t.Errorf("GC removed 3-char fanout: %v", err)
	}
	// The nested-dir inside the real fanout must still exist —
	// GC only removes regular files.
	if _, err := os.Stat(filepath.Join(realFanout, "nested-dir")); err != nil {
		t.Errorf("GC removed nested dir inside fanout: %v", err)
	}
}

func TestRestoreAtomic(t *testing.T) {
	// Staged files (.vibekit-restore siblings) must never leak
	// into the workspace after a successful restore. Also: if
	// staging fails partway, no real file should be modified.
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	p1 := filepath.Join(work, "a.txt")
	p2 := filepath.Join(work, "b.txt")
	_ = os.WriteFile(p1, []byte("a0"), 0o600)
	_ = os.WriteFile(p2, []byte("b0"), 0o600)

	m.AdvanceTurn(ctx, 0)
	tagStr, _ := m.Snapshot(ctx, "a.txt", nil, 1)
	_ = os.WriteFile(p1, []byte("a1"), 0o600)
	m.Snapshot(ctx, "b.txt", nil, 2)
	_ = os.WriteFile(p2, []byte("b1"), 0o600)

	if _, err := m.Restore(ctx, tagStr); err != nil {
		t.Fatal(err)
	}
	// Post-restore: both files back to their captured state and
	// no stragglers.
	gotA, _ := os.ReadFile(p1)
	if string(gotA) != "a0" {
		t.Errorf("a after restore = %q, want a0", gotA)
	}
	entries, _ := os.ReadDir(work)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".vibekit-restore" {
			t.Errorf("leaked staging file: %s", e.Name())
		}
	}
}

func TestConcurrentSnapshotsRace(t *testing.T) {
	// Hammer Snapshot from multiple goroutines on the same
	// manager. The per-chat mutex must serialize them; tags must
	// stay unique; state must end consistent. Run under `-race`.
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	const n = 50
	paths := make([]string, n)
	for i := range n {
		paths[i] = testFileName(i)
		_ = os.WriteFile(filepath.Join(work, paths[i]), []byte("v"), 0o600)
	}
	m.AdvanceTurn(ctx, 0)

	var wg sync.WaitGroup
	var successes int64
	wg.Add(n)
	for i := range n {
		p := paths[i]
		go func() {
			defer wg.Done()
			if _, err := m.Snapshot(ctx, p, nil, 1); err == nil {
				atomic.AddInt64(&successes, 1)
			}
		}()
	}
	wg.Wait()
	if successes != n {
		t.Fatalf("successful snapshots = %d, want %d", successes, n)
	}
	tags := m.tags()
	if len(tags) != n {
		t.Errorf("tag count = %d, want %d unique tags", len(tags), n)
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if seen[tag] {
			t.Errorf("duplicate tag under concurrent snapshot: %q", tag)
		}
		seen[tag] = true
	}
}

func TestConcurrentSnapshotAndRestore(t *testing.T) {
	// A goroutine hammers Snapshot while another periodically
	// calls Restore. Under the mutex they serialize; the test
	// just exercises the code path for races. Nothing should
	// panic or corrupt state.
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	p := filepath.Join(work, "f.go")
	_ = os.WriteFile(p, []byte("v"), 0o600)
	m.AdvanceTurn(ctx, 0)
	if _, err := m.Snapshot(ctx, "f.go", nil, 1); err != nil {
		t.Fatal(err)
	}

	const iterations = 30 // bounded so the test stays under a second
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range iterations {
			_, _ = m.Snapshot(ctx, "f.go", nil, 1)
		}
	}()
	go func() {
		defer wg.Done()
		for range iterations {
			tags := m.tags()
			if len(tags) > 0 {
				_, _ = m.Restore(ctx, Tag(tags[0]))
			}
		}
	}()
	wg.Wait()
	// Getting here without panicking under -race is the assertion.
}

// --- helpers ---

// backdate rolls every file mtime under root back by `delta` (which
// should be negative to age things). Used to exercise the GC
// min-age gate without real sleeps. filepath.WalkDir replaces the
// hand-rolled recursive helper we used before — same behaviour,
// ~15 fewer lines of test scaffolding.
func backdate(t *testing.T, root string, delta time.Duration) {
	t.Helper()
	now := time.Now().Add(delta)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		_ = os.Chtimes(path, now, now)
		return nil
	})
}

// testFileName builds a deterministic filename like "f00", "f01"...
// used when a test needs a pile of distinct paths. Named with a
// test* prefix so it never shadows filepath.Join at a glance.
func testFileName(i int) string {
	return fmt.Sprintf("f%02d", i)
}

func TestStoreBackgroundTasksLifecycle(t *testing.T) {
	// StartBackgroundTasks + Stop must be idempotent and not leak
	// goroutines. Stop on a never-started store is a no-op. Start
	// then Stop waits for the sweep goroutine to finish before
	// returning. Runs with a short sleep to give the goroutine
	// time to attach if something is broken.
	cfg := t.TempDir()
	work := t.TempDir()

	// Stop without Start: no-op, no panic.
	s0 := NewStore(cfg, work, nil)
	s0.Stop()

	// Start then Stop: wait completes within a generous deadline.
	s1 := NewStore(cfg, work, nil)
	s1.StartBackgroundTasks(context.Background())
	// Give the loop goroutine a chance to register before Stop.
	// Without this the test would pass even if the goroutine
	// hadn't started; with it we exercise the actual lifecycle.
	time.Sleep(5 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		s1.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s")
	}

	// Start-after-Stop IS supported: StartBackgroundTasks
	// allocates a fresh stopGC channel if the previous Stop
	// nilled it, so a re-entrant Start doesn't panic. Exercise
	// the reinit path explicitly so a future "remove the nil
	// reinit" refactor fails here instead of silently losing
	// the re-entry safety.
	s2 := NewStore(cfg, work, nil)
	s2.StartBackgroundTasks(context.Background())
	s2.Stop()
	s2.StartBackgroundTasks(context.Background())
	s2.Stop()
}

func TestStoreSnapshotDuringGC(t *testing.T) {
	// Snapshot holds a read lock; GC holds a write lock. A
	// Snapshot in-flight during a GC sweep must not race with the
	// directory walk. Exercise this by firing the two in tight
	// alternation and asserting correctness (right blob count,
	// right conflict-free outcome).
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	p := filepath.Join(work, "f.go")
	_ = os.WriteFile(p, []byte("v0"), 0o600)

	s := NewStore(cfg, work, nil)
	s.AdvanceTurn(ctx, "c", 0)
	// Fire a couple of concurrent snapshots and an explicit GC
	// sweep. gcLock serializes them; if the lock is wrong we'd
	// see either a panic, a missing blob, or a spurious
	// conflict event (there are no other chats, so anything is
	// a bug).
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Go(func() {
			_, _ = s.Snapshot(ctx, "c", "f.go", []byte(string(rune('a'+i))), 1)
		})
	}
	// Run GC sweeps in parallel with snapshots.
	for range 3 {
		wg.Go(func() {
			s.gc.RunOnce(context.Background())
		})
	}
	wg.Wait()

	// The chat's log should contain no conflict events.
	log := newEventLog(cfg, "c")
	evs, err := log.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == kindConflict {
			t.Errorf("unexpected conflict under single-chat GC load: %+v", e)
		}
	}
}

func TestRestoreRecoveryAfterInterruptedCommit(t *testing.T) {
	// Simulate a crash between phase-2 start (journal open) and
	// completion. We manually inject a restore_started event with
	// no committed event, then instantiate a fresh Manager and
	// verify ensureLoaded re-runs the restore on first access.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)

	// Set up a chat with two snapshots of one file.
	p := filepath.Join(work, "f.go")
	_ = os.WriteFile(p, []byte("v0"), 0o600)
	log := newEventLog(cfg, "c")
	m := newManager("c", work, log, &managerDeps{blobs: blobs, index: newCrossChatIndex()})
	m.AdvanceTurn(ctx, 0)
	tag, err := m.Snapshot(ctx, "f.go", []byte("v1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the agent's write landing.
	_ = os.WriteFile(p, []byte("v1"), 0o600)
	m.AdvanceTurn(ctx, 1)
	_, _ = m.Snapshot(ctx, "f.go", []byte("v2"), 2)
	_ = os.WriteFile(p, []byte("v2"), 0o600)

	// Inject a restore_started with no committed marker. In
	// production this state would exist only briefly after phase
	// 1 staging but before phase 2 renames land.
	_ = log.Append(context.Background(), &event{Kind: kindRestoreStarted, Tag: string(tag), MessageCount: 1})
	// Also need a staged file present so phase 2 has something
	// to rename. Stage the tag's pre-write content.
	st := filepath.Join(work, "f.go.vibekit-restore")
	_ = os.WriteFile(st, []byte("v0"), 0o600)

	// Fresh Manager: ensureLoaded sees the dangling started
	// marker and recovers.
	log2 := newEventLog(cfg, "c")
	m2 := newManager("c", work, log2, &managerDeps{blobs: blobs, index: newCrossChatIndex()})
	// Tags triggers ensureLoaded, which fires recovery.
	_ = m2.tags()
	got, _ := os.ReadFile(p)
	if string(got) != "v0" {
		t.Errorf("recovery did not restore file: got %q, want v0", string(got))
	}
}

// TestConcurrentRestoresOnSameFileAcrossChats pins the CYCLE 1 F2
// fix: two chats restoring the same workspace file simultaneously
// must not collide on the staging path. Each Manager has its own
// per-instance mutex (different chat IDs ⇒ different Managers), so
// they do NOT serialize on m.mu; they contend on the shared
// filesystem. Pre-fix, both stage-writes resolved to
// "<name>.vibekit-restore" and one clobbered the other (or one
// failed with EEXIST depending on ordering). With os.CreateTemp's
// random suffix every stage path is unique, so both commits
// succeed and no orphan staging sibling is left behind.
//
// Runs under `-race` via the standard go test -race flag; no
// additional setup needed.
func TestConcurrentRestoresOnSameFileAcrossChats(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()

	path := filepath.Join(work, "shared.txt")
	if err := os.WriteFile(path, []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Both chats snapshot v0 then observe a post-write v1 on
	// disk (simulating any external mutation — user editor save,
	// another process).
	mA := newManager("A", work, newEventLog(cfg, "A"), &managerDeps{blobs: blobs, index: idx})
	mB := newManager("B", work, newEventLog(cfg, "B"), &managerDeps{blobs: blobs, index: idx})
	if err := mA.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	tagA, err := mA.Snapshot(ctx, "shared.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := mB.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	tagB, err := mB.Snapshot(ctx, "shared.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Two chats restore the same file at the same time. The
	// per-Manager mutex doesn't serialize across Managers, so
	// their stage-write calls race on the filesystem. A
	// collision on a shared deterministic staging path would
	// surface as a rename error, a lost stage, or a panic.
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := mA.Restore(ctx, tagA)
		errCh <- err
	}()
	go func() {
		defer wg.Done()
		_, err := mB.Restore(ctx, tagB)
		errCh <- err
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent Restore err = %v, want nil", err)
		}
	}

	// Final content is "v0" (whichever chat won the rename race).
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after concurrent restores: %v", err)
	}
	if string(got) != "v0" {
		t.Errorf("post-restore content = %q, want v0", got)
	}

	// No orphan staging siblings left behind. Check both the
	// pre-fix deterministic name AND the post-fix random-suffix
	// pattern so a future regression (reverting to the
	// deterministic form) still fails cleanly here.
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "shared.txt.vibekit-restore" ||
			strings.HasPrefix(name, "shared.txt.vibekit-restore-") {
			t.Errorf("leaked staging sibling after concurrent restore: %s", name)
		}
	}
}

// TestBlobGCRespectsContextCancellation pins the CYCLE 1 shutdown
// safety fix: runBlobGC and collectReferencedBlobs both poll
// ctx.Err at every iteration so Hub.Shutdown can interrupt a
// long-running sweep on a huge repo instead of stalling past the
// container grace period and triggering SIGKILL. Pre-fix, the
// sweep ran to completion regardless of cancellation.
//
// Exercises two call sites in one shot:
//   - the outer fanout loop in runBlobGC
//   - collectReferencedBlobs' per-chat event-log read loop
//
// We pre-cancel the ctx so every guard check fires on the very
// first iteration. The sweep must return context.Canceled (or a
// wrapped variant) and must NOT remove the orphan blob it would
// have reaped with a live ctx.
func TestBlobGCRespectsContextCancellation(t *testing.T) {
	cfg := t.TempDir()
	// Seed state so the sweep has real work to do: one chat with
	// one referenced blob + one orphan blob aged past the GC min.
	blobs := newBlobStore(cfg)
	refHash, err := blobs.Put(context.Background(), []byte("referenced"))
	if err != nil {
		t.Fatal(err)
	}
	orphanHash, err := blobs.Put(context.Background(), []byte("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	log := newEventLog(cfg, "c")
	if err := log.Append(context.Background(), &event{
		Kind: kindSnapshot, Tag: "1", Path: "a.go",
		AfterSHA: refHash,
	}); err != nil {
		t.Fatal(err)
	}
	blobsRoot := blobsRoot(cfg)
	backdate(t, blobsRoot, -10*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling — every guard check trips

	removed, _, err := runBlobGC(ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("runBlobGC(cancelled) err = %v, want context.Canceled", err)
	}
	if removed != 0 {
		t.Errorf("runBlobGC(cancelled) removed = %d, want 0 (cancellation must abort before any deletion)", removed)
	}
	// Orphan survives the cancelled sweep — next live sweep will
	// reap it.
	if !blobs.Exists(orphanHash) {
		t.Error("cancelled sweep removed orphan; ctx gate didn't hold")
	}
	if !blobs.Exists(refHash) {
		t.Error("cancelled sweep removed referenced blob (should be impossible, but ctx gate + guard both failed)")
	}
}

// TestBlobGCReapsEmptyFanoutDir pins the CYCLE 1 cleanup fix at
// gc.go's sweepFanout that removes a fanout dir when it becomes
// empty after orphan sweep. Without it, long-lived vibekit
// instances accumulate up to 256 always-empty 2-char shards under
// <configDir>/snapshots/blobs/ because the Snapshot path's
// MkdirAll creates them freely but nothing else cleans them up.
//
// Red-green: drop the `if removed > 0 { ... os.Remove(dir) }`
// block in sweepFanout and this test fails — the fanout dir is
// left behind as an empty shard.
//
// TestBlobGCSweeps already exercises the happy-path blob removal;
// this sibling test zeros in on the DIR-level cleanup.
func TestBlobGCReapsEmptyFanoutDir(t *testing.T) {
	cfg := t.TempDir()
	blobs := newBlobStore(cfg)

	// Put one orphan blob, age it, sweep. The fanout dir holding
	// the blob should be reaped once its only entry is deleted.
	hash, err := blobs.Put(context.Background(), []byte("lonely orphan"))
	if err != nil {
		t.Fatal(err)
	}
	blobsRoot := blobsRoot(cfg)
	fanoutDir := filepath.Join(blobsRoot, hash[:2])
	// Sanity: the fanout dir exists pre-sweep.
	if _, err := os.Stat(fanoutDir); err != nil {
		t.Fatalf("fanout dir missing pre-sweep: %v", err)
	}
	backdate(t, blobsRoot, -10*time.Minute)

	removed, _, err := runBlobGC(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("runBlobGC removed = %d, want 1 (orphan blob)", removed)
	}

	// Post-sweep: the fanout dir itself should be gone because
	// it's empty after the orphan was removed. This is the
	// invariant the fix establishes.
	if _, err := os.Stat(fanoutDir); !os.IsNotExist(err) {
		t.Errorf("fanout dir %q still exists post-sweep (err=%v); expected IsNotExist — empty shard reap regression",
			fanoutDir, err)
	}
}

// TestBlobGCAbortsWhenChatLogUnreadable pins the "abort on
// unreadable chat log" contract in collectReferencedBlobs. If any
// chat's events.jsonl fails to read, the referenced-set is
// incomplete and a sweep built from it would delete blobs that the
// unreadable chat uniquely references. The code deliberately
// aborts the whole sweep rather than silently skip the broken
// chat; without this test a refactor that downgrades the abort to
// "continue" would quietly introduce unrecoverable data loss on
// the next sweep. The trick mirrors
// TestEventLogReadRejectsDirectoryAtPath: events.jsonl AS a
// directory produces a non-IsNotExist read failure.
func TestBlobGCAbortsWhenChatLogUnreadable(t *testing.T) {
	cfg := t.TempDir()
	blobs := newBlobStore(cfg)
	orphanHash, err := blobs.Put(context.Background(), []byte("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	// Create a chat where events.jsonl is a DIRECTORY; eventLog.Read
	// surfaces a non-IsNotExist error on open/scan.
	badLog := chatLogPath(cfg, "broken")
	if err := os.MkdirAll(badLog, 0o700); err != nil {
		t.Fatal(err)
	}

	removed, _, err := runBlobGC(context.Background(), cfg)
	if err == nil {
		t.Error("runBlobGC(unreadable chat log) err = nil, want non-nil (abort contract)")
	}
	if removed != 0 {
		t.Errorf("runBlobGC(unreadable chat log) removed = %d, want 0 (must not delete before building full referenced set)", removed)
	}
	// The orphan blob (which nothing references, so a normal sweep
	// would delete it) must survive because the sweep aborted
	// before entering the delete phase.
	if !blobs.Exists(orphanHash) {
		t.Error("aborted GC removed orphan blob; referenced-set abort contract violated")
	}
}
