package textsearch

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"unicode/utf16"
)

// occurrencesFixture is testdata/occurrences.json: the match kernel's contract
// with its TypeScript twin, produced here from real Occurrences output and
// consumed by static-src/textsearch's tests, which assert every row's hit count
// and utf16 positions.
type occurrencesFixture struct {
	Comment []string         `json:"_comment"`
	Rows    []occurrencesRow `json:"rows"`
}

// occurrencesRow is one scan and its hits.
type occurrencesRow struct {
	Name          string       `json:"name"`
	Text          string       `json:"text"`
	Needle        string       `json:"needle"`
	CaseSensitive bool         `json:"case_sensitive"`
	Hits          []fixtureHit `json:"hits"`
}

// fixtureHit is a Hit plus the position in UTF-16 code units, the unit the
// TypeScript side addresses text in.
type fixtureHit struct {
	Rune  int `json:"rune"`
	Byte  int `json:"byte"`
	UTF16 int `json:"utf16"`
}

const occurrencesFixturePath = "testdata/occurrences.json"

const occurrencesFixtureCmd = "UPDATE_GOLDEN=1 go test ./internal/textsearch/ -run TestOccurrencesFixture"

var occurrencesFixtureComment = []string{
	"Occurrences of a needle in a text, as the Go kernel reports them.",
	"",
	"Go PRODUCES this file from textsearch.Needle.Occurrences (TestOccurrencesFixture) and",
	"the TypeScript twin in static-src/textsearch CONSUMES it, asserting each row's hit",
	"count and every utf16 position. Positions index the ORIGINAL text: rune is a code",
	"point index, byte a UTF-8 offset, utf16 a UTF-16 code unit index (two per code point",
	"above U+FFFF). Both folds are Unicode's simple per-code-point lowercase, so the",
	"final-sigma rows pin that a context-sensitive lowercase would diverge.",
	"",
	"Regenerate with: " + occurrencesFixtureCmd,
	"then, from static-src/: npx vitest --run --project node textsearch/occurrences.node.test.ts",
}

// occurrencesRows is the fixture's input set, hits unfilled.
func occurrencesRows() []occurrencesRow {
	return []occurrencesRow{
		{Name: "ascii_non_overlapping", Text: "aaa", Needle: "aa"},
		{Name: "ascii_adjacent_matches", Text: "aaaa", Needle: "aa"},
		{Name: "ascii_multi_rune_non_overlapping", Text: "abababa", Needle: "aba"},
		{Name: "case_insensitive_ascii", Text: "Retry retry RETRY", Needle: "retry"},
		{Name: "case_sensitive_ascii", Text: "Retry retry RETRY", Needle: "retry", CaseSensitive: true},
		{Name: "kelvin_sign_folds_to_k", Text: "\u212A\u212A\u212Akelvin", Needle: "k"},
		{Name: "kelvin_sign_in_the_needle", Text: "kelvin", Needle: "\u212A"},
		{Name: "case_sensitive_kelvin_sign_is_literal", Text: "K k \u212A", Needle: "\u212A", CaseSensitive: true},
		{Name: "dotted_capital_I_folds_to_i", Text: "\u0130\u0130i", Needle: "i"},
		{Name: "capital_sharp_s_folds_to_sharp_s", Text: "Stra\u1E9Ee", Needle: "\u00DF"},
		{Name: "final_sigma_medial_needle_matches", Text: "ΟΔΟΣ", Needle: "σ"},
		{Name: "final_sigma_final_needle_misses", Text: "ΟΔΟΣ", Needle: "ς"},
		{Name: "astral_runes_before_the_match", Text: "😀😀x", Needle: "x"},
		{Name: "astral_runes_between_matches", Text: "a😀b😀c", Needle: "b"},
		{Name: "astral_rune_as_the_needle", Text: "x😀y😀", Needle: "😀"},
		{Name: "kelvin_astral_and_final_sigma_together", Text: "\u212A😀ΟΔΟΣ k", Needle: "k"},
		{Name: "needle_longer_than_text", Text: "ab", Needle: "abc"},
		{Name: "empty_text", Text: "", Needle: "x"},
	}
}

// TestOccurrencesFixture pins real Occurrences output to testdata/occurrences.json.
func TestOccurrencesFixture(t *testing.T) {
	fx := occurrencesFixture{Comment: occurrencesFixtureComment, Rows: occurrencesRows()}
	matched := 0
	for i := range fx.Rows {
		row := &fx.Rows[i]
		row.Hits = make([]fixtureHit, 0)
		for h := range NewNeedle(row.Needle, row.CaseSensitive).Occurrences(row.Text) {
			units := len(utf16.Encode([]rune(row.Text[:h.Byte])))
			row.Hits = append(row.Hits, fixtureHit{Rune: h.Rune, Byte: h.Byte, UTF16: units})
		}
		if len(row.Hits) > 0 {
			matched++
		}
	}
	if matched == 0 {
		t.Fatal("no fixture row has a hit; the TS side would pin nothing")
	}

	got, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	got = append(got, '\n')

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(occurrencesFixturePath, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(occurrencesFixturePath)
	if err != nil {
		t.Fatalf("read golden %s (run %s): %v", occurrencesFixturePath, occurrencesFixtureCmd, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Occurrences output drifted from %s.\n--- want (fixture)\n%s\n--- got\n%s\nRegenerate with %s, then run static-src/textsearch/occurrences.node.test.ts.",
			occurrencesFixturePath, want, got, occurrencesFixtureCmd)
	}
}
