package textsearch

import (
	"fmt"
	"slices"
	"testing"
)

func collect(n Needle, text string) []Hit {
	return slices.Collect(n.Occurrences(text))
}

// TestOccurrences_ByteAddressesTheOriginal covers the 27 runes
// whose simple lowercase changes UTF-8 length (Unicode 17.0.0). Three copies of
// each precede the needle, so a Byte taken from the folded text lands off by
// three times the length delta.
func TestOccurrences_ByteAddressesTheOriginal(t *testing.T) {
	cases := []struct {
		r        string
		wantByte int
	}{
		{"\u0130", 6}, // 2 bytes, lowercase 1
		{"\u023A", 6}, // 2 bytes, lowercase 3
		{"\u023E", 6}, // 2 bytes, lowercase 3
		{"\u1E9E", 9}, // 3 bytes, lowercase 2
		{"\u2126", 9}, // 3 bytes, lowercase 2
		{"\u212A", 9}, // 3 bytes, lowercase 1
		{"\u212B", 9}, // 3 bytes, lowercase 2
		{"\u2C62", 9},
		{"\u2C64", 9},
		{"\u2C6D", 9},
		{"\u2C6E", 9},
		{"\u2C6F", 9},
		{"\u2C70", 9},
		{"\u2C7E", 9},
		{"\u2C7F", 9},
		{"\uA78D", 9},
		{"\uA7AA", 9},
		{"\uA7AB", 9},
		{"\uA7AC", 9},
		{"\uA7AD", 9},
		{"\uA7AE", 9},
		{"\uA7B0", 9},
		{"\uA7B1", 9},
		{"\uA7B2", 9},
		{"\uA7C5", 9},
		{"\uA7CB", 9},
		{"\uA7DC", 9},
	}
	needle := NewNeedle("needle", false)
	for _, tc := range cases {
		t.Run(fmt.Sprintf("U+%04X", []rune(tc.r)[0]), func(t *testing.T) {
			text := tc.r + tc.r + tc.r + "needle"
			hits := collect(needle, text)
			want := []Hit{{Rune: 3, Byte: tc.wantByte}}
			if !slices.Equal(hits, want) {
				t.Fatalf("Occurrences(%q) = %v, want %v", text, hits, want)
			}
			if got := text[hits[0].Byte:]; got != "needle" {
				t.Errorf("text[%d:] = %q, want %q", hits[0].Byte, got, "needle")
			}
		})
	}
}

func TestOccurrences_NeedleHoldingALengthChangingRune(t *testing.T) {
	hits := collect(NewNeedle("\u212A", false), "xxkx")
	want := []Hit{{Rune: 2, Byte: 2}}
	if !slices.Equal(hits, want) {
		t.Errorf("Occurrences(%q) with needle KELVIN SIGN = %v, want %v", "xxkx", hits, want)
	}
}

func TestOccurrences_DoNotOverlap(t *testing.T) {
	cases := []struct {
		text, needle string
		want         []Hit
	}{
		{"aaa", "aa", []Hit{{Rune: 0, Byte: 0}}},
		{"abababa", "aba", []Hit{{Rune: 0, Byte: 0}, {Rune: 4, Byte: 4}}},
		{"aaaa", "aa", []Hit{{Rune: 0, Byte: 0}, {Rune: 2, Byte: 2}}},
	}
	for _, tc := range cases {
		hits := collect(NewNeedle(tc.needle, true), tc.text)
		if !slices.Equal(hits, tc.want) {
			t.Errorf("Occurrences(%q) with needle %q = %v, want %v", tc.text, tc.needle, hits, tc.want)
		}
	}
}

func TestOccurrences_SimpleMappingMatchesTheMedialSigma(t *testing.T) {
	if got := NewNeedle("σ", false).Count("ΟΔΟΣ"); got != 1 {
		t.Errorf("Count(%q) with needle σ = %d, want 1", "ΟΔΟΣ", got)
	}
	if got := NewNeedle("ς", false).Count("ΟΔΟΣ"); got != 0 {
		t.Errorf("Count(%q) with needle ς = %d, want 0", "ΟΔΟΣ", got)
	}
}

