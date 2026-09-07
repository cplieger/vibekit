package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"mime"
	"net"
	"net/http"
	"strings"
)

// compressMinBytes is the body size at which gzip starts paying for itself: below it
// the framing and CPU cost outweigh the saving.
const compressMinBytes = 1024

// compressSkipPaths are answered without wrapping at all, decided BEFORE the handler
// runs: a response whose contract is that each write reaches the client immediately
// cannot be buffered to measure it. compressWriter degrades on Flush and Hijack as a
// backstop, but a stream that never reaches the wrapper cannot be broken by a later
// change to that backstop.
var compressSkipPaths = []string{"/api/events", "/api/shell/ws"}

// compressJSON negotiates Content-Encoding: gzip for JSON response bodies over
// compressMinBytes. Three conditions, all required: the request offers gzip, the
// Content-Type is JSON, and the body reaches the threshold. The Content-Type gate is
// also what keeps precompressed static assets out — they carry their own
// Content-Encoding, which gzipCandidate refuses.
func compressJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCompressSkipped(r.URL.Path) {
			// Not negotiated at all, so its representation does not vary and it
			// gets no Vary either.
			next.ServeHTTP(w, r)
			return
		}
		// Announced before the outcome is known: a below-threshold body and a
		// `gzip;q=0` refusal were selected on Accept-Encoding just as much as a
		// compressed one, and a shared cache cannot see that from the body.
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

// isCompressSkipped reports whether path is a streaming surface the wrapper must not
// see.
func isCompressSkipped(path string) bool {
	for _, p := range compressSkipPaths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip. A `gzip;q=0` is a
// REFUSAL rather than an offer (RFC 9110 section 12.5.3), and a bare `*` with no gzip
// entry is an offer.
func acceptsGzip(header string) bool {
	wildcard := false
	for part := range strings.SplitSeq(header, ",") {
		token, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name := strings.ToLower(strings.TrimSpace(token))
		if name != encodingGzip && name != "*" {
			continue
		}
		if qZero(params) {
			if name == encodingGzip {
				return false
			}
			continue
		}
		if name == encodingGzip {
			return true
		}
		wildcard = true
	}
	return wildcard
}

// qZero reports whether an Accept-Encoding parameter list carries q=0, the
// spelling that turns an entry from an offer into a refusal.
func qZero(params string) bool {
	for part := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		switch strings.TrimSpace(v) {
		case "0", "0.", "0.0", "0.00", "0.000":
			return true
		}
	}
	return false
}

// isJSONMediaType reports whether a Content-Type names JSON: exactly
// application/json, or a structured `+json` suffix (RFC 6839). Deliberately NOT a
// substring test — application/x-ndjson contains "json" and is a STREAM whose flush
// is its liveness signal, so a substring match would buffer the one JSON-shaped
// response that must not be buffered.
func isJSONMediaType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	if mt == "application/json" {
		return true
	}
	_, suffix, ok := strings.Cut(mt, "+")
	return ok && suffix == "json"
}

// compressMode is what compressWriter has decided about the body so far.
type compressMode int

const (
	// modeBuffering holds the body, and the status line with it, until the size is
	// worth compressing.
	modeBuffering compressMode = iota
	// modePlain passes every write through untouched.
	modePlain
	// modeGzip writes through a gzip.Writer.
	modeGzip
)

// compressWriter decides per response whether to gzip it. The decision needs the
// Content-Type, known at WriteHeader, AND the body size, known only as the body
// arrives, so the status line is held back until the threshold is crossed or the
// handler returns. Everything the handler can do in between resolves to a plain
// pass-through.
type compressWriter struct {
	http.ResponseWriter
	gz     *gzip.Writer
	buf    bytes.Buffer
	status int
	mode   compressMode
	// headerWritten records that the HANDLER committed a status, making a second
	// WriteHeader the no-op net/http's own writer makes it.
	headerWritten bool
	// committed records that the status line reached the underlying writer, which
	// while buffering is not the same question.
	committed bool
}

