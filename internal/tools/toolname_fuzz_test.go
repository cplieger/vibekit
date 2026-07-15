package tools

import (
	"strings"
	"testing"
)

// FuzzValidToolName verifies validToolName returns true only for
// strings of [a-zA-Z0-9._\-+@]{1,80}, plus at most one '/' and only in
// @scope/name form (npm scoped packages).
//
// Bug class: charset bypass via multi-byte UTF-8 runes that appear as
// valid ASCII bytes, length confusion between bytes and runes, slash
// smuggling into filesystem paths.
func FuzzValidToolName(f *testing.F) {
	f.Add("my-tool")
	f.Add("tool.v2")
	f.Add("")
	f.Add("a+b@c")
	f.Add("@scope/name")
	f.Add("scope/name")
	f.Add("@a/b/c")
	f.Add("name with space")
	f.Add("tool;inject")
	f.Add("\x00hidden")
	f.Add("../escape")

	f.Fuzz(func(t *testing.T, name string) {
		got := validToolName(name)

		if got {
			// Invariant 1: non-empty, max 80 bytes.
			if len(name) == 0 || len(name) > 80 {
				t.Fatalf("validToolName(%q)=true but len=%d", name, len(name))
			}
			// Invariant 2: every rune is in the allowed set.
			slashes := 0
			for _, r := range name {
				switch {
				case r >= 'a' && r <= 'z':
				case r >= 'A' && r <= 'Z':
				case r >= '0' && r <= '9':
				case r == '.' || r == '-' || r == '_' || r == '+' || r == '@':
				case r == '/':
					slashes++
				default:
					t.Fatalf("validToolName(%q)=true but contains %q", name, r)
				}
			}
			// Invariant 3: at most one slash, only in @-scoped names.
			if slashes > 1 {
				t.Fatalf("validToolName(%q)=true with %d slashes", name, slashes)
			}
			if slashes == 1 && !strings.HasPrefix(name, "@") {
				t.Fatalf("validToolName(%q)=true with unscoped slash", name)
			}
		}

		// Invariant 4: idempotent.
		if validToolName(name) != got {
			t.Fatalf("validToolName(%q) not idempotent", name)
		}
	})
}
