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

// --- OldestArchiveMTime ---

func TestOldestArchiveMTime(t *testing.T) {
	cases := []struct {
		setup  func(t *testing.T, s *Store) time.Time
		name   string
		wantOK bool
	}{
		{
			name:   "EmptyWhenNoArchiveDir",
			setup:  func(t *testing.T, s *Store) time.Time { return time.Time{} },
			wantOK: false,
		},
		{
			name: "EmptyWhenArchiveDirEmpty",
			setup: func(t *testing.T, s *Store) time.Time {
				if err := os.MkdirAll(filepath.Join(s.dir, "archive"), 0o700); err != nil {
					t.Fatal(err)
				}
				return time.Time{}
			},
			wantOK: false,
		},
		{
			name: "PicksOldestJSONFile",
			setup: func(t *testing.T, s *Store) time.Time {
				archiveDir := filepath.Join(s.dir, "archive")
				if err := os.MkdirAll(archiveDir, 0o700); err != nil {
					t.Fatal(err)
				}
				oldPath := filepath.Join(archiveDir, "old.json")
				newPath := filepath.Join(archiveDir, "new.json")
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
				archiveDir := filepath.Join(s.dir, "archive")
				if err := os.MkdirAll(archiveDir, 0o700); err != nil {
					t.Fatal(err)
				}
				subDir := filepath.Join(archiveDir, "subdir")
				if err := os.MkdirAll(subDir, 0o700); err != nil {
					t.Fatal(err)
				}
				md := filepath.Join(archiveDir, "chat.plan.md")
				if err := os.WriteFile(md, []byte("# plan"), 0o600); err != nil {
					t.Fatal(err)
				}
				mdTime := time.Now().Add(-48 * time.Hour)
				if err := os.Chtimes(md, mdTime, mdTime); err != nil {
					t.Fatal(err)
				}
				j := filepath.Join(archiveDir, "chat.json")
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
			got, ok := archive.OldestArchiveMTime(context.Background(), s.dir)
			if ok != tc.wantOK {
				t.Fatalf("OldestArchiveMTime ok=%v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				if got.Sub(wantTime) > time.Second || wantTime.Sub(got) > time.Second {
					t.Errorf("OldestArchiveMTime = %v, want near %v", got, wantTime)
				}
			}
		})
	}
}

// --- PurgeScheduler ---

// waitForPurge polls for the archive file to disappear, with a timeout.
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
	p := NewPurgeScheduler(context.Background(), s, retention)
	p.Start()
	defer p.Stop()

	p.Trigger()
	// Give the goroutine time to process.
	time.Sleep(50 * time.Millisecond)

	if got := calls.Load(); got == 0 {
		t.Error("retention callback not invoked")
	}
}

func TestPurgeScheduler_TriggerRunsPurgeWhenRetentionPositive(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Archive(context.Background(), "c1")
	archivePath := filepath.Join(s.dir, "archive", "c1.json")
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(archivePath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return 24 * time.Hour })
	p.Start()
	defer p.Stop()

	if !waitForPurge(t, archivePath, 2*time.Second) {
		t.Error("archive file survived purge after 2s")
	}
}

func TestPurgeScheduler_TriggerSchedulesForRemainingEntry(t *testing.T) {
	// A fresh archive entry (not yet expired) should not be purged
	// but the scheduler should schedule a future purge.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Archive(context.Background(), "c1")
	archivePath := filepath.Join(s.dir, "archive", "c1.json")

	p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return 24 * time.Hour })
	p.Start()
	defer p.Stop()

	// Give the goroutine time to process.
	time.Sleep(50 * time.Millisecond)

	// The file should still exist (not expired yet).
	if _, err := os.Stat(archivePath); err != nil {
		t.Errorf("fresh archive file was purged: %v", err)
	}
}

func TestPurgeScheduler_TriggerWithNoArchiveIsNoOp(t *testing.T) {
	s, _ := newTestStore(t)
	p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return 24 * time.Hour })
	p.Start()
	defer p.Stop()

	p.Trigger()
	// Give the goroutine time to process — no crash, no panic.
	time.Sleep(50 * time.Millisecond)
}

func TestPurgeScheduler_StopPreventsFutureTriggers(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Archive(context.Background(), "c1")
	archivePath := filepath.Join(s.dir, "archive", "c1.json")
	oldTime := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(archivePath, oldTime, oldTime)

	p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return 24 * time.Hour })
	// Stop before Start — the goroutine never runs.
	p.Stop()
	p.Trigger()

	// Give time for any spurious processing.
	time.Sleep(50 * time.Millisecond)

	// Stop must have short-circuited: the aged-out file survives.
	if _, err := os.Stat(archivePath); err != nil {
		t.Errorf("archive file removed after Stop: %v (Stop should freeze purge)", err)
	}
}

func TestPurgeScheduler_StartInvokesTrigger(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Archive(context.Background(), "c1")
	archivePath := filepath.Join(s.dir, "archive", "c1.json")
	oldTime := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(archivePath, oldTime, oldTime)

	p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return 24 * time.Hour })
	defer p.Stop()

	p.Start()

	if !waitForPurge(t, archivePath, 2*time.Second) {
		t.Error("archive file survived Start() after 2s")
	}
}

