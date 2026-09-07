package agent

// The named heartbeat: both of its gates, its sequence, its shutdown join, and the
// one property that decides whether a real browser ever sees it — that an empty
// Topic broadcasts to a topic-FILTERED client.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// --- helpers ---

// waitUntil polls cond until it holds, failing with what it was waiting for.
// Deadline-bounded rather than a sleep: a sleep that is too short reports a
// mechanism as absent, and one that is long enough is slower than this.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("waited 5s for %s and it never happened", what)
}

// connectClient opens a REAL SSE client against rt and returns once the hub counts
// it. ClientCount is exactly what the heartbeat's first gate reads, so a faked
// subscriber would satisfy a different predicate than production consults.
//
// The recorder is written by the spawned goroutine and never read here, which is
// what keeps this free of a data race under -race: a test that needs the wire bytes
// drives handleSSE on its own goroutine instead (see the topic-filtered case below).
func connectClient(t *testing.T, rt *Runtime) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
		rt.handleSSE(httptest.NewRecorder(), req)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitUntil(t, "an SSE client to register with the hub", func() bool {
		return rt.bus.fanout.ClientCount() > 0
	})
}

// headID is the ring's newest assigned id, so a test can name the window it is
// about rather than filtering the whole ring.
func headID(rt *Runtime) uint64 {
	_, head := rt.bus.fanout.Bounds()
	return head
}

// heartbeatsSince returns the heartbeat frames published after sinceID, decoded.
// It matches on the frame's NAME, which is what separates a heartbeat from every
// other thing in the ring.
func heartbeatsSince(t *testing.T, rt *Runtime, sinceID uint64) []heartbeatPayload {
	t.Helper()
	var out []heartbeatPayload
	for _, e := range rt.bus.fanout.Buffered() {
		if e.ID <= sinceID || e.Event.Name != heartbeatEventName {
			continue
		}
		if e.Event.Topic != "" {
			t.Errorf("heartbeat %d carried topic %q, want empty: a scoped heartbeat reaches no topic-filtered client",
				e.ID, e.Event.Topic)
		}
		var p heartbeatPayload
		if err := json.Unmarshal(e.Event.Data, &p); err != nil {
			t.Fatalf("unmarshal heartbeat %d (%s): %v", e.ID, e.Event.Data, err)
		}
		out = append(out, p)
	}
	return out
}

// --- the two gates ---

func TestHeartbeat_PublishesANamedEventWhenIdle(t *testing.T) {
	rt, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, rt) })
	connectClient(t, rt)

	// Construction and MCP readiness may have emitted, and the gate reads real
	// publishes. Zeroing it is this test's way of saying "the stream has been quiet
	// for a full interval", which is the condition under test.
	rt.bus.lastPublishAt.Store(0)
	before := headID(rt)

	if got := rt.publishHeartbeat(0); got != 1 {
		t.Errorf("publishHeartbeat(0) = %d, want 1: a published beat advances the sequence", got)
	}

	beats := heartbeatsSince(t, rt, before)
	if len(beats) != 1 {
		t.Fatalf("publishHeartbeat(0) put %d heartbeats on the ring, want 1", len(beats))
	}
	if beats[0].Seq != 1 {
		t.Errorf("the first heartbeat carried seq %d, want 1", beats[0].Seq)
	}
}

func TestHeartbeat_SkipsWhenAnEventWasJustPublished(t *testing.T) {
	rt, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, rt) })
	connectClient(t, rt)

	// Through emit, the real broadcast path, rather than by stamping the clock
	// directly: what is under test is that an ORDINARY event closes the gate, which
	// also pins emit's own stamp.
	rt.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	before := headID(rt)

	if got := rt.publishHeartbeat(3); got != 3 {
		t.Errorf("publishHeartbeat(3) = %d, want 3: a skipped beat consumes no sequence number", got)
	}
	if beats := heartbeatsSince(t, rt, before); len(beats) != 0 {
		t.Errorf("publishHeartbeat put %d heartbeats on the ring over a stream that just published, want 0", len(beats))
	}
}

