package git

import (
	"testing"
)

func FuzzStatusLabel(f *testing.F) {
	f.Add(byte('M'))
	f.Add(byte('A'))
	f.Add(byte('D'))
	f.Add(byte('R'))
	f.Add(byte('C'))
	f.Add(byte('U'))
	f.Add(byte('?'))
	f.Add(byte('X'))
	f.Add(byte(0))

	f.Fuzz(func(t *testing.T, c byte) {
		result := statusLabel(c)
		if result == "" {
			t.Fatal("statusLabel returned empty string")
		}
	})
}

func FuzzTruncateDiff(f *testing.F) {
	f.Add("short diff", 100)
	f.Add("hello world", 5)
	f.Add("", 0)
	f.Add("abc", -1)

	f.Fuzz(func(t *testing.T, diff string, maxBytes int) {
		if maxBytes < 0 {
			return
		}
		result := truncateDiff(diff, maxBytes)
		if len(diff) <= maxBytes {
			if result != diff {
				t.Fatalf("no truncation needed but result differs")
			}
		} else {
			if len(result) > maxBytes+len(diffTruncatedSuffix) {
				t.Fatalf("result too long: %d", len(result))
			}
		}
	})
}
