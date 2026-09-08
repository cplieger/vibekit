package agent

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/webhttp/v2/sse"
)

// heartbeatEventName is the SSE `event:` field of the named keepalive, and the
// name the client registers its own listener under.
//
// A COMMENT cannot serve here. The transport's own keepalive is `": keepalive"`,
// which the EventSource parser DISCARDS — so an idle healthy stream produces no
// observable frame in the browser at all, and a client watchdog measuring
// time-since-last-received-event has nothing to measure. A named event is the
// smallest thing that reaches the client.
//
// It also has to be published through the HUB rather than written into the live
// stream. webhttp/v2@v2.1.0's sse.Serve writes the comment keepalive from its own
// stream goroutine (sse/serve.go writeKeepalive) and hands sse.Writer only to the
// OnConnect hook, so a second goroutine writing to that ResponseWriter would
// INTERLEAVE: writeFrame makes several Fprintf calls per frame and no mutex spans
// them. Widening the library needs a release plus a pin bump in this repo, which is
// out of scope, so hub.Publish with a non-empty Event.Name is the only in-vibekit
// channel for a named event.
const heartbeatEventName = "heartbeat"

// heartbeatInterval is the cadence of the named heartbeat, and it IS
// keepaliveInterval: one liveness cadence for the whole stream rather than two
// numbers that can drift apart. A var only so a test can drive the loop in
// milliseconds (healBaseDelay's established shape); never reassigned in production.
var heartbeatInterval = keepaliveInterval

// heartbeatPayload is the heartbeat frame's data.
//
// Deliberately NOT a vibekit.ServerEvent and deliberately not a generated wire
// type: the frame carries a NAME, so it reaches the client's own `heartbeat`
// listener rather than EventSource.onmessage, and that listener reads the frame's
// ARRIVAL and its id without decoding the body at all. The sequence number is here
// for a human reading the wire and for the monotonicity test.
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

// heartbeatLoop publishes a named heartbeat at interval, while a client is connected
// and the stream has been quiet for one interval. It runs on the runtime's own loop
// WaitGroup and exits on lifecycle.done, so Shutdown joins it.
//
// THE RING COST, stated rather than hidden. hub.Publish appends to the
// replayBufSize (1024) replay ring, so an otherwise-idle instance with one client
// connected fills it with heartbeats in 1024 * 15s = 15360s, about 4.3 hours —
// after which `floor` has advanced past every older real event and a client
// reconnecting from before that window gets a transport:gap instead of a clean
// replay. That is correct behaviour for a 4-hour outage (the gap handler clears
// thinking, reloads the headers and refetches the active chat), and it is WHY the
// heartbeat is IDLE-GATED: on a busy instance real events prove liveness, so no
// heartbeat is published at all and real-event retention is unharmed. Do NOT raise
// replayBufSize to compensate — a message_appended frame can itself be megabytes,
// so a bigger ring is a bigger memory ceiling.
//
// The library's comment keepalive stays exactly as it was (WithKeepalive is
// untouched): it is the tested proxy-idle protection at ~100 bytes per 15s, and the
// named heartbeat being idle-gated means the two never both fire for one quiet
// interval.
// heartbeatLoopEvery binds the cadence for one runtime's loop, reading
// heartbeatInterval on the CONSTRUCTOR's goroutine rather than inside the loop.
//
// That is a race fix, not a style choice. lc.loops.Go returns before its goroutine
// runs its first line, and this package leaves dozens of runtimes alive for the whole
// test binary — so a loop reading the package var lazily can read it at any later
// instant, which races the write a test makes to drive the cadence in milliseconds.
// Captured here, the goroutine never touches the var at all — which is why the
// captured value is threaded on into publishHeartbeat's idle gate rather than that
// gate re-reading the var, the one read that kept the race alive.
func (rt *Runtime) heartbeatLoopEvery(interval time.Duration) func() {
	return func() { rt.heartbeatLoop(interval) }
}

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

// publishHeartbeat emits one heartbeat when both gates open and returns the
// sequence number to carry into the next tick. Split out of the loop so the gates
// are testable without driving a ticker.
//
// Both gates are load-bearing. ClientCount answers "is anyone listening": a
// heartbeat published to nobody would spend a ring slot and prove nothing. The idle
// gate answers "has the stream already proved itself": a real event within the
// interval IS the liveness signal the client's watchdog measures, so publishing
// beside it would double the ring cost for no added information.
//
// The Topic is EMPTY, which is REQUIRED rather than incidental: sse.topicMatches
// delivers a scoped event only to a subscriber whose filter matches exactly, so a
// heartbeat published on a chat's topic would never reach a client connected with a
// different chat_id — and a topic-filtered client is the common case here. An empty
// topic broadcasts to every subscriber.
//
// interval is the caller's OWN cadence, passed in rather than read off
// heartbeatInterval here: this runs on the loop goroutine, and a lazy read of the
// package var races the write a test makes to drive the cadence in milliseconds.
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
		// not consume a number, or the published sequence carries a hole no
		// reader can explain.
		slog.Error("heartbeat marshal", "seq", next, "error", err)
		return seq
	}
	rt.bus.fanout.Publish(sse.Event{Name: heartbeatEventName, Data: data})
	// Stamped by the heartbeat itself as well as by emit, so consecutive beats are
	// spaced by the interval rather than every tick re-reading the last REAL event.
	rt.bus.notePublish()
	return next
}
