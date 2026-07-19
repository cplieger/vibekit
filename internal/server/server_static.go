package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// Static-asset cache policy. The embedded assets (the bundled app.js, its
// lazy chunks, style.css, sw.js, icons) are identical for every user and
// change only with the binary. Each asset carries a startup-computed strong
// ETag plus `Cache-Control: no-cache` (revalidate-always): browsers keep a
// copy and spend one conditional GET per asset, answered 304 until the
// binary changes. The HTML entrypoint (and the SPA fallback serving it)
// stays `no-store` — fresh HTML is what makes a new release's script graph
// take effect immediately. Same pattern as subflux's staticcache.
//
// Compression: cmd/bundle writes precompressed .gz siblings for every
// .js/.css/.map it emits. When the client accepts gzip and a sibling
// exists, the sibling's bytes are served with Content-Encoding: gzip and
// the ORIGINAL file's Content-Type; the sibling has its own ETag (a
// different representation per RFC 9110), and net/http still handles
// If-None-Match/ranges against whichever representation is served.

// computeStaticETags walks the embedded static tree once and hashes every
// file into a strong ETag, keyed by URL-relative path ("app.js",
// "chunks/chat-XXXX.js"). The embedded FS is immutable for the process
// lifetime, so the map never changes after boot.
func computeStaticETags(fsys fs.FS) (map[string]string, error) {
	etags := make(map[string]string)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, rerr := fs.ReadFile(fsys, p)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", p, rerr)
		}
		sum := sha256.Sum256(data)
		etags[p] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	if err != nil {
		return nil, err
	}
	return etags, nil
}

// spaHandler serves static files from the embedded FS, falling back to
// index.html for any path that doesn't match a real file (client-side
// routing via the History API). Assets get the ETag+no-cache policy and
// the precompressed-sibling upgrade; HTML (direct or fallback) is no-store.
func spaHandler(staticFS fs.FS) http.Handler {
	etags, err := computeStaticETags(staticFS)
	if err != nil {
		// An unreadable embedded FS is a build defect, not a runtime
		// condition — fail construction loudly.
		panic("server: static etags: " + err.Error())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && p != "index.html" && !strings.HasSuffix(p, ".gz") {
			if info, statErr := fs.Stat(staticFS, p); statErr == nil && !info.IsDir() {
				serveAsset(w, r, staticFS, etags, p)
				return
			}
		}
		// SPA fallback (and index.html itself): always-fresh HTML.
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFileFS(w, r, staticFS, "index.html")
	})
}

// serveAsset serves one embedded asset with the revalidation policy,
// preferring the precompressed .gz sibling for gzip-accepting clients.
// net/http's ServeFileFS evaluates If-None-Match against the pre-set ETag,
// so conditional requests get their 304 (and correct range handling) from
// the stdlib.
func serveAsset(w http.ResponseWriter, r *http.Request, staticFS fs.FS, etags map[string]string, p string) {
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Add("Vary", "Accept-Encoding")

	serve := p
	if acceptsGzip(r) {
		gz := p + ".gz"
		if _, err := fs.Stat(staticFS, gz); err == nil {
			// Content-Type from the ORIGINAL extension: ServeFileFS would
			// otherwise sniff the gzip bytes. The sibling's own ETag keeps
			// the two representations distinct for caches.
			if ct := mime.TypeByExtension(filepath.Ext(p)); ct != "" {
				h.Set("Content-Type", ct)
			}
			h.Set("Content-Encoding", "gzip")
			serve = gz
		}
	}
	if etag, ok := etags[serve]; ok {
		h.Set("ETag", etag)
	}
	http.ServeFileFS(w, r, staticFS, serve) //nolint:gosec // G703: embedded static assets
}

// acceptsGzip reports whether the request advertises gzip support. A
// simple substring match over Accept-Encoding is sufficient here: every
// real browser sends a plain token list, and a q=0 opt-out of gzip is not
// a thing browsers do for static assets.
func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}
