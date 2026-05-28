package ids

import (
	"strings"
	"testing"
)

func FuzzNew(f *testing.F) {
	f.Add(6, 0) // StdLower
	f.Add(6, 1) // HexUpper (actually 0 is HexUpper, 1 is StdLower)
	f.Add(1, 0)
	f.Add(16, 0)
	f.Add(32, 1)

	f.Fuzz(func(t *testing.T, byteLen int, encInt int) {
		if byteLen < 1 || byteLen > 64 {
			return
		}
		enc := Encoding(encInt)
		if enc != HexUpper && enc != StdLower {
			return
		}

		result := New(byteLen, enc)

		// Output length: ceil(byteLen*8/5) for base32.
		expectedLen := (byteLen*8 + 4) / 5
		if len(result) != expectedLen {
			t.Errorf("New(%d, %d) len = %d, want %d", byteLen, enc, len(result), expectedLen)
		}

		// Charset validation.
		switch enc {
		case HexUpper:
			for _, c := range result {
				if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'V')) {
					t.Errorf("New(%d, HexUpper) contains invalid char %q", byteLen, string(c))
					break
				}
			}
		case StdLower:
			lower := strings.ToLower(result)
			if lower != result {
				t.Errorf("New(%d, StdLower) not lowercase: %q", byteLen, result)
			}
			for _, c := range result {
				if !((c >= 'a' && c <= 'z') || (c >= '2' && c <= '7')) {
					t.Errorf("New(%d, StdLower) contains invalid char %q", byteLen, string(c))
					break
				}
			}
		}
	})
}
