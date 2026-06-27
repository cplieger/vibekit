package forges

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// FuzzLoadGHHosts feeds arbitrary bytes through the gh hosts.yml parser
// and asserts no panic. If parsing succeeds, a round-trip through
// marshal+load must produce equivalent data.
func FuzzLoadGHHosts(f *testing.F) {
	f.Add([]byte("github.com:\n    oauth_token: tok\n    user: alice\n    git_protocol: https\n"))
	f.Add([]byte(""))
	f.Add([]byte("host:\n"))
	f.Add([]byte("host.example.com:\n    oauth_token: tok123\n"))
	f.Add([]byte("a:\n    user: u\nb:\n    oauth_token: t\n    git_protocol: ssh\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "hosts.yml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		hosts, err := loadGHHosts(path)
		if err != nil {
			return // parse failure is fine, just no panic
		}
		// Normalize: marshal fills empty GitProtocol with "https".
		for k, e := range hosts {
			if e.GitProtocol == "" {
				e.GitProtocol = "https"
				hosts[k] = e
			}
		}
		// Round-trip: marshal then reload must produce the same map.
		marshaled := marshalGHHosts(hosts)
		rtPath := filepath.Join(tmp, "hosts-rt.yml")
		if err := os.WriteFile(rtPath, []byte(marshaled), 0o600); err != nil {
			t.Fatal(err)
		}
		hosts2, err := loadGHHosts(rtPath)
		if err != nil {
			t.Fatalf("round-trip load failed: %v", err)
		}
		if !reflect.DeepEqual(hosts, hosts2) {
			t.Errorf("round-trip mismatch:\n  original: %+v\n  reloaded: %+v", hosts, hosts2)
		}
	})
}

// FuzzLoadGLabConfig feeds arbitrary bytes through the glab config parser
// and asserts no panic. If parsing succeeds, a round-trip through
// marshal+load must produce equivalent data.
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
		// Normalize: marshal fills empty Protocol with "https".
		for k, e := range cfg.Hosts {
			if e.Protocol == "" {
				e.Protocol = "https"
				cfg.Hosts[k] = e
			}
		}
		marshaled := marshalGLabConfig(cfg)
		rtPath := filepath.Join(tmp, "config-rt.yml")
		if err := os.WriteFile(rtPath, []byte(marshaled), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg2, err := loadGLabConfig(rtPath)
		if err != nil {
			t.Fatalf("round-trip load failed: %v", err)
		}
		if !reflect.DeepEqual(cfg.Hosts, cfg2.Hosts) {
			t.Errorf("round-trip mismatch:\n  original: %+v\n  reloaded: %+v", cfg.Hosts, cfg2.Hosts)
		}
	})
}

// FuzzLoadTeaConfig feeds arbitrary bytes through the tea config parser
// and asserts no panic. If parsing succeeds, a round-trip through
// marshal+load must produce equivalent data.
func FuzzLoadTeaConfig(f *testing.F) {
	f.Add([]byte("logins:\n    - name: codeberg.org\n      url: https://codeberg.org\n      token: tok\n      user: alice\n      default: true\n"))
	f.Add([]byte(""))
	f.Add([]byte("logins:\n"))
	f.Add([]byte("logins:\n    - name: gitea.local\n      url: https://gitea.local\n      token: abc\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "config.yml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadTeaConfig(path)
		if err != nil {
			return
		}
		marshaled := marshalTeaConfig(cfg)
		rtPath := filepath.Join(tmp, "config-rt.yml")
		if err := os.WriteFile(rtPath, []byte(marshaled), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg2, err := loadTeaConfig(rtPath)
		if err != nil {
			t.Fatalf("round-trip load failed: %v", err)
		}
		if !reflect.DeepEqual(cfg.Logins, cfg2.Logins) {
			t.Errorf("round-trip mismatch:\n  original: %+v\n  reloaded: %+v", cfg.Logins, cfg2.Logins)
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
