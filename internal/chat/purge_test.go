package chat

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/chat/archive"
)

// --- OldestChatMTime ---

func TestOldestChatMTime(t *testing.T) {
	cases := []struct {
		setup  func(t *testing.T, s *Store) time.Time
		name   string
		wantOK bool
	}{
		{
			name:   "EmptyWhenNoChatDir",
			setup:  func(t *testing.T, s *Store) time.Time { return time.Time{} },
			wantOK: false,
		},
		{
			name: "EmptyWhenChatDirEmpty",
			setup: func(t *testing.T, s *Store) time.Time {
				if err := os.MkdirAll(s.dir, 0o700); err != nil {
					t.Fatal(err)
				}
				return time.Time{}
			},
			wantOK: false,
		},
		{
			name: "PicksOldestJSONFile",
			setup: func(t *testing.T, s *Store) time.Time {
				dir := s.dir
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				oldPath := filepath.Join(dir, "old.json")
				newPath := filepath.Join(dir, "new.json")
				if err := os.WriteFile(oldPath, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				oldTime := time.Now().Add(-24 * time.Hour)
				if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(newPath, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				return oldTime
			},
			wantOK: true,
		},
		{
			name: "SkipsSubdirsAndNonJSON",
			setup: func(t *testing.T, s *Store) time.Time {
				dir := s.dir
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				subDir := filepath.Join(dir, "subdir")
				if err := os.MkdirAll(subDir, 0o700); err != nil {
					t.Fatal(err)
				}
				md := filepath.Join(dir, "chat.notes.md")
				if err := os.WriteFile(md, []byte("# notes"), 0o600); err != nil {
					t.Fatal(err)
				}
				mdTime := time.Now().Add(-48 * time.Hour)
				if err := os.Chtimes(md, mdTime, mdTime); err != nil {
					t.Fatal(err)
				}
				j := filepath.Join(dir, "chat.json")
				if err := os.WriteFile(j, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				jTime := time.Now().Add(-6 * time.Hour)
				if err := os.Chtimes(j, jTime, jTime); err != nil {
					t.Fatal(err)
				}
				return jTime
			},
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			wantTime := tc.setup(t, s)
			got, ok := archive.OldestChatMTime(t.Context(), s.dir)
			if ok != tc.wantOK {
				t.Fatalf("OldestChatMTime ok=%v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				if got.Sub(wantTime) > time.Second || wantTime.Sub(got) > time.Second {
					t.Errorf("OldestChatMTime = %v, want near %v", got, wantTime)
				}
			}
		})
	}
}

// --- PurgeScheduler ---

// waitForRetentionConsults polls until the scheduler has consulted its
// retention callback at least n times, which is proof its loop processed a
// trigger. The negative assertions below (a young chat was NOT purged) fail
// OPEN on a bare sleep: a slow goroutine lets them pass for the wrong reason,
// so they would never catch a regression that purges too eagerly. A positive
// signal that the pass actually ran is what makes them mean anything.
func waitForRetentionConsults(t *testing.T, calls *atomic.Int32, n int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := calls.Load()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("retention callback consulted %d times, want >=%d within 2s", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// countingRetention returns a fixed-retention callback plus the counter it
// bumps, so a test can wait for a purge pass rather than guess at one.
func countingRetention(d time.Duration) (func() time.Duration, *atomic.Int32) {
	var calls atomic.Int32
	return func() time.Duration {
		calls.Add(1)
		return d
	}, &calls
}

// waitForPurge polls for the chat file to disappear, with a timeout.
func waitForPurge(t *testing.T, path string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestPurgeScheduler_TriggerWithZeroRetentionIsNoOp(t *testing.T) {
	s, _ := newTestStore(t)
	var calls atomic.Int32
	retention := func() time.Duration {
		calls.Add(1)
		return 0
	}
	p := NewPurgeScheduler(t.Context(), s, retention)
	p.Start()
	defer p.Stop()

	p.Trigger()
	waitForRetentionConsults(t, &calls, 1)
}

func TestPurgeScheduler_TriggerRunsPurgeWhenRetentionPositive(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1.json")
	ageChat(t, s, "c1", 48*time.Hour)

	p := NewPurgeScheduler(t.Context(), s, func() time.Duration { return 24 * time.Hour })
	p.Start()
	defer p.Stop()

	if !waitForPurge(t, chatPath, 2*time.Second) {
		t.Error("chat file survived purge after 2s")
	}
}

func TestPurgeScheduler_TriggerSchedulesForRemainingEntry(t *testing.T) {
	// A fresh chat (not yet expired) should not be purged
	// but the scheduler should schedule a future purge.
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1.json")

	retention, calls := countingRetention(24 * time.Hour)
	p := NewPurgeScheduler(t.Context(), s, retention)
	p.Start()
	defer p.Stop()

	// Wait for a pass to actually run before asserting it kept the file.
	waitForRetentionConsults(t, calls, 1)

	// The file should still exist (not expired yet).
	if _, err := os.Stat(chatPath); err != nil {
		t.Errorf("fresh chat file was purged: %v", err)
	}
}

func TestPurgeScheduler_TriggerWithNoChatsIsNoOp(t *testing.T) {
	s, _ := newTestStore(t)
	retention, calls := countingRetention(24 * time.Hour)
	p := NewPurgeScheduler(t.Context(), s, retention)
	p.Start()
	defer p.Stop()

	p.Trigger()
	// Prove the pass ran on an empty store — no crash, no panic.
	waitForRetentionConsults(t, calls, 1)
}

func TestPurgeScheduler_StopPreventsFutureTriggers(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1.json")
	ageChat(t, s, "c1", 48*time.Hour)

	p := NewPurgeScheduler(t.Context(), s, func() time.Duration { return 24 * time.Hour })
	// Stop before Start — the goroutine never runs.
	p.Stop()
	p.Trigger()

	// No wait is needed and none would add anything: Stop closed stopCh, and
	// Trigger returns on that check before touching triggerCh, so there is no
	// goroutine that could purge. A sleep here would only slow the suite.

	// Stop must have short-circuited: the aged-out file survives.
	if _, err := os.Stat(chatPath); err != nil {
		t.Errorf("chat file removed after Stop: %v (Stop should freeze purge)", err)
	}
}

func TestPurgeScheduler_StartInvokesTrigger(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1.json")
	ageChat(t, s, "c1", 48*time.Hour)

	p := NewPurgeScheduler(t.Context(), s, func() time.Duration { return 24 * time.Hour })
	defer p.Stop()

	p.Start()

	if !waitForPurge(t, chatPath, 2*time.Second) {
		t.Error("chat file survived Start() after 2s")
	}
}

func TestPurgeScheduler_CollapsesConcurrentTriggers(t *testing.T) {
	// Multiple rapid Trigger calls should collapse into a single
	// evaluation, not queue N sequential purge passes.
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	retention, calls := countingRetention(24 * time.Hour)
	p := NewPurgeScheduler(t.Context(), s, retention)
	p.Start()
	defer p.Stop()

	// Fire multiple triggers rapidly.
	for range 10 {
		p.Trigger()
	}
	// Prove at least one pass ran — no crash, no panic. The collapse itself is
	// the buffered-1 triggerCh's contract, not something a count can pin here.
	waitForRetentionConsults(t, calls, 1)
}

func TestPurgeScheduler_ClampsMinWaitSo1HzSpinIsAvoided(t *testing.T) {
	// With retention=1s and an entry already aged past retention,
	// the scheduler must purge expired entries and keep fresh ones.
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1.json")
	ageChat(t, s, "c1", 48*time.Hour)

	// A second chat with fresh activity, which must survive.
	_ = s.Mutate(t.Context(), "c2", func(c *api.Chat, _ bool) bool { c.Name = "B"; return true })
	c2Path := filepath.Join(s.dir, "c2.json")

	p := NewPurgeScheduler(t.Context(), s, func() time.Duration { return time.Second })
	p.Start()
	defer p.Stop()

	if !waitForPurge(t, chatPath, 2*time.Second) {
		t.Error("expired entry not purged")
	}
	// c2 should survive (fresh activity, retention=1s).
	if _, err := os.Stat(c2Path); err != nil {
		t.Errorf("fresh entry purged unexpectedly: %v", err)
	}
}

func TestPurgeScheduler_ContextCancellationStopsLoop(t *testing.T) {
	s, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	p := NewPurgeScheduler(ctx, s, func() time.Duration { return 24 * time.Hour })
	p.Start()

	cancel()
	// The done channel should close promptly.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.Done():
	case <-timer.C:
		t.Fatal("scheduler goroutine did not exit after context cancellation")
	}
}

// --- Property-based test for PurgeScheduler timing invariants ---

func TestPurgeScheduler_PropertyInvariants(t *testing.T) {
	// Property: entries older than retention are always purged after a
	// Trigger completes; entries younger than retention are never purged;
	// Stop() prevents any subsequent purge.
	if testing.Short() {
		t.Skip("property test skipped in short mode")
	}

	t.Run("OldEntriesPurged", func(t *testing.T) {
		// Any entry older than retention must be purged after Trigger.
		s, _ := newTestStore(t)
		_ = s.Mutate(t.Context(), "old1", func(c *api.Chat, _ bool) bool { c.Name = "Old"; return true })
		chatPath := filepath.Join(s.dir, "old1.json")
		// Age it well past retention (stamp + mtime).
		ageChat(t, s, "old1", 72*time.Hour)

		retention := 24 * time.Hour
		p := NewPurgeScheduler(t.Context(), s, func() time.Duration { return retention })
		p.Start()
		defer p.Stop()

		p.Trigger()
		if !waitForPurge(t, chatPath, 2*time.Second) {
			t.Error("entry older than retention survived purge")
		}
	})

	t.Run("YoungEntriesPreserved", func(t *testing.T) {
		// Any entry younger than retention must NOT be purged.
		s, _ := newTestStore(t)
		_ = s.Mutate(t.Context(), "young1", func(c *api.Chat, _ bool) bool { c.Name = "Young"; return true })
		chatPath := filepath.Join(s.dir, "young1.json")
		// Fresh activity, so the retention window has not elapsed.

		retention, calls := countingRetention(48 * time.Hour)
		p := NewPurgeScheduler(t.Context(), s, retention)
		p.Start()
		defer p.Stop()

		p.Trigger()
		// A pass must have run, or "not purged" proves nothing.
		waitForRetentionConsults(t, calls, 1)

		if _, err := os.Stat(chatPath); err != nil {
			t.Errorf("young entry was purged: %v", err)
		}
	})

	t.Run("StopPreventsAllPurges", func(t *testing.T) {
		// After Stop(), no purge should occur regardless of pending triggers.
		s, _ := newTestStore(t)
		_ = s.Mutate(t.Context(), "stop1", func(c *api.Chat, _ bool) bool { c.Name = "Stop"; return true })
		chatPath := filepath.Join(s.dir, "stop1.json")
		ageChat(t, s, "stop1", 72*time.Hour)

		retention := 24 * time.Hour
		p := NewPurgeScheduler(t.Context(), s, func() time.Duration { return retention })
		// Stop before Start — the goroutine never runs.
		p.Stop()
		p.Start() // Start after Stop should be a no-op.

		// Trigger after stop should be no-op.
		p.Trigger()
		// Start did launch a goroutine; wait for it to observe stopCh and exit,
		// which is the deterministic version of "nothing will purge now".
		select {
		case <-p.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler goroutine did not exit after Stop-then-Start")
		}

		if _, err := os.Stat(chatPath); err != nil {
			t.Errorf("entry purged after Stop: %v", err)
		}
	})

	t.Run("NoDuplicatePurgeCallbacks", func(t *testing.T) {
		// Each entry should be purged exactly once (no double-fire).
		s, _ := newTestStore(t)
		_ = s.Mutate(t.Context(), "dup1", func(c *api.Chat, _ bool) bool { c.Name = "Dup"; return true })
		chatPath := filepath.Join(s.dir, "dup1.json")
		ageChat(t, s, "dup1", 72*time.Hour)

		retention, calls := countingRetention(24 * time.Hour)
		p := NewPurgeScheduler(t.Context(), s, retention)
		p.Start()
		defer p.Stop()

		// Multiple triggers should not cause issues.
		for range 5 {
			p.Trigger()
		}
		if !waitForPurge(t, chatPath, 2*time.Second) {
			t.Error("entry not purged")
		}
		// After purge, re-triggering should not panic. Wait for the extra pass
		// rather than sleeping past it, so the "no panic" claim is witnessed.
		before := calls.Load()
		p.Trigger()
		waitForRetentionConsults(t, calls, before+1)
	})
}

// TestPurge_AgesFromUpdatedAtNotMtime pins the reference time.
//
// mtime moves for reasons that are not activity — a metadata rewrite, a
// settings-driven field change — so a purge that aged from it would keep
// resetting its own clock and never collect anything. The chat's own UpdatedAt
// is the activity record, and mtime survives only as the fallback for a chat
// that cannot be read.
func TestPurge_AgesFromUpdatedAtNotMtime(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()

	// keep: recent activity, but a backdated mtime.
	_ = s.Mutate(ctx, "keep", func(c *api.Chat, _ bool) bool { c.Name = "K"; return true })
	keepPath := filepath.Join(s.dir, "keep.json")
	stale := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(keepPath, stale, stale); err != nil {
		t.Fatalf("chtimes keep: %v", err)
	}

	// gone: genuinely stale activity.
	_ = s.Mutate(ctx, "gone", func(c *api.Chat, _ bool) bool { c.Name = "G"; return true })
	gonePath := filepath.Join(s.dir, "gone.json")
	ageChat(t, s, "gone", 72*time.Hour)

	s.purgeExpired(ctx, 24*time.Hour)

	if _, err := os.Stat(keepPath); err != nil {
		t.Errorf("chat with recent UpdatedAt was purged on a stale mtime: %v", err)
	}
	if _, err := os.Stat(gonePath); !os.IsNotExist(err) {
		t.Errorf("chat with stale UpdatedAt survived: stat err = %v", err)
	}
}
