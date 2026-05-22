package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRoutePrecedence verifies that the three overlapping route
// registrations (/api/mcp, /api/mcp/, /api/mcp/registry/search) don't
// accidentally let a user-created server named "registry" or "status"
// shadow the sibling handlers. Go's http.ServeMux uses longest-prefix
// match; this test is the guard against a future refactor that adds a
// new subtree and forgets to check precedence.
func TestRoutePrecedence(t *testing.T) {
	mux := http.NewServeMux()
	s := newTestStore(t)
	s.RegisterRoutes(mux)

	// Also register a stand-in for the registry-proxy path so we can
	// verify the overlap doesn't resolve to handleOne.
	var registryHit, statusHit bool
	mux.HandleFunc("/api/mcp/registry/search", func(w http.ResponseWriter, _ *http.Request) {
		registryHit = true
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/mcp/status", func(w http.ResponseWriter, _ *http.Request) {
		statusHit = true
		w.WriteHeader(http.StatusNoContent)
	})

	tests := []struct {
		wantHit    *bool
		path       string
		wantStatus int
	}{
		{path: "/api/mcp/registry/search?q=x", wantStatus: http.StatusNoContent, wantHit: &registryHit},
		{path: "/api/mcp/status", wantStatus: http.StatusNoContent, wantHit: &statusHit},
		// No server with this ID; handleOne → 404.
		{path: "/api/mcp/nonexistent-id", wantStatus: http.StatusNotFound, wantHit: nil},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tt.wantStatus {
			t.Errorf("%s: status = %d, want %d", tt.path, rec.Code, tt.wantStatus)
		}
		if tt.wantHit != nil && !*tt.wantHit {
			t.Errorf("%s: expected sibling handler to be hit", tt.path)
		}
	}
}
