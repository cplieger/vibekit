package hub

import (
	"sync"

	"vibekit/internal/api"
)

// replayRing is a fixed-size ring buffer for SSE events. It stores the
// most recent N events for replay on client reconnect. The ring uses
// head/tail index arithmetic for O(1) append instead of O(n) slice
// shifting. It has its own mutex, decoupling replay-buffer access from
// the Hub's main mutex that protects client fan-out and bridge state.
type replayRing struct {
	buf  []sseEvent
	cap  int
	head int
	len  int
	mu   sync.Mutex
}

// newReplayRing returns a ring buffer pre-allocated to capacity.
func newReplayRing(capacity int) *replayRing {
	return &replayRing{
		buf: make([]sseEvent, capacity),
		cap: capacity,
	}
}

// Append adds an event to the ring, evicting the oldest if at capacity.
// O(1) regardless of buffer size.
func (r *replayRing) Append(e sseEvent) {
	r.mu.Lock()
	r.buf[r.head] = e
	r.head = (r.head + 1) % r.cap
	if r.len < r.cap {
		r.len++
	}
	r.mu.Unlock()
}

// Replay returns events with eventID > sinceID, optionally filtered by
// chatID. If chatFilter is empty, all events are returned.
func (r *replayRing) Replay(sinceID uint64, chatFilter api.ChatID) []sseEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []sseEvent
	start := (r.head - r.len + r.cap) % r.cap
	for i := range r.len {
		e := r.buf[(start+i)%r.cap]
		if e.eventID <= sinceID {
			continue
		}
		if chatFilter != "" && e.chatID != "" && e.chatID != chatFilter {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Bounds returns (floor, head) of the ring. Floor is the oldest event
// ID still replayable; head is the newest. When empty, both are 0.
func (r *replayRing) Bounds() (floor, head uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.len == 0 {
		return 0, 0
	}
	oldest := (r.head - r.len + r.cap) % r.cap
	newest := (r.head - 1 + r.cap) % r.cap
	return r.buf[oldest].eventID, r.buf[newest].eventID
}

// Len returns the current number of events in the ring.
func (r *replayRing) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.len
}

// Events returns a snapshot of all events currently in the ring,
// ordered oldest to newest. Used by tests that need to inspect the
// buffer contents.
func (r *replayRing) Events() []sseEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sseEvent, r.len)
	start := (r.head - r.len + r.cap) % r.cap
	for i := range r.len {
		out[i] = r.buf[(start+i)%r.cap]
	}
	return out
}
