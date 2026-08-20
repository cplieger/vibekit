package agent

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestIsSafeExternalURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://auth.example.com/oauth?code=1", true},
		{"http://localhost:8080/callback", true},
		{"HTTPS://Example.com", true}, // scheme is lowercased by url.Parse
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"data:text/html,<script>", false},
		{"ftp://example.com", false},
		{"", false},
		{"not a url", false},
		{"//example.com", false}, // no scheme
	}
	for _, tt := range tests {
		if got := isSafeExternalURL(tt.url); got != tt.want {
			t.Errorf("isSafeExternalURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

// openExternalURLMsg builds a _kiro/openExternalUrl A→C request with the
// given url.
func openExternalURLMsg(t *testing.T, id int64, url string) *vibekit.RPCResponse {
	t.Helper()
	return &vibekit.RPCResponse{
		Method: methodKiroOpenExternalURL,
		ID:     &id,
		Params: mustJSON(t, map[string]any{"url": url}),
	}
}

func TestHandleOpenExternalURL(t *testing.T) {
	t.Run("SafeURLBroadcasts", func(t *testing.T) {
		h, _, _ := newTestHub()
		_, before := h.bus.fanout.Bounds()
		h.translateACPEvent("c1", openExternalURLMsg(t, 1, "https://auth.example.com/oauth"))
		types := extractTypes(t, bufferedSince(h, before))
		if missing := missingEvents(types, string(vibekit.EventOpenExternalURL)); len(missing) > 0 {
			t.Errorf("missing events %v; got %v", missing, types)
		}
	})

	t.Run("UnsafeURLDoesNotBroadcast", func(t *testing.T) {
		h, _, _ := newTestHub()
		_, before := h.bus.fanout.Bounds()
		h.translateACPEvent("c1", openExternalURLMsg(t, 2, "javascript:alert(1)"))
		types := extractTypes(t, bufferedSince(h, before))
		for _, ty := range types {
			if ty == string(vibekit.EventOpenExternalURL) {
				t.Fatalf("unsafe URL must not broadcast open_external_url; got %v", types)
			}
		}
	})

	t.Run("HandledAsClientRequest", func(t *testing.T) {
		// handleKiroClientRequest must claim the method (return true) so it
		// never falls through to the unhandled-extension debug log.
		h, _, _ := newTestHub()
		if !h.inbound.handleKiroClientRequest(t.Context(), "c1", openExternalURLMsg(t, 3, "https://x.example")) {
			t.Fatal("handleKiroClientRequest should handle _kiro/openExternalUrl")
		}
	})
}

// TestAuthTokenLatchTracksTheLastVend pins the latch's two halves. It has to
// SET, or readiness cannot report a signed-out runtime; and it has to CLEAR on
// the next success, because the failure it serves is an expired SSO refresh chain
// and signing back in is exactly what makes the latch stale. A set-only latch
// would keep reporting unready after the user fixed it, with nothing on the wire
// to say otherwise.
func TestAuthTokenLatchTracksTheLastVend(t *testing.T) {
	h, _, _ := newTestHub()

	if h.AuthTokenUnavailable() {
		t.Error("a runtime that has never vended a token must not report a dead sign-in")
	}

	// A runtime with no token source is the ErrNoSource path, which is the same
	// failure shape as an expired login as far as readiness is concerned.
	if _, err := h.inbound.kiroAccessTokenResult(t.Context()); err == nil {
		t.Fatal("kiroAccessTokenResult with no source should fail")
	}
	if !h.AuthTokenUnavailable() {
		t.Error("a failed vend must latch, or /api/health has nothing to report")
	}

	h.authLatch.record(nil)
	if h.AuthTokenUnavailable() {
		t.Error("a successful vend must clear the latch: the sign-in that fixes this is what makes it stale")
	}
}

// TestAuthTokenLatchIsNilSafe covers the composition-root ordering: the readiness
// option is handed h.AuthTokenUnavailable as a method value, and a runtime built
// without the latch (a test double, a partially-wired composition) must read as
// signed-in rather than panicking inside a health probe.
func TestAuthTokenLatchIsNilSafe(t *testing.T) {
	// A bare Runtime, which is the partially-wired case the readiness option's
	// method value can be taken from.
	if (&Runtime{}).AuthTokenUnavailable() {
		t.Error("a runtime with no inbound ladder must not report a dead sign-in")
	}
	// A bare ladder, which is the case that survives the latch itself being nil.
	in := &inbound{}
	if in.AuthTokenUnavailable() {
		t.Error("a ladder with no latch must not report a dead sign-in")
	}
	// The vend path must also survive it, since the latch is written there.
	if _, err := in.kiroAccessTokenResult(t.Context()); err == nil {
		t.Fatal("expected the no-source error")
	}
}

// TestAccessTokenFailureBroadcastsTheAuthError pins the client half of D106: the
// failure used to exist only as one slog line and a JSON-RPC error to KAS, and
// KAS's answer to that error is to run UNAUTHENTICATED — the session opens and
// every service-backed surface behind it fails, with nothing on screen saying
// why. The frame carries the distinct code static-src/handlers/error-routing.ts
// routes to the sign-in banner.
func TestAccessTokenFailureBroadcastsTheAuthError(t *testing.T) {
	h, _, _ := newTestHub()
	_, before := h.bus.fanout.Bounds()

	// kiroToken is nil on a test agent, so the vend fails with ErrNoSource.
	id := int64(7)
	h.inbound.respondKiroAccessToken(t.Context(), "c1", &vibekit.RPCResponse{
		Method: methodKiroGetAccessToken,
		ID:     &id,
	})

	events := bufferedSince(h, before)
	types := extractTypes(t, events)
	if missing := missingEvents(types, string(vibekit.EventError)); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, types)
	}

	var found bool
	for _, e := range events {
		var msg vibekit.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type != vibekit.EventError {
			continue
		}
		raw, err := json.Marshal(msg.Payload)
		if err != nil {
			t.Fatalf("remarshal payload: %v", err)
		}
		var p vibekit.ErrorPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("unmarshal error payload: %v", err)
		}
		if p.Code != vibekit.ErrCodeAuthTokenUnavailable {
			t.Errorf("code = %q, want %q", p.Code, vibekit.ErrCodeAuthTokenUnavailable)
		}
		if p.Message == "" {
			t.Error("the frame must carry kiro-cli's own reason: no wording invented here is more specific")
		}
		found = true
	}
	if !found {
		t.Error("no error event was broadcast for a failed token vend")
	}
}