func TestOccurrences_CaseSensitivity(t *testing.T) {
	const text = "Retry retry RETRY"
	insensitive := collect(NewNeedle("retry", false), text)
	wantInsensitive := []Hit{{Rune: 0, Byte: 0}, {Rune: 6, Byte: 6}, {Rune: 12, Byte: 12}}
	if !slices.Equal(insensitive, wantInsensitive) {
		t.Errorf("case-insensitive Occurrences(%q) = %v, want %v", text, insensitive, wantInsensitive)
	}
	sensitive := collect(NewNeedle("retry", true), text)
	wantSensitive := []Hit{{Rune: 6, Byte: 6}}
	if !slices.Equal(sensitive, wantSensitive) {
		t.Errorf("case-sensitive Occurrences(%q) = %v, want %v", text, sensitive, wantSensitive)
	}
}

func TestOccurrences_RuneAddressesTheOriginalPastAstralRunes(t *testing.T) {
	hits := collect(NewNeedle("b", false), "a😀b😀c")
	want := []Hit{{Rune: 2, Byte: 5}}
	if !slices.Equal(hits, want) {
		t.Errorf("Occurrences(%q) = %v, want %v", "a😀b😀c", hits, want)
	}
}

func TestOccurrences_EmptyNeedleYieldsNothing(t *testing.T) {
	if hits := collect(NewNeedle("", false), "abc"); len(hits) != 0 {
		t.Errorf("Occurrences(%q) with empty needle = %v, want none", "abc", hits)
	}
}

func TestOccurrences_InvalidBytesInTextAreOneRuneEach(t *testing.T) {
	hits := collect(NewNeedle("B", false), "\xff\xfeb")
	want := []Hit{{Rune: 2, Byte: 2}}
	if !slices.Equal(hits, want) {
		t.Errorf("Occurrences(%q) = %v, want %v", "\xff\xfeb", hits, want)
	}
}

func TestOccurrences_InvalidBytesInQueryNeverMatchMidRune(t *testing.T) {
	hits := collect(NewNeedle("\x82\xAC", true), "\u20AC")
	if len(hits) != 0 {
		t.Errorf("Occurrences(%q) with a mid-rune byte needle = %v, want none", "\u20AC", hits)
	}
}

func TestOccurrences_StopsWhenTheConsumerBreaks(t *testing.T) {
	var seen []Hit
	for h := range NewNeedle("a", true).Occurrences("aaaa") {
		seen = append(seen, h)
		if len(seen) == 2 {
			break
		}
	}
	want := []Hit{{Rune: 0, Byte: 0}, {Rune: 1, Byte: 1}}
	if !slices.Equal(seen, want) {
		t.Errorf("first two hits = %v, want %v", seen, want)
	}
}

func TestCountAndContains_AgreeWithOccurrences(t *testing.T) {
	cases := []struct {
		text, needle string
		want         int
	}{
		{"", "x", 0},
		{"x", "", 0},
		{"kelvin", "\u212A", 1},
		{"\u212A\u212A\u212A", "k", 3},
		{"ΟΔΟΣ", "ς", 0},
		{"abababa", "aba", 2},
	}
	for _, tc := range cases {
		n := NewNeedle(tc.needle, false)
		if got := n.Count(tc.text); got != tc.want {
			t.Errorf("Count(%q) with needle %q = %d, want %d", tc.text, tc.needle, got, tc.want)
		}
		if got := n.Contains(tc.text); got != (tc.want > 0) {
			t.Errorf("Contains(%q) with needle %q = %v, want %v", tc.text, tc.needle, got, tc.want > 0)
		}
	}
}

// TestOccurrences_PositionsIndexTheOriginal is the contract stated once over
// every fixture row: the needle's runes, cut from the ORIGINAL text at Hit.Rune
// and at Hit.Byte, fold to the folded needle.
func TestOccurrences_PositionsIndexTheOriginal(t *testing.T) {
	for _, row := range occurrencesRows() {
		t.Run(row.Name, func(t *testing.T) {
			needle := row.Needle
			if !row.CaseSensitive {
				needle = Fold(needle)
			}
			needleRunes := len([]rune(needle))
			runes := []rune(row.Text)
			for _, h := range collect(NewNeedle(row.Needle, row.CaseSensitive), row.Text) {
				byRune := string(runes[h.Rune : h.Rune+needleRunes])
				if !row.CaseSensitive {
					byRune = Fold(byRune)
				}
				if byRune != needle {
					t.Errorf("runes[%d:%d] of %q folds to %q, want %q", h.Rune, h.Rune+needleRunes, row.Text, byRune, needle)
				}
				if start := len(string(runes[:h.Rune])); start != h.Byte {
					t.Errorf("Byte %d does not address rune %d of %q (that rune starts at byte %d)", h.Byte, h.Rune, row.Text, start)
				}
			}
		})
	}
}
