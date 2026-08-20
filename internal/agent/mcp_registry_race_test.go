package agent

import (
	"sync"
	"testing"
)

// TestMCPRegistry_ConcurrentRecordClear exercises recordConnected,
// recordInitFailure, and clearAll in parallel. Under -race this
// validates that the registry's RWMutex correctly guards the servers
// map and that Broadcast doesn't deadlock against clearAll.
func TestMCPRegistry_ConcurrentRecordClear(t *testing.T) {
	h, _, _ := newTestHub()
	reg := h.mcpRegistry

	// mcpConfig == nil makes originFor short-circuit to (OriginUser, true), so
	// every record lands and this exercises the lock rather than the filter.
	h.mcpConfig = nil

	const N = 100
	var wg sync.WaitGroup

	// Recorders.
	wg.Go(func() {
		for i := range N {
			name := "server-" + string(rune('A'+i%10))
			reg.recordConnected(h.lifecycle.shutdownCtx, name, nil, nil, nil)
		}
	})

	wg.Go(func() {
		for i := range N {
			name := "server-" + string(rune('A'+i%10))
			reg.recordInitFailure(h.lifecycle.shutdownCtx, name, "timeout")
		}
	})

	// Clearers.
	wg.Go(func() {
		for range N / 10 {
			reg.clearAll(h.lifecycle.shutdownCtx)
		}
	})

	// Snapshot readers.
	wg.Go(func() {
		for range N {
			_ = reg.Snapshot()
		}
	})

	wg.Wait()
}

// TestMCPRegistry_SignalReadyConcurrent verifies that calling
// signalReady from multiple goroutines doesn't double-close the
// readyCh channel (which would panic).
func TestMCPRegistry_SignalReadyConcurrent(t *testing.T) {
	h, _, _ := newTestHub()
	reg := h.mcpRegistry

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			reg.signalReady()
		})
	}
	wg.Wait()

	// Verify readyCh is closed.
	select {
	case <-reg.readyCh:
		// good
	default:
		t.Fatal("readyCh not closed after signalReady")
	}
}
