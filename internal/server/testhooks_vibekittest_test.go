//go:build vibekit_test

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/sse"
	"github.com/cplieger/vibekit/internal/push"
)

// probeEngine is a fakeEngine that also answers the SSE probe, so the hooks mount.
type probeEngine struct {
	fakeEngine
	armed   int
	clients int
	legacy  uint64
	v3      uint64
}

func (p *probeEngine) SSEClientCount() int           { return p.clients }
func (p *probeEngine) SSEConnects() (uint64, uint64) { return p.legacy, p.v3 }
func (p *probeEngine) CloseNextSSEAfter(n int)       { p.armed = n }

func TestTestHooks_CensusReadsTheProbe(t *testing.T) {
	eng := &probeEngine{clients: 2, legacy: 1, v3: 5}
	mux := http.NewServeMux()
	(&Server{agent: eng}).registerTestHooks(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/test/sse", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/test/sse = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got sseProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Clients != 2 || got.LegacyConnect != 1 || got.V3Connect != 5 || got.Presence == nil {
		t.Errorf("census = %+v, want clients 2, legacy 1, v3 5, presence []", got)
	}
}

func TestTestHooks_CloseAfterArmsTheProbe(t *testing.T) {
	eng := &probeEngine{}
	mux := http.NewServeMux()
	(&Server{agent: eng}).registerTestHooks(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/test/sse/close-after", strings.NewReader(`{"after":3}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || eng.armed != 3 {
		t.Errorf("close-after = %d, armed %d; want 204 and 3", rec.Code, eng.armed)
	}

	for _, body := range []string{`{"after":0}`, `{"after":-1}`, `not json`} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/sse/close-after", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("close-after %s = %d, want 400", body, rec.Code)
		}
	}
}

// An engine that is not the runtime mounts nothing: the hooks are the runtime's
// probe, and a server without one has no census to serve.
func TestTestHooks_NarrowEngineMountsNothing(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{agent: &fakeEngine{}}).registerTestHooks(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/test/sse", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/test/sse with a narrow engine = %d, want 404", rec.Code)
	}
}

// The census carries the presence table and the send filter's counters when the
// push service answers the probe: one row per tag with its verdict, the two
// transitions, and one suppressed count per registered kind.
func TestTestHooks_CensusReportsPresenceAndSuppression(t *testing.T) {
	presence := push.NewPresence()
	presence.Observe(&sse.PresenceEvent{Kind: sse.PresenceConnected, Tag: "amxAEqwvwjG23476CxNmK6"})
	svc := push.New(context.Background(), t.TempDir(), "mailto:test@example.com", push.WithPresence(presence))
	t.Cleanup(svc.Close)
	mux := http.NewServeMux()
	(&Server{agent: &probeEngine{}, push: svc}).registerTestHooks(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/test/sse", http.NoBody))
	var got sseProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Presence) != 1 || got.Presence[0].Tag != "amxAEqwvwjG23476CxNmK6" ||
		got.Presence[0].Connected != 1 || got.Presence[0].Gone || got.Presence[0].LastAliveAt == "" {
		t.Errorf("presence = %+v, want one present row for the connected tag with its acknowledgement time", got.Presence)
	}
	if got.PresenceAlive != 1 || got.PresenceExpired != 0 {
		t.Errorf("transitions = (alive %d, expired %d), want (1, 0)", got.PresenceAlive, got.PresenceExpired)
	}
	for _, kr := range push.Kinds() {
		if _, ok := got.Suppressed[kr.Kind]; !ok {
			t.Errorf("push_suppressed_total lacks kind %q", kr.Kind)
		}
	}
}
