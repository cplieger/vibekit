package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/uistate"
)

func newUIStateServer(t *testing.T) *Server {
	t.Helper()
	st, err := uistate.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{uiState: st}
}

func TestUIState_GetThenPutRoundTrips(t *testing.T) {
	s := newUIStateServer(t)

	rec := httptest.NewRecorder()
	s.handleUIState(rec, httptest.NewRequest(http.MethodGet, "/api/ui-state", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", rec.Code)
	}
	var doc uistate.Document
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Revision != 0 {
		t.Errorf("fresh Revision = %d, want 0", doc.Revision)
	}

	body := `{"revision":0,"tab_order":["__git__","chat-a"],"theme":"dark"}`
	rec = httptest.NewRecorder()
	s.handleUIState(rec, httptest.NewRequest(http.MethodPut, "/api/ui-state", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Revision != 1 || strings.Join(doc.TabOrder, ",") != "__git__,chat-a" || doc.Theme != "dark" {
		t.Errorf("PUT returned %+v", doc)
	}
}

// A stale writer gets 409 AND the current document, so it can re-apply without a
// second round trip. Without the body it would have to GET before every retry.
func TestUIState_StaleWriteIs409WithCurrentDocument(t *testing.T) {
	s := newUIStateServer(t)
	rec := httptest.NewRecorder()
	s.handleUIState(rec, httptest.NewRequest(http.MethodPut, "/api/ui-state",
		strings.NewReader(`{"revision":0,"tab_order":["a"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("first PUT = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.handleUIState(rec, httptest.NewRequest(http.MethodPut, "/api/ui-state",
		strings.NewReader(`{"revision":0,"tab_order":["stale"]}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale PUT = %d, want 409", rec.Code)
	}
	var doc uistate.Document
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Revision != 1 || strings.Join(doc.TabOrder, ",") != "a" {
		t.Errorf("409 body = %+v, want the current document", doc)
	}
}

func TestUIState_RejectsTrailingDataAndBadMethod(t *testing.T) {
	s := newUIStateServer(t)
	rec := httptest.NewRecorder()
	s.handleUIState(rec, httptest.NewRequest(http.MethodPut, "/api/ui-state",
		strings.NewReader(`{"revision":0}{"revision":0}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("trailing data = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.handleUIState(rec, httptest.NewRequest(http.MethodDelete, "/api/ui-state", http.NoBody))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d, want 405", rec.Code)
	}
}

// An unwired store must still let a client boot: GET answers the empty document
// rather than 404, because a client that cannot read the arrangement has to open
// SOMETHING.
func TestUIState_UnwiredStoreStillAnswersGet(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleUIState(rec, httptest.NewRequest(http.MethodGet, "/api/ui-state", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Errorf("GET with no store = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.handleUIState(rec, httptest.NewRequest(http.MethodPut, "/api/ui-state",
		strings.NewReader(`{"revision":0}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("PUT with no store = %d, want 503", rec.Code)
	}
}
