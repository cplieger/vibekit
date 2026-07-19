package forges

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// FuzzLoadGLabConfig feeds arbitrary bytes through the read-only glab
// discovery parser and asserts no panic, deterministic re-parse, and
// no blank host keys. (The marshal round-trip half died with the config
// writers — the parser is discovery-only now; see glab_config.go.)
func FuzzLoadGLabConfig(f *testing.F) {
	f.Add([]byte("hosts:\n    gitlab.com:\n        token: tok\n        user: alice\n        git_protocol: https\n        api_host: gitlab.com\n"))
	f.Add([]byte(""))
	f.Add([]byte("hosts:\n"))
	f.Add([]byte("editor: vim\nhosts:\n    self.host:\n        token: tk\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "config.yml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadGLabConfig(path)
		if err != nil {
			return
		}
		// Invariant 1: the parser never emits a blank host key (blank
		// names clear the current-host cursor instead).
		for k := range cfg.Hosts {
			if k == "" {
				t.Errorf("parser produced a blank host key: %+v", cfg.Hosts)
			}
		}
		// Invariant 2: parsing is deterministic — the same bytes parse
		// to the same map.
		cfg2, err := loadGLabConfig(path)
		if err != nil {
			t.Fatalf("re-parse of identical input failed: %v", err)
		}
		if !reflect.DeepEqual(cfg.Hosts, cfg2.Hosts) {
			t.Errorf("re-parse mismatch:\n  first:  %+v\n  second: %+v", cfg.Hosts, cfg2.Hosts)
		}
	})
}

// FuzzMakeID asserts MakeID always produces a non-empty "kind:host"
// string whose prefix is the requested kind.
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
		if !strings.HasPrefix(id, kind+":") {
			t.Fatalf("MakeID(%q,%q) = %q: prefix mismatch", kind, host, id)
		}
	})
}

// FuzzSplitID checks splitID never panics and matches strings.Cut: a
// no-colon input yields empty kind and host, otherwise the kind/host
// are the pieces before/after the first colon.
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
		// A colon-free id has no kind or host.
		if !strings.Contains(id, ":") {
			if kind != "" || host != "" {
				t.Fatalf("splitID(%q) = (%q, %q): expected empty for no-colon input", id, kind, host)
			}
			return
		}
		// Otherwise kind/host are the halves around the first colon.
		wantKind, wantHost, _ := strings.Cut(id, ":")
		if string(kind) != wantKind {
			t.Fatalf("splitID(%q) kind=%q, want %q", id, kind, wantKind)
		}
		if host != wantHost {
			t.Fatalf("splitID(%q) host=%q, want %q", id, host, wantHost)
		}
	})
}

// FuzzSplitFirst verifies the head/tail split round-trips: when a
// separator is found, head+"/"+tail reconstructs the input; otherwise
// head is the whole input and tail is empty.
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
