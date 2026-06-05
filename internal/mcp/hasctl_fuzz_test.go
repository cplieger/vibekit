package mcp

import "testing"

func FuzzHasCtl(f *testing.F) {
	f.Add("hello")
	f.Add("\x00")
	f.Add("\x1F")
	f.Add("\x7F")
	f.Add("日本語")
	f.Add("\xC0\x80")
	f.Add("abc\ndef")

	f.Fuzz(func(t *testing.T, s string) {
		got := hasCtl(s)
		// Verify against reference implementation.
		want := false
		for i := range len(s) {
			if s[i] < 0x20 || s[i] == 0x7F {
				want = true
				break
			}
		}
		if got != want {
			t.Fatalf("hasCtl(%q) = %v, want %v", s, got, want)
		}
	})
}
