package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/rpcerr"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestDispatcher_MethodNotAllowed(t *testing.T) {
	d := New()
	req := httptest.NewRequest(http.MethodGet, "/api/command", nil)
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", w.Code)
	}
}

// The drain refusal is NOT tested here any more, because it is not this
// package's behaviour any more: it moved to a route wrapper covering both gated
// routes (hub.refuseWhenDraining), and hub.TestRegisterRoutes_DrainingGate asserts
// it through the mux, which is stronger — a test calling this dispatcher directly
// would bypass the wrapper and pass whether or not it is wired.

func TestDispatcher_InvalidJSON(t *testing.T) {
	d := New()
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
	d := New()
	body := `{"type":"test","request_id":"../../etc/passwd"}`
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestDispatcher_InvalidChatID(t *testing.T) {
	d := New()
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
	d := New()
	body := `{"type":"nonexistent_command","chat_id":"abc123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestDispatcher_BodyTooLarge(t *testing.T) {
	d := New()
	bigBody := bytes.Repeat([]byte("x"), 2*1024*1024)
	req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(bigBody))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 413 or 400", w.Code)
	}
}

// TestStatusError_UnwrapsToTheCause pins the reason statusError has an Unwrap
// that no code names directly, which is why it needs a test rather than a
// comment: the reference is an errors.As inside rpcerr, and both punused and a
// reader lose it.
//
// Four handlers (compact, mode, rewind, steer) forward a bridge Call failure and
// the dispatcher renders it with rpcerr.Text. Text asks rpcerr.Details, which
// does errors.As for an error carrying KAS's `error.data`. If the status wrapper
// does not Unwrap, that As fails, Text falls back to err.Error(), and on a
// -32603 err.Error() is KAS's literal "Internal error" while the real cause sits
// unread in error.data.
//
// Red-check: delete (*statusError).Unwrap and the details assertion fails with
// "Internal error".
func TestStatusError_UnwrapsToTheCause(t *testing.T) {
	cause := &vibekit.RPCError{
		Code:    -32603,
		Message: "Internal error",
		Data:    json.RawMessage(`{"details":"the model refused the tool call"}`),
	}
	wrapped := StatusError(http.StatusBadGateway, cause)

	if got := statusOf(wrapped); got != http.StatusBadGateway {
		t.Errorf("statusOf = %d, want %d", got, http.StatusBadGateway)
	}
	// The property: the wrapper does not hide what the dispatcher renders.
	if got := rpcerr.Text(wrapped); got != "the model refused the tool call" {
		t.Errorf("rpcerr.Text(wrapped) = %q, want the error.data details — the status "+
			"wrapper is hiding the cause from errors.As", got)
	}
	// And the sentinel case, which is the other thing an Unwrap buys.
	if !errors.Is(StatusError(http.StatusNotFound, ErrChatNotFound), ErrChatNotFound) {
		t.Error("errors.Is cannot see ErrChatNotFound through the status wrapper")
	}
}