// Unwrap exposes the wrapped writer to http.ResponseController, so a handler reaching
// Flush or SetWriteDeadline through it finds the real implementation.
func (cw *compressWriter) Unwrap() http.ResponseWriter { return cw.ResponseWriter }

// WriteHeader records the status and decides whether this response is a gzip candidate
// at all. A candidate's status line is held back until Write or finish settles size.
func (cw *compressWriter) WriteHeader(code int) {
	if cw.headerWritten {
		return
	}
	cw.headerWritten = true
	cw.status = code
	if !cw.gzipCandidate(code) {
		cw.passThrough()
	}
}

// encodingGzip is the content-coding token, on the wire and in Accept-Encoding.
const encodingGzip = "gzip"

// gzipCandidate reports whether a response with this status and these headers
// could carry a gzip body.
func (cw *compressWriter) gzipCandidate(code int) bool {
	if code < http.StatusOK || code == http.StatusNoContent || code == http.StatusNotModified {
		return false
	}
	h := cw.Header()
	if h.Get("Content-Encoding") != "" {
		return false
	}
	return isJSONMediaType(h.Get("Content-Type"))
}

// passThrough commits the held status line and every buffered byte as-is.
func (cw *compressWriter) passThrough() {
	cw.mode = modePlain
	cw.commit()
	if cw.buf.Len() > 0 {
		_, _ = cw.ResponseWriter.Write(cw.buf.Bytes())
		cw.buf.Reset()
	}
}

// commit writes the status line to the underlying writer, once.
func (cw *compressWriter) commit() {
	if cw.committed {
		return
	}
	cw.committed = true
	if cw.status == 0 {
		cw.status = http.StatusOK
	}
	cw.ResponseWriter.WriteHeader(cw.status)
}

func (cw *compressWriter) Write(p []byte) (int, error) {
	if !cw.headerWritten {
		cw.WriteHeader(http.StatusOK)
	}
	if cw.mode == modePlain {
		return cw.ResponseWriter.Write(p)
	}
	if cw.mode == modeGzip {
		return cw.gz.Write(p)
	}
	cw.buf.Write(p)
	if cw.buf.Len() >= compressMinBytes {
		cw.startGzip()
	}
	return len(p), nil
}

// startGzip switches a buffering response over to gzip, replaying what is buffered.
// Content-Length is dropped rather than recomputed: the handler's value describes the
// identity representation, and the encoded length is unknown until the body ends.
func (cw *compressWriter) startGzip() {
	h := cw.Header()
	gz, err := gzip.NewWriterLevel(cw.ResponseWriter, gzip.DefaultCompression)
	if err != nil {
		// Only an invalid level reaches this and the level is a constant, so it
		// is unreachable — answer plainly rather than losing the body.
		cw.passThrough()
		return
	}
	h.Set("Content-Encoding", encodingGzip)
	h.Del("Content-Length")
	cw.mode = modeGzip
	cw.gz = gz
	cw.commit()
	if cw.buf.Len() > 0 {
		_, _ = gz.Write(cw.buf.Bytes())
		cw.buf.Reset()
	}
}

// Flush resolves a still-undecided response as plain first: a handler that flushes is
// telling the client to expect these bytes now, which is the one claim buffering
// cannot honour. compressSkipPaths covers the known streams; this catches a new one.
func (cw *compressWriter) Flush() {
	if cw.mode == modeBuffering {
		cw.passThrough()
	}
	if cw.mode == modeGzip {
		_ = cw.gz.Flush()
	}
	_ = http.NewResponseController(cw.ResponseWriter).Flush()
}

// Hijack gives up on encoding first: a hijacked connection is no longer HTTP framing,
// so nothing this wrapper does can apply to it.
func (cw *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := cw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("compress: underlying ResponseWriter is not a Hijacker")
	}
	cw.mode = modePlain
	return hj.Hijack()
}

// finish closes out whichever mode the response ended in. Called from the middleware's
// defer, so a panicking handler still leaves a consistent response for the recoverer.
func (cw *compressWriter) finish() {
	switch cw.mode {
	case modeGzip:
		_ = cw.gz.Close()
	case modeBuffering:
		cw.passThrough()
	case modePlain:
	}
}
