package server

import "testing"

// FuzzValidToolNameServer verifies the server package's validToolName returns
// true only for strings matching [a-zA-Z0-9._\-+@]{1,80}.
//
// Bug class: charset bypass via multi-byte UTF-8 runes that appear as valid
// ASCII bytes, length confusion between bytes and runes.
func FuzzValidToolNameServer(f *testing.F) {
	f.Add("my-tool")
	f.Add("tool.v2")
	f.Add("")
	f.Add("a+b@c")
	f.Add("name with space")
	f.Add("tool;inject")
	f.Add("\x00hidden")

	f.Fuzz(func(t *testing.T, name string) {
		got := validToolName(name)

		if got {
			// Invariant 1: non-empty, max 80 bytes.
			if len(name) == 0 || len(name) > 80 {
				t.Fatalf("validToolName(%q)=true but len=%d", name, len(name))
			}
			// Invariant 2: every rune is in the allowed set.
			for _, r := range name {
				switch {
				case r >= 'a' && r <= 'z':
				case r >= 'A' && r <= 'Z':
				case r >= '0' && r <= '9':
				case r == '.' || r == '-' || r == '_' || r == '+' || r == '@':
				default:
					t.Fatalf("validToolName(%q)=true but contains %q", name, r)
				}
			}
		}

		// Invariant 3: idempotent.
		if validToolName(name) != got {
			t.Fatalf("validToolName(%q) not idempotent", name)
		}
	})
}
