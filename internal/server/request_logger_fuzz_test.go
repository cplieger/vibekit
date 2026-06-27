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

// FuzzValidReqID verifies validReqID accepts only alphanumeric + dash/underscore
// strings of length 1-64, rejecting all other inputs.
//
// Bug class: validation bypass via multi-byte UTF-8 runes that appear as allowed
// ASCII bytes, off-by-one in length check.
func FuzzValidReqID(f *testing.F) {
	f.Add("abc-123")
	f.Add("A_B_C")
	f.Add("")
	f.Add("x")
	f.Add("with space")
	f.Add("\x00null")
	f.Add("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1") // 65 chars

	f.Fuzz(func(t *testing.T, s string) {
		got := validReqID(s)

		if got {
			// Invariant 1: length in [1, 64].
			if len(s) < 1 || len(s) > 64 {
				t.Fatalf("validReqID(%q)=true but len=%d", s, len(s))
			}
			// Invariant 2: all runes in allowed set.
			for _, r := range s {
				switch {
				case r >= 'a' && r <= 'z':
				case r >= 'A' && r <= 'Z':
				case r >= '0' && r <= '9':
				case r == '-' || r == '_':
				default:
					t.Fatalf("validReqID(%q)=true but contains %q", s, r)
				}
			}
		}
	})
}
