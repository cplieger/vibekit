package agent

import (
	"testing"
	"unicode/utf8"
)

// FuzzByteRingWrite exercises the circular buffer with multiple writes
// and verifies invariants: the stored byte count never exceeds capacity,
// Bytes never panics, and String is valid UTF-8 when all input is valid
// UTF-8.
func FuzzByteRingWrite(f *testing.F) {
	f.Add([]byte("hello"), []byte(" world"), 8)
	f.Add([]byte("abc"), []byte("defghij"), 4)
	f.Add([]byte{0xc3, 0xa9}, []byte{0xc3, 0xa9}, 3)
	f.Add([]byte{}, []byte("x"), 1)

	f.Fuzz(func(t *testing.T, a, b []byte, cap int) {
		if cap < 1 || cap > 1024 {
			return
		}
		r := newByteRing(cap)
		r.Write(a)
		r.Write(b)

		stored := r.Bytes()
		if len(stored) > cap {
			t.Errorf("ring holds %d bytes, exceeds capacity %d", len(stored), cap)
		}

		s := r.String()

		// String must be valid UTF-8 when all input was valid UTF-8.
		if utf8.Valid(a) && utf8.Valid(b) && !utf8.ValidString(s) {
			t.Error("String() returned invalid UTF-8 for valid UTF-8 input")
		}
	})
}
