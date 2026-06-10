// Package metrics provides Prometheus text format metrics using
// github.com/cplieger/metrics/v2. The registry prefix ("vibekit") is applied
// to every metric name by the library.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	m "github.com/cplieger/metrics/v2"
)

var registry = m.NewRegistry("vibekit")

// Exported metrics (names auto-prefixed with "vibekit_").
var (
	HTTPRequests = m.NewLabeledCounter("http_requests_total", "Total HTTP requests", []string{"method", "path", "status"})
	SSEClients   = m.NewGauge("sse_clients", "Current SSE client count")
	BridgeSpawns = m.NewCounter("bridge_spawns_total", "Total bridge spawns")
	PushSends    = m.NewCounter("push_sends_total", "Total push notification sends")
	HTTPDuration = m.NewHistogram("http_request_duration_seconds", "HTTP request latency")
)

func init() {
	registry.RegisterLabeledCounter(HTTPRequests)
	registry.RegisterGauge(SSEClients)
	registry.RegisterCounter(BridgeSpawns)
	registry.RegisterCounter(PushSends)
	registry.RegisterHistogram(HTTPDuration)
}

// Handler returns an HTTP handler serving Prometheus text format.
func Handler() http.HandlerFunc { return registry.Handler() }

// NewStatusRecorder wraps w to capture its response status code.
func NewStatusRecorder(w http.ResponseWriter) *m.StatusRecorder { return m.NewStatusRecorder(w) }

// RecordHTTP records one request into the package HTTP metrics.
func RecordHTTP(method, path string, status int, d time.Duration) {
	m.RecordHTTP(HTTPRequests, HTTPDuration, d, method, path, strconv.Itoa(status))
}
