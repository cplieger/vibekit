package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The SSE transport (fan-out, replay ring, Last-Event-ID resume, slow-client
// eviction, keepalives) is github.com/cplieger/webhttp/sse and is tested
// there. These tests pin vibekit's layer: emit marshaling + chat topics, the
// connected handshake's floor/head payload, initial-state replay, and the
// draining gate.

// --- emit / replay buffer ---

func TestEmit_AppendsToReplayBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})

	evts := h.bus.fanout.Buffered()
	if len(evts) != 2 {
		t.Fatalf("replay len = %d, want 2", len(evts))
	}
	if evts[0].Event.Topic != "c1" || evts[1].Event.Topic != "c2" {
		t.Errorf("replay topics: %q, %q", evts[0].Event.Topic, evts[1].Event.Topic)
	}
	if evts[0].ID >= evts[1].ID {
		t.Errorf("event IDs not monotonic: %d → %d", evts[0].ID, evts[1].ID)
	}
	if !strings.Contains(string(evts[0].Event.Data), `"chat_id":"c1"`) {
		t.Errorf("payload not the marshaled ServerEvent: %s", evts[0].Event.Data)
	}
}

func TestEmit_CapsBufferAtReplayBufSize(t *testing.T) {
	h, _, _ := newTestHub()
	for range replayBufSize + 100 {
		h.bus.emit(vibekit.ServerEvent{Type: "test"})
	}
	floor, head := h.replayBounds()
	if head-floor+1 != uint64(replayBufSize) {
		t.Errorf("window = %d, want cap %d", head-floor+1, replayBufSize)
	}
}

func TestEmit_TopicCarriesChatID(t *testing.T) {
	// Per-chat delivery filtering is the sse library's tested behavior;
	// vibekit's contract is that emit maps ChatID onto the event topic
	// (empty ChatID = global broadcast) so that filtering applies.
	h, _, _ := newTestHub()
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "connected"}) // global: empty topic

	got := h.bus.fanout.Buffered()
	if len(got) != 3 {
		t.Fatalf("buffered = %d events, want 3", len(got))
	}
	wantTopics := []string{"c1", "c2", ""}
	for i, e := range got {
		if e.Event.Topic != wantTopics[i] {
			t.Errorf("event %d topic = %q, want %q", i, e.Event.Topic, wantTopics[i])
		}
	}
}

// --- HandleSSE (integration-ish, direct call) ---

