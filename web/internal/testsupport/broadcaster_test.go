package testsupport

import (
	"context"
	"sync"
	"testing"

	"vibekit/internal/api"
)

func TestNopBroadcaster_NoPanic(t *testing.T) {
	var b NopBroadcaster
	b.Broadcast(context.Background(), api.ServerEvent{})
}

func TestCaptureBroadcaster_SnapshotAndReset(t *testing.T) {
	var c CaptureBroadcaster
	ctx := context.Background()

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
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				c.Broadcast(ctx, api.ServerEvent{Type: "concurrent"})
			}
		}()
	}
	wg.Wait()
	if got := len(c.Snapshot()); got != 1000 {
		t.Errorf("captured %d events, want 1000", got)
	}
}
