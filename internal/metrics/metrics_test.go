package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// --- test-arch-metrics-benchmark-p1 ---

func BenchmarkHistogramObserve(b *testing.B) {
	h := &Histogram{name: "bench_hist", help: "bench"}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		h.Observe(0.042)
	}
}

func BenchmarkHistogramObserve_Parallel(b *testing.B) {
	h := &Histogram{name: "bench_hist_par", help: "bench"}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			h.Observe(0.042)
		}
	})
}

func BenchmarkLabeledCounterInc(b *testing.B) {
	lc := &LabeledCounter{
		name:   "bench_lc",
		help:   "bench",
		labels: []string{"method", "path", "status"},
		vals:   make(map[labelKey]*atomic.Int64),
	}
	// Pre-populate a key so we benchmark the existing-key fast path.
	lc.Inc("GET", "/api", "200")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		lc.Inc("GET", "/api", "200")
	}
}

func BenchmarkLabeledCounterInc_NewKey(b *testing.B) {
	lc := &LabeledCounter{
		name:   "bench_lc_new",
		help:   "bench",
		labels: []string{"method", "path", "status"},
		vals:   make(map[labelKey]*atomic.Int64),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		lc.Inc("GET", "/api/"+strings.Repeat("x", i%8), "200")
	}
}

func BenchmarkLabeledCounterInc_Parallel(b *testing.B) {
	lc := &LabeledCounter{
		name:   "bench_lc_par",
		help:   "bench",
		labels: []string{"method", "path", "status"},
		vals:   make(map[labelKey]*atomic.Int64),
	}
	lc.Inc("GET", "/api", "200")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lc.Inc("GET", "/api", "200")
		}
	})
}

// --- test-arch-metrics-handler-unit-p4 ---

func TestMetricsHandler(t *testing.T) {
	// Reset counters for deterministic output.
	h := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("unexpected Content-Type: %s", ct)
	}
	// Verify key metric families are present.
	for _, want := range []string{
		"vibekit_http_request_duration_seconds",
		"vibekit_bridge_spawns_total",
		"vibekit_push_sends_total",
		"vibekit_sse_clients",
		"process_goroutines",
		"process_heap_bytes",
		"process_uptime_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// Verify Prometheus text format markers.
	if !strings.Contains(body, "# HELP") {
		t.Error("output missing # HELP lines")
	}
	if !strings.Contains(body, "# TYPE") {
		t.Error("output missing # TYPE lines")
	}
}

func BenchmarkMetricsHandler(b *testing.B) {
	// Seed some data so the handler has work to do.
	HTTPRequests.Inc("GET", "/api", "200")
	HTTPDuration.Observe(0.05)
	BridgeSpawns.Inc()

	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}
