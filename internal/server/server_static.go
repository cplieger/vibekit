package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/cplieger/webhttp"
)

// Static-asset serving. The revalidation and compression mechanics are
// webhttp.StaticHandler's: startup content-hash strong ETags (embed.FS has a
// zero ModTime, so a bare FileServer never revalidates), construction-time
// gzip at BestCompression kept only when smaller, Vary: Accept-Encoding, and
// distinct per-representation ETags with their own 304 handling. vibekit
// keeps only its policy and its SPA shape: assets revalidate (the handler's
// default `no-cache`), HTML is always fresh (`no-store`), and any path that
// is not a real embedded file falls back to index.html for History-API
// client routing. cmd/bundle no longer emits precompressed .gz siblings —
// the handler compresses the original bytes at the same level, so the
// embedded tree carries each asset exactly once.

// spaHandler serves static files from the embedded FS, falling back to
// index.html for any path that doesn't match a real file (client-side
// routing via the History API). Assets get ETag + no-cache + gzip variants
// from webhttp.StaticHandler; HTML (direct or fallback) is no-store and
// deliberately un-ETagged, so a new release's script graph takes effect on
// the next load.
func spaHandler(staticFS fs.FS) http.Handler {
	static, err := webhttp.StaticHandler(staticFS)
	if err != nil {
		// An unreadable embedded FS is a build defect, not a runtime
		// condition — fail construction loudly.
		panic("server: static handler: " + err.Error())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && p != "index.html" {
			if info, statErr := fs.Stat(staticFS, p); statErr == nil && !info.IsDir() {
				static.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback (and index.html itself): always-fresh HTML.
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFileFS(w, r, staticFS, "index.html")
	})
}
