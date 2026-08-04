package archive

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// TestPurge_RetentionCutoff is the core retention contract: files whose
// mtime is older than (now - maxAge) are purged; files newer than the
// cutoff survive. A bug that inverts this deletes live data.
func TestPurge_RetentionCutoff(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	olderPath := writeAgedChat(t, dir, "older", 25*time.Hour)
	newerPath := writeAgedChat(t, dir, "newer", 23*time.Hour)

	svc.Purge(context.Background(), 24*time.Hour)

	if exists(t, olderPath) {
		t.Errorf("file older than cutoff (25h > 24h) survived: %s", olderPath)
	}
	if !exists(t, newerPath) {
		t.Errorf("file newer than cutoff (23h < 24h) was purged: %s", newerPath)
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"older"}) {
		t.Errorf("onPurge fired for %v, want [older]", got)
	}
}

// TestPurge_SkipsNonChatFiles verifies Purge only touches files that are
// valid chat .json files: non-.json files, files with invalid chat ids,
// and subdirectories are left untouched even when old.
func TestPurge_SkipsNonChatFiles(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	chatPath := writeAgedChat(t, dir, "valid01", 48*time.Hour)

	// Non-.json file (old): must survive.
	notesPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(notesPath, old, old); err != nil {
		t.Fatalf("chtimes notes: %v", err)
	}

	// .json file with an invalid chat id ('.' is not allowed): must survive.
	badIDPath := filepath.Join(dir, "bad.id.json")
	if err := os.WriteFile(badIDPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write bad-id file: %v", err)
	}
	if err := os.Chtimes(badIDPath, old, old); err != nil {
		t.Fatalf("chtimes bad-id: %v", err)
	}

	// Directory with a .json suffix: must survive (IsDir guard).
	dirPath := filepath.Join(dir, "skipdir.json")
	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		t.Fatalf("mkdir skipdir: %v", err)
	}

	svc.Purge(context.Background(), 24*time.Hour)

	if exists(t, chatPath) {
		t.Errorf("valid old chat was not purged: %s", chatPath)
	}
	if !exists(t, notesPath) {
		t.Errorf("non-.json file was purged: %s", notesPath)
	}
	if !exists(t, badIDPath) {
		t.Errorf("invalid-id .json file was purged: %s", badIDPath)
	}
	if !exists(t, dirPath) {
		t.Errorf("subdirectory was purged: %s", dirPath)
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"valid01"}) {
		t.Errorf("onPurge fired for %v, want [valid01]", got)
	}
}

// TestPurge_EmptyAndMissingDir verifies Purge is a safe no-op when the
// chat directory is empty or absent.
func TestPurge_EmptyAndMissingDir(t *testing.T) {
	t.Run("empty chat dir", func(t *testing.T) {
		var rec purgeRecorder
		svc, _, _ := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
		svc.Purge(context.Background(), 24*time.Hour)
		if got := rec.sorted(); len(got) != 0 {
			t.Errorf("onPurge fired %v on empty dir, want none", got)
		}
	})

	t.Run("missing chat dir", func(t *testing.T) {
		var rec purgeRecorder
		dir := t.TempDir() // never created
		svc := New(newFakeStore(dir), WithOnPurge(rec.recordPurge))
		svc.Purge(context.Background(), 24*time.Hour)
		if got := rec.sorted(); len(got) != 0 {
			t.Errorf("onPurge fired %v on missing dir, want none", got)
		}
	})
}

// TestPurge_NilOnPurgeCallback verifies Purge does not panic when no
// onPurge callback is registered.
func TestPurge_NilOnPurgeCallback(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	chatPath := writeAgedChat(t, dir, "nocb", 48*time.Hour)

	svc.Purge(context.Background(), 24*time.Hour)

	if exists(t, chatPath) {
		t.Errorf("old chat survived purge with nil callback: %s", chatPath)
	}
}

// TestPurgeScheduler_InitialEvaluationPurges verifies Start runs an
// initial purge evaluation that removes an over-retention chat.
func TestPurgeScheduler_InitialEvaluationPurges(t *testing.T) {
	purged := make(chan api.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id api.ChatID, _ []string) { purged <- id }))
	writeAgedChat(t, dir, "sched1", 48*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start()
	defer sched.Stop()

	if got := recvWithin(t, purged, 3*time.Second); got != "sched1" {
		t.Errorf("initial evaluation purged %q, want sched1", got)
	}
}

