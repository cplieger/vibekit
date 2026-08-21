package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/cplieger/webhttp/v2"
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

// shellContentType is what the SPA shell is served as. Fixed rather than
// sniffed: the bytes are always the embedded index.html, so there is nothing
// to detect, and this is byte-identical to what net/http derived from the
// ".html" extension on the path this replaced.
const shellContentType = "text/html; charset=utf-8"

// spaHandler serves static files from the embedded FS, falling back to
// index.html for any path that doesn't match a real file (client-side
// routing via the History API). Assets get ETag + no-cache + gzip variants
// from webhttp.StaticHandler; HTML (direct or fallback) is no-store and
// deliberately un-ETagged, so a new release's script graph takes effect on
// the next load.
//
// # The fallback WRITES the shell rather than serving it as a file
//
// It used to be http.ServeFileFS(w, r, staticFS, "index.html"), and that is a
// defect however constant the name looks. net/http.serveFile applies the
// index-page canonicalization to r.URL.Path BEFORE it opens the name it was
// given, and unconditionally — the redirect parameter does not gate it
// (fs.go:686-689 on go1.27.0). So EVERY request whose decoded path ends in
// "/index.html" was answered with a bare 301 to "./" carrying no body,
// whatever bytes the caller was actually asking for.
//
// That is reachable from vibekit's own routes, not a curiosity. _Measured on
// go1.27.0_ against the pre-fix handler: `/index.html` -> 301 "./" (a
// zero-length body, while TestSpaHandler_indexHTMLIsNoStore asserted only
// headers and so passed on it), and so did `/file/static-src/index.html`,
// `/files/some/dir/index.html`, `/docs/index.html` and the percent-encoded
// `/file/static-src/%69ndex.html`. The first of those is the file editor's
// deep link for a file NAMED index.html — this repo's own
// static-src/index.html among them — so opening it by URL landed the router on
// the parent directory instead of the editor.
//
// Go 1.27 made one spelling of it louder rather than causing it: localRedirect
// now answers 404 instead of 301 when the escaped path carries %2F
// (fs.go:786-792), so `/file/a%2Fb/index.html` went 301 -> 404 with the bump.
// _Measured_ over ten escaped-slash shapes, that is the whole of the 1.27
// delta on this mount: `/sub%2Findex.html` and `/%2Findex.html` moved 301 ->
// 404, both index redirects; `/sub%2Fleaf.txt` still serves 200 (the path is
// decoded before lookup), and `/sub%2Fdir` never reached the change because
// the fs.Stat gate above catches the directory and falls through here.
//
// Writing the bytes removes the whole class: the shell is a constant, so no
// property of the request may select or redirect it. Two things go with
// ServeFileFS and both are stated rather than discovered later — Range
// requests on the shell (nothing ranges an HTML entrypoint), and serveFile's
// 400 for a ".." element in r.URL.Path. The second is not a containment loss:
// the name here is the literal "index.html", never joined with anything from
// the request, and ServeMux already 307s the unencoded spelling before this
// handler runs. The encoded spelling now gets the shell instead of a 400,
// which is what every other unrouted path already got.
func spaHandler(staticFS fs.FS) http.Handler {
	static, err := webhttp.StaticHandler(staticFS)
	if err != nil {
		// An unreadable embedded FS is a build defect, not a runtime
		// condition — fail construction loudly.
		panic("server: static handler: " + err.Error())
	}
	// Read once: the tree is embedded, so this is a build-time constant, and
	// failing here is the same class as the StaticHandler error above. It also
	// takes one fs.Open off every client-side navigation.
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
		// SPA fallback (and index.html itself): always-fresh HTML.
		h := w.Header()
		h.Set("Content-Type", shellContentType)
		h.Set("Content-Length", shellLen)
		h.Set("Cache-Control", "no-store")
		// net/http suppresses the body for HEAD itself, so there is no method
		// branch here; the Content-Length above is what makes that answer
		// complete rather than chunked.
		if _, wErr := w.Write(shell); wErr != nil {
			slog.Debug("server: spa shell write failed", "error", wErr)
		}
	})
}
