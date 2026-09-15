package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/sse"
	"github.com/cplieger/webhttp/v3"
)

// recordingPresence is the table as the runtime feeds it, recording what arrived.
type recordingPresence struct {
	mu     sync.Mutex
	events []sse.PresenceEvent
	alive  []string
}

func (p *recordingPresence) Observe(ev *sse.PresenceEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, *ev)
}

func (p *recordingPresence) Alive(tag string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.alive = append(p.alive, tag)
}

func (p *recordingPresence) snapshot() (events []sse.PresenceEvent, alive []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sse.PresenceEvent(nil), p.events...), append([]string(nil), p.alive...)
}

func postAlive(rt *Runtime, tag string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	rt.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/events/alive", nil)
	if tag != "" {
		req.Header.Set(clientTagHeader, tag)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleAlive_RecordsAValidTagAndAnswers204(t *testing.T) {
	cs := newFakeChatStore()
	br := newFakeBridge()
	table := &recordingPresence{}
	h := New(t.Context(), "/tmp/work", func() ACPBridge { return br }, cs, WithPresence(table))
	cs.Bus = h
	t.Cleanup(func() { shutdownHub(t, h) })

	rec := postAlive(h, "amxAEqwvwjG23476CxNmK6")
	if rec.Code != http.StatusNoContent {
		t.Errorf("POST /api/events/alive with a valid tag = %d, want 204; body %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 body = %q, want empty", rec.Body.String())
	}
	if _, alive := table.snapshot(); len(alive) != 1 || alive[0] != "amxAEqwvwjG23476CxNmK6" {
		t.Errorf("table.Alive calls = %q, want exactly the presented tag", alive)
	}
}

func TestHandleAlive_RefusesAnAbsentOrMalformedTag(t *testing.T) {
	cases := []struct {
		name string
		tag  string
	}{
		{"absent", ""},
		{"65_chars", strings.Repeat("a", 65)},
		{"outside_grammar", "tag with spaces"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newFakeChatStore()
			br := newFakeBridge()
			table := &recordingPresence{}
			h := New(t.Context(), "/tmp/work", func() ACPBridge { return br }, cs, WithPresence(table))
			cs.Bus = h
			t.Cleanup(func() { shutdownHub(t, h) })

			rec := postAlive(h, tc.tag)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			var body webhttp.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not the error envelope: %v: %q", err, rec.Body.String())
			}
			if body.Code != aliveInvalidCode {
				t.Errorf("code = %q, want %q", body.Code, aliveInvalidCode)
			}
			if _, alive := table.snapshot(); len(alive) != 0 {
				t.Errorf("table.Alive calls = %q, want none for a refused tag", alive)
			}
		})
	}
}

// A runtime wired without a table still answers the route: the receipt is dropped
// and the client sees ok, which is the fail-open direction.
func TestHandleAlive_WithoutATableAcceptsAndDrops(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	if rec := postAlive(h, "amxAEqwvwjG23476CxNmK6"); rec.Code != http.StatusNoContent {
		t.Errorf("status without a table = %d, want 204", rec.Code)
	}
}

// The hub's presence hook reaches the wired table with the tag the connect
// presented: one connected and one disconnected per served connection.
func TestPresenceHook_ForwardsConnectAndDisconnectWithTheTag(t *testing.T) {
	cs := newFakeChatStore()
	br := newFakeBridge()
	table := &recordingPresence{}
	h := New(t.Context(), "/tmp/work", func() ACPBridge { return br }, cs, WithPresence(table))
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	t.Cleanup(func() { shutdownHub(t, h) })

	ctx, cancel := context.WithTimeout(t.Context(), fixtureConnectDeadline)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.Header.Set(wireHeader, "1")
	req.Header.Set(clientTagHeader, "amxAEqwvwjG23476CxNmK6")
	h.handleSSE(httptest.NewRecorder(), req)

	events, _ := table.snapshot()
	if len(events) != 2 {
		t.Fatalf("presence events = %+v, want connected then disconnected", events)
	}
	if events[0].Kind != "connected" || events[0].Tag != "amxAEqwvwjG23476CxNmK6" {
		t.Errorf("first event = %+v, want connected with the presented tag", events[0])
	}
	if events[1].Kind != "disconnected" || events[1].Tag != "amxAEqwvwjG23476CxNmK6" || events[1].Cause == "" {
		t.Errorf("second event = %+v, want disconnected with the tag and a cause", events[1])
	}
}
