package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"

	m "github.com/cplieger/metrics"
)

func FuzzHistogramObserve(f *testing.F) {
	f.Add(0.001)
	f.Add(0.5)
	f.Add(1.0)
	f.Add(10.0)
	f.Add(0.0)
	f.Add(-1.0)

	h := m.NewHistogram("fuzz_test", "fuzz")
	reg := m.NewRegistry("")
	reg.RegisterHistogram(h)

	f.Fuzz(func(t *testing.T, val float64) {
		h.Observe(val)

		// Verify the handler doesn't panic after observation.
		rec := httptest.NewRecorder()
		reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		body := rec.Body.String()
		if !strings.Contains(body, "fuzz_test_count") {
			t.Error("output missing fuzz_test_count")
		}
	})
}
