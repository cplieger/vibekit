package composition

import "testing"

// TestParseCleanupPeriodDays covers the pure parse core extracted from
// readCleanupPeriodDays: kiro-cli appends a " (global)" / " (local)"
// scope suffix to non-empty setting values, which must be stripped
// before the integer parse; unparseable and negative values clamp to 0
// ("retention disabled").
func TestParseCleanupPeriodDays(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int
	}{
		{"plain integer", "1", 1},
		{"larger integer", "7", 7},
		{"global scope suffix stripped", "1 (global)", 1},
		{"local scope suffix stripped", "7 (local)", 7},
		{"no space before suffix", "10(global)", 10},
		{"zero", "0", 0},
		{"zero with suffix", "0 (global)", 0},
		{"surrounding whitespace trimmed", "  3  ", 3},
		{"negative clamps to zero", "-5", 0},
		{"negative with suffix clamps to zero", "-3 (global)", 0},
		{"unparseable yields zero", "abc", 0},
		{"empty yields zero", "", 0},
		{"suffix only yields zero", "(global)", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCleanupPeriodDays(tc.out); got != tc.want {
				t.Errorf("parseCleanupPeriodDays(%q) = %d, want %d", tc.out, got, tc.want)
			}
		})
	}
}
