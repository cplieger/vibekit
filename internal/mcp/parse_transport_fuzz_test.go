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
		// sse is a first-class transport (no longer folded into http):
		// KAS accepts a distinct {type:"sse"} mcpServers entry on the v3
		// wire, so ParseTransport preserves it as TransportSSE.
		if s == "sse" && tr != TransportSSE {
			t.Fatalf("sse must map to TransportSSE, got %q", tr)
		}
	})
}
