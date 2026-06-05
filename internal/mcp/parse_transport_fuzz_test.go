package mcp

import "testing"

func FuzzParseTransport(f *testing.F) {
	f.Add("stdio")
	f.Add("http")
	f.Add("sse")
	f.Add("")
	f.Add("STDIO")
	f.Add("unknown")

	f.Fuzz(func(t *testing.T, s string) {
		tr, err := ParseTransport(s)
		if err != nil {
			return
		}
		if !tr.Valid() {
			t.Fatalf("ParseTransport(%q) returned invalid transport %q", s, tr)
		}
		if s == "sse" && tr != TransportHTTP {
			t.Fatalf("sse must map to TransportHTTP, got %q", tr)
		}
	})
}
