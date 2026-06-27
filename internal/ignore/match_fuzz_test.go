package ignore

import (
	"strings"
	"testing"
)

// FuzzParseIgnoreLine exercises the gitignore line parser with arbitrary
// input. Beyond "no panic", it pins the ReDoS security invariant: an
// accepted rule's pattern never carries more than four "**" segments (the
// cap that bounds segMatchBounded's backtracking). A removed or weakened cap
// would let a 5+-"**" line through and trip the assertion.
func FuzzParseIgnoreLine(f *testing.F) {
	// Seed corpus from existing test cases.
	f.Add("node_modules")
	f.Add("/node_modules")
	f.Add("node_modules/")
	f.Add("/node_modules/")
	f.Add("!.env.example")
	f.Add("!/secret")
	f.Add("src/foo")
	f.Add("*.log")
	f.Add("# comment")
	f.Add("")
	f.Add("   ")
	f.Add("**/**/**/**/**/**/target")
	f.Add("build\r")

	f.Fuzz(func(t *testing.T, line string) {
		r, ok := parseIgnoreLine(line)
		if !ok {
			return
		}
		if r.pattern == "" {
			t.Error("parseIgnoreLine returned ok=true with empty pattern")
		}
		if n := strings.Count(r.pattern, "**"); n > 4 {
			t.Errorf("parseIgnoreLine(%q) accepted a pattern with %d '**' segments; cap is 4 (ReDoS bound)", line, n)
		}
	})
}

// FuzzMatchSegments exercises the glob matcher with arbitrary pattern/path
// combinations. Beyond "no panic", it pins a metamorphic invariant:
// unanchored matching is a superset of anchored matching, so any pattern
// that matches when anchored must also match when unanchored.
func FuzzMatchSegments(f *testing.F) {
	// Seed corpus covering key shapes.
	f.Add("node_modules", "node_modules", true)
	f.Add("*.log", "app.log", false)
	f.Add("**/*.go", "src/main.go", true)
	f.Add("src/**", "src/deep/file.txt", true)
	f.Add("foo", "bar/foo", false)
	f.Add("foo", "bar/foo", true)
	f.Add("**/test/**", "a/test/b/c.go", true)
	f.Add("", "anything", true)
	f.Add("a?b", "axb", false)

	f.Fuzz(func(t *testing.T, pattern, path string, anchored bool) {
		// Cap input length to avoid pathological recursion in the fuzzer
		// (the ** cap in parseIgnoreLine doesn't apply to pre-split input).
		if len(pattern) > 200 || len(path) > 200 {
			return
		}
		pSegs := strings.Split(pattern, "/")
		xSegs := strings.Split(path, "/")

		// No panic on the caller-supplied anchoring.
		_ = matchSegments(pSegs, xSegs, anchored)

		// Metamorphic: anchored is a subset of unanchored.
		if matchSegments(pSegs, xSegs, true) && !matchSegments(pSegs, xSegs, false) {
			t.Errorf("matchSegments(%q, %q): anchored matched but unanchored did not (unanchored must be a superset)", pattern, path)
		}
	})
}

// FuzzMatchSegments_Anchored targets the segMatchBounded budget-exhaustion
// path with longer inputs than FuzzMatchSegments (which caps at 200). It
// pins termination (bounded by maxSegMatchSteps) and determinism: the same
// input must always yield the same verdict.
func FuzzMatchSegments_Anchored(f *testing.F) {
	f.Add("**/**/**/**/a", "a/b/c/d/e/f/g/h/i/j/k/l/m/n/o/p/q/r/s/t")
	f.Add("**/x/**/**/y/**", "a/b/c/x/d/e/f/y/g/h")
	f.Add("**/**/**/**/**/**", "a/b/c/d/e/f/g/h/i/j")
	f.Add("a/**/b/**/c/**/d", "a/x/x/x/b/x/x/c/x/d")

	f.Fuzz(func(t *testing.T, pattern, path string) {
		if len(pattern) > 500 || len(path) > 500 {
			return
		}
		pSegs := strings.Split(pattern, "/")
		xSegs := strings.Split(path, "/")
		// Must terminate (bounded by maxSegMatchSteps) and be deterministic.
		r1 := matchSegments(pSegs, xSegs, true)
		r2 := matchSegments(pSegs, xSegs, true)
		if r1 != r2 {
			t.Fatalf("matchSegments(%q, %q, anchored) non-deterministic: %v vs %v", pattern, path, r1, r2)
		}
	})
}

// FuzzSegMatchBounded verifies the bounded backtracking matcher always
// terminates within its step budget and produces deterministic results.
//
// Bug class: ReDoS via pathological ** patterns, step counter bypass.
func FuzzSegMatchBounded(f *testing.F) {
	f.Add("src/**/*.go", "src/pkg/main.go")
	f.Add("**", "any/path")
	f.Add("a/b/c", "a/b/c")
	f.Add("**/node_modules/**", "deep/nested/node_modules/pkg/index.js")
	f.Add("", "")
	f.Add("**/**/**/**", "a/b/c/d/e/f/g")

	f.Fuzz(func(t *testing.T, pattern, path string) {
		pSegs := strings.Split(pattern, "/")
		xSegs := strings.Split(path, "/")

		// Call twice to verify determinism.
		r1 := segMatchBounded(pSegs, xSegs)
		r2 := segMatchBounded(pSegs, xSegs)

		if r1 != r2 {
			t.Fatalf("segMatchBounded(%q, %q) non-deterministic: %v vs %v", pattern, path, r1, r2)
		}
	})
}
