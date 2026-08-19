package command

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/ids"
)

func TestDispatcher_MethodNotAllowed(t *testing.T) {
	d := New(newBenchDeps())
	req := httptest.NewRequest(http.MethodGet, "/api/command", nil)
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", w.Code)
	}
}

func TestDispatcher_Draining(t *testing.T) {
	deps := newBenchDeps()
	d := New(deps)
	// Override Draining via a wrapper
	dd := &drainingDeps{benchDeps: deps}
	d2 := New(dd)
	body := `{"type":"test","chat_id":"c1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d2.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
	_ = d // suppress unused
}

// drainingDeps wraps benchDeps with Draining() = true.
type drainingDeps struct{ *benchDeps }

func (d *drainingDeps) Draining() bool { return true }

func TestDispatcher_InvalidJSON(t *testing.T) {
	d := New(newBenchDeps())
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader("{invalid"))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
	// A decode failure must surface as "invalid json", not fall through
	// to a zero-value command that dispatches to the unknown-command body.
	if got := w.Body.String(); !strings.Contains(got, "invalid json") {
		t.Errorf("body = %q, want it to contain %q", got, "invalid json")
	}
}

func TestDispatcher_InvalidRequestID(t *testing.T) {
	d := New(newBenchDeps())
	body := `{"type":"test","request_id":"../../etc/passwd"}`
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestDispatcher_InvalidChatID(t *testing.T) {
	d := New(newBenchDeps())
	body := `{"type":"test","chat_id":"has spaces"}`
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
	// A non-empty but invalid chat_id must be rejected with the
	// invalid-chat-id message, not allowed through to dispatch.
	if got := w.Body.String(); !strings.Contains(got, ids.ErrMsgInvalidChatID) {
		t.Errorf("body = %q, want it to contain %q", got, ids.ErrMsgInvalidChatID)
	}
}

func TestDispatcher_UnknownCommand(t *testing.T) {
	d := New(newBenchDeps())
	body := `{"type":"nonexistent_command","chat_id":"abc123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestDispatcher_BodyTooLarge(t *testing.T) {
	d := New(newBenchDeps())
	bigBody := bytes.Repeat([]byte("x"), 2*1024*1024)
	req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(bigBody))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 413 or 400", w.Code)
	}
}
