package hub

import (
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// TestSSEController_EmitRemoveConcurrent exercises the race between emit
// (which iterates sc.clients and may evict slow clients) and remove
// (which deletes from the same map).
func TestSSEController_EmitRemoveConcurrent(t *testing.T) {
	sc := newSSEController(64)
	const N = 50

	clients := make([]*sseClient, N)
	for i := range clients {
		clients[i] = &sseClient{
			ch:     make(chan sseEvent, 1),
			cancel: func() {},
			chatID: "chat",
		}
		sc.add(clients[i])
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			sc.emit(api.ServerEvent{ChatID: "chat", Type: "test"}, []byte(`{}`))
		}
	})
	wg.Go(func() {
		for _, c := range clients {
			sc.remove(c)
		}
	})
	wg.Wait()
}

// TestSSEController_SeqNoGaps verifies that after concurrent emits,
// all expected sequence numbers are present in the ring (no lost events).
// Note: ring insertion order may differ from eventID order because
// seq.Add runs before the lock, but no events should be lost.
func TestSSEController_SeqNoGaps(t *testing.T) {
	const total = 800
	sc := newSSEController(total) // large enough to hold all

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range total / 8 {
				sc.emit(api.ServerEvent{Type: "test"}, []byte(`{}`))
			}
		})
	}
	wg.Wait()

	events := sc.replay.Events()
	if len(events) != total {
		t.Fatalf("expected %d events in ring, got %d", total, len(events))
	}
	seen := make(map[uint64]bool, total)
	for _, e := range events {
		if seen[e.eventID] {
			t.Fatalf("duplicate eventID %d", e.eventID)
		}
		seen[e.eventID] = true
	}
	for i := uint64(1); i <= total; i++ {
		if !seen[i] {
			t.Fatalf("missing eventID %d", i)
		}
	}
}
