package command

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlashExecute_MethodNotAllowed(t *testing.T) {
	sh := &slashHandler{deps: newBenchDeps()}
	req := httptest.NewRequest(http.MethodGet, "/api/slash/execute", nil)
	w := httptest.NewRecorder()
	sh.handleExecute(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", w.Code)
	}
}

func TestSlashExecute_MissingFields(t *testing.T) {
	sh := &slashHandler{deps: newBenchDeps()}
	body := `{"chat_id":"","command":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/slash/execute", strings.NewReader(body))
	w := httptest.NewRecorder()
	sh.handleExecute(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestSlashExecute_InvalidChatID(t *testing.T) {
	sh := &slashHandler{deps: newBenchDeps()}
	body := `{"chat_id":"../etc","command":"help"}`
	req := httptest.NewRequest(http.MethodPost, "/api/slash/execute", strings.NewReader(body))
	w := httptest.NewRecorder()
	sh.handleExecute(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

func TestSlashExecute_NoBridge(t *testing.T) {
	sh := &slashHandler{deps: newBenchDeps()}
	body := `{"chat_id":"abc123","command":"help"}`
	req := httptest.NewRequest(http.MethodPost, "/api/slash/execute", strings.NewReader(body))
	w := httptest.NewRecorder()
	sh.handleExecute(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", w.Code)
	}
}
