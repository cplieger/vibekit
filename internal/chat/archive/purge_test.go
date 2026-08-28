package archive

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestPurge_RetentionCutoff is the core retention contract: files whose
// mtime is older than (now - maxAge) are purged; files newer than the
// cutoff survive. A bug that inverts this deletes live data.
func TestPurge_RetentionCutoff(t *testing.T) {
	var rec purgeRecorder
	svc, _, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	olderPath := writeAgedChat(t, dir, "older", 25*time.Hour)
	newerPath := writeAgedChat(t, dir, "newer", 23*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

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

	svc.Purge(t.Context(), 24*time.Hour)

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
		svc.Purge(t.Context(), 24*time.Hour)
		if got := rec.sorted(); len(got) != 0 {
			t.Errorf("onPurge fired %v on empty dir, want none", got)
		}
	})

	t.Run("missing chat dir", func(t *testing.T) {
		var rec purgeRecorder
		dir := t.TempDir() // never created
		svc := New(newFakeStore(dir), WithOnPurge(rec.recordPurge))
		svc.Purge(t.Context(), 24*time.Hour)
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

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, chatPath) {
		t.Errorf("old chat survived purge with nil callback: %s", chatPath)
	}
}

// TestPurgeScheduler_InitialEvaluationPurges verifies Start runs an
// initial purge evaluation that removes an over-retention chat.
func TestPurgeScheduler_InitialEvaluationPurges(t *testing.T) {
	purged := make(chan vibekit.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id vibekit.ChatID, _ []string) { purged <- id }))
	writeAgedChat(t, dir, "sched1", 48*time.Hour)

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	defer sched.Stop()

	if got := recvWithin(t, purged, 3*time.Second); got != "sched1" {
		t.Errorf("initial evaluation purged %q, want sched1", got)
	}
}

// TestPurgeScheduler_ReArmsAndProcessesSecondTrigger verifies the loop
// keeps processing triggers after the first pass (it is not one-shot).
func TestPurgeScheduler_ReArmsAndProcessesSecondTrigger(t *testing.T) {
	purged := make(chan vibekit.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id vibekit.ChatID, _ []string) { purged <- id }))
	writeAgedChat(t, dir, "first", 48*time.Hour)

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
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
	purged := make(chan vibekit.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id vibekit.ChatID, _ []string) { purged <- id }))
	chatPath := writeAgedChat(t, dir, "keepforever", 9000*time.Hour)

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 0 })
	sched.Start(t.Context())
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
	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	sched.Stop()

	select {
	case <-sched.done:
	default:
		t.Error("done channel not closed after Stop()")
	}

	sched.Stop() // idempotent: must not panic
}

// TestPurgeScheduler_ContextCancellationStopsLoop pins the loop's OTHER
// exit path: cancelling the context the scheduler was STARTED with must
// drain the goroutine without any Stop() call (Stop closes stopCh, which
// is a different select arm — asserting through it would pass even if the
// ctx arm were gone).
func TestPurgeScheduler_ContextCancellationStopsLoop(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	ctx, cancel := context.WithCancel(t.Context())
	sched := NewPurgeScheduler(svc, func() time.Duration { return 24 * time.Hour })
	sched.Start(ctx)

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
	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Start(t.Context())
	sched.Stop()
	sched.Trigger() // must return without panic
}

// TestPurgeScheduler_StopWithoutStart verifies Stop is safe before Start
// (it must not block waiting on a goroutine that never launched).
func TestPurgeScheduler_StopWithoutStart(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })
	sched.Stop()
}

// TestOldestChatMTime covers the helper that the scheduler uses to
// time its next wake-up.
func TestOldestChatMTime(t *testing.T) {
	t.Run("missing dir returns false", func(t *testing.T) {
		dir := t.TempDir() // never created
		if _, ok := OldestChatMTime(t.Context(), dir); ok {
			t.Error("ok = true for missing chat dir, want false")
		}
	})

	t.Run("empty dir returns false", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if _, ok := OldestChatMTime(t.Context(), dir); ok {
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
		got, ok := OldestChatMTime(t.Context(), dir)
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
		got, ok := OldestChatMTime(t.Context(), dir)
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

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, ok := OldestChatMTime(ctx, dir); ok {
			t.Error("ok = true for cancelled context, want false")
		}
	})
}

