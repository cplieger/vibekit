package checkpoint

import (
	"encoding/hex"
	"testing"
)

// FuzzHashOfDeterminism verifies hashOf produces a valid, stable
// hex-encoded SHA-256 for arbitrary input and never panics.
func FuzzHashOfDeterminism(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("hello world"))
	f.Add([]byte("\x00\xff\xfe"))

	f.Fuzz(func(t *testing.T, data []byte) {
		h1 := hashOf(data)
		h2 := hashOf(data)
		if h1 != h2 {
			t.Fatalf("non-deterministic: %q vs %q", h1, h2)
		}
		if len(h1) != 64 {
			t.Fatalf("expected 64-char hex, got %d chars", len(h1))
		}
		if _, err := hex.DecodeString(h1); err != nil {
			t.Fatalf("not valid hex: %v", err)
		}
	})
}
