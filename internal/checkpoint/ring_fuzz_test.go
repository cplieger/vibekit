package checkpoint

import "testing"

// FuzzRingAppendSliceInvariant exercises the generic Ring buffer with
// arbitrary append sequences and asserts:
//  1. Len never exceeds the ring capacity.
//  2. Slice returns exactly Len elements.
//  3. The most recently appended value is always present in Slice.
func FuzzRingAppendSliceInvariant(f *testing.F) {
	f.Add(1, 5)
	f.Add(3, 3)
	f.Add(10, 1)
	f.Add(100, 50)
	f.Add(1, 1)

	f.Fuzz(func(t *testing.T, capacity, numAppends int) {
		if capacity < 1 || capacity > 10000 || numAppends < 0 || numAppends > 10000 {
			return
		}

		r := NewRing[int](capacity)

		for i := range numAppends {
			r.Append(i)
		}

		expectedLen := min(numAppends, capacity)

		if r.Len() != expectedLen {
			t.Fatalf("Len() = %d, want %d (cap=%d, appends=%d)",
				r.Len(), expectedLen, capacity, numAppends)
		}

		sl := r.Slice()
		if len(sl) != expectedLen {
			t.Fatalf("len(Slice()) = %d, want %d", len(sl), expectedLen)
		}

		if numAppends == 0 {
			return
		}

		// Most recent value must be last in slice.
		last := numAppends - 1
		if sl[len(sl)-1] != last {
			t.Fatalf("Slice()[-1] = %d, want %d", sl[len(sl)-1], last)
		}

		// Slice must be in ascending order (FIFO).
		for i := 1; i < len(sl); i++ {
			if sl[i] <= sl[i-1] {
				t.Fatalf("Slice not in order at index %d: %d <= %d", i, sl[i], sl[i-1])
			}
		}
	})
}
