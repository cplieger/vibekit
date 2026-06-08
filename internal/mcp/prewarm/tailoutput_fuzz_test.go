package prewarm

import (
	"testing"
	"unicode/utf8"
)

// FuzzTailOutput exercises TailOutput with arbitrary byte slices and
// tail lengths, verifying UTF-8 boundary safety and length invariants.
func FuzzTailOutput(f *testing.F) {
	f.Add([]byte("hello world"), 5)
	f.Add([]byte(""), 0)
	f.Add([]byte("abc"), 100)
	f.Add([]byte("éléphant"), 4)
	f.Add([]byte{0xc3, 0xa9, 0xc3, 0xa9}, 3)       // mid-rune cut
	f.Add([]byte{0xf0, 0x9f, 0x98, 0x80, 0x41}, 2) // emoji + ASCII

	f.Fuzz(func(t *testing.T, data []byte, n int) {
		if n < 0 || n > 1<<20 {
			return
		}
		result := TailOutput(data, n)

		// Result must be valid UTF-8 when input is valid UTF-8.
		if utf8.Valid(data) && !utf8.ValidString(result) {
			t.Errorf("TailOutput produced invalid UTF-8 for valid input")
		}

		// When input fits, result equals input (no prefix).
		if len(data) <= n {
			if result != string(data) {
				t.Errorf("TailOutput(%q, %d) = %q, want %q", data, n, result, string(data))
			}
		} else {
			// Truncated result must start with "…".
			if len(result) < 3 || result[:3] != "…" {
				t.Errorf("truncated TailOutput missing '…' prefix: %q", result)
			}
		}
	})
}
