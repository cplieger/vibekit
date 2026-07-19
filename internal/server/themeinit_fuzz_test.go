package server

import (
	"regexp"
	"testing"
)

var sha256Re = regexp.MustCompile(`^'sha256-[A-Za-z0-9+/=]+'$`)

// FuzzThemeInitHashToken fuzzes the one remaining inline-script CSP hash
// extractor (the anti-FOUC theme-init block; the importmap block died with
// the pre-bundler pipeline). Invariants: never panics, a nil error implies
// a well-formed CSP sha256 token, and extraction is deterministic.
func FuzzThemeInitHashToken(f *testing.F) {
	f.Add([]byte(`<html><script data-theme-init>(function(){})();</script></html>`))
	f.Add([]byte(`<script data-theme-init>
var x = 1;
</script>`))
	f.Add([]byte(`no script here`))
	f.Add([]byte(""))
	f.Add([]byte("\x00\x01\x02"))

	f.Fuzz(func(t *testing.T, html []byte) {
		result, err := themeInitHashToken(html)

		// Never panics (implicit).

		// If err == nil, output matches sha256 format.
		if err == nil {
			if !sha256Re.MatchString(result) {
				t.Errorf("themeInitHashToken: result %q doesn't match sha256 format", result)
			}
		}

		// Deterministic: same input → same output.
		result2, err2 := themeInitHashToken(html)
		if result != result2 || (err == nil) != (err2 == nil) {
			t.Errorf("themeInitHashToken not deterministic: %q/%v vs %q/%v", result, err, result2, err2)
		}
	})
}
