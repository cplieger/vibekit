package hub

import (
	"sync"
	"testing"

	"vibekit/internal/api"
)

// TestPendingPermsTracker_ConcurrentAddRemoveList exercises concurrent
// Add, Remove, ClearForChat, and List operations. Under the race
// detector this catches missing or misused locks.
func TestPendingPermsTracker_ConcurrentAddRemoveList(t *testing.T) {
	tracker := newPendingPermsTracker()
	const N = 100

	var wg sync.WaitGroup

	// Writer: add entries.
	wg.Go(func() {
		for i := range N {
			evt := api.ServerEvent{ChatID: api.ChatID("chat-1"), Type: "permission_needed"}
			tracker.Add(int64(i), evt)
		}
	})

	// Writer: add entries for a different chat.
	wg.Go(func() {
		for i := range N {
			evt := api.ServerEvent{ChatID: api.ChatID("chat-2"), Type: "permission_needed"}
			tracker.Add(int64(N+i), evt)
		}
	})

	// Remover: remove entries for chat-1.
	wg.Go(func() {
		for i := range N {
			tracker.Remove(int64(i))
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