// TestPurgeScheduler_ReArmsAndProcessesSecondTrigger verifies the loop
// keeps processing triggers after the first pass (it is not one-shot).
func TestPurgeScheduler_ReArmsAndProcessesSecondTrigger(t *testing.T) {
	purged := make(chan api.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id api.ChatID, _ []string) { purged <- id }))
	writeAgedChat(t, dir, "first", 48*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start()
	defer sched.Stop()

	if got := recvWithin(t, purged, 3*time.Second); got != "first" {
		t.Fatalf("first pass purged %q, want first", got)
	}

	// A chat added after the first pass is purged on the next trigger.
	writeAgedChat(t, dir, "second", 48*time.Hour)
	sched.Trigger()
	if got := recvWithin(t, purged, 3*time.Second); got != "second" {
		t.Errorf("second trigger purged %q, want second", got)
	}
}

// TestPurgeScheduler_ZeroRetentionSkipsPurge verifies a retention of 0
// ("keep forever") disables purging entirely. A bug here would delete
// every chat, at any age.
func TestPurgeScheduler_ZeroRetentionSkipsPurge(t *testing.T) {
	purged := make(chan api.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id api.ChatID, _ []string) { purged <- id }))
	chatPath := writeAgedChat(t, dir, "keepforever", 9000*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 0 })
	sched.Start()
	sched.Stop() // waits for the loop goroutine to finish its cycle and exit

	select {
	case id := <-purged:
		t.Errorf("retention 0 purged %q, want nothing purged", id)
	default:
	}
	if !exists(t, chatPath) {
		t.Errorf("retention 0 deleted a chat: %s", chatPath)
	}
}

// TestPurgeScheduler_StopClosesDone verifies Stop drains the scheduler
// goroutine (its done channel closes) and is safe to call more than once.
func TestPurgeScheduler_StopClosesDone(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start()
	sched.Stop()

	select {
	case <-sched.done:
	default:
		t.Error("done channel not closed after Stop()")
	}

	sched.Stop() // idempotent: must not panic
}

// TestPurgeScheduler_ContextCancellationStopsLoop pins the loop's OTHER
// exit path: cancelling the context the scheduler was built with must
// drain the goroutine without any Stop() call (Stop closes stopCh, which
// is a different select arm — asserting through it would pass even if the
// ctx arm were gone).
func TestPurgeScheduler_ContextCancellationStopsLoop(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	sched := NewPurgeScheduler(ctx, svc, func() time.Duration { return 24 * time.Hour })
	sched.Start()

	cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-sched.done:
	case <-timer.C:
		t.Fatal("scheduler goroutine did not exit after context cancellation")
	}
}

// TestPurgeScheduler_TriggerAfterStopIsNoop verifies Trigger is a safe
// no-op once the scheduler is stopped.
func TestPurgeScheduler_TriggerAfterStopIsNoop(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start()
	sched.Stop()
	sched.Trigger() // must return without panic
}

// TestPurgeScheduler_StopWithoutStart verifies Stop is safe before Start
// (it must not block waiting on a goroutine that never launched).
func TestPurgeScheduler_StopWithoutStart(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Stop()
}

// TestOldestChatMTime covers the helper that the scheduler uses to
// time its next wake-up.
func TestOldestChatMTime(t *testing.T) {
	t.Run("missing dir returns false", func(t *testing.T) {
		dir := t.TempDir() // never created
		if _, ok := OldestChatMTime(context.Background(), dir); ok {
			t.Error("ok = true for missing chat dir, want false")
		}
	})

	t.Run("empty dir returns false", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if _, ok := OldestChatMTime(context.Background(), dir); ok {
			t.Error("ok = true for empty chat dir, want false")
		}
	})

	t.Run("returns mtime of oldest chat file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		oldestPath := writeAgedChat(t, dir, "oldest", 72*time.Hour)
		writeAgedChat(t, dir, "middle", 48*time.Hour)
		writeAgedChat(t, dir, "newest", 24*time.Hour)

		info, err := os.Stat(oldestPath)
		if err != nil {
			t.Fatalf("stat oldest: %v", err)
		}
		got, ok := OldestChatMTime(context.Background(), dir)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if !got.Equal(info.ModTime()) {
			t.Errorf("oldest mtime = %v, want %v", got, info.ModTime())
		}
	})

	t.Run("ignores non-chat files and dirs", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// An old non-.json file that must be ignored.
		txt := filepath.Join(dir, "old.txt")
		if err := os.WriteFile(txt, []byte("x"), 0o600); err != nil {
			t.Fatalf("write txt: %v", err)
		}
		ancient := time.Now().Add(-1000 * time.Hour)
		if err := os.Chtimes(txt, ancient, ancient); err != nil {
			t.Fatalf("chtimes txt: %v", err)
		}
		chatPath := writeAgedChat(t, dir, "thechat", 24*time.Hour)

		info, err := os.Stat(chatPath)
		if err != nil {
			t.Fatalf("stat chat: %v", err)
		}
		got, ok := OldestChatMTime(context.Background(), dir)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if !got.Equal(info.ModTime()) {
			t.Errorf("oldest mtime = %v, want the .json file's mtime %v (the older .txt must be ignored)", got, info.ModTime())
		}
	})

	t.Run("cancelled context returns false", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeAgedChat(t, dir, "present", 24*time.Hour)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, ok := OldestChatMTime(ctx, dir); ok {
			t.Error("ok = true for cancelled context, want false")
		}
	})
}

