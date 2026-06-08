package testsupport

import (
	"context"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
)

// Compile-time assertions.
var _ api.Broadcaster = (*NopBroadcaster)(nil)
var _ api.Broadcaster = (*CaptureBroadcaster)(nil)

// NopBroadcaster is a zero-alloc no-op satisfying api.Broadcaster.
type NopBroadcaster struct{}

// Broadcast is a no-op.
func (NopBroadcaster) Broadcast(_ context.Context, _ api.ServerEvent) {}

// CaptureBroadcaster is a thread-safe event capture implementing api.Broadcaster.
type CaptureBroadcaster struct {
	events []api.ServerEvent
	mu     sync.Mutex
}

// Broadcast captures the event.
func (c *CaptureBroadcaster) Broadcast(_ context.Context, e api.ServerEvent) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// Snapshot returns a copy of all captured events.
func (c *CaptureBroadcaster) Snapshot() []api.ServerEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]api.ServerEvent(nil), c.events...)
}

// Reset clears all captured events.
func (c *CaptureBroadcaster) Reset() {
	c.mu.Lock()
	c.events = nil
	c.mu.Unlock()
}