// TestHeartbeat_SkipsTheTickAfterItsOwnBeat pins the heartbeat stamping the publish
// clock as well as emit doing so. Without it the gate would only ever read the last
// REAL event, so every tick of a quiet stream would publish and the ring would fill
// at the tick rate rather than at the heartbeat rate.
func TestHeartbeat_SkipsTheTickAfterItsOwnBeat(t *testing.T) {
	rt, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, rt) })
	connectClient(t, rt)

	rt.bus.lastPublishAt.Store(0)
	before := headID(rt)
	seq := rt.publishHeartbeat(0)

	// The interval has not elapsed since that beat, so the next tick is a skip.
	if got := rt.publishHeartbeat(seq); got != seq {
		t.Errorf("the tick after a beat returned %d, want %d: it published again", got, seq)
	}
	if beats := heartbeatsSince(t, rt, before); len(beats) != 1 {
		t.Errorf("two consecutive ticks put %d heartbeats on the ring, want 1", len(beats))
	}
}

func TestHeartbeat_SkipsWithNoSubscribers(t *testing.T) {
	rt, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, rt) })

	// No connectClient: the idle gate is deliberately OPEN, so the only thing that
	// can refuse this beat is the client-count gate.
	rt.bus.lastPublishAt.Store(0)
	before := headID(rt)

	if got := rt.publishHeartbeat(0); got != 0 {
		t.Errorf("publishHeartbeat(0) = %d, want 0: a beat nobody could receive consumes no sequence number", got)
	}
	if beats := heartbeatsSince(t, rt, before); len(beats) != 0 {
		t.Errorf("publishHeartbeat put %d heartbeats on the ring with no client connected, want 0", len(beats))
	}
}

func TestHeartbeat_SequenceIsMonotonic(t *testing.T) {
	rt, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, rt) })
	connectClient(t, rt)

	before := headID(rt)
	seq := uint64(0)
	for range 3 {
		// Each published beat stamps the publish clock, so reopening the idle gate
		// is what stands in for the interval of quiet between two ticks.
		rt.bus.lastPublishAt.Store(0)
		seq = rt.publishHeartbeat(seq)
	}
	if seq != 3 {
		t.Errorf("three beats left the sequence at %d, want 3", seq)
	}

	beats := heartbeatsSince(t, rt, before)
	if len(beats) != 3 {
		t.Fatalf("three beats put %d heartbeats on the ring, want 3", len(beats))
	}
	for i, b := range beats {
		if want := uint64(i + 1); b.Seq != want {
			t.Errorf("heartbeat %d carried seq %d, want %d: the wire sequence must be consecutive", i, b.Seq, want)
		}
	}
}

// TestHeartbeat_StopsAtShutdown proves the loop is JOINED rather than leaked.
//
// It fails as an EXPIRED BUDGET rather than as a value mismatch: a leaked goroutine
// never returns from lifecycle.loops.Wait, so Shutdown answers
// "background loops still running: context deadline exceeded" once the 10s budget
// below runs out. A hang past that is go test's own timeout.
func TestHeartbeat_StopsAtShutdown(t *testing.T) {
	// The loop reads the interval when it starts, and New starts it — so the
	// override has to be installed before the runtime is built.
	prev := heartbeatInterval
	heartbeatInterval = time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	rt, _, _ := newTestHub()
	connectClient(t, rt)

	// Shut down a RUNNING loop rather than one parked on its first tick, which is
	// the state a leak would actually be hiding in.
	before := headID(rt)
	waitUntil(t, "the heartbeat loop to publish twice", func() bool {
		return len(heartbeatsSince(t, rt, before)) >= 2
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rt.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown with the heartbeat loop running: %v", err)
	}
}

// TestHandleSSE_HeartbeatReachesATopicFilteredClient is the empty-Topic property
// measured on the real wire: sse.topicMatches delivers a SCOPED event only on an
// exact filter match, so a heartbeat published on a chat's topic would reach no
// client connected with a different chat_id — and a chat-scoped connect is the
// ordinary case.
func TestHandleSSE_HeartbeatReachesATopicFilteredClient(t *testing.T) {
	rt, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, rt) })

	// The connect replay cannot carry it: sse.subscribe snapshots the ring only for
	// a RESUMING client (lastID > 0), and this connect sends no cursor. So the beat
	// has to be published into the live loop, from beside it.
	published := make(chan struct{})
	go func() {
		defer close(published)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if rt.bus.fanout.ClientCount() > 0 {
				rt.bus.lastPublishAt.Store(0)
				rt.publishHeartbeat(0)
				return
			}
			runtime.Gosched()
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events?chat_id=c1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	// Driven on the TEST's goroutine, so the recorder has exactly one writer and
	// reading its body below races nothing.
	rt.handleSSE(rec, req)
	<-published

	body := rec.Body.String()
	if !strings.Contains(body, "event: "+heartbeatEventName) {
		t.Fatalf("a client filtered to chat_id=c1 received no %q frame; body was:\n%s", heartbeatEventName, body)
	}
}
