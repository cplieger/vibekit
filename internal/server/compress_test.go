package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// bigJSON is a JSON body comfortably over compressMinBytes that compresses
// well, which is what a chat list is.
func bigJSON() string {
	return `{"chats":[` + strings.Repeat(`{"id":"c1","name":"a chat"},`, 200) + `{"id":"z"}]}`
}

// jsonHandler answers every request with body under Content-Type ct.
func jsonHandler(ct, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ct)
		_, _ = io.WriteString(w, body)
	})
}

// serveCompressed runs one request through the middleware and returns the
// recorder.
func serveCompressed(t *testing.T, h http.Handler, path, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	compressJSON(h).ServeHTTP(rec, req)
	return rec
}

func TestCompressJSON_LargeJSONRoundTrips(t *testing.T) {
	want := bigJSON()
	rec := serveCompressed(t, jsonHandler("application/json", want), "/api/chats", "gzip, deflate, br")

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want %q", got, "gzip")
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want it to name Accept-Encoding", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader over the response body: %s", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read the decompressed body: %s", err)
	}
	if string(got) != want {
		t.Errorf("decompressed body = %d bytes, want the %d the handler wrote", len(got), len(want))
	}
	if rec.Body.Len() >= len(want) {
		t.Errorf("encoded body = %d bytes, want fewer than the %d identity bytes", rec.Body.Len(), len(want))
	}
}

func TestCompressJSON_SmallJSONStaysPlain(t *testing.T) {
	want := `{"ok":true}`
	rec := serveCompressed(t, jsonHandler("application/json", want), "/api/health", "gzip")

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want empty for a %d-byte body under the %d threshold",
			got, len(want), compressMinBytes)
	}
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestCompressJSON_NonJSONStaysPlain(t *testing.T) {
	// application/x-ndjson is the trap a substring match on "json" falls into:
	// the git-clone progress stream carries it.
	for _, ct := range []string{"text/html; charset=utf-8", "application/x-ndjson", "application/zip"} {
		want := bigJSON()
		rec := serveCompressed(t, jsonHandler(ct, want), "/api/anything", "gzip")
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("Content-Type %q: Content-Encoding = %q, want empty", ct, got)
		}
		if got := rec.Body.String(); got != want {
			t.Errorf("Content-Type %q: body = %d bytes, want the %d written", ct, len(got), len(want))
		}
	}
}

func TestCompressJSON_AlreadyEncodedIsNotDoubleCompressed(t *testing.T) {
	// webhttp.StaticHandler serves its own precompressed assets this way.
	want := bigJSON()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = io.WriteString(w, want)
	})
	rec := serveCompressed(t, h, "/api/anything", "gzip")

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want the handler's own %q untouched", got, "gzip")
	}
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %d bytes, want the %d the handler wrote (no second gzip layer)", len(got), len(want))
	}
}

func TestCompressJSON_EventsPathIsUntouched(t *testing.T) {
	// The SSE stream's whole contract is that a write reaches the client now.
	// The wrapper must not even be installed on it: proven by the ResponseWriter
	// the handler receives being the recorder itself.
	var seen http.ResponseWriter
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen = w
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: hello\n\n")
	})
	rec := serveCompressed(t, h, "/api/events", "gzip")

	if _, wrapped := seen.(*compressWriter); wrapped {
		t.Error("handler saw a *compressWriter on /api/events, want the raw ResponseWriter")
	}
	if got := rec.Body.String(); got != "data: hello\n\n" {
		t.Errorf("body = %q, want the event verbatim", got)
	}
}

func TestCompressJSON_FlushedJSONReachesTheClientUnbuffered(t *testing.T) {
	// A JSON-typed handler that flushes mid-body is streaming, and its earlier
	// writes must be on the wire before the later ones are produced. The path
	// skip cannot cover an endpoint nobody has added yet; this is the backstop.
	const first = `{"progress":"cloning"}`
	// What the recorder held the instant the handler flushed. Read inside the
	// handler because that is the only place the claim can be checked: after
	// ServeHTTP returns, a buffered response and a streamed one look identical.
	atFlush := "<never flushed>"
	rec := httptest.NewRecorder()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, first)
		// A direct http.Flusher assertion, the spelling download_zip.go uses:
		// the wrapper has to satisfy it, not only ResponseController.
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("the handler's ResponseWriter is not an http.Flusher")
			return
		}
		f.Flush()
		atFlush = rec.Body.String()
		_, _ = io.WriteString(w, `{"output":"done"}`)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/git/clone", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	compressJSON(h).ServeHTTP(rec, req)

	if atFlush != first {
		t.Errorf("body on the wire at Flush = %q, want the first write %q already there", atFlush, first)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want empty: a flushed response is not buffered", got)
	}
	if got := rec.Body.String(); got != first+`{"output":"done"}` {
		t.Errorf("body = %q, want both writes in order", got)
	}
	if !rec.Flushed {
		t.Error("recorder was never flushed, want the handler's Flush to reach the writer underneath")
	}
}

func TestCompressJSON_StatusIsPreserved(t *testing.T) {
	want := bigJSON()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, want)
	})
	rec := serveCompressed(t, h, "/api/health", "gzip")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want %q on a 503 with a large JSON body", got, "gzip")
	}
}

func TestCompressJSON_BodylessStatusCarriesNoEncoding(t *testing.T) {
	for _, code := range []int{http.StatusNoContent, http.StatusNotModified} {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
		})
		rec := serveCompressed(t, h, "/api/anything", "gzip")
		if rec.Code != code {
			t.Errorf("status = %d, want %d", rec.Code, code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("status %d: Content-Encoding = %q, want empty", code, got)
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"GZIP", true},
		{"gzip, deflate, br", true},
		{"br, gzip;q=0.5", true},
		{"deflate", false},
		{"*", true},
		{"*;q=0", false},
		// A q=0 is a refusal, and an explicit refusal of gzip beats a wildcard
		// offer — a client that says "anything but gzip" means it.
		{"gzip;q=0", false},
		{"gzip;q=0.0, *", false},
		{"*, gzip;q=0", false},
		{"identity", false},
	}
	for _, test := range tests {
		if got := acceptsGzip(test.header); got != test.want {
			t.Errorf("acceptsGzip(%q) = %t, want %t", test.header, got, test.want)
		}
	}
}

func TestIsJSONMediaType(t *testing.T) {
	tests := []struct {
		ct   string
		want bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"APPLICATION/JSON", true},
		{"application/problem+json", true},
		// The two near-misses that decide whether a stream gets buffered.
		{"application/x-ndjson", false},
		{"application/jsonl", false},
		{"text/json", false},
		{"text/html", false},
		{"", false},
		{"not a media type", false},
	}
	for _, test := range tests {
		if got := isJSONMediaType(test.ct); got != test.want {
			t.Errorf("isJSONMediaType(%q) = %t, want %t", test.ct, got, test.want)
		}
	}
}

func TestIsCompressSkipped(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/api/events", true},
		{"/api/shell/ws", true},
		{"/api/shell/ws/anything", true},
		{"/api/chats", false},
		{"/api/eventsource", false},
		{"/api/shell", false},
	}
	for _, test := range tests {
		if got := isCompressSkipped(test.path); got != test.want {
			t.Errorf("isCompressSkipped(%q) = %t, want %t", test.path, got, test.want)
		}
	}
}
