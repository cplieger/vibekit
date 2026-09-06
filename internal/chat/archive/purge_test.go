package archive

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
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

// The wake-up a pass with nothing to purge arms, which is the regression this
// scheduler exists to prevent: aging from the oldest chat FILE's mtime floored at
// 5s meant an exempt chat (never removed at any age) held that floor in the past
// permanently, so the loop re-scanned every 5 seconds forever, full-decoding every
// chat file each time.
func TestPurgeScheduler_APassWithNothingToPurgeBacksOff(t *testing.T) {
	svc, _, dir := newPurgeTestService(t,
		WithOpenTabs(func(vibekit.ChatID) bool { return true }))
	// Far past the window and exempt: the shape that used to pin the floor.
	writeAgedChat(t, dir, "pinned", 500*time.Hour)
	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })

	first := sched.armWait(time.Hour, svc.Purge(t.Context(), time.Hour))
	if first < idleBase {
		t.Fatalf("first idle wait = %v, want at least %v: an exempt chat must not pull the "+
			"wake-up down to the floor", first, idleBase)
	}
	second := sched.armWait(time.Hour, svc.Purge(t.Context(), time.Hour))
	if second <= first {
		t.Errorf("second idle wait = %v, want longer than the first (%v): consecutive passes "+
			"that purge nothing must back off", second, first)
	}
	if !exists(t, filepath.Join(dir, "pinned.json")) {
		t.Error("the exempt chat was purged; the premise of this test is gone")
	}
}

// Retention off is not a reason to go dark, and not a reason to spin either: the
// settings path does not Trigger, so the loop re-checks on the ceiling.
func TestPurgeScheduler_RetentionOffWaitsTheCeiling(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	writeAgedChat(t, dir, "present", 1*time.Hour)
	sched := NewPurgeScheduler(svc, func() time.Duration { return 0 })

	if got := sched.armWait(0, PurgeResult{}); got != maxWait {
		t.Errorf("armWait(retention off) = %v, want the %v ceiling", got, maxWait)
	}
}

// A pass that KEPT a chat on age alone arms a timer at that chat's own deadline,
// and the earliest one wins. This is the wake-up that replaced the mtime scan, so
// it has to be the chat's activity stamp plus the window and nothing else.
func TestPurgeScheduler_ArmsTheEarliestAgeKeptDeadline(t *testing.T) {
	svc, store, dir := newPurgeTestService(t)
	retention := time.Hour
	// One chat 10 minutes old (deadline ~50m away), one 50 minutes old (~10m).
	// Both are kept; the closer deadline is what the loop must arm.
	store.header = &RetentionHeader{UpdatedAt: time.Now().Add(-50 * time.Minute).UnixMilli()}
	writeAgedChat(t, dir, "nearer", 0)
	sched := NewPurgeScheduler(svc, func() time.Duration { return retention })

	res := svc.Purge(t.Context(), retention)
	if res.NextDeadline.IsZero() {
		t.Fatal("a pass that kept a chat on age reported no deadline; the loop has nothing to arm")
	}
	got := sched.armWait(retention, res)
	if got < 9*time.Minute || got > 11*time.Minute {
		t.Errorf("armWait = %v, want ~10m (a chat 50m into a 1h window)", got)
	}
}

// A deadline moments away, or already reached, arms the floor rather than a
// zero-length timer: an age-kept deadline is in the future by construction, but a
// boundary case must not turn the loop into a hot spin either.
func TestPurgeScheduler_FloorsADeadlineThatIsUponUs(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })

	got := sched.armWait(time.Hour, PurgeResult{Kept: 1, NextDeadline: time.Now()})
	if got != minWait {
		t.Errorf("armWait for a deadline that is upon us = %v, want the %v floor", got, minWait)
	}
}

// A pass that PURGED something resets the back-off: work happening is evidence
// the store is in use, so the next idle pass starts from the base again rather
// than inheriting an hour-long wait.
func TestPurgeScheduler_APurgeResetsTheBackOff(t *testing.T) {
	svc, _, _ := newPurgeTestService(t)
	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })

	for range 4 {
		sched.armWait(time.Hour, PurgeResult{})
	}
	grown := sched.idleWait
	if grown <= idleBase {
		t.Fatalf("idle wait after four empty passes = %v, want more than %v", grown, idleBase)
	}
	if got := sched.armWait(time.Hour, PurgeResult{Purged: 1}); got != idleBase {
		t.Errorf("armWait after a pass that purged = %v, want the %v base (grown wait was %v)",
			got, idleBase, grown)
	}
}

