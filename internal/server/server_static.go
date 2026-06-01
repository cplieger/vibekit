package server

import (
	"io/fs"
	"net/http"
	"strings"
)

// spaHandler serves static files from the embedded FS, falling back to
// index.html for any path that doesn't match a real file. This enables
// client-side routing via the History API.
func spaHandler(staticFS fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		// Serve the file if it exists and is not a directory.
		if info, err := fs.Stat(staticFS, p); err == nil && !info.IsDir() {
			http.ServeFileFS(w, r, staticFS, p) //nolint:gosec // G703: embedded static assets
			return
		}
		// SPA fallback: serve index.html for client-side routes.
		http.ServeFileFS(w, r, staticFS, "index.html")
	})
}