func TestHandleSSE_EmitsConnectedHandshake(t *testing.T) {
	h, _, _ := newTestHub()
	// Seed 3 events so the replay buffer has a known floor/head.
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"connected"`) {
		t.Errorf("SSE body missing connected event: %q", body)
	}
	// Floor == first emitted event id (1), head == last emitted id (3).
	if !strings.Contains(body, `"floor":1`) {
		t.Errorf("connected payload missing floor: %s", body)
	}
	if !strings.Contains(body, `"head":3`) {
		t.Errorf("connected payload missing head: %s", body)
	}
	// The handshake frame carries the head as its SSE id, so a client that
	// connects and immediately drops resumes from head, not 0.
	if !strings.Contains(body, "id: 3\n") {
		t.Errorf("handshake frame missing id: 3: %q", body)
	}
}

func TestReplayBounds_EmptyBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	floor, head := h.replayBounds()
	if floor != 0 {
		t.Errorf("empty floor = %d, want 0", floor)
	}
	if head != 0 {
		t.Errorf("empty head = %d, want 0", head)
	}
}

func TestReplayBounds_FollowsBufferWindow(t *testing.T) {
	h, _, _ := newTestHub()
	// Overflow the ring so the floor advances past 1.
	for range replayBufSize + 5 {
		h.bus.emit(vibekit.ServerEvent{Type: "test"})
	}
	floor, head := h.replayBounds()
	if floor <= 1 {
		t.Errorf("floor = %d, want > 1 after overflow", floor)
	}
	wantHead := uint64(replayBufSize + 5)
	if head != wantHead {
		t.Errorf("head = %d, want %d", head, wantHead)
	}
	if head-floor+1 != uint64(replayBufSize) {
		t.Errorf("window = %d, want %d", head-floor+1, replayBufSize)
	}
}

func TestHandleSSE_ReplaysSinceLastEventID(t *testing.T) {
	h, _, _ := newTestHub()

	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "1") // skip event 1 only
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `"chat_id":"c1"`) {
		t.Errorf("replay included event <= Last-Event-ID: %s", body)
	}
	if !strings.Contains(body, `"chat_id":"c2"`) || !strings.Contains(body, `"chat_id":"c3"`) {
		t.Errorf("replay missed events after Last-Event-ID: %s", body)
	}
}

// TestHandleSSE_ReplaysNewestBeyondClientBuffer pins the replay-delivery
// guarantee across a large gap: replayed frames are written directly to the
// response (not through the per-client delivery buffer), so a reconnect that
// missed hundreds of events still receives the NEWEST ones.
func TestHandleSSE_ReplaysNewestBeyondClientBuffer(t *testing.T) {
	h, _, _ := newTestHub()

	const n = 300
	for i := 1; i <= n; i++ {
		h.bus.emit(vibekit.ServerEvent{Type: "chat_updated", ChatID: vibekit.ChatID(fmt.Sprintf("c%d", i))})
	}

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "1")
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, fmt.Sprintf(`"chat_id":"c%d"`, n)) {
		t.Errorf("replay dropped the newest event c%d", n)
	}
	if !strings.Contains(body, `"chat_id":"c290"`) {
		t.Error("replay dropped event c290 past the old 256 client-buffer cap")
	}
	if strings.Contains(body, `"chat_id":"c1"`) {
		t.Error("replay included c1 which is <= Last-Event-ID")
	}
}

func TestHandleSSE_RejectsNonFlusher(t *testing.T) {
	h, _, _ := newTestHub()
	rec := &nonFlusherWriter{}
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	h.handleSSE(rec, req)
	if rec.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.status)
	}
}

// TestRegisterRoutes_DrainingGate asserts the drain refusal through the MUX, not
// by calling a handler directly. That is the whole point: the gate is a route
// wrapper applied at registration (agent.refuseWhenDraining), so a test that calls
// handleSSE or the dispatcher directly would bypass it and pass whether or not it
// is wired. Both gated routes are checked, and one ungated route is checked too,
// because the gate must NOT become global — the middleware chain also covers
// /api/health and the static assets, and a health probe during wind-down is what
// reports the wind-down.
func TestRegisterRoutes_DrainingGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"event stream", http.MethodGet, "/api/events", ""},
		{"command", http.MethodPost, "/api/command", `{"type":"create_chat","request_id":"r1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			h.lifecycle.draining.Store(true)

			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			// A bounded context so a REGRESSION fails fast instead of hanging:
			// without the gate the event stream opens and blocks forever, which
			// would turn this test into a 10-minute timeout rather than a failure
			// naming the status it got.
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			req := httptest.NewRequest(tc.method, tc.path, body).WithContext(ctx)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503 while draining", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "shutting down") {
				t.Errorf("body = %q, want vibekit's shutting-down envelope", rec.Body.String())
			}
		})
	}

	t.Run("an ungated route still answers while draining", func(t *testing.T) {
		h, _, _ := newTestHub()
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		h.lifecycle.draining.Store(true)

		req := httptest.NewRequest(http.MethodGet, "/api/config-template", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code == http.StatusServiceUnavailable {
			t.Error("an ungated route answered 503: the drain gate has become global")
		}
	})
}

// --- Helper: a ResponseWriter with no Flusher ---

type nonFlusherWriter struct {
	hdr    http.Header
	body   strings.Builder
	status int
}

func (w *nonFlusherWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}
func (w *nonFlusherWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *nonFlusherWriter) WriteHeader(code int)        { w.status = code }

// BenchmarkEmit measures the marshal+publish hot path (ring append; fan-out
// scaling is benchmarked in the sse library).
func BenchmarkEmit(b *testing.B) {
	h, _, _ := newTestHub()
	evt := vibekit.ServerEvent{Type: "chat_updated", ChatID: "bench"}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		h.bus.emit(evt)
	}
}

// TestSupervisedState_Property is GONE with supervisedState. Its invariants were
// about per-turn TRUST — a way to wave past vibekit's per-write staging gate for
// the rest of a turn. KAS reviews the whole turn in one approval, so there is no
// per-write gate and nothing to trust past.
