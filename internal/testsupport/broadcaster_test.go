package testsupport

import (
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestNopBroadcaster_NoPanic(t *testing.T) {
	var b NopBroadcaster
	b.Broadcast(t.Context(), api.ServerEvent{})
}

func TestCaptureBroadcaster_SnapshotAndReset(t *testing.T) {
	var c CaptureBroadcaster
	ctx := t.Context()

	c.Broadcast(ctx, api.ServerEvent{Type: "test1"})
	c.Broadcast(ctx, api.ServerEvent{Type: "test2"})

	snap := c.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("Snapshot() len = %d, want 2", len(snap))
	}
	if snap[0].Type != "test1" || snap[1].Type != "test2" {
		t.Errorf("unexpected events: %+v", snap)
	}

	c.Reset()
	if len(c.Snapshot()) != 0 {
		t.Error("Reset() did not clear events")
	}
}

func TestCaptureBroadcaster_ConcurrentSafety(t *testing.T) {
	var c CaptureBroadcaster
	ctx := t.Context()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 100 {
				c.Broadcast(ctx, api.ServerEvent{Type: "concurrent"})
			}
		})
	}
	wg.Wait()
	if got := len(c.Snapshot()); got != 1000 {
		t.Errorf("captured %d events, want 1000", got)
	}
}
