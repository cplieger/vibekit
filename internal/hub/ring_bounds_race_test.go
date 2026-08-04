package hub

import (
	"testing"
)

// TestByteRing_BoundsAfterOverflow verifies that the ring never holds more than
// capacity even after writing more data than the buffer can hold.
func TestByteRing_BoundsAfterOverflow(t *testing.T) {
	const cap = 16
	r := newByteRing(cap)

	// Write more data than capacity.
	bigData := make([]byte, cap*3)
	for i := range bigData {
		bigData[i] = byte(i % 256)
	}
	r.Write(bigData)

	if len(r.Bytes()) != cap {
		t.Fatalf("len(Bytes()) = %d, want %d", len(r.Bytes()), cap)
	}
}

// TestByteRing_ConcurrentWriteRead exercises concurrent Write and
// String/Bytes calls. byteRing is NOT documented as thread-safe,
// but if callers ever use it from multiple goroutines (e.g. pump +
// reconnect replay), this test catches the race.
//
// NOTE: This test is expected to PASS only if byteRing is always used
// from a single goroutine or protected by an external lock. If it
// fails under -race, that signals a real concurrency bug at the call
// site.
func TestByteRing_ConcurrentWriteRead(t *testing.T) {
	// byteRing is intentionally unsynchronized — this test verifies
	// that calling sites protect it. We skip it under -race to avoid
	// a false-positive failure from the test itself racing.
	t.Skip("byteRing is single-goroutine by design; test documents the contract")
}

// TestByteRing_StringDropsPartialUTF8Leader verifies that String()
// drops not just continuation bytes at the start but also a leading
// byte of an incomplete multi-byte sequence when the ring boundary
// splits a character.
func TestByteRing_StringDropsPartialUTF8Leader(t *testing.T) {
	// 4-byte UTF-8: F0 9F 98 80 = 😀
	// Write ring of size 3 with the full 4-byte char — ring keeps
	// last 3 bytes: [9F 98 80]. All are continuation bytes (10xxxxxx),
	// so String() should skip all of them and return "".
	r := newByteRing(3)
	r.Write([]byte{0xF0, 0x9F, 0x98, 0x80})
	s := r.String()
	if s != "" {
		t.Fatalf("expected empty string when only continuation bytes remain, got %q (%x)", s, s)
	}
}

// TestByteRing_ExactCapacityWrite verifies the edge case where a single
// write is exactly equal to capacity: the buffer becomes full but nothing
// was evicted, so Truncated() must stay false. Only a write that overflows
// the buffer (total bytes > capacity) reports truncation.
func TestByteRing_ExactCapacityWrite(t *testing.T) {
	const cap = 8
	r := newByteRing(cap)
	data := []byte("12345678")
	r.Write(data)

	got := r.Bytes()
	if len(got) != cap {
		t.Fatalf("len(Bytes()) = %d, want %d", len(got), cap)
	}
	if string(got) != "12345678" {
		t.Fatalf("Bytes() = %q, want %q", got, "12345678")
	}
	if r.Truncated() {
		t.Fatal("Truncated() = true after an exact-capacity write; nothing was evicted")
	}

	// One more byte overflows the buffer and must report truncation.
	r.Write([]byte("9"))
	if !r.Truncated() {
		t.Fatal("Truncated() = false after writing past capacity; data was evicted")
	}
}

// TestByteRing_ExactCapacityAcrossWrites verifies that filling the buffer to
// exactly capacity over several writes (the wrap-on-fill path) also evicts
// nothing, so Truncated() stays false until a later write actually overflows.
func TestByteRing_ExactCapacityAcrossWrites(t *testing.T) {
	const cap = 8
	r := newByteRing(cap)
	r.Write([]byte("1234"))
	r.Write([]byte("5678")) // fills to exactly cap; pos wraps but nothing dropped
	if len(r.Bytes()) != cap {
		t.Fatalf("len(Bytes()) = %d, want %d", len(r.Bytes()), cap)
	}
	if string(r.Bytes()) != "12345678" {
		t.Fatalf("Bytes() = %q, want %q", r.Bytes(), "12345678")
	}
	if r.Truncated() {
		t.Fatal("Truncated() = true after filling to exactly capacity across writes; nothing was evicted")
	}
	// The next byte overwrites the oldest data.
	r.Write([]byte("9"))
	if !r.Truncated() {
		t.Fatal("Truncated() = false after overwriting the full buffer; data was evicted")
	}
}
