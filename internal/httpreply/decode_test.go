package httpreply

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDecodeJSON_ReportsWhetherTheHandlerMayProceed pins the return value that
// every handler branches on, in both directions.
//
// DecodeJSON's contract is that true means "v is populated and nothing has been
// written to w", so the handler may go on to do its work, and false means "an
// error response is already on the wire", so the handler must return without
// touching w. Getting that backwards is invisible to the caller: a handler told
// false for a body that decoded fine returns a 400 for a valid request, and one
// told true for a body that did not decode reads a zero-valued struct and acts on
// a request the client never sent — while a second write onto an already-written
// response would be the only trace either way.
func TestDecodeJSON_ReportsWhetherTheHandlerMayProceed(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	t.Run("a well-formed body decodes and leaves the response untouched", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{"name":"vibekit"}`))
		req.Header.Set("Content-Type", MIMETypeJSON)
		rec := httptest.NewRecorder()

		var got payload
		if !DecodeJSON(rec, req, &got) {
			t.Fatalf("DecodeJSON(valid body) = false, want true (body %q)", `{"name":"vibekit"}`)
		}
		if got.Name != "vibekit" {
			t.Errorf("decoded name = %q, want %q", got.Name, "vibekit")
		}
		if rec.Body.Len() != 0 {
			t.Errorf("DecodeJSON wrote %q on the success path, want nothing", rec.Body.String())
		}
	})

	t.Run("a malformed body is refused with 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{not json`))
		req.Header.Set("Content-Type", MIMETypeJSON)
		rec := httptest.NewRecorder()

		var got payload
		if DecodeJSON(rec, req, &got) {
			t.Fatalf("DecodeJSON(%q) = true, want false", `{not json`)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if !strings.Contains(rec.Body.String(), "invalid json") {
			t.Errorf("body = %q, want it to name the invalid json", rec.Body.String())
		}
	})

	t.Run("a non-JSON content type is refused before the body is read", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{"name":"vibekit"}`))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()

		var got payload
		if DecodeJSON(rec, req, &got) {
			t.Fatalf("DecodeJSON(text/plain) = true, want false")
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if got.Name != "" {
			t.Errorf("decoded name = %q, want the destination untouched", got.Name)
		}
	})
}
