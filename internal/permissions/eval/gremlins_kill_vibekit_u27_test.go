package eval

import "testing"

// gk_vibekit_u27_assertFields fails the test if ShellFields(in) did not
// return exactly want.
func gk_vibekit_u27_assertFields(t *testing.T, in string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("ShellFields(%q) = %v (len %d), want %v (len %d)", in, got, len(got), want, len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ShellFields(%q) = %v, want %v", in, got, want)
			return
		}
	}
}

// TestGkVibekitU27_WriteOptionMatches_ShortPrefix kills both mutants at
// eval.go:122:39 (the `len(tok) > 2` guard in writeOption.Matches for the
// ShortPrefix mode): CONDITIONALS_NEGATION (`> 2` -> `<= 2`) and
// CONDITIONALS_BOUNDARY (`> 2` -> `>= 2`).
func TestGkVibekitU27_WriteOptionMatches_ShortPrefix(t *testing.T) {
	// NEGATION: a value-bearing short option (len 10 > 2) matches via the
	// HasPrefix clause. `<= 2` would make 10<=2 false and drop the match.
	if got := (writeOption{token: "-o", mode: ShortPrefix}).Matches("-ofile.txt"); !got {
		t.Errorf("Matches(ShortPrefix -o).Matches(%q) = false, want true", "-ofile.txt")
	}
	// BOUNDARY: with a 1-char token, a len-2 token sharing the prefix but not
	// equal to the token must NOT match — `2 > 2` is false. The `2 >= 2`
	// mutation flips it to a (wrong) match.
	if got := (writeOption{token: "-", mode: ShortPrefix}).Matches("-x"); got {
		t.Errorf("Matches(ShortPrefix -).Matches(%q) = true, want false (len==2 boundary)", "-x")
	}
	// The exact short option matches via the equality clause regardless.
	if got := (writeOption{token: "-o", mode: ShortPrefix}).Matches("-o"); !got {
		t.Errorf("Matches(ShortPrefix -o).Matches(%q) = false, want true", "-o")
	}
	// Realistic public path also kills the NEGATION mutant.
	if got := HasWriteOption("cmd -ofile.txt"); !got {
		t.Errorf("HasWriteOption(%q) = false, want true", "cmd -ofile.txt")
	}
}

// TestGkVibekitU27_MatchWildcard_TrailingSuffix kills the
// CONDITIONALS_NEGATION mutant at match.go:60 (`last != ""`). With a
// non-empty trailing suffix the command must actually end with it; the
// `== ""` mutation skips the HasSuffix check and returns true.
func TestGkVibekitU27_MatchWildcard_TrailingSuffix(t *testing.T) {
	// Loop validates prefix "a" and finds "b" mid-string (pos advances past
	// it) without early-returning, then line 60 requires HasSuffix(_, "b").
	// "abx" does not end with "b" -> false. Mutation returns true.
	if got := MatchWildcard("a*b", "abx"); got {
		t.Errorf("MatchWildcard(%q, %q) = true, want false", "a*b", "abx")
	}
	// Same pattern where the command does end with the suffix -> true.
	if got := MatchWildcard("a*b", "axb"); !got {
		t.Errorf("MatchWildcard(%q, %q) = false, want true", "a*b", "axb")
	}
	// Pattern ending in '*' -> empty trailing part -> matches once prefix holds.
	if got := MatchWildcard("ab*", "abc"); !got {
		t.Errorf("MatchWildcard(%q, %q) = false, want true", "ab*", "abc")
	}
}

// TestGkVibekitU27_ShellFields_MultipleTokens kills two NEGATION mutants:
//   - shell_fields.go:10 (`i < len(s)` in the inter-token whitespace skip):
//     flipping to `i >= len(s)` stops skipping whitespace, so the scanner
//     wedges on the space and the no-progress guard truncates the result.
//   - shell_fields.go:26 (`i == prev` no-progress guard): flipping to
//     `i != prev` breaks after the first progressing iteration.
// In both cases a multi-token command collapses to its first token.
func TestGkVibekitU27_ShellFields_MultipleTokens(t *testing.T) {
	gk_vibekit_u27_assertFields(t, "a b", ShellFields("a b"), []string{"a", "b"})
	gk_vibekit_u27_assertFields(t, "a b c", ShellFields("a b c"), []string{"a", "b", "c"})
	gk_vibekit_u27_assertFields(t, "  ls", ShellFields("  ls"), []string{"ls"})
}

// TestGkVibekitU27_ShellFields_EmptyQuotedTokenDropped kills the
// CONDITIONALS_BOUNDARY mutant at shell_fields.go:18 (`if len(tok) > 0`).
// An empty quoted span decodes to "" while advancing the scanner; `> 0`
// drops the empty token, `>= 0` would append it.
func TestGkVibekitU27_ShellFields_EmptyQuotedTokenDropped(t *testing.T) {
	gk_vibekit_u27_assertFields(t, `""`, ShellFields(`""`), []string{})
	gk_vibekit_u27_assertFields(t, `x ""`, ShellFields(`x ""`), []string{"x"})
}

// TestGkVibekitU27_ShellFields_SingleQuoteOffset kills the ARITHMETIC_BASE
// mutant at shell_fields.go:42 (`i+1` opening-quote skip in the single-quote
// branch of scanToken). `i-1` re-reads the character before the quote, which
// duplicates characters: "a'b'" decodes to "ab" but mutates to "aabb".
func TestGkVibekitU27_ShellFields_SingleQuoteOffset(t *testing.T) {
	gk_vibekit_u27_assertFields(t, "a'b'", ShellFields("a'b'"), []string{"ab"})
}
