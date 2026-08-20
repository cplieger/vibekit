package agent

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestPendingPermsTracker_ConcurrentAddTakeList exercises concurrent
// Add, TakeIfPresent, ClearForChat, and List operations. Under the race
// detector this catches missing or misused locks.
func TestPendingPermsTracker_ConcurrentAddTakeList(t *testing.T) {
	tracker := newPendingPermsTracker()
	const N = 100

	var wg sync.WaitGroup

	// Writer: add entries.
	wg.Go(func() {
		for i := range N {
			evt := vibekit.ServerEvent{ChatID: vibekit.ChatID("chat-1"), Type: "permission_needed"}
			tracker.Add(int64(i), evt)
		}
	})

	// Writer: add entries for a different chat.
	wg.Go(func() {
		for i := range N {
			evt := vibekit.ServerEvent{ChatID: vibekit.ChatID("chat-2"), Type: "permission_needed"}
			tracker.Add(int64(N+i), evt)
		}
	})

	// Taker: claim entries for chat-1.
	wg.Go(func() {
		for i := range N {
			tracker.TakeIfPresent(int64(i))
		}
	})

	// Clear: clear all for chat-2.
	wg.Go(func() {
		for range 10 {
			tracker.ClearForChat("chat-2")
		}
	})

	// Reader: list concurrently.
	wg.Go(func() {
		for range N {
			_ = tracker.List("")
			_ = tracker.List("chat-1")
		}
	})

	wg.Wait()
}

// TestPendingPermsTracker_TakeIfPresent_OneWinnerPerRequest is the reason
// TakeIfPresent exists. Two surfaces answer one request at the same instant —
// two browser tabs, or a human racing the unattended floor's deadline — and
// exactly one of them may be told to go ahead. The check-then-act pair this
// replaced let both through, and kiro-cli then discarded the loser's answer
// without telling anyone, so the winner was decided there rather than here.
//
// Every goroutine contends for ONE id, so the count is the assertion: any
// number other than 1 is the bug, whichever side wins.
func TestPendingPermsTracker_TakeIfPresent_OneWinnerPerRequest(t *testing.T) {
	const answerers = 16
	const rounds = 200

	for round := range rounds {
		tracker := newPendingPermsTracker()
		id := int64(round)
		want := vibekit.ServerEvent{ChatID: "chat-1", Type: vibekit.EventPermissionNeeded}
		tracker.Add(id, want)

		var wins atomic.Int64
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range answerers {
			wg.Go(func() {
				<-start
				got, ok := tracker.TakeIfPresent(id)
				if !ok {
					return
				}
				wins.Add(1)
				// The winner also gets the event, which is what tells the caller
				// which kind of decision it just settled.
				if got.Type != want.Type || got.ChatID != want.ChatID {
					t.Errorf("winner got event %+v, want %+v", got, want)
				}
			})
		}
		close(start)
		wg.Wait()

		if n := wins.Load(); n != 1 {
			t.Fatalf("round %d: %d answerers claimed one request, want exactly 1", round, n)
		}
		if _, ok := tracker.TakeIfPresent(id); ok {
			t.Fatalf("round %d: request still claimable after being taken", round)
		}
	}
}
