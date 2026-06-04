package server

import (
	"regexp"
	"testing"
)

var sha256Re = regexp.MustCompile(`^'sha256-[A-Za-z0-9+/=]+'$`)

func FuzzImportMapHashToken(f *testing.F) {
	f.Add([]byte(`<html><script type="importmap">{"imports":{}}</script></html>`))
	f.Add([]byte(`<script type="importmap">
{
  "imports": { "foo": "./foo.js" }
}
</script>`))
	f.Add([]byte(`no script here`))
	f.Add([]byte(""))
	f.Add([]byte("\x00\x01\x02"))

	f.Fuzz(func(t *testing.T, html []byte) {
		result, err := importMapHashToken(html)

		// Never panics (implicit).

		// If err == nil, output matches sha256 format.
		if err == nil {
			if !sha256Re.MatchString(result) {
				t.Errorf("importMapHashToken: result %q doesn't match sha256 format", result)
			}
		}

		// Deterministic: same input → same output.
		result2, err2 := importMapHashToken(html)
		if result != result2 || (err == nil) != (err2 == nil) {
			t.Errorf("importMapHashToken not deterministic: %q/%v vs %q/%v", result, err, result2, err2)
		}
	})
}
