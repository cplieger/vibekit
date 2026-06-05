package forges

import (
	"strings"
	"testing"
)

func FuzzMakeID(f *testing.F) {
	f.Add("github", "github.com")
	f.Add("gitlab", "")
	f.Add("gitea", "codeberg.org")
	f.Add("", "")
	f.Add("unknown", "host.example.com")

	f.Fuzz(func(t *testing.T, kind, host string) {
		id := MakeID(Kind(kind), host)
		if id == "" {
			t.Fatal("MakeID returned empty string")
		}
		if !strings.Contains(id, ":") {
			t.Fatalf("MakeID result missing colon: %q", id)
		}
		// The kind prefix must match input
		if !strings.HasPrefix(id, kind+":") {
			t.Fatalf("MakeID(%q,%q) = %q: prefix mismatch", kind, host, id)
		}
	})
}

func FuzzSplitFirst(f *testing.F) {
	f.Add("api/forges/github:github.com")
	f.Add("noslash")
	f.Add("")
	f.Add("/leading")
	f.Add("a/b/c")

	f.Fuzz(func(t *testing.T, s string) {
		head, tail, found := splitFirst(s)
		if found {
			reconstructed := head + "/" + tail
			if reconstructed != s {
				t.Fatalf("splitFirst(%q): head=%q tail=%q doesn't reconstruct", s, head, tail)
			}
		} else {
			if head != s {
				t.Fatalf("splitFirst(%q): no slash but head=%q != input", s, head)
			}
			if tail != "" {
				t.Fatalf("splitFirst(%q): no slash but tail=%q", s, tail)
			}
		}
	})
}
