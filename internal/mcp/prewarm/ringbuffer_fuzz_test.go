package prewarm

import "testing"

// FuzzRingBuffer exercises the prewarm RingBuffer with arbitrary write
// sequences, verifying capacity invariants and data consistency.
func FuzzRingBuffer(f *testing.F) {
	f.Add([]byte("hello"), []byte(" world"), 8)
	f.Add([]byte("abc"), []byte("defghij"), 5)
	f.Add([]byte{}, []byte("x"), 1)
	f.Add([]byte("overflow"), []byte(""), 3)

	f.Fuzz(func(t *testing.T, first, second []byte, cap int) {
		if cap < 1 || cap > 4096 {
			return
		}
		r := &RingBuffer{Cap: cap}

		n1, err1 := r.Write(first)
		if err1 != nil || n1 != len(first) {
			t.Fatalf("Write(first) = (%d, %v), want (%d, nil)", n1, err1, len(first))
		}

		n2, err2 := r.Write(second)
		if err2 != nil || n2 != len(second) {
			t.Fatalf("Write(second) = (%d, %v), want (%d, nil)", n2, err2, len(second))
		}

		got := r.Bytes()
		if len(got) > cap {
			t.Errorf("Bytes() len %d exceeds cap %d", len(got), cap)
		}

		// Buffer must contain the tail of the combined writes.
		combined := append(append([]byte(nil), first...), second...)
		if len(combined) <= cap {
			if string(got) != string(combined) {
				t.Errorf("small input: got %q, want %q", got, combined)
			}
		} else {
			want := combined[len(combined)-cap:]
			if string(got) != string(want) {
				t.Errorf("overflow: got %q, want %q", got, want)
			}
		}
	})
}