// TestPurge_HandsTheSessionChainToOnPurge pins that a purge reaps its OWN session
// directories rather than leaving them to the hourly orphan sweep, a residue
// collector. The ordering is the point: onPurge fires AFTER os.Remove(entry.path),
// so the chain must be read before the file goes or the ids are unrecoverable.
func TestPurge_HandsTheSessionChainToOnPurge(t *testing.T) {
	var rec purgeRecorder
	svc, store, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))

	// A chat that ran on two sessions before falling out of the window.
	chatPath := writeAgedChat(t, dir, "chained", 48*time.Hour)
	// purgeReferenceTime reads the chat through the STORE's retention projection,
	// so the chain has to come from the fake's header. The composition of the two
	// id fields into a chain is vibekit's own and is pinned where it lives.
	store.header = &RetentionHeader{SessionChain: []string{"sess_old", "sess_new"}}
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

// TestPurge_NeverPurgesALiveChat is what makes purging the main chat directory safe
// at all: the purge scans the directory live chats live in, so age alone is NOT
// grounds to delete — a conversation open for weeks is older than any retention
// window. The exemption is the only thing separating abandoned work from live work.
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

// TestPurgeScheduler_AlwaysArmsATimer: purgeAndReschedule must NEVER return a nil
// timer, or the loop has no wake-up and no way back except Trigger, whose only
// production caller is Start. Both nothing-to-schedule inputs are ordinary states —
// a fresh container's empty chat directory, and "keep forever" — and the settings
// path does not Trigger, so the loop stays dark after retention is turned back on.
// Asserted on the returned timer because the poll ceiling is an hour.
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

