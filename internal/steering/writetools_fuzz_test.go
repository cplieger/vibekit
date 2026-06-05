package steering

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzWriteTools verifies writeTools never panics on arbitrary JSON bytes and
// produces valid UTF-8 output.
//
// Bug class: panic on malformed JSON, type assertion failure on unexpected
// map entry types, unbounded slice growth.
func FuzzWriteTools(f *testing.F) {
	f.Add([]byte(`{"runtimes":{"node":{"version":"20.1"}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`invalid json`))
	f.Add([]byte(`{"npm":{"pkg":{"version":"1.0","binaries":["a","b"]}}}`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, data []byte) {
		var b strings.Builder
		writeTools(&b, data)
		result := b.String()

		// Invariant 1: output is valid UTF-8.
		if !utf8.ValidString(result) {
			t.Fatalf("writeTools produced invalid UTF-8 for input %q", data)
		}

		// Invariant 2: if output is non-empty, starts with the header.
		if result != "" && !strings.HasPrefix(result, "## Installed tools\n") {
			t.Fatalf("writeTools output doesn't start with expected header: %q", result[:min(50, len(result))])
		}
	})
}
