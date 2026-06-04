// Package metrics provides Prometheus text format metrics using github.com/cplieger/metrics.
package metrics

import (
	"net/http"

	m "github.com/cplieger/metrics"
)

var registry = m.NewRegistry("vibekit")

// Exported metrics.
var (
	HTTPRequests = m.NewLabeledCounter("vibekit_http_requests_total", "Total HTTP requests", []string{"method", "path", "status"})
	SSEClients   = m.NewGauge("vibekit_sse_clients", "Current SSE client count")
	BridgeSpawns = m.NewCounter("vibekit_bridge_spawns_total", "Total bridge spawns")
	PushSends    = m.NewCounter("vibekit_push_sends_total", "Total push notification sends")
	HTTPDuration = m.NewHistogram("vibekit_http_request_duration_seconds", "HTTP request latency")
)

func init() {
	registry.RegisterLabeledCounter(HTTPRequests)
	registry.RegisterGauge(SSEClients)
	registry.RegisterCounter(BridgeSpawns)
	registry.RegisterCounter(PushSends)
	registry.RegisterHistogram(HTTPDuration)
}

// Handler returns an HTTP handler serving Prometheus text format.
func Handler() http.HandlerFunc {
	return registry.Handler()
}
