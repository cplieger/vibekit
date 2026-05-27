package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"vibekit/internal/api"
	"vibekit/internal/command"
)

// slashMux creates a ServeMux with slash routes registered for testing.
func slashMux(h *Hub) *http.ServeMux {
	mux := http.NewServeMux()
	command.RegisterSlashRoutes(mux, h)
	return mux
}

func TestSlashExecute_RequiresPost(t *testing.T) {
	h, _, _ := newTestHub()
	mux := slashMux(h)
	req := httptest.NewRequest(http.MethodGet, "/api/slash/execute", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestSlashExecute_RequiresChatIDAndCommand(t *testing.T) {
	h, _, _ := newTestHub()
	mux := slashMux(h)

	tests := []struct {
		name string
		body string
	}{
		{"empty", `{}`},
		{"no command", `{"chat_id":"c1"}`},
		{"no chat_id", `{"command":"/tools"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/slash/execute",
				strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestSlashExecute_NoBridgeReturns409(t *testing.T) {
	h, _, _ := newTestHub()
	mux := slashMux(h)
	body := `{"chat_id":"c1","command":"/tools"}`
	req := httptest.NewRequest(http.MethodPost, "/api/slash/execute",
		strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}




// TestSlashExecute_RejectsInvalidChatID pins the validChatID guard parity.
func TestSlashExecute_RejectsInvalidChatID(t *testing.T) {
	h, _, _ := newTestHub()
	mux := slashMux(h)
	bad := []string{"../etc", "has space", "has\nnewline", "has/slash"}
	for _, id := range bad {
		body := `{"chat_id":"` + id + `","command":"/tools"}`
		req := httptest.NewRequest(http.MethodPost, "/api/slash/execute",
			strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("chat_id=%q status=%d, want 400", id, rec.Code)
		}
	}
}

// TestSlashOptions_RejectsInvalidChatID mirrors the execute guard.

// TestSlashExecute_HappyPathForwardsResult pins the bridge call +
// raw-JSON forwarding + nosniff header.
func TestSlashExecute_HappyPathForwardsResult(t *testing.T) {
	h, cs, _ := newTestHub()
	mux := slashMux(h)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	sb, err := h.getOrCreateBridge(context.Background(), "c1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	fb := sb.bridge.(*fakeBridge)
	fb.mu.Lock()
	fb.calls = nil
	fb.mu.Unlock()

	body := `{"chat_id":"c1","command":"/tools"}`
	req := httptest.NewRequest(http.MethodPost, "/api/slash/execute",
		strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	fb.mu.Lock()
	calls := append([]string(nil), fb.calls...)
	fb.mu.Unlock()
	if !slices.Contains(calls, "_kiro.dev/commands/execute") {
		t.Errorf("_kiro.dev/commands/execute not called; calls = %v", calls)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("nosniff header missing")
	}
}

// TestSlashRegisterRoutes pins that RegisterSlashRoutes wires both paths.
func TestSlashRegisterRoutes(t *testing.T) {
	h, _, _ := newTestHub()
	mux := http.NewServeMux()
	h.RegisterSlashRoutes(mux)
	for _, path := range []string{"/api/slash/execute"} {
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, path, nil))
		if pattern == "" {
			t.Errorf("route %q not registered", path)
		}
	}
}

// TestHandleSlashExecute_ValidationMatrix is a table-driven test covering
// all input validation paths for the slash-command execute endpoint.
func TestHandleSlashExecute_ValidationMatrix(t *testing.T) {
	h, _, _ := newTestHub()
	mux := slashMux(h)

	tests := []struct {
		name       string
		method     string
		body       string
		wantStatus int
	}{
		// Method validation
		{"GET rejected", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"PUT rejected", http.MethodPut, `{"chat_id":"c1","command":"/tools"}`, http.StatusMethodNotAllowed},
		{"DELETE rejected", http.MethodDelete, "", http.StatusMethodNotAllowed},

		// Body parsing
		{"empty body", http.MethodPost, "", http.StatusBadRequest},
		{"invalid JSON", http.MethodPost, `{invalid`, http.StatusBadRequest},
		{"null body", http.MethodPost, `null`, http.StatusBadRequest},

		// Required fields
		{"missing chat_id and command", http.MethodPost, `{}`, http.StatusBadRequest},
		{"missing command", http.MethodPost, `{"chat_id":"c1"}`, http.StatusBadRequest},
		{"missing chat_id", http.MethodPost, `{"command":"/tools"}`, http.StatusBadRequest},
		{"empty chat_id", http.MethodPost, `{"chat_id":"","command":"/tools"}`, http.StatusBadRequest},
		{"empty command", http.MethodPost, `{"chat_id":"c1","command":""}`, http.StatusBadRequest},

		// Invalid chat_id patterns
		{"chat_id with path traversal", http.MethodPost, `{"chat_id":"../etc","command":"/tools"}`, http.StatusBadRequest},
		{"chat_id with space", http.MethodPost, `{"chat_id":"has space","command":"/tools"}`, http.StatusBadRequest},
		{"chat_id with newline", http.MethodPost, `{"chat_id":"has\nnewline","command":"/tools"}`, http.StatusBadRequest},
		{"chat_id with slash", http.MethodPost, `{"chat_id":"has/slash","command":"/tools"}`, http.StatusBadRequest},

		// Valid request but no bridge → 409
		{"valid but no bridge", http.MethodPost, `{"chat_id":"c1","command":"/tools"}`, http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.body != "" {
				req = httptest.NewRequest(tt.method, "/api/slash/execute",
					strings.NewReader(tt.body))
			} else {
				req = httptest.NewRequest(tt.method, "/api/slash/execute", nil)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}
