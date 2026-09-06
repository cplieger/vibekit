package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/cplieger/webhttp/v2"
)

// shellContentType is what the SPA shell is served as.
const shellContentType = "text/html; charset=utf-8"

// Each policy is a claim about the NAME, not the bytes: immutableAsset on a name
// whose content can change serves a stale asset for a year with no request that
// could discover it.
const (
	immutableAsset  = "public, max-age=31536000, immutable"
	revalidateAsset = "no-cache"
	noStoreHTML     = "no-store"
)

// contentHashedAsset matches the one naming shape whose bytes its name pins:
// cmd/bundle's `chunks/[name]-[hash]`, the hash 8 uppercase base32 characters.
// Anchored and pinned to the chunk directory so a hand-authored asset can never
// match and get cached for a year; it degrades to revalidating if ChunkNames moves.
var contentHashedAsset = regexp.MustCompile(`^chunks/[^/]+-[A-Z0-9]{8}\.js(\.map)?$`)

// assetCachePolicy is the per-asset Cache-Control policy webhttp.StaticHandler asks
// for. assetPath is normalized: no leading slash, "index.html" for a root request.
func assetCachePolicy(assetPath string) string {
	switch {
	case contentHashedAsset.MatchString(assetPath):
		return immutableAsset
	case strings.HasSuffix(assetPath, ".html"):
		return noStoreHTML
	default:
		return revalidateAsset
	}
}

// spaHandler serves the embedded FS, falling back to index.html for any path
// that is not a real file (History-API client routing). Assets get their ETag,
// gzip and cache policy from webhttp.StaticHandler; HTML is no-store so a
// release takes effect on the next load. The fallback WRITES the shell rather
// than calling http.ServeFileFS: net/http's serveFile applies index-page
// canonicalization to r.URL.Path unconditionally (fs.go:686-689, go1.27.0), so
// every path ending in "/index.html" answered a bodyless 301 to "./".
func spaHandler(staticFS fs.FS) http.Handler {
	static, err := webhttp.StaticHandler(staticFS, webhttp.WithStaticCacheControl(assetCachePolicy))
	if err != nil {
		// An unreadable embedded FS is a build defect, not a runtime condition.
		panic("server: static handler: " + err.Error())
	}
	shell, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		panic("server: static handler: read index.html: " + err.Error())
	}
	shellLen := strconv.Itoa(len(shell))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && p != "index.html" {
			if info, statErr := fs.Stat(staticFS, p); statErr == nil && !info.IsDir() {
				static.ServeHTTP(w, r)
				return
			}
		}
		h := w.Header()
		h.Set("Content-Type", shellContentType)
		h.Set("Content-Length", shellLen)
		h.Set("Cache-Control", "no-store")
		// net/http suppresses the body for HEAD, so no method branch is needed.
		if _, wErr := w.Write(shell); wErr != nil {
			slog.Debug("server: spa shell write failed", "error", wErr)
		}
	})
}
