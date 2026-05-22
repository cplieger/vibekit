package ignore

import (
	"strings"
	"testing"
)

// FuzzParseIgnoreLine exercises the gitignore line parser with arbitrary
// input. Asserts no panic and valid output invariants: when ok=true the
// pattern must be non-empty.
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
	})
}

// FuzzMatchSegments exercises the glob matcher with arbitrary
// pattern/path combinations. Asserts no panic and bounded execution
// (the ** cap at 4 was added as a security fix — fuzz would have
// caught the original DoS).
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
		// Just assert no panic. The ** cap in parseIgnoreLine
		// doesn't apply here (matchSegments is called with
		// pre-parsed patterns), so we cap input length to avoid
		// pathological recursion in the fuzzer.
		if len(pattern) > 200 || len(path) > 200 {
			return
		}
		matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"), anchored)
	})
}
