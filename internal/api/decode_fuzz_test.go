package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzDecodeJSONContentType targets the DecodeJSON Content-Type gate.
// Bug class: content-type bypass where a crafted Content-Type header
// passes the HasPrefix check but carries an incompatible charset or
// boundary that causes silent data corruption during JSON decode —
// or blocks legitimate requests with empty Content-Type (which SHOULD
// be allowed per the implementation).
func FuzzDecodeJSONContentType(f *testing.F) {
	f.Add("application/json", `{"key":"value"}`)
	f.Add("application/json; charset=utf-8", `{"key":"value"}`)
	f.Add("text/plain", `{"key":"value"}`)
	f.Add("", `{"key":"value"}`)
	f.Add("application/json\x00extra", `{"key":"value"}`)
	f.Add("APPLICATION/JSON", `{"key":"value"}`)
	f.Add("application/jsonl", `{"key":"value"}`)

	f.Fuzz(func(t *testing.T, contentType, body string) {
		req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()

		var dst map[string]any
		ok := DecodeJSON(rec, req, &dst)

		// Invariant 1: if Content-Type is non-empty and doesn't start with
		// "application/json", DecodeJSON must reject (return false).
		if contentType != "" && !strings.HasPrefix(contentType, MIMETypeJSON) {
			if ok {
				t.Fatalf("DecodeJSON accepted non-JSON content-type %q", contentType)
			}
			return
		}

		// Invariant 2: if DecodeJSON returns true, the destination must be populated
		// with the body's JSON content.
		if ok && dst == nil {
			t.Fatalf("DecodeJSON returned true but dst is nil for body %q", body)
		}

		// Invariant 3: the HTTP status must be 4xx on failure, not 5xx.
		if !ok && rec.Code >= 500 {
			t.Fatalf("DecodeJSON wrote 5xx status %d for input ct=%q body=%q",
				rec.Code, contentType, body)
		}
	})
}
