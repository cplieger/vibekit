package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// FuzzDispatcherServeHTTP exercises the Dispatcher's top-level HTTP handler
// with arbitrary POST bodies to verify no panics and consistent error responses.
func FuzzDispatcherServeHTTP(f *testing.F) {
	f.Add([]byte(`{"command":"test_cmd","chatId":"c1","requestId":"r1"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"command":"unknown"}`))
	f.Add([]byte(`not json at all`))

	d := New()
	d.Register("test_cmd", func(_ context.Context, w http.ResponseWriter, _ *vibekit.ClientCommand) {
		w.WriteHeader(http.StatusOK)
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		d.ServeHTTP(w, req)

		code := w.Code
		validCodes := map[int]bool{200: true, 400: true, 404: true, 413: true, 503: true}
		if !validCodes[code] {
			t.Errorf("unexpected status code %d for input %q", code, data)
		}
		if code != 503 && w.Body.Len() > 0 {
			if !json.Valid(w.Body.Bytes()) {
				t.Errorf("non-JSON response body for status %d: %q", code, w.Body.String())
			}
		}
	})
}
