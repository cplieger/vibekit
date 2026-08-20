package server

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// Assets carry a startup-computed strong ETag, and a matching
// If-None-Match revalidation gets its 304 from net/http.
func TestSpaHandler_assetETagRevalidation(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html></html>")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
	h := spaHandler(fsys)

	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("asset response missing ETag")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("revalidation status = %d, want 304", rec2.Code)
	}
}

// A gzip-accepting client gets the construction-time gzip representation
// with Content-Encoding: gzip and the ORIGINAL extension's Content-Type; a
// client without gzip support gets the identity bytes. The two
// representations carry distinct ETags. (There are no precompressed .gz
// siblings anymore — webhttp.StaticHandler compresses the original bytes at
// construction.)
func TestSpaHandler_gzipVariant(t *testing.T) {
	plain := []byte(strings.Repeat("console.log('gzip variant fixture');\n", 40))
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html></html>")},
		"app.js":     {Data: plain},
	}
	h := spaHandler(fsys)

	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Content-Type"); got == "" || got == "application/gzip" {
		t.Errorf("Content-Type = %q, want the original text/javascript type", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("response not gzip: %v", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(zr); err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !bytes.Equal(out.Bytes(), plain) {
		t.Errorf("decompressed body differs from the identity asset")
	}
	gzETag := rec.Header().Get("ETag")

	reqID := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	recID := httptest.NewRecorder()
	h.ServeHTTP(recID, reqID)
	if got := recID.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("identity response Content-Encoding = %q, want empty", got)
	}
	if !bytes.Equal(recID.Body.Bytes(), plain) {
		t.Errorf("identity body differs from the asset")
	}
	if idETag := recID.Header().Get("ETag"); idETag == "" || idETag == gzETag {
		t.Errorf("representation ETags must be distinct: identity %q vs gzip %q", idETag, gzETag)
	}
}

// Any path that is not a real embedded file — client routes, deleted
// assets — falls back to the SPA entrypoint with always-fresh HTML.
func TestSpaHandler_unknownPathFallsBackToIndex(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html>fallback</html>")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
	h := spaHandler(fsys)

	for _, path := range []string{"/chat/abc123", "/missing.js", "/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !bytes.Contains(rec.Body.Bytes(), []byte("fallback")) {
			t.Errorf("%s: want the SPA fallback body, got %q", path, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", path, got)
		}
	}
}

// index.html requested directly is HTML, so it takes the no-store branch
// (never the asset ETag policy), keeping releases immediate — and it answers
// with the shell, not a redirect.
//
// The status assertion is the half that was missing. This test used to check
// only Cache-Control and ETag, and both of those are set before the fallback
// runs, so it passed while the handler answered 301 "./" with a zero-length
// body (measured on go1.27.0 before the fix). See spaHandler's comment for the
// stdlib branch that caused it.
func TestSpaHandler_indexHTMLIsNoStore(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html>fresh</html>")},
	}
	h := spaHandler(fsys)

	req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (Location %q), want 200 with the shell body",
			rec.Code, rec.Header().Get("Location"))
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("fresh")) {
		t.Errorf("body = %q, want the shell", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("ETag"); got != "" {
		t.Errorf("index.html carries ETag %q, want none (no-store HTML)", got)
	}
}

// Every client route whose path ENDS in /index.html gets the shell.
//
// This is the class net/http.serveFile answered with a bare 301 to "./"
// (fs.go:686-689: the index canonicalization reads r.URL.Path and runs before
// the name it was handed is opened). `/file/{path}` is the file editor's deep
// link, so a file genuinely named index.html — this repo has one under
// static-src/ — could not be opened by URL: the browser followed the redirect
// to the parent directory and the router rendered the wrong view.
//
// The %2F row is the Go 1.27 half: localRedirect now answers 404 rather than
// 301 once the escaped path carries an escaped slash (fs.go:786-792), so that
// spelling changed status with the toolchain while staying just as wrong. All
// four must be the shell.
func TestSpaHandler_indexHTMLSuffixedRoutesGetTheShell(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html>shell</html>")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
	h := spaHandler(fsys)

	for _, path := range []string{
		"/file/static-src/index.html", // the editor deep link for a file named index.html
		"/files/some/dir/index.html",  // the browser route for a directory holding one
		"/docs/index.html",            // any other client route ending the same way
		"/file/a%2Fb/index.html",      // 301 on go1.26, 404 on go1.27 — wrong either way
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (Location %q), want 200: net/http's index-page "+
					"canonicalization must not reach a constant shell", rec.Code,
					rec.Header().Get("Location"))
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte("shell")) {
				t.Errorf("body = %q, want the shell", rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if got := rec.Header().Get("Content-Type"); got != shellContentType {
				t.Errorf("Content-Type = %q, want %q", got, shellContentType)
			}
		})
	}
}

// A HEAD on the shell carries the length and no body. net/http suppresses the
// body itself; the explicit Content-Length is what keeps the answer complete
// rather than chunked, which is what ServeFileFS used to provide.
func TestSpaHandler_headOnTheShellIsLengthOnly(t *testing.T) {
	body := []byte("<html>shell</html>")
	h := spaHandler(fstest.MapFS{"index.html": {Data: body}})

	req := httptest.NewRequest(http.MethodHead, "/chat/abc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want %d", got, len(body))
	}
}
