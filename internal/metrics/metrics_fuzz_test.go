package metrics

import (
	"math"
	"testing"
)

func FuzzHistogramObserve(f *testing.F) {
	f.Add(0.001)
	f.Add(0.5)
	f.Add(1.0)
	f.Add(10.0)
	f.Add(math.MaxFloat64)
	f.Add(0.0)
	f.Add(-1.0)

	h := &Histogram{name: "fuzz_test", help: "fuzz"}

	f.Fuzz(func(t *testing.T, val float64) {
		countBefore := h.count.Load()
		h.Observe(val)
		countAfter := h.count.Load()

		if countAfter != countBefore+1 {
			t.Errorf("count did not increment: before=%d after=%d", countBefore, countAfter)
		}

		if !math.IsNaN(val) && !math.IsInf(val, 0) {
			sumBits := h.sumBits.Load()
			sum := math.Float64frombits(sumBits)
			if math.IsNaN(sum) {
				t.Error("sum became NaN from finite input")
			}
		}

		// Bucket counts must be monotonically non-decreasing
		var prev int64
		for i := range h.buckets {
			cur := h.buckets[i].Load()
			if cur < prev {
				t.Errorf("bucket[%d] count %d < prev %d", i, cur, prev)
			}
			prev = cur
		}
	})
}
