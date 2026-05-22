// Package metrics provides hand-rolled Prometheus text format metrics.
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var startTime = time.Now()

// Counter is a monotonically increasing counter.
type Counter struct {
	name string
	help string
	val  atomic.Int64
}

func (c *Counter) Inc() { c.val.Add(1) }

// Gauge is a value that can go up and down.
type Gauge struct {
	name string
	help string
	val  atomic.Int64
}

func (g *Gauge) Inc()        { g.val.Add(1) }
func (g *Gauge) Dec()        { g.val.Add(-1) }
func (g *Gauge) Set(v int64) { g.val.Store(v) }

// LabeledCounter tracks counts per label combination.
type LabeledCounter struct {
	vals   map[string]*atomic.Int64
	name   string
	help   string
	labels []string
	mu     sync.RWMutex
}

func (lc *LabeledCounter) Inc(labelVals ...string) {
	key := strings.Join(labelVals, "\x00")
	lc.mu.RLock()
	v, ok := lc.vals[key]
	lc.mu.RUnlock()
	if ok {
		v.Add(1)
		return
	}
	lc.mu.Lock()
	if v, ok = lc.vals[key]; ok {
		lc.mu.Unlock()
		v.Add(1)
		return
	}
	v = &atomic.Int64{}
	v.Store(1)
	lc.vals[key] = v
	lc.mu.Unlock()
}

// Histogram tracks a distribution using cumulative buckets and atomic CAS for sum.
type Histogram struct {
	name    string
	help    string
	sumBits atomic.Uint64
	count   atomic.Int64
	buckets [len(defaultBuckets) + 1]atomic.Int64
}

var defaultBuckets = [8]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0}

func (h *Histogram) Observe(seconds float64) {
	for {
		old := h.sumBits.Load()
		newF := math.Float64frombits(old) + seconds
		if h.sumBits.CompareAndSwap(old, math.Float64bits(newF)) {
			break
		}
	}
	h.count.Add(1)
	for i, bound := range defaultBuckets {
		if seconds <= bound {
			for j := i; j < len(defaultBuckets); j++ {
				h.buckets[j].Add(1)
			}
			break
		}
	}
	h.buckets[len(defaultBuckets)].Add(1)
}

// Exported metrics.
var (
	HTTPRequests = &LabeledCounter{
		name:   "vibekit_http_requests_total",
		help:   "Total HTTP requests",
		labels: []string{"method", "path", "status"},
		vals:   make(map[string]*atomic.Int64),
	}
	SSEClients = &Gauge{
		name: "vibekit_sse_clients",
		help: "Current SSE client count",
	}
	BridgeSpawns = &Counter{
		name: "vibekit_bridge_spawns_total",
		help: "Total bridge spawns",
	}
	PushSends = &Counter{
		name: "vibekit_push_sends_total",
		help: "Total push notification sends",
	}
	HTTPDuration = &Histogram{
		name: "vibekit_http_request_duration_seconds",
		help: "HTTP request latency",
	}
)

// Handler returns an HTTP handler serving Prometheus text format.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var b strings.Builder
		writeLabeledCounter(&b, HTTPRequests)
		writeGauge(&b, SSEClients)
		writeCounter(&b, BridgeSpawns)
		writeCounter(&b, PushSends)
		writeHistogram(&b, HTTPDuration)
		writeProcessMetrics(&b)
		w.Write([]byte(b.String())) //nolint:errcheck // ResponseWriter writes never return an actionable error
	}
}

func writeCounter(b *strings.Builder, c *Counter) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n",
		c.name, c.help, c.name, c.name, c.val.Load())
}

func writeGauge(b *strings.Builder, g *Gauge) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n",
		g.name, g.help, g.name, g.name, g.val.Load())
}

func writeLabeledCounter(b *strings.Builder, lc *LabeledCounter) {
	lc.mu.RLock()
	keys := make([]string, 0, len(lc.vals))
	for k := range lc.vals {
		keys = append(keys, k)
	}
	lc.mu.RUnlock()
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", lc.name, lc.help, lc.name)
	for _, key := range keys {
		lc.mu.RLock()
		v := lc.vals[key]
		lc.mu.RUnlock()
		parts := strings.Split(key, "\x00")
		// Sort labels alphabetically for Prometheus compliance.
		type lp struct {
			k, v string
		}
		pairs := make([]lp, len(lc.labels))
		for i, l := range lc.labels {
			pairs[i] = lp{l, parts[i]}
		}
		sort.Slice(pairs, func(a, c int) bool { return pairs[a].k < pairs[c].k })
		var labelStr strings.Builder
		for i, p := range pairs {
			if i > 0 {
				labelStr.WriteByte(',')
			}
			fmt.Fprintf(&labelStr, "%s=%q", p.k, p.v)
		}
		fmt.Fprintf(b, "%s{%s} %d\n", lc.name, labelStr.String(), v.Load())
	}
}

func writeHistogram(b *strings.Builder, h *Histogram) {
	sum := math.Float64frombits(h.sumBits.Load())
	count := h.count.Load()
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
	for i, bound := range defaultBuckets {
		fmt.Fprintf(b, "%s_bucket{le=%q} %d\n", h.name, formatBound(bound), h.buckets[i].Load())
	}
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", h.name, h.buckets[len(defaultBuckets)].Load())
	fmt.Fprintf(b, "%s_sum %.6f\n", h.name, sum)
	fmt.Fprintf(b, "%s_count %d\n", h.name, count)
}

func formatBound(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func writeProcessMetrics(b *strings.Builder) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Fprintf(b, "# HELP process_goroutines Number of goroutines\n# TYPE process_goroutines gauge\nprocess_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(b, "# HELP process_heap_bytes Heap memory in use\n# TYPE process_heap_bytes gauge\nprocess_heap_bytes %d\n", m.HeapAlloc)
	fmt.Fprintf(b, "# HELP process_gc_pause_seconds_total Total GC pause time\n# TYPE process_gc_pause_seconds_total counter\nprocess_gc_pause_seconds_total %.6f\n", float64(m.PauseTotalNs)/1e9)
	fmt.Fprintf(b, "# HELP process_uptime_seconds Process uptime\n# TYPE process_uptime_seconds gauge\nprocess_uptime_seconds %.3f\n", time.Since(startTime).Seconds())
}