// recvWithin receives one chat ID from ch or fails after d.
func recvWithin(t *testing.T, ch <-chan vibekit.ChatID, d time.Duration) vibekit.ChatID {
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

	svc.Purge(t.Context(), 24*time.Hour)

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

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 0 })
	timer, _ := sched.purgeAndReschedule(t.Context())
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

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })

	if _, ok := sched.nextWait(t.Context(), 0); ok {
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

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 24 * time.Hour })

	if _, ok := sched.nextWait(t.Context(), 24*time.Hour); !ok {
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

	sched := NewPurgeScheduler(svc,
		func() time.Duration { return 1 * time.Hour })

	wait, ok := sched.nextWait(t.Context(), 1*time.Hour)
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
// clean up nothing but checkpoints. At the DefaultChatRetentionDays window the
// purge is what reaches an expired chat's session directories; a purge that
// reaped nothing but checkpoints would leave the hourly orphan sweep, a residue
// collector, as the only thing that ever removed them.
func TestPurge_HandsTheSessionChainToOnPurge(t *testing.T) {
	var rec purgeRecorder
	svc, store, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	// A chat that ran on two sessions before falling out of the window.
	chatPath := writeAgedChat(t, dir, "chained", 48*time.Hour)
	// purgeReferenceTime reads the chat through the STORE now (chats no longer
	// live in a separate directory the purge package parses itself), so the
	// chain has to come from the fake's Load.
	store.loadResult = &vibekit.Chat{
		ID:                 "chained",
		Name:               "C",
		ACPSessionID:       "sess_new",
		PriorACPSessionIDs: []string{"sess_old"},
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(chatPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	svc.Purge(t.Context(), 24*time.Hour)

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
	live := map[vibekit.ChatID]bool{"open": true}
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(rec.recordPurge),
		WithLiveChats(func(id vibekit.ChatID) bool { return live[id] }),
	)

	// Both are far past the window; only one is in use.
	openPath := writeAgedChat(t, dir, "open", 72*time.Hour)
	abandonedPath := writeAgedChat(t, dir, "abandoned", 72*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

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

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, p) {
		t.Error("purge did nothing with no live predicate wired")
	}
}

// TestPurgeScheduler_AlwaysArmsATimer pins the invariant the loop depends on
// and that nothing previously covered: purgeAndReschedule must NEVER return a
// nil timer. It used to return (nil, nil) whenever nextWait reported not-ok,
// which left the loop with no wake-up at all and no way back except Trigger(),
// whose only production caller is Start.
//
// Both not-ok inputs are ordinary states, which is what made the bug quiet: a
// fresh container has an empty chat directory, and "keep forever" sets
// retention to a non-positive value. In either case the loop went dark, and in
// the retention case it stayed dark after the setting was turned back on,
// because the settings path does not Trigger.
//
// Asserting on the returned timer rather than on an elapsed wake-up is
// deliberate: the poll ceiling is an hour, so a timing test would either sleep
// for an hour or need a production seam to shorten it. The nil return IS the
// defect, so it is what the test names.
func TestPurgeScheduler_AlwaysArmsATimer(t *testing.T) {
	cases := map[string]struct {
		aged      bool          // seed one purgeable chat
		retention time.Duration // what the settings hook reports
	}{
		"empty directory, retention on":  {aged: false, retention: 24 * time.Hour},
		"chats present, retention off":   {aged: true, retention: 0},
		"empty directory, retention off": {aged: false, retention: 0},
		"keep forever":                   {aged: true, retention: -1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _, dir := newPurgeTestService(t)
			if tc.aged {
				writeAgedChat(t, dir, "aged", 48*time.Hour)
			}
			sched := NewPurgeScheduler(svc,
				func() time.Duration { return tc.retention })

			timer, timerC := sched.purgeAndReschedule(t.Context())
			if timer == nil || timerC == nil {
				t.Fatalf("purgeAndReschedule() = (%v, %v), want an armed timer: "+
					"a nil channel leaves the loop with no wake-up and it never purges again",
					timer, timerC)
			}
			timer.Stop()
		})
	}
}

// TestPurgeScheduler_CapsTheArmedWait pins the ceiling on how long the loop may
// sleep. Without it the armed wait was the oldest chat's age plus the whole
// retention window, so a 30-day retention slept ~29 days and no settings change
// could shorten it: the loop was already asleep when the change happened and
// nothing woke it.
//
// The assertion is on nextWait EXCEEDING the ceiling for a realistic input, and
// on the ceiling being the smaller of the two. An earlier version of this test
// asserted `min(wait, maxWait) == maxWait` after a skip guard, which is
// arithmetically true whenever the guard passes and therefore tested nothing.
func TestPurgeScheduler_CapsTheArmedWait(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	// Brand new chat plus a long retention: the natural deadline is far beyond
	// the ceiling, which is the case the cap exists for.
	writeAgedChat(t, dir, "fresh", 0)
	retention := 30 * 24 * time.Hour
	sched := NewPurgeScheduler(svc, func() time.Duration { return retention })

	natural, ok := sched.nextWait(t.Context(), retention)
	if !ok {
		t.Fatal("nextWait reported no work for a chat that exists")
	}
	// The premise: without a cap this is what would have been armed.
	if natural <= maxWait {
		t.Fatalf("natural wait %v does not exceed the %v ceiling, so this test's "+
			"premise no longer holds; pick a longer retention", natural, maxWait)
	}
	// The property, read off the function the LOOP calls rather than recomputed
	// here. An earlier version of this test computed min(natural, maxWait) itself
	// and asserted it equalled maxWait, which is arithmetically true whatever
	// production does — it stayed green with the clamp deleted from purge.go.
	if armed, _ := sched.armWait(t.Context(), retention); armed != maxWait {
		t.Errorf("armWait = %v, want the %v ceiling (natural wait was %v)",
			armed, maxWait, natural)
	}

	// And the loop really does arm a timer on this path.
	timer, timerC := sched.purgeAndReschedule(t.Context())
	if timer == nil || timerC == nil {
		t.Fatal("purgeAndReschedule returned no timer")
	}
	timer.Stop()
}

// A chat that loads but carries no activity timestamp ages from its file mtime,
// the same fallback a chat that cannot be read at all gets. Treating a zero
// UpdatedAt as a real instant dates the chat to the epoch, which purges a file
// written seconds ago.
func TestPurge_ChatWithoutAnActivityTimestampAgesFromMtime(t *testing.T) {
	svc, store, dir := newPurgeTestService(t)
	// Load succeeds for every id, with no UpdatedAt set.
	store.loadResult = &vibekit.Chat{}

	freshPath := writeAgedChat(t, dir, "fresh01", 0)
	stalePath := writeAgedChat(t, dir, "stale01", 48*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if !exists(t, freshPath) {
		t.Errorf("chat with UpdatedAt=0 and a fresh mtime was purged: %s", freshPath)
	}
	if exists(t, stalePath) {
		t.Errorf("chat with UpdatedAt=0 and a 48h-old mtime survived a 24h retention: %s", stalePath)
	}
}

// capturePurgeLogs redirects the slog default into a buffer for one test. The
// default logger is process-global, so a test using this must not run in
// parallel.
func capturePurgeLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// The end-of-pass summary is the operator's only record of a purge, so it must
// report what the pass actually did — and a pass in which nothing failed is not
// announced as a failure.
func TestPurge_PassSummaryReportsWhatThePassDid(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	writeAgedChat(t, dir, "gone0001", 48*time.Hour)
	writeAgedChat(t, dir, "kept0001", 1*time.Hour)
	buf := capturePurgeLogs(t)

	svc.Purge(t.Context(), 24*time.Hour)

	got := buf.String()
	for _, want := range []string{"purged=1", "kept=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("purge summary = %q, want it to contain %q (one stale chat, one live one)", got, want)
		}
	}
	if !strings.Contains(got, "level=INFO") {
		t.Errorf("purge summary = %q, want it logged at INFO: nothing failed in this pass", got)
	}
	if strings.Contains(got, "with errors") {
		t.Errorf("purge summary = %q, want no error summary: nothing failed in this pass", got)
	}
}

// The draft exemption, and it is the other half of a decision made in the store
// rather than a second rule. Store.SetDraft deliberately does not stamp
// UpdatedAt — a 600ms autosave would push the purge cutoff out a whole window
// per keystroke — so the age test structurally cannot see a chat someone is
// typing in. Without this, a paragraph typed into a month-old chat looks exactly
// as abandoned as one nobody has touched, right up to the moment the reaper takes
// both, and the draft is the only copy of the words.
func TestPurge_NeverPurgesAChatHoldingADraft(t *testing.T) {
	cases := map[string]struct {
		draft     string
		wantKept  bool
		wantReaps []string
	}{
		"a chat with an unsent draft is defended": {
			draft:     "half a question I have not sent yet",
			wantKept:  true,
			wantReaps: nil,
		},
		// The exemption is exactly the non-empty draft, not "has a draft field".
		// An empty draft is how a sent or abandoned message clears, so treating
		// it as work in progress would make every chat that ever had one
		// permanent.
		"an empty draft defends nothing": {
			draft:     "",
			wantKept:  false,
			wantReaps: []string{"aged"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var rec purgeRecorder
			svc, store, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
			p := writeAgedChat(t, dir, "aged", 72*time.Hour)
			// The draft reaches the purge decision through the store's Load, which
			// is the one read purgeReferenceTime already makes.
			store.loadResult = &vibekit.Chat{ID: "aged", Name: "C", Draft: tc.draft}

			svc.Purge(t.Context(), 24*time.Hour)

			if got := exists(t, p); got != tc.wantKept {
				t.Errorf("chat survived = %v, want %v (draft %q)", got, tc.wantKept, tc.draft)
			}
			if got := rec.sorted(); !slices.Equal(got, tc.wantReaps) {
				t.Errorf("onPurge fired for %v, want %v", got, tc.wantReaps)
			}
		})
	}
}

// The exemption covers the DRAFT and deliberately not the attachments beside it:
// an attachment is a path to a file that lives on disk in its own right, so
// purging the chat loses a reference, while a draft is the only copy of the text.
func TestPurge_StagedAttachmentsAloneDoNotDefendAChat(t *testing.T) {
	var rec purgeRecorder
	svc, store, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
	p := writeAgedChat(t, dir, "aged", 72*time.Hour)
	store.loadResult = &vibekit.Chat{ID: "aged", Name: "C", Attachments: []string{"docs/spec.pdf"}}

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, p) {
		t.Error("a chat holding only staged attachments survived; the file it names is still on disk")
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"aged"}) {
		t.Errorf("onPurge fired for %v, want [aged]", got)
	}
}

