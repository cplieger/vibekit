package textsearch

import (
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestFold_PreservesRuneCount is the construction gate: every rune index into
// Fold(s) must be a rune index into s, over the whole code space.
func TestFold_PreservesRuneCount(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if utf8.RuneLen(r) < 0 {
			continue
		}
		s := string(r)
		if got := utf8.RuneCountInString(Fold(s)); got != 1 {
			t.Fatalf("Fold(U+%04X) has %d runes, want 1", r, got)
		}
	}
}

func TestFold_InvalidByteBecomesOneReplacementRune(t *testing.T) {
	if got := Fold("a\xffb\xfe\xfd"); got != "a\uFFFDb\uFFFD\uFFFD" {
		t.Errorf("Fold(%q) = %q, want %q", "a\xffb\xfe\xfd", got, "a\uFFFDb\uFFFD\uFFFD")
	}
}

func TestFold_UsesSimpleMappingNotFinalSigma(t *testing.T) {
	if got := Fold("ΟΔΟΣ"); got != "οδοσ" {
		t.Errorf("Fold(%q) = %q, want %q", "ΟΔΟΣ", got, "οδοσ")
	}
}

func TestFold_LowercasesLengthChangingRunes(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"\u212A", "k"},      // KELVIN SIGN, 3 bytes to 1
		{"\u0130", "i"},      // LATIN CAPITAL LETTER I WITH DOT ABOVE, 2 to 1
		{"\u1E9E", "\u00DF"}, // LATIN CAPITAL LETTER SHARP S, 3 to 2
		{"\u023A", "\u2C65"}, // LATIN CAPITAL LETTER A WITH STROKE, 2 to 3
	}
	for _, tc := range cases {
		if got := Fold(tc.in); got != tc.want {
			t.Errorf("Fold(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
