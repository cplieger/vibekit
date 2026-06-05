package server

import "testing"

// FuzzReqIDOrNew checks that reqIDOrNew always returns an ID that
// itself passes validReqID.
//
// Bug class: validation bypass via attacker-crafted inbound request IDs.
// If reqIDOrNew were to return an unvalidated input verbatim under any
// circumstance, downstream code that trusts the request ID format
// (logs, metrics, headers) could be confused or injected.
func FuzzReqIDOrNew(f *testing.F) {
	f.Add("")
	f.Add("abc-123")
	f.Add("A_B_C")
	f.Add("with space")
	f.Add("with/slash")
	f.Add("withnewline\n")
	f.Add(string(make([]byte, 65))) // > maxLen
	f.Add(";rm -rf /")

	f.Fuzz(func(t *testing.T, in string) {
		out := reqIDOrNew(in)
		if !validReqID(out) {
			t.Fatalf("reqIDOrNew(%q) = %q; not validReqID", in, out)
		}
		if validReqID(in) && out != in {
			t.Fatalf("reqIDOrNew dropped valid input: %q -> %q", in, out)
		}
	})
}
