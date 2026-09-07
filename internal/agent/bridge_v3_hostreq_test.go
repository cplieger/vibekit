package agent

import (
	"errors"
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
		{"HTTPS://Example.com", true},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"data:text/html,<script>", false},
		{"ftp://example.com", false},
		{"", false},
		{"not a url", false},
		{"//example.com", false},
	}
	for _, tt := range tests {
		if got := isSafeExternalURL(tt.url); got != tt.want {
			t.Errorf("isSafeExternalURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

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
}

func TestHandleKiroClientRequest_DoesNotClaimGetAccessToken(t *testing.T) {
	h, _, _ := newTestHub()
	id := int64(3)
	tests := map[string]struct {
		msg  *vibekit.RPCResponse
		want bool
	}{
		"relay_owned_auth": {
			msg:  &vibekit.RPCResponse{ID: &id, Method: "_kiro/auth/get" + "AccessToken"},
			want: false,
		},
		"shell_type": {
			msg:  &vibekit.RPCResponse{ID: &id, Method: methodKiroShellType},
			want: true,
		},
		"open_external_url": {
			msg:  openExternalURLMsg(t, id, "https://x.example"),
			want: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := h.inbound.handleKiroClientRequest(t.Context(), "c1", tc.msg); got != tc.want {
				t.Errorf("handleKiroClientRequest(%q) = %v, want %v", tc.msg.Method, got, tc.want)
			}
		})
	}
}

func TestHandleOpenExternalURL_UnsafeSchemeIsRefusedWithInvalidParams(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)

	h.translateACPEvent("c1", openExternalURLMsg(t, 9, "javascript:alert(1)"))

	resp, ok := br.lastResponse()
	if !ok {
		t.Fatal("the unsafe URL went unanswered; the agent would block on a request nothing will answer")
	}
	if resp.err == nil {
		t.Fatalf("openExternalUrl(javascript:) was answered with a success (%v), want an error", resp.result)
	}
	rpcErr, isRPC := errors.AsType[*vibekit.RPCError](resp.err)
	if !isRPC {
		t.Errorf("response error = %T, want *vibekit.RPCError", resp.err)
	} else if rpcErr.Code != -32602 {
		t.Errorf("response error code = %d, want -32602", rpcErr.Code)
	}
}
