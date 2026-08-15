package hub

import (
	"testing"

	"github.com/cplieger/vibekit/internal/api"
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
func openExternalURLMsg(t *testing.T, id int64, url string) *api.RPCResponse {
	t.Helper()
	return &api.RPCResponse{
		Method: methodKiroOpenExternalURL,
		ID:     &id,
		Params: mustJSON(t, map[string]any{"url": url}),
	}
}

func TestHandleOpenExternalURL(t *testing.T) {
	t.Run("SafeURLBroadcasts", func(t *testing.T) {
		h, _, _ := newTestHub()
		_, before := h.sse.hub.Bounds()
		h.translateACPEvent("c1", openExternalURLMsg(t, 1, "https://auth.example.com/oauth"))
		types := extractTypes(t, bufferedSince(h, before))
		wantSubset(t, types, string(api.EventOpenExternalURL))
	})

	t.Run("UnsafeURLDoesNotBroadcast", func(t *testing.T) {
		h, _, _ := newTestHub()
		_, before := h.sse.hub.Bounds()
		h.translateACPEvent("c1", openExternalURLMsg(t, 2, "javascript:alert(1)"))
		types := extractTypes(t, bufferedSince(h, before))
		for _, ty := range types {
			if ty == string(api.EventOpenExternalURL) {
				t.Fatalf("unsafe URL must not broadcast open_external_url; got %v", types)
			}
		}
	})

	t.Run("HandledAsClientRequest", func(t *testing.T) {
		// handleKiroClientRequest must claim the method (return true) so it
		// never falls through to the unhandled-extension debug log.
		h, _, _ := newTestHub()
		if !h.handleKiroClientRequest(t.Context(), "c1", openExternalURLMsg(t, 3, "https://x.example")) {
			t.Fatal("handleKiroClientRequest should handle _kiro/openExternalUrl")
		}
	})
}