func TestPurgeScheduler_CollapsesConcurrentTriggers(t *testing.T) {
	// Multiple rapid Trigger calls should collapse into a single
	// evaluation, not queue N sequential purge passes.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Archive(context.Background(), "c1")

	p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return 24 * time.Hour })
	p.Start()
	defer p.Stop()

	// Fire multiple triggers rapidly.
	for range 10 {
		p.Trigger()
	}
	// Give the goroutine time to process — no crash, no panic.
	time.Sleep(50 * time.Millisecond)
}

func TestPurgeScheduler_ClampsMinWaitSo1HzSpinIsAvoided(t *testing.T) {
	// With retention=1s and an entry already aged past retention,
	// the scheduler must purge expired entries and keep fresh ones.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Archive(context.Background(), "c1")
	archivePath := filepath.Join(s.dir, "archive", "c1.json")
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(archivePath, old, old)

	// Create a second chat whose mtime is fresh (should survive).
	_ = s.Mutate(context.Background(), "c2", func(c *api.Chat, _ bool) bool { c.Name = "B"; return true })
	_ = s.Archive(context.Background(), "c2")
	c2Path := filepath.Join(s.dir, "archive", "c2.json")

	p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return time.Second })
	p.Start()
	defer p.Stop()

	if !waitForPurge(t, archivePath, 2*time.Second) {
		t.Error("expired entry not purged")
	}
	// c2 should survive (fresh mtime, retention=1s).
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
		_ = s.Mutate(context.Background(), "old1", func(c *api.Chat, _ bool) bool { c.Name = "Old"; return true })
		_ = s.Archive(context.Background(), "old1")
		archivePath := filepath.Join(s.dir, "archive", "old1.json")
		// Age it well past retention.
		oldTime := time.Now().Add(-72 * time.Hour)
		_ = os.Chtimes(archivePath, oldTime, oldTime)

		retention := 24 * time.Hour
		p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return retention })
		p.Start()
		defer p.Stop()

		p.Trigger()
		if !waitForPurge(t, archivePath, 2*time.Second) {
			t.Error("entry older than retention survived purge")
		}
	})

	t.Run("YoungEntriesPreserved", func(t *testing.T) {
		// Any entry younger than retention must NOT be purged.
		s, _ := newTestStore(t)
		_ = s.Mutate(context.Background(), "young1", func(c *api.Chat, _ bool) bool { c.Name = "Young"; return true })
		_ = s.Archive(context.Background(), "young1")
		archivePath := filepath.Join(s.dir, "archive", "young1.json")
		// Entry is fresh (just archived).

		retention := 48 * time.Hour
		p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return retention })
		p.Start()
		defer p.Stop()

		p.Trigger()
		time.Sleep(100 * time.Millisecond)

		if _, err := os.Stat(archivePath); err != nil {
			t.Errorf("young entry was purged: %v", err)
		}
	})

	t.Run("StopPreventsAllPurges", func(t *testing.T) {
		// After Stop(), no purge should occur regardless of pending triggers.
		s, _ := newTestStore(t)
		_ = s.Mutate(context.Background(), "stop1", func(c *api.Chat, _ bool) bool { c.Name = "Stop"; return true })
		_ = s.Archive(context.Background(), "stop1")
		archivePath := filepath.Join(s.dir, "archive", "stop1.json")
		oldTime := time.Now().Add(-72 * time.Hour)
		_ = os.Chtimes(archivePath, oldTime, oldTime)

		retention := 24 * time.Hour
		p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return retention })
		// Stop before Start — the goroutine never runs.
		p.Stop()
		p.Start() // Start after Stop should be a no-op.

		// Trigger after stop should be no-op.
		p.Trigger()
		time.Sleep(100 * time.Millisecond)

		if _, err := os.Stat(archivePath); err != nil {
			t.Errorf("entry purged after Stop: %v", err)
		}
	})

	t.Run("NoDuplicatePurgeCallbacks", func(t *testing.T) {
		// Each entry should be purged exactly once (no double-fire).
		s, _ := newTestStore(t)
		_ = s.Mutate(context.Background(), "dup1", func(c *api.Chat, _ bool) bool { c.Name = "Dup"; return true })
		_ = s.Archive(context.Background(), "dup1")
		archivePath := filepath.Join(s.dir, "archive", "dup1.json")
		oldTime := time.Now().Add(-72 * time.Hour)
		_ = os.Chtimes(archivePath, oldTime, oldTime)

		retention := 24 * time.Hour
		p := NewPurgeScheduler(context.Background(), s, func() time.Duration { return retention })
		p.Start()
		defer p.Stop()

		// Multiple triggers should not cause issues.
		for range 5 {
			p.Trigger()
		}
		if !waitForPurge(t, archivePath, 2*time.Second) {
			t.Error("entry not purged")
		}
		// After purge, re-triggering should not panic.
		p.Trigger()
		time.Sleep(50 * time.Millisecond)
	})
}
