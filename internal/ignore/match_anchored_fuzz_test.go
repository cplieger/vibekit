package ignore

import (
	"strings"
	"testing"
)

// FuzzMatchSegments_Anchored targets the segMatchBounded budget exhaustion
// path with longer inputs than the existing FuzzMatchSegments (which caps at 200).
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
		// Must not panic and must terminate (bounded by maxSegMatchSteps).
		matchSegments(pSegs, xSegs, true)
	})
}