// recvWithin receives one chat ID from ch or fails after d.
func recvWithin(t *testing.T, ch <-chan api.ChatID, d time.Duration) api.ChatID {
	t.Helper()
	select {
	case id := <-ch:
		return id
	case <-time.After(d):
		t.Fatalf("timeout waiting for purge callback")
		return ""
	}
}

// TestPurge_SkipsVanishedEntryWithoutAborting verifies a directory entry
// that is listed by ReadDir but fails to stat (it vanished between the
// scan and the per-file stat — a dangling symlink reproduces this
// deterministically) is treated as "skipped": Purge neither counts nor
// removes it, must not panic on it, and still purges the genuinely-old
// chats alongside it.
func TestPurge_SkipsVanishedEntryWithoutAborting(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	// A dangling symlink with a valid chat-id .json name: ReadDir lists
	// it, but os.Stat (which follows the link) fails with ErrNotExist —
	// exactly the "entry disappeared mid-scan" path purgeOne must skip.
	vanished := filepath.Join(dir, "vanished.json")
	if err := os.Symlink(filepath.Join(dir, "no-such-target.json"), vanished); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	realOld := writeAgedChat(t, dir, "realold", 48*time.Hour)

	svc.Purge(context.Background(), 24*time.Hour)

	if exists(t, realOld) {
		t.Errorf("a genuinely-old chat was not purged when a vanished sibling entry was present: %s", realOld)
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"realold"}) {
		t.Errorf("onPurge fired for %v, want [realold] (the vanished entry must be skipped, not counted)", got)
	}
}

// TestPurgeScheduler_RescheduleWithZeroRetentionDoesNotPurge verifies the
// reschedule cycle does not run a purge when retention is 0 ("keep
// forever"). Exercises purgeAndReschedule directly so the assertion is
// deterministic (no goroutine/stop race): a retention of 0 must skip the
// Purge call entirely, leaving even ancient chats in place.
func TestPurgeScheduler_RescheduleWithZeroRetentionDoesNotPurge(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
	chatPath := writeAgedChat(t, dir, "keepforever", 9000*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 0 })
	timer, _ := sched.purgeAndReschedule()
	if timer != nil {
		timer.Stop()
	}

	if !exists(t, chatPath) {
		t.Errorf("retention 0 purged a chat during reschedule: %s", chatPath)
	}
	if got := rec.sorted(); len(got) != 0 {
		t.Errorf("retention 0 fired onPurge for %v, want nothing purged", got)
	}
}

// TestPurgeScheduler_NextWaitZeroRetentionReturnsFalse verifies nextWait
// reports "nothing to schedule" (ok=false) when retention is 0, even with
// a non-empty chat dir. Retention 0 means keep-forever, so the scheduler
// must not arm a timer.
func TestPurgeScheduler_NextWaitZeroRetentionReturnsFalse(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	writeAgedChat(t, dir, "present", 1*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })

	if _, ok := sched.nextWait(0); ok {
		t.Error("nextWait(0) ok = true, want false: retention 0 disables scheduling")
	}
}

// TestPurgeScheduler_NextWaitPositiveRetentionReturnsTrue verifies that
// with a positive retention and a non-empty chat dir nextWait schedules a
// wake-up (ok=true). The complement of the zero-retention case: a
// positive retention must arm the next purge.
func TestPurgeScheduler_NextWaitPositiveRetentionReturnsTrue(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	writeAgedChat(t, dir, "present", 1*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })

	if _, ok := sched.nextWait(24 * time.Hour); !ok {
		t.Error("nextWait(24h) ok = false, want true: a non-empty chat dir with positive retention must schedule a wake-up")
	}
}

