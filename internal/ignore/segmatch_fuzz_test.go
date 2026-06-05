package ignore

import (
	"strings"
	"testing"
)

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
