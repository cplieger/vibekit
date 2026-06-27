package eval

import (
	"slices"
	"testing"
)

// assertFields fails the test if ShellFields(in) did not return exactly want.
func assertFields(t *testing.T, in string, want []string) {
	t.Helper()
	got := ShellFields(in)
	if !slices.Equal(got, want) {
		t.Errorf("ShellFields(%q) = %v, want %v", in, got, want)
	}
}

func TestShellFields_SplitsMultipleTokens(t *testing.T) {
	// Inter-token whitespace is skipped and every token is emitted, so a
	// multi-word command never collapses to its first token.
	assertFields(t, "a b", []string{"a", "b"})
	assertFields(t, "a b c", []string{"a", "b", "c"})
	// Leading whitespace is skipped before the first token.
	assertFields(t, "  ls", []string{"ls"})
}

func TestShellFields_DropsEmptyQuotedToken(t *testing.T) {
	// An empty quoted span decodes to "" while still advancing the
	// scanner past both quotes; the empty token is dropped, not emitted.
	assertFields(t, `""`, nil)
	assertFields(t, `x ""`, []string{"x"})
}

func TestShellFields_DecodesQuoteAttachedToToken(t *testing.T) {
	// A quote opening mid-token contributes only its contents: the opening
	// quote is skipped (not re-read), so "a'b'" decodes to "ab", not "aabb".
	assertFields(t, "a'b'", []string{"ab"})
}
