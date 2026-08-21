package sanitize

import (
	"testing"
	"unicode/utf8"
)

func FuzzSanitizeOutput(f *testing.F) {
	f.Add("")
	f.Add("hello")
	f.Add("\x1b[31m\u200Bred\x1b[0m")
	f.Add("a\u200B\x1b(\u200C0b")
	f.Add("\x1b[\u200B31m")
	f.Add("\x1b]\u2060title\x07")

	f.Fuzz(func(t *testing.T, s string) {
		out := Output(s)
		if ansiRe.MatchString(out) {
			t.Errorf("Output(%q) still contains ANSI", s)
		}
		for _, r := range out {
			if isHidden(r) {
				t.Errorf("Output(%q) contains hidden U+%04X", s, r)
			}
		}
		if out2 := Output(out); out2 != out {
			t.Errorf("not idempotent: %q → %q → %q", s, out, out2)
		}
		// Output must always be valid UTF-8: strings.Map normalizes any
		// invalid byte in the input to U+FFFD, so the persisted result is
		// always safe to JSON-encode.
		if !utf8.ValidString(out) {
			t.Errorf("Output(%q) = %q is not valid UTF-8", s, out)
		}
		// Sanitizing only removes runes or replaces an invalid byte with a
		// single U+FFFD rune, so the rune count never grows. (Byte length
		// CAN grow — one invalid byte becomes a 3-byte U+FFFD — which is
		// why this is a rune-count guard, not a byte-length guard.)
		if utf8.RuneCountInString(out) > utf8.RuneCountInString(s) {
			t.Errorf("rune count grew: %d > %d (in=%q out=%q)",
				utf8.RuneCountInString(out), utf8.RuneCountInString(s), s, out)
		}
	})
}
