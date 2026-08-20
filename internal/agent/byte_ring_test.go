package agent

// Unit tests for byte_ring.go wrap-around behaviour. Fuzz coverage is
// in byte_ring_fuzz_test.go / byte_ring_utf8_fuzz_test.go.

import "testing"

// After more writes than the capacity, the ring keeps only the newest
// bytes, reports Truncated, and holds exactly capacity bytes.
func TestByteRing_WrapBehaviour(t *testing.T) {
	r := newByteRing(4)
	r.Write([]byte("ab")) // partial fill, pos=2
	r.Write([]byte("cd")) // fills + wraps, pos=0, full
	r.Write([]byte("ef")) // overwrites oldest two
	if got := r.String(); got != "cdef" {
		t.Errorf("byteRing wrap String() = %q, want %q", got, "cdef")
	}
	if !r.Truncated() {
		t.Errorf("byteRing.Truncated() = false, want true after wrap")
	}
	if got := len(r.Bytes()); got != 4 {
		t.Errorf("byteRing stored bytes = %d, want 4", got)
	}
}
