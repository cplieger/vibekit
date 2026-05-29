package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"vibekit/internal/metrics"
)

// requestIDHeader is the canonical HTTP header carrying the per-request id.
const requestIDHeader = "X-Request-ID"

type reqIDKey struct{}

// requestLogger wraps next with method/path/status/latency/request-id
// access logging at slog.Info. Skips long-lived paths (SSE /api/events
// and WebSocket /api/shell/ws) that run for the lifetime of a session.
//
// Each request gets a stable id available via requestIDFromContext. An
// inbound X-Request-ID header is reused when it matches the validated
// shape (alphanumeric + dashes + underscores, 1..64 chars); otherwise
// a 16-byte random hex id is generated. The id is echoed on the
// response so callers can correlate without server logs.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Long-lived connections log their own lifecycle; per-request
		// access logging would just mark "opened" without useful latency.
		switch r.URL.Path {
		case "/api/events", "/api/shell/ws":
			next.ServeHTTP(w, r)
			return
		}

		id := reqIDOrNew(r.Header.Get(requestIDHeader))
		w.Header().Set(requestIDHeader, id)
		ctx := context.WithValue(r.Context(), reqIDKey{}, id)

		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rw, r.WithContext(ctx))

		dur := time.Since(start)
		slog.InfoContext(ctx, "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", dur.Milliseconds(),
			"request_id", id,
		)
		metrics.HTTPRequests.Inc(r.Method, r.URL.Path, strconv.Itoa(rw.status))
		metrics.HTTPDuration.Observe(dur.Seconds())
	})
}

type statusWriter struct {
	http.ResponseWriter

	status      int
	wroteHeader bool
}

func (sw *statusWriter) WriteHeader(code int) {
	if !sw.wroteHeader {
		sw.status = code
		sw.wroteHeader = true
	}
	sw.ResponseWriter.WriteHeader(code)
}

func reqIDOrNew(inbound string) string {
	if validReqID(inbound) {
		return inbound
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

func validReqID(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
