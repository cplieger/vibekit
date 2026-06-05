package mcp

import "testing"

// FuzzParseTransport exercises transport string parsing with arbitrary
// input. Asserts no panic and that accepted values produce a valid
// transport.
func FuzzParseTransport(f *testing.F) {
	f.Add("stdio")
	f.Add("http")
	f.Add("sse")
	f.Add("")
	f.Add("grpc")
	f.Add("STDIO")

	f.Fuzz(func(t *testing.T, raw string) {
		tr, err := ParseTransport(raw)
		if err != nil {
			return
		}
		if !tr.Valid() {
			t.Errorf("ParseTransport(%q) returned invalid transport %q", raw, tr)
		}
	})
}
