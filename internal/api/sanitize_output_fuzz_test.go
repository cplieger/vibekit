package api

import "testing"

func FuzzSanitizeOutput(f *testing.F) {
	f.Add("")
	f.Add("hello")
	f.Add("\x1b[31m\u200Bred\x1b[0m")
	f.Add("a\u200B\x1b(\u200C0b")
	f.Add("\x1b[\u200B31m")
	f.Add("\x1b]\u2060title\x07")

	f.Fuzz(func(t *testing.T, s string) {
		out := SanitizeOutput(s)
		if ansiRe.MatchString(out) {
			t.Errorf("SanitizeOutput(%q) still contains ANSI", s)
		}
		for _, r := range out {
			if isHiddenUnicode(r) {
				t.Errorf("SanitizeOutput(%q) contains hidden U+%04X", s, r)
			}
		}
		if out2 := SanitizeOutput(out); out2 != out {
			t.Errorf("not idempotent: %q → %q → %q", s, out, out2)
		}
		if len(out) > len(s) {
			t.Errorf("output longer than input: %d > %d", len(out), len(s))
		}
	})
}
