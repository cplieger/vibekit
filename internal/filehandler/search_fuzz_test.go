package filehandler

import (
	"path"
	"strings"
	"testing"
)

// FuzzMatchGlob pins the search filter's stated convention across arbitrary
// patterns and paths: a pattern holding no "/" is matched against the BASENAME,
// and one holding a "/" against the whole path the caller passed, which for the
// search is the path under the folder searched. The convention is
// the whole reason this function exists rather than a bare path.Match call (whose
// `*` does not cross "/"), so the property is the convention itself rather than a
// no-panic check: the oracle re-derives the subject independently and the two
// answers must agree, which catches an inverted condition, a dropped Base, or a
// swallowed pattern error.
func FuzzMatchGlob(f *testing.F) {
	seeds := []struct{ pattern, rel string }{
		{"*.go", "src/a.go"},
		{"*.go", "a.go"},
		{"src/*.go", "src/a.go"},
		{"src/*.go", "src/deep/a.go"},
		{"node_modules", "node_modules"},
		{"node_modules", "a/node_modules"},
		{"", ""},
		{"[a-", "a"},          // malformed: must not match, must not panic
		{"*", "a/b/c"},        // basename form, so it matches
		{"/*", "/a"},          // separator form on an absolute-looking rel
		{"a/**/b", "a/x/y/b"}, // no ** support: path.Match treats each * separately
		{"\x00", "\x00"},
		{"é*", "src/één.go"},
		{"**", "a/b"},
	}
	for _, s := range seeds {
		f.Add(s.pattern, s.rel)
	}

	f.Fuzz(func(t *testing.T, pattern, rel string) {
		got := matchGlob(pattern, rel)

		// The oracle: pick the subject by the documented rule, then ask
		// path.Match directly. A pattern path.Match rejects can never match.
		subject := rel
		if !strings.Contains(pattern, "/") {
			subject = path.Base(rel)
		}
		want, err := path.Match(pattern, subject)
		if err != nil {
			want = false
		}
		if got != want {
			t.Fatalf("matchGlob(%q, %q) = %v, want %v (subject %q)", pattern, rel, got, want, subject)
		}

		// A separator-free pattern must never depend on anything but the
		// basename: two paths sharing a basename must get the same answer, which
		// is what makes `*.go` reach every depth.
		if !strings.Contains(pattern, "/") {
			base := path.Base(rel)
			if matchGlob(pattern, base) != got {
				t.Fatalf("matchGlob(%q, %q) = %v but matchGlob(%q, %q) = %v: a "+
					"separator-free pattern must read the basename only",
					pattern, rel, got, pattern, base, matchGlob(pattern, base))
			}
		}
	})
}

// FuzzParseGlobs pins the pattern-list parser: every pattern it RETURNS must be
// one path.Match accepts (a malformed pattern is an error, never a silently
// unmatchable entry), none may be empty or carry surrounding space, and the
// comma split must not invent patterns the input did not contain.
func FuzzParseGlobs(f *testing.F) {
	f.Add("*.go,*.md")
	f.Add("  *.go  ,, ,*.md")
	f.Add("")
	f.Add(",")
	f.Add("[a-")
	f.Add("src/*.go")
	f.Add("\x00,*")
	f.Add(strings.Repeat("a,", 64))

	f.Fuzz(func(t *testing.T, raw string) {
		patterns, err := parseGlobs([]string{raw})
		if err != nil {
			if len(patterns) != 0 {
				t.Fatalf("parseGlobs(%q) returned %d patterns alongside an error", raw, len(patterns))
			}
			return
		}
		for _, p := range patterns {
			if p == "" {
				t.Fatalf("parseGlobs(%q) kept an empty pattern", raw)
			}
			if strings.TrimSpace(p) != p {
				t.Fatalf("parseGlobs(%q) kept unTRIMmed pattern %q", raw, p)
			}
			if _, mErr := path.Match(p, ""); mErr != nil {
				t.Fatalf("parseGlobs(%q) kept malformed pattern %q", raw, p)
			}
			if !strings.Contains(raw, p) {
				t.Fatalf("parseGlobs(%q) invented pattern %q", raw, p)
			}
		}
	})
}
