package hub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"vibekit/internal/checkpoint"
)

// FuzzCheckpointHTTPRouting exercises the checkpoint HTTP handler with
// arbitrary URL paths to verify no panics and consistent status codes.
func FuzzCheckpointHTTPRouting(f *testing.F) {
	f.Add("/api/checkpoints/chat1/list")
	f.Add("/api/checkpoints//list")
	f.Add("/api/checkpoints/")
	f.Add("/api/checkpoints/chat1/blob/abc123")
	f.Add("/api/checkpoints/chat1/unknown-resource")
	f.Add("/api/checkpoints/a/b/c/d/e")

	h, _, _ := newTestHub()
	cfgDir := f.TempDir()
	workDir := f.TempDir()
	s := checkpoint.NewStore(cfgDir, workDir, nil)
	h.checkpoints = s
	defer s.Stop()

	f.Fuzz(func(t *testing.T, path string) {
		// Skip paths that would cause httptest.NewRequest to panic
		// (control characters, invalid URL encoding).
		if _, err := url.Parse(path); err != nil {
			t.Skip("invalid URL")
		}
		for _, b := range []byte(path) {
			if b < 0x20 {
				t.Skip("control character in path")
			}
		}

		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()

		h.handleCheckpoint(w, req)

		code := w.Code
		validCodes := map[int]bool{200: true, 400: true, 404: true, 405: true}
		if !validCodes[code] {
			t.Errorf("unexpected status %d for path %q", code, path)
		}
	})
}