// An unreadable chat file reports no draft, which is the safe direction: a chat
// the store cannot decode has no draft anyone could recover, so defending it
// would keep a corrupt file forever. The fake's Load fails by default, so this is
// that path.
func TestPurge_AnUnreadableChatIsNotDefendedByADraft(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	p := writeAgedChat(t, dir, "corrupt", 72*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, p) {
		t.Error("an unreadable expired chat survived; a load failure must not read as a draft")
	}
}

// The OPEN-TAB exemption, retention's second predicate and the one the tab
// collection made possible. It answers the case the draft predicate misses: a
// reader who is READING an old chat rather than typing into it leaves no trace
// the age test can see, because reading stamps nothing at all.
//
// It makes retention OPT-OUT for a chat left open forever, which is accepted —
// that is the honest reading of "in use", and the alternative is closing a tab
// under someone to satisfy a timer.
func TestPurge_NeverPurgesAChatWithAnOpenTab(t *testing.T) {
	cases := map[string]struct {
		open      map[string]bool
		wantKept  bool
		wantReaps []string
	}{
		"a chat on the strip is defended however old": {
			open:      map[string]bool{"aged": true},
			wantKept:  true,
			wantReaps: nil,
		},
		"a chat whose tab was closed is not": {
			open:      map[string]bool{"someone-else": true},
			wantKept:  false,
			wantReaps: []string{"aged"},
		},
		"no tabs open at all defends nothing": {
			open:      nil,
			wantKept:  false,
			wantReaps: []string{"aged"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var rec purgeRecorder
			svc, _, dir := newPurgeTestService(t,
				WithOnPurge(rec.recordPurge),
				WithOpenTabs(func(id vibekit.ChatID) bool { return tc.open[string(id)] }))
			p := writeAgedChat(t, dir, "aged", 72*time.Hour)

			svc.Purge(t.Context(), 24*time.Hour)

			if got := exists(t, p); got != tc.wantKept {
				t.Errorf("chat survived = %v, want %v (open tabs %v)", got, tc.wantKept, tc.open)
			}
			if got := rec.sorted(); !slices.Equal(got, tc.wantReaps) {
				t.Errorf("onPurge fired for %v, want %v", got, tc.wantReaps)
			}
		})
	}
}

