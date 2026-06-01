package eval

import (
	"strings"
	"testing"
)

func FuzzMatchWildcard(f *testing.F) {
	f.Add("ls", "ls")
	f.Add("*", "anything")
	f.Add("npm *", "npm install")
	f.Add("a*b", "axb")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, pattern, command string) {
		// Must not panic.
		result := MatchWildcard(pattern, command)

		// If pattern has no '*', MatchWildcard should behave like exact match.
		if !strings.Contains(pattern, "*") {
			if result != (pattern == command) {
				t.Errorf("MatchWildcard(%q, %q) = %v; no '*' so expected exact match = %v",
					pattern, command, result, pattern == command)
			}
		}

		// Universal match: "*" matches everything.
		if pattern == "*" && !result {
			t.Errorf("MatchWildcard(\"*\", %q) = false, want true", command)
		}

		// Identity: MatchWildcard(s, s) == true when s has no '*'.
		if pattern == command && !strings.Contains(pattern, "*") && !result {
			t.Errorf("MatchWildcard(%q, %q) = false, want true (identity)", pattern, command)
		}
	})
}

func FuzzMatchPattern_MetaGuardInvariant(f *testing.F) {
	f.Add("ls", "ls; rm")
	f.Add("npm *", "npm install && echo done")
	f.Add("git status", "git status")

	f.Fuzz(func(t *testing.T, pattern, command string) {
		// Must not panic.
		result := MatchPattern(pattern, command)

		// MetaGuard invariant: if pattern has no metachar and command
		// has metachar, result must be false.
		if !strings.ContainsAny(pattern, ShellMetacharacters) &&
			strings.ContainsAny(command, ShellMetacharacters) &&
			result {
			t.Errorf("MatchPattern(%q, %q) = true; violates metachar guard", pattern, command)
		}
	})
}