// TestPurgeScheduler_NextWaitFloorsAtMinWait verifies that when the oldest
// chat file's retention deadline is already well in the past, nextWait
// floors the returned delay at the 5s minimum rather than a non-positive
// value (which would busy-spin the scheduler goroutine).
func TestPurgeScheduler_NextWaitFloorsAtMinWait(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	// Oldest file is 100h old; with a 1h retention its deadline is ~99h
	// in the past, so time.Until(deadline) is strongly negative and the
	// result must clamp to the minWait floor.
	writeAgedChat(t, dir, "ancient", 100*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 1 * time.Hour })

	wait, ok := sched.nextWait(1 * time.Hour)
	if !ok {
		t.Fatal("nextWait ok = false, want true for a non-empty chat dir")
	}
	if wait != 5*time.Second {
		t.Errorf("nextWait floored to %v, want 5s: a deadline in the past must clamp to minWait, not 0", wait)
	}
}

// TestPurge_HandsTheSessionChainToOnPurge pins that a purge reaps its OWN
// session directories rather than leaving them to the orphan sweep.
//
// The ordering is the whole point: onPurge fires AFTER os.Remove(entry.path),
// so the chain has to be read before the file goes or the ids are unrecoverable
// — which is why the callback used to receive only a chat id and the purge could
// clean up nothing but checkpoints. With DefaultChatRetentionDays at 1, nearly
// every session directory would otherwise become an orphan within a day, making
// the sweep the primary retention mechanism instead of a residue collector.
func TestPurge_HandsTheSessionChainToOnPurge(t *testing.T) {
	var rec purgeRecorder
	svc, store, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	// A chat that ran on two sessions before falling out of the window.
	chatPath := writeAgedChat(t, dir, "chained", 48*time.Hour)
	// purgeReferenceTime reads the chat through the STORE now (chats no longer
	// live in a separate directory the purge package parses itself), so the
	// chain has to come from the fake's Load.
	store.loadResult = &api.Chat{
		ID:                 "chained",
		Name:               "C",
		ACPSessionID:       "sess_new",
		PriorACPSessionIDs: []string{"sess_old"},
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(chatPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	svc.Purge(context.Background(), 24*time.Hour)

	if exists(t, chatPath) {
		t.Fatalf("expired chat survived the purge: %s", chatPath)
	}
	got := rec.chainFor("chained")
	want := []string{"sess_old", "sess_new"}
	if !slices.Equal(got, want) {
		t.Errorf("onPurge received chain %v, want %v — the purge cannot reap what it was not told about", got, want)
	}
}

// TestPurge_NeverPurgesALiveChat pins the hard rule that makes purging the main
// chat directory safe at all.
//
// Chats no longer move to an archive directory, so the purge now scans the same
// directory live chats live in. Age alone is therefore NOT sufficient grounds to
// delete: a conversation someone has had open for weeks is older than any
// retention window, and deleting it out from under its own tab is never what a
// retention setting meant. The exemption is the only thing separating "abandoned
// work" from "work in progress".
func TestPurge_NeverPurgesALiveChat(t *testing.T) {
	var rec purgeRecorder
	live := map[api.ChatID]bool{"open": true}
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(rec.recordPurge),
		WithLiveChats(func(id api.ChatID) bool { return live[id] }),
	)

	// Both are far past the window; only one is in use.
	openPath := writeAgedChat(t, dir, "open", 72*time.Hour)
	abandonedPath := writeAgedChat(t, dir, "abandoned", 72*time.Hour)

	svc.Purge(context.Background(), 24*time.Hour)

	if !exists(t, openPath) {
		t.Error("a LIVE chat was purged for being old — retention deleted work in progress")
	}
	if exists(t, abandonedPath) {
		t.Error("an abandoned chat past the window survived")
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"abandoned"}) {
		t.Errorf("onPurge fired for %v, want [abandoned]", got)
	}
}

// TestPurge_WithoutTheLivePredicateStillPurges guards the wiring's fail mode.
// isLive is injected, so a construction path that forgets it must degrade to
// age-only purging rather than panicking on a nil call.
func TestPurge_WithoutTheLivePredicateStillPurges(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
	p := writeAgedChat(t, dir, "old", 72*time.Hour)

	svc.Purge(context.Background(), 24*time.Hour)

	if exists(t, p) {
		t.Error("purge did nothing with no live predicate wired")
	}
}