// The two exemptions are INDEPENDENT: either one alone defends a chat, which is
// what "an open tab OR a non-empty draft" means. A test that only ever set both
// would pass with the predicates ANDed.
func TestPurge_TheOpenTabAndDraftExemptionsAreIndependent(t *testing.T) {
	var rec purgeRecorder
	svc, store, dir := newPurgeTestService(t,
		WithOnPurge(rec.recordPurge),
		WithOpenTabs(func(id vibekit.ChatID) bool { return id == "open-no-draft" }))
	openNoDraft := writeAgedChat(t, dir, "open-no-draft", 72*time.Hour)
	draftNoTab := writeAgedChat(t, dir, "draft-no-tab", 72*time.Hour)
	// One loadResult serves both entries; only the drafting one needs it non-empty,
	// so the open-tab chat is defended by its tab alone — the tab predicate runs
	// before the record is even read.
	store.loadResult = &vibekit.Chat{ID: "draft-no-tab", Name: "C", Draft: "unsent"}

	svc.Purge(t.Context(), 24*time.Hour)

	if !exists(t, openNoDraft) {
		t.Error("a chat with an open tab and no draft was purged; the tab alone must defend it")
	}
	if !exists(t, draftNoTab) {
		t.Error("a chat with a draft and no open tab was purged; the draft alone must defend it")
	}
	if got := rec.sorted(); len(got) != 0 {
		t.Errorf("onPurge fired for %v, want nothing", got)
	}
}
