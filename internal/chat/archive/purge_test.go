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
	svc, _, archiveDir := newArchiveTestService(t, WithOnPurge(rec.record))

	olderPath := writeArchivedChat(t, archiveDir, "older", 25*time.Hour)
	newerPath := writeArchivedChat(t, archiveDir, "newer", 23*time.Hour)

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

// TestPurge_RemovesPlanDraft verifies the companion plan-draft file is
// removed alongside a purged chat.
func TestPurge_RemovesPlanDraft(t *testing.T) {
	svc, _, archiveDir := newArchiveTestService(t)

	chatPath := writeArchivedChat(t, archiveDir, "withdraft", 48*time.Hour)
	draftPath := filepath.Join(archiveDir, "withdraft"+planDraftSuffix)
	if err := os.WriteFile(draftPath, []byte("# plan"), 0o600); err != nil {
		t.Fatalf("write plan draft: %v", err)
	}

	svc.Purge(context.Background(), 24*time.Hour)

	if exists(t, chatPath) {
		t.Errorf("purged chat file survived: %s", chatPath)
	}
	if exists(t, draftPath) {
		t.Errorf("plan draft survived purge: %s", draftPath)
	}
}

// TestPurge_SkipsNonChatFiles verifies Purge only touches files that are
// valid chat .json files: non-.json files, files with invalid chat ids,
// and subdirectories are left untouched even when old.
func TestPurge_SkipsNonChatFiles(t *testing.T) {
	var rec purgeRecorder
	svc, _, archiveDir := newArchiveTestService(t, WithOnPurge(rec.record))

	chatPath := writeArchivedChat(t, archiveDir, "valid01", 48*time.Hour)

	// Non-.json file (old): must survive.
	notesPath := filepath.Join(archiveDir, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(notesPath, old, old); err != nil {
		t.Fatalf("chtimes notes: %v", err)
	}

	// .json file with an invalid chat id ('.' is not allowed): must survive.
	badIDPath := filepath.Join(archiveDir, "bad.id.json")
	if err := os.WriteFile(badIDPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write bad-id file: %v", err)
	}
	if err := os.Chtimes(badIDPath, old, old); err != nil {
		t.Fatalf("chtimes bad-id: %v", err)
	}

	// Directory with a .json suffix: must survive (IsDir guard).
	dirPath := filepath.Join(archiveDir, "skipdir.json")
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
// archive directory is empty or absent.
func TestPurge_EmptyAndMissingDir(t *testing.T) {
	t.Run("empty archive dir", func(t *testing.T) {
		var rec purgeRecorder
		svc, _, _ := newArchiveTestService(t, WithOnPurge(rec.record))
		svc.Purge(context.Background(), 24*time.Hour)
		if got := rec.sorted(); len(got) != 0 {
			t.Errorf("onPurge fired %v on empty dir, want none", got)
		}
	})

	t.Run("missing archive dir", func(t *testing.T) {
		var rec purgeRecorder
		dir := t.TempDir() // no archive subdir created
		svc := New(newFakeStore(dir), WithOnPurge(rec.record))
		svc.Purge(context.Background(), 24*time.Hour)
		if got := rec.sorted(); len(got) != 0 {
			t.Errorf("onPurge fired %v on missing dir, want none", got)
		}
	})
}

// TestPurge_NilOnPurgeCallback verifies Purge does not panic when no
// onPurge callback is registered.
func TestPurge_NilOnPurgeCallback(t *testing.T) {
	svc, _, archiveDir := newArchiveTestService(t)
	chatPath := writeArchivedChat(t, archiveDir, "nocb", 48*time.Hour)

	svc.Purge(context.Background(), 24*time.Hour)

	if exists(t, chatPath) {
		t.Errorf("old chat survived purge with nil callback: %s", chatPath)
	}
}

// TestPurgeScheduler_InitialEvaluationPurges verifies Start runs an
// initial purge evaluation that removes an over-retention chat.
func TestPurgeScheduler_InitialEvaluationPurges(t *testing.T) {
	purged := make(chan api.ChatID, 8)
	svc, _, archiveDir := newArchiveTestService(t,
		WithOnPurge(func(id api.ChatID) { purged <- id }))
	writeArchivedChat(t, archiveDir, "sched1", 48*time.Hour)

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
	svc, _, archiveDir := newArchiveTestService(t,
		WithOnPurge(func(id api.ChatID) { purged <- id }))
	writeArchivedChat(t, archiveDir, "first", 48*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start()
	defer sched.Stop()

	if got := recvWithin(t, purged, 3*time.Second); got != "first" {
		t.Fatalf("first pass purged %q, want first", got)
	}

	// A chat added after the first pass is purged on the next trigger.
	writeArchivedChat(t, archiveDir, "second", 48*time.Hour)
	sched.Trigger()
	if got := recvWithin(t, purged, 3*time.Second); got != "second" {
		t.Errorf("second trigger purged %q, want second", got)
	}
}

// TestPurgeScheduler_ZeroRetentionSkipsPurge verifies a retention of 0
// ("keep forever") disables purging entirely. A bug here would delete
// every archived chat.
func TestPurgeScheduler_ZeroRetentionSkipsPurge(t *testing.T) {
	purged := make(chan api.ChatID, 8)
	svc, _, archiveDir := newArchiveTestService(t,
		WithOnPurge(func(id api.ChatID) { purged <- id }))
	chatPath := writeArchivedChat(t, archiveDir, "keepforever", 9000*time.Hour)

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
		t.Errorf("retention 0 deleted an archived chat: %s", chatPath)
	}
}

// TestPurgeScheduler_StopClosesDone verifies Stop drains the scheduler
// goroutine (its done channel closes) and is safe to call more than once.
func TestPurgeScheduler_StopClosesDone(t *testing.T) {
	svc, _, _ := newArchiveTestService(t)
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
	svc, _, _ := newArchiveTestService(t)
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
	svc, _, _ := newArchiveTestService(t)
	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start()
	sched.Stop()
	sched.Trigger() // must return without panic
}

// TestPurgeScheduler_StopWithoutStart verifies Stop is safe before Start
// (it must not block waiting on a goroutine that never launched).
func TestPurgeScheduler_StopWithoutStart(t *testing.T) {
	svc, _, _ := newArchiveTestService(t)
	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Stop()
}

// TestOldestArchiveMTime covers the helper that the scheduler uses to
// time its next wake-up.
func TestOldestArchiveMTime(t *testing.T) {
	t.Run("missing dir returns false", func(t *testing.T) {
		dir := t.TempDir() // no archive subdir
		if _, ok := OldestArchiveMTime(context.Background(), dir); ok {
			t.Error("ok = true for missing archive dir, want false")
		}
	})

	t.Run("empty dir returns false", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, Subdir), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if _, ok := OldestArchiveMTime(context.Background(), dir); ok {
			t.Error("ok = true for empty archive dir, want false")
		}
	})

	t.Run("returns mtime of oldest chat file", func(t *testing.T) {
		dir := t.TempDir()
		archiveDir := filepath.Join(dir, Subdir)
		if err := os.MkdirAll(archiveDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		oldestPath := writeArchivedChat(t, archiveDir, "oldest", 72*time.Hour)
		writeArchivedChat(t, archiveDir, "middle", 48*time.Hour)
		writeArchivedChat(t, archiveDir, "newest", 24*time.Hour)

		info, err := os.Stat(oldestPath)
		if err != nil {
			t.Fatalf("stat oldest: %v", err)
		}
		got, ok := OldestArchiveMTime(context.Background(), dir)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if !got.Equal(info.ModTime()) {
			t.Errorf("oldest mtime = %v, want %v", got, info.ModTime())
		}
	})

	t.Run("ignores non-chat files and dirs", func(t *testing.T) {
		dir := t.TempDir()
		archiveDir := filepath.Join(dir, Subdir)
		if err := os.MkdirAll(archiveDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// An old non-.json file that must be ignored.
		txt := filepath.Join(archiveDir, "old.txt")
		if err := os.WriteFile(txt, []byte("x"), 0o600); err != nil {
			t.Fatalf("write txt: %v", err)
		}
		ancient := time.Now().Add(-1000 * time.Hour)
		if err := os.Chtimes(txt, ancient, ancient); err != nil {
			t.Fatalf("chtimes txt: %v", err)
		}
		chatPath := writeArchivedChat(t, archiveDir, "thechat", 24*time.Hour)

		info, err := os.Stat(chatPath)
		if err != nil {
			t.Fatalf("stat chat: %v", err)
		}
		got, ok := OldestArchiveMTime(context.Background(), dir)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if !got.Equal(info.ModTime()) {
			t.Errorf("oldest mtime = %v, want the .json file's mtime %v (the older .txt must be ignored)", got, info.ModTime())
		}
	})

	t.Run("cancelled context returns false", func(t *testing.T) {
		dir := t.TempDir()
		archiveDir := filepath.Join(dir, Subdir)
		if err := os.MkdirAll(archiveDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeArchivedChat(t, archiveDir, "present", 24*time.Hour)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, ok := OldestArchiveMTime(ctx, dir); ok {
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
	svc, _, archiveDir := newArchiveTestService(t, WithOnPurge(rec.record))

	// A dangling symlink with a valid chat-id .json name: ReadDir lists
	// it, but os.Stat (which follows the link) fails with ErrNotExist —
	// exactly the "entry disappeared mid-scan" path purgeOne must skip.
	vanished := filepath.Join(archiveDir, "vanished.json")
	if err := os.Symlink(filepath.Join(archiveDir, "no-such-target.json"), vanished); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	realOld := writeArchivedChat(t, archiveDir, "realold", 48*time.Hour)

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
// Purge call entirely, leaving even ancient archived chats in place.
func TestPurgeScheduler_RescheduleWithZeroRetentionDoesNotPurge(t *testing.T) {
	var rec purgeRecorder
	svc, _, archiveDir := newArchiveTestService(t, WithOnPurge(rec.record))
	chatPath := writeArchivedChat(t, archiveDir, "keepforever", 9000*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 0 })
	timer, _ := sched.purgeAndReschedule()
	if timer != nil {
		timer.Stop()
	}

	if !exists(t, chatPath) {
		t.Errorf("retention 0 purged an archived chat during reschedule: %s", chatPath)
	}
	if got := rec.sorted(); len(got) != 0 {
		t.Errorf("retention 0 fired onPurge for %v, want nothing purged", got)
	}
}

// TestPurgeScheduler_NextWaitZeroRetentionReturnsFalse verifies nextWait
// reports "nothing to schedule" (ok=false) when retention is 0, even with
// a non-empty archive. Retention 0 means keep-forever, so the scheduler
// must not arm a timer.
func TestPurgeScheduler_NextWaitZeroRetentionReturnsFalse(t *testing.T) {
	svc, _, archiveDir := newArchiveTestService(t)
	writeArchivedChat(t, archiveDir, "present", 1*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })

	if _, ok := sched.nextWait(0); ok {
		t.Error("nextWait(0) ok = true, want false: retention 0 disables scheduling")
	}
}

// TestPurgeScheduler_NextWaitPositiveRetentionReturnsTrue verifies that
// with a positive retention and a non-empty archive nextWait schedules a
// wake-up (ok=true). The complement of the zero-retention case: a
// positive retention must arm the next purge.
func TestPurgeScheduler_NextWaitPositiveRetentionReturnsTrue(t *testing.T) {
	svc, _, archiveDir := newArchiveTestService(t)
	writeArchivedChat(t, archiveDir, "present", 1*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 24 * time.Hour })

	if _, ok := sched.nextWait(24 * time.Hour); !ok {
		t.Error("nextWait(24h) ok = false, want true: a non-empty archive with positive retention must schedule a wake-up")
	}
}

// TestPurgeScheduler_NextWaitFloorsAtMinWait verifies that when the oldest
// archived file's retention deadline is already well in the past, nextWait
// floors the returned delay at the 5s minimum rather than a non-positive
// value (which would busy-spin the scheduler goroutine).
func TestPurgeScheduler_NextWaitFloorsAtMinWait(t *testing.T) {
	svc, _, archiveDir := newArchiveTestService(t)
	// Oldest file is 100h old; with a 1h retention its deadline is ~99h
	// in the past, so time.Until(deadline) is strongly negative and the
	// result must clamp to the minWait floor.
	writeArchivedChat(t, archiveDir, "ancient", 100*time.Hour)

	sched := NewPurgeScheduler(context.Background(), svc,
		func() time.Duration { return 1 * time.Hour })

	wait, ok := sched.nextWait(1 * time.Hour)
	if !ok {
		t.Fatal("nextWait ok = false, want true for a non-empty archive")
	}
	if wait != 5*time.Second {
		t.Errorf("nextWait floored to %v, want 5s: a deadline in the past must clamp to minWait, not 0", wait)
	}
}