// TestPurgeScheduler_CapsTheArmedWait pins the ceiling on how long the loop may sleep:
// an uncapped wait is the chat's whole remaining window, so a 30-day retention sleeps
// ~30 days and no settings change can shorten it. The premise is asserted first — the
// pass's own deadline really is beyond the ceiling — so the two cannot agree by luck.
func TestPurgeScheduler_CapsTheArmedWait(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	// Brand new chat plus a long retention: the natural deadline is far beyond
	// the ceiling, which is the case the cap exists for.
	writeAgedChat(t, dir, "fresh", 0)
	retention := 30 * 24 * time.Hour
	sched := NewPurgeScheduler(svc, func() time.Duration { return retention })

	res := svc.Purge(t.Context(), retention)
	natural := time.Until(res.NextDeadline)
	if natural <= maxWait {
		t.Fatalf("natural wait %v does not exceed the %v ceiling, so this test's "+
			"premise no longer holds; pick a longer retention", natural, maxWait)
	}
	if armed := sched.armWait(retention, res); armed != maxWait {
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
	// The projection reads for every id, with no UpdatedAt set.
	store.header = &RetentionHeader{}

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

// The draft exemption is the other half of a store decision: Store.SetDraft does not
// stamp UpdatedAt, because a 600ms autosave would push the cutoff out a whole window per
// keystroke — so the age test structurally cannot see a chat someone is typing in, and
// the draft is the only copy of the words. What COUNTS as drafting is the projection's
// answer, pinned on the read that decides it (chat.TestLoadRetentionHeader_*).
func TestPurge_NeverPurgesAChatHoldingADraft(t *testing.T) {
	cases := map[string]struct {
		drafting  bool
		wantKept  bool
		wantReaps []string
	}{
		"a chat with an unsent draft is defended": {
			drafting:  true,
			wantKept:  true,
			wantReaps: nil,
		},
		"a chat with nothing unsent is not": {
			drafting:  false,
			wantKept:  false,
			wantReaps: []string{"aged"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var rec purgeRecorder
			svc, store, dir := newPurgeTestService(t, WithOnPurge(rec.recordPurge))
			p := writeAgedChat(t, dir, "aged", 72*time.Hour)
			// The draft flag reaches the decision through the retention projection,
			// which is the one read purgeReferenceTime already makes.
			store.header = &RetentionHeader{Drafting: tc.drafting}

			svc.Purge(t.Context(), 24*time.Hour)

			if got := exists(t, p); got != tc.wantKept {
				t.Errorf("chat survived = %v, want %v (drafting %v)", got, tc.wantKept, tc.drafting)
			}
			if got := rec.sorted(); !slices.Equal(got, tc.wantReaps) {
				t.Errorf("onPurge fired for %v, want %v", got, tc.wantReaps)
			}
		})
	}
}

// A chat kept by a DRAFT contributes no deadline: its age stays past the window for as
// long as the words are unsent, so a wake-up derived from it fires immediately, purges
// nothing and re-arms the same instant forever.
func TestPurge_AnExemptChatContributesNoDeadline(t *testing.T) {
	svc, store, dir := newPurgeTestService(t)
	writeAgedChat(t, dir, "drafting", 72*time.Hour)
	store.header = &RetentionHeader{Drafting: true}

	res := svc.Purge(t.Context(), 24*time.Hour)

	if res.Kept != 1 {
		t.Fatalf("kept = %d, want 1: the drafting chat must be kept", res.Kept)
	}
	if !res.NextDeadline.IsZero() {
		t.Errorf("NextDeadline = %v, want none: an exempt chat's age is not a wake-up",
			res.NextDeadline)
	}
}

// An unreadable chat file reports no draft, the safe direction: a chat the store cannot
// decode has no draft anyone could recover, so defending it keeps a corrupt file
// forever. The fake's Load fails by default, so this is that path.
func TestPurge_AnUnreadableChatIsNotDefendedByADraft(t *testing.T) {
	svc, _, dir := newPurgeTestService(t)
	p := writeAgedChat(t, dir, "corrupt", 72*time.Hour)

	svc.Purge(t.Context(), 24*time.Hour)

	if exists(t, p) {
		t.Error("an unreadable expired chat survived; a load failure must not read as a draft")
	}
}

// The OPEN-TAB exemption answers the case the draft predicate misses: reading an old
// chat stamps nothing at all, so the age test sees no trace of it. It makes retention
// OPT-OUT for a chat left open forever, which is accepted — the alternative is closing
// a tab under someone to satisfy a timer.
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
	// One header serves both entries; only the drafting one needs it, so the
	// open-tab chat is defended by its tab alone — the tab predicate runs before
	// the record is even read.
	store.header = &RetentionHeader{Drafting: true}

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

// THE IDLE BACK-OFF'S END. A pass that keeps every chat on an exemption reports no
// deadline (see PurgeResult), so the loop lands on the doubling idle wait whose
// ceiling is an hour, and only a Trigger shortens that — so clearing the last
// exemption on a month-old chat must wake it or the chat outlives its window by up to
// that hour. The wait's length stays pinned by the back-off test; reading idleWait
// from here would race the loop goroutine that owns it.
func TestPurgeScheduler_ATriggerEndsTheIdleBackOff(t *testing.T) {
	var exempt atomic.Bool
	exempt.Store(true)
	// One buffered slot per predicate call the test waits on, so a send can never
	// block the pass it is observing.
	passes := make(chan struct{}, 64)
	purged := make(chan vibekit.ChatID, 8)
	svc, _, dir := newPurgeTestService(t,
		WithOnPurge(func(id vibekit.ChatID, _ []string) { purged <- id }),
		WithOpenTabs(func(vibekit.ChatID) bool {
			// Read BEFORE the handshake: the test flips the flag the moment it
			// receives one, so a scheduler descheduled between send and load would
			// purge on the FIRST pass and green a run where no Trigger did anything.
			answer := exempt.Load()
			select {
			case passes <- struct{}{}:
			default:
			}
			return answer
		}))
	chat := writeAgedChat(t, dir, "pinned", 500*time.Hour)

	sched := NewPurgeScheduler(svc, func() time.Duration { return time.Hour })
	sched.Start(t.Context())
	defer sched.Stop()

	// The predicate answering is the handshake for "the first pass reached this
	// chat"; a sleep here would pass whether or not the pass had run.
	select {
	case <-passes:
	case <-time.After(3 * time.Second):
		t.Fatal("the first pass never consulted the open-tab predicate")
	}
	if !exists(t, chat) {
		t.Fatal("Setup: the exempt chat was purged on the first pass, so there is no " +
			"back-off to end")
	}

	// The exemption clears — the reader closed the tab — and the clearing path
	// wakes the scheduler.
	exempt.Store(false)
	sched.Trigger()

	if got := recvWithin(t, purged, 3*time.Second); got != "pinned" {
		t.Errorf("the pass a Trigger ran purged %q, want pinned: a cleared exemption has "+
			"to be noticed on the wake rather than at the end of an hour-long back-off", got)
	}
}
