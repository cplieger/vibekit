package sanitize

import (
	"strings"
	"testing"
)

func FuzzSanitizeUnicode(f *testing.F) {
	// Seed corpus from TestSanitizeUnicode cases.
	seeds := []string{
		"",
		"hello world",
		"line1\n\tline2",
		"a\u200Bb",
		"a\u200Db",
		"\uFEFFhello",
		"he\u00ADllo",
		"a\u2060b",
		"\u200Ehello",
		"hello\u200F",
		"a\u202Ab\u202Ec",
		"a\u2066b\u2069c",
		"visible\U000E0041\U000E0062hidden",
		"a\u00A0b",
		"café",
		"hi 😀",
		"a\u200B\u200C\u200D\u2060b",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Unicode(s)
		// Invariant: output must contain no runes where isHidden returns true.
		for _, r := range out {
			if isHidden(r) {
				t.Errorf("Unicode(%q) output contains hidden rune U+%04X", s, r)
			}
		}
		// Invariant: must not panic (implicit by reaching here).
		// Invariant: idempotent.
		if out2 := Unicode(out); out2 != out {
			t.Errorf("Unicode not idempotent: first=%q second=%q", out, out2)
		}
	})
}

func FuzzStripANSI(f *testing.F) {
	// Seed corpus from TestStripANSI + TestStripANSI_edge_cases.
	seeds := []string{
		"",
		"hello world",
		"\x1b[31mred\x1b[0m",
		"\x1b[1;32mbold green\x1b[0m",
		"\x1b]0;title\x07rest",
		"\x1b]0;title\x1b\\rest",
		"\x1b(B",
		"\x1b)0",
		"\x1b7\x1b8",
		"\x1b[?25l",
		"\x1b[38;2;255;0;0mtruecolor\x1b[0m",
		"no escapes here",
		"\x1b[m",
		"\x1b[K",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := StripANSI(s)
		// Invariant: output must contain no bytes matching ansiRe.
		if ansiRe.MatchString(out) {
			t.Errorf("StripANSI(%q) output still contains ANSI escapes", s)
		}
		// Invariant: must not panic (implicit by reaching here).
		// Invariant: idempotent.
		if out2 := StripANSI(out); out2 != out {
			t.Errorf("StripANSI not idempotent: first=%q second=%q", out, out2)
		}
	})
}

func BenchmarkSanitizeOutput(b *testing.B) {
	cleanShort := strings.Repeat("Hello, world! ", 7)                                          // ~100 bytes
	ansiHeavy := strings.Repeat("\x1b[31mred\x1b[0m \x1b[1;32mbold\x1b[0m ", 200)              // ~4KB dense ANSI
	unicodeHeavy := strings.Repeat("text\u200B\u200C\u200D\u2060more ", 200)                   // ~4KB scattered hidden codepoints
	mixedLarge := strings.Repeat("\x1b[33m"+strings.Repeat("output ", 20)+"\x1b[0m\u200B", 50) // ~16KB realistic

	b.Run("clean_short", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			Output(cleanShort)
		}
	})
	b.Run("ansi_heavy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			Output(ansiHeavy)
		}
	})
	b.Run("unicode_heavy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			Output(unicodeHeavy)
		}
	})
	b.Run("mixed_large", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			Output(mixedLarge)
		}
	})
}
