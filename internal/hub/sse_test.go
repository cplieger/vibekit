package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"pgregory.net/rapid"
)

// The SSE transport (fan-out, replay ring, Last-Event-ID resume, slow-client
// eviction, keepalives) is github.com/cplieger/webhttp/sse and is tested
// there. These tests pin vibekit's layer: emit marshaling + chat topics, the
// connected handshake's floor/head payload, initial-state replay, and the
// draining gate.

// --- emit / replay buffer ---

func TestEmit_AppendsToReplayBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c2"})

	evts := h.sse.hub.Buffered()
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
		h.emit(api.ServerEvent{Type: "test"})
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
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.emit(api.ServerEvent{Type: "connected"}) // global: empty topic

	got := h.sse.hub.Buffered()
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
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
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
		h.emit(api.ServerEvent{Type: "test"})
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

	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c1"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c2"})
	h.emit(api.ServerEvent{Type: "chat_updated", ChatID: "c3"})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
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
		h.emit(api.ServerEvent{Type: "chat_updated", ChatID: api.ChatID(fmt.Sprintf("c%d", i))})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
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

func TestHandleSSE_DrainingGate(t *testing.T) {
	h, _, _ := newTestHub()
	h.lifecycle.draining.Store(true)
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()
	h.handleSSE(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 while draining", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "shutting down") {
		t.Errorf("body = %q, want vibekit's shutting-down envelope", rec.Body.String())
	}
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
	evt := api.ServerEvent{Type: "chat_updated", ChatID: "bench"}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		h.emit(evt)
	}
}

// TestSupervisedState_Property uses pgregory.net/rapid to verify
// supervisedState Set/Clear/HasTrust invariants under random operation
// sequences:
//   - After SetTrust(id), HasTrust(id) == true
//   - After ClearTrust(id, _), HasTrust(id) == false
//   - SetTrust is idempotent: duplicate calls don't fire broadcast twice
func TestSupervisedState_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var broadcasts int
		ss := newSupervisedState(func(_ context.Context, _ api.ServerEvent) { broadcasts++ })

		// Model: track expected trust state.
		model := make(map[api.ChatID]bool)

		ops := rapid.IntRange(1, 200).Draw(t, "ops")
		for range ops {
			chatID := api.ChatID(rapid.StringMatching(`^c[1-5]$`).Draw(t, "chatID"))
			action := rapid.IntRange(0, 1).Draw(t, "action")

			switch action {
			case 0: // SetTrust
				prevBroadcasts := broadcasts
				wasSet := model[chatID]
				ss.SetTrust(chatID)
				model[chatID] = true
				if !ss.HasTrust(chatID) {
					t.Fatalf("HasTrust(%q) = false after SetTrust", chatID)
				}
				if wasSet && broadcasts != prevBroadcasts {
					t.Fatalf("duplicate SetTrust(%q) fired broadcast", chatID)
				}
				if !wasSet && broadcasts != prevBroadcasts+1 {
					t.Fatalf("first SetTrust(%q) did not fire broadcast", chatID)
				}
			case 1: // ClearTrust
				ss.ClearTrust(chatID, "cancelled")
				model[chatID] = false
				if ss.HasTrust(chatID) {
					t.Fatalf("HasTrust(%q) = true after ClearTrust", chatID)
				}
			}
		}
	})
}
