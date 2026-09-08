package agent

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/webhttp/v2/sse"
)

// heartbeatEventName is the SSE `event:` field of the named keepalive, and the name
// the client registers its own listener under. A NAMED event because the transport's
// own `": keepalive"` comment is discarded by the EventSource parser.
//
// It must be published through the hub: sse.Serve writes its own keepalive from the
// stream goroutine and hands sse.Writer only to OnConnect, so a second goroutine
// writing that ResponseWriter interleaves mid-frame.
const heartbeatEventName = "heartbeat"

// heartbeatInterval is the cadence of the named heartbeat, and it IS
// keepaliveInterval: one liveness cadence rather than two numbers that can drift.
// A var only so a test can drive the loop in milliseconds; never reassigned here.
var heartbeatInterval = keepaliveInterval

// heartbeatPayload is the heartbeat frame's data. Deliberately not a
// vibekit.ServerEvent and not a generated wire type: the client's `heartbeat`
// listener reads the frame's arrival and id without decoding the body, so the
// sequence number is for a human reading the wire and for the monotonicity test.
type heartbeatPayload struct {
	Seq uint64 `json:"seq"`
}

// notePublish records that something reached the fan-out. The heartbeat's idle
// gate reads it, which is what makes a busy instance publish no heartbeats at all.
func (b *bus) notePublish() {
	b.lastPublishAt.Store(time.Now().UnixNano())
}

// idleFor reports whether nothing has been published for at least d. A hub that
// has never published counts as idle.
func (b *bus) idleFor(d time.Duration) bool {
	last := b.lastPublishAt.Load()
	if last == 0 {
		return true
	}
	return time.Now().UnixNano()-last >= int64(d)
}

// heartbeatLoopEvery binds the cadence for one runtime's loop, reading
// heartbeatInterval on the CONSTRUCTOR's goroutine.
//
// A race fix: lc.loops.Go returns before its goroutine runs its first line, so a
// loop reading the package var lazily races the write a test makes to drive the
// cadence. The captured value is threaded into publishHeartbeat for the same reason.
func (rt *Runtime) heartbeatLoopEvery(interval time.Duration) func() {
	return func() { rt.heartbeatLoop(interval) }
}

// heartbeatLoop publishes a named heartbeat at interval while a client is connected
// and the hub has been quiet for one interval. It runs on the runtime's loop
// WaitGroup and exits on lifecycle.done, so Shutdown joins it.
func (rt *Runtime) heartbeatLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var seq uint64
	for {
		select {
		case <-rt.lifecycle.done:
			return
		case <-ticker.C:
			seq = rt.publishHeartbeat(seq, interval)
		}
	}
}

// publishHeartbeat emits one heartbeat when both gates open and returns the sequence
// number to carry into the next tick. Split out of the loop so the gates are testable
// without driving a ticker; interval is the caller's own captured cadence.
//
// ClientCount answers "is anyone listening" and the idle gate "has the stream already
// proved itself"; a beat beside a real event spends a ring slot for nothing. The Topic
// must stay EMPTY — a scoped event reaches only an exactly-matching chat_id filter,
// and a topic-filtered client is the common case.
func (rt *Runtime) publishHeartbeat(seq uint64, interval time.Duration) uint64 {
	if rt.bus.fanout.ClientCount() == 0 {
		return seq
	}
	if !rt.bus.idleFor(interval) {
		return seq
	}
	next := seq + 1
	data, err := json.Marshal(heartbeatPayload{Seq: next})
	if err != nil {
		// The sequence does NOT advance: a beat that never reached the wire must
		// not consume a number, or the published sequence carries a hole.
		slog.Error("heartbeat marshal", "seq", next, "error", err)
		return seq
	}
	rt.bus.fanout.Publish(sse.Event{Name: heartbeatEventName, Data: data})
	// Stamped by the heartbeat itself as well as by emit, so consecutive beats are
	// spaced by the interval rather than every tick re-reading the last REAL event.
	rt.bus.notePublish()
	return next
}
