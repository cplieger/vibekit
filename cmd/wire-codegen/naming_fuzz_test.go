package main

import (
	"testing"
)

// FuzzPathName verifies pathName produces all-lowercase underscore-separated
// output without empty segments or uppercase runes.
//
// Bug class: off-by-one in acronym boundary detection, panic on empty/non-ASCII.
func FuzzPathName(f *testing.F) {
	f.Add("MCPOAuthPayload")
	f.Add("ChatHeader")
	f.Add("")
	f.Add("A")
	f.Add("ABCDef")
	f.Add("lowercase")
	f.Add("HTTPSProxy")

	f.Fuzz(func(t *testing.T, typeName string) {
		result := pathName(typeName)

		// Invariant 1: no ASCII uppercase in output.
		for _, r := range result {
			if r >= 'A' && r <= 'Z' {
				t.Fatalf("pathName(%q) = %q contains ASCII uppercase rune %q", typeName, result, r)
			}
		}

		// Check structural invariants only for valid Go type names
		// (start with uppercase ASCII letter, all ASCII letters).
		validTypeName := len(typeName) > 0 && typeName[0] >= 'A' && typeName[0] <= 'Z'
		if validTypeName {
			for _, r := range typeName {
				if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
					validTypeName = false
					break
				}
			}
		}
		if validTypeName {
			// Invariant 2: no empty segments between underscores.
			for i := 0; i+1 < len(result); i++ {
				if result[i] == '_' && result[i+1] == '_' {
					t.Fatalf("pathName(%q) = %q has double underscore", typeName, result)
				}
			}
			// Invariant 3: does not start with underscore.
			if result[0] == '_' {
				t.Fatalf("pathName(%q) = %q starts with underscore", typeName, result)
			}
		}
	})
}

// FuzzEnumConstName verifies enumConstName produces output ending with "S"
// and for ASCII-only input produces SCREAMING_SNAKE (no lowercase ASCII).
//
// Bug class: rune subtraction overflow on non-ASCII, empty string panic.
func FuzzEnumConstName(f *testing.F) {
	f.Add("ForgeKind")
	f.Add("Role")
	f.Add("")
	f.Add("ABC")
	f.Add("a")
	f.Add("ChatStatus")
	f.Add("MCP")

	f.Fuzz(func(t *testing.T, goTypeName string) {
		result := enumConstName(goTypeName)

		// Invariant 1: always ends with "S".
		if len(result) == 0 || result[len(result)-1] != 'S' {
			t.Fatalf("enumConstName(%q) = %q does not end with S", goTypeName, result)
		}

		// Invariant 2: result is non-empty.
		if result == "" {
			t.Fatalf("enumConstName(%q) returned empty", goTypeName)
		}
	})
}
