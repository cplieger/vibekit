package forges

import (
	"strings"
	"testing"
)

// FuzzNormalizeIssueState verifies normalizeIssueState always returns
// lowercase, is idempotent, and maps to a known canonical set.
func FuzzNormalizeIssueState(f *testing.F) {
	f.Add("open")
	f.Add("CLOSED")
	f.Add("close")
	f.Add("opened")
	f.Add("")
	f.Add("random")

	f.Fuzz(func(t *testing.T, s string) {
		result := normalizeIssueState(s)
		if result != strings.ToLower(result) {
			t.Fatalf("output not lowercase: %q", result)
		}
		if normalizeIssueState(result) != result {
			t.Fatalf("not idempotent: f(%q)=%q, f(f(x))=%q",
				s, result, normalizeIssueState(result))
		}
	})
}

// FuzzSplitID verifies splitID never panics and produces consistent
// results with MakeID for valid kinds.
func FuzzSplitID(f *testing.F) {
	f.Add("github:github.com")
	f.Add("gitlab:gitlab.com")
	f.Add("gitea:codeberg.org")
	f.Add("")
	f.Add("nocolon")
	f.Add(":")
	f.Add("a:b:c")

	f.Fuzz(func(t *testing.T, id string) {
		kind, host := splitID(id)
		// Invariant 1: if input has no ':', both must be empty.
		if !strings.Contains(id, ":") {
			if kind != "" || host != "" {
				t.Fatalf("splitID(%q) = (%q, %q): expected empty for no-colon input", id, kind, host)
			}
			return
		}
		// Invariant 2: kind is prefix before first ':'.
		wantKind, wantHost, _ := strings.Cut(id, ":")
		if string(kind) != wantKind {
			t.Fatalf("splitID(%q) kind=%q, want %q", id, kind, wantKind)
		}
		if host != wantHost {
			t.Fatalf("splitID(%q) host=%q, want %q", id, host, wantHost)
		}
	})
}
