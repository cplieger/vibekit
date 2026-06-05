package forges

import (
	"testing"
)

// FuzzGHHostsRoundtrip verifies the asymmetry invariant:
// marshalGHHosts(loadGHHosts(data)) parsed again must yield the same
// map as the first parse. This detects lossy marshal/unmarshal cycles
// that could corrupt credential storage.
func FuzzGHHostsRoundtrip(f *testing.F) {
	f.Add("github.com:\n    oauth_token: gho_abc\n    user: alice\n    git_protocol: https\n")
	f.Add("")
	f.Add("host.example.com:\n    oauth_token: tok123\n")
	f.Add("a:\n    user: u\nb:\n    oauth_token: t\n    git_protocol: ssh\n")

	f.Fuzz(func(t *testing.T, data string) {
		// Write to a temp file, parse, marshal, re-parse.
		path := t.TempDir() + "/hosts.yml"
		if err := writeYAML(path, data); err != nil {
			return
		}
		hosts1, err := loadGHHosts(path)
		if err != nil {
			return
		}
		marshaled := marshalGHHosts(hosts1)
		path2 := t.TempDir() + "/hosts2.yml"
		if err := writeYAML(path2, marshaled); err != nil {
			t.Fatalf("writeYAML roundtrip: %v", err)
		}
		hosts2, err := loadGHHosts(path2)
		if err != nil {
			t.Fatalf("loadGHHosts roundtrip: %v", err)
		}
		// Compare: every entry in hosts1 must exist in hosts2 with
		// the same values. Empty tokens are dropped during marshal,
		// so only compare entries with non-empty tokens.
		for host, e1 := range hosts1 {
			if e1.OAuthToken == "" {
				continue
			}
			e2, ok := hosts2[host]
			if !ok {
				t.Errorf("host %q lost after roundtrip", host)
				continue
			}
			if e1.OAuthToken != e2.OAuthToken {
				t.Errorf("host %q token: got %q, want %q", host, e2.OAuthToken, e1.OAuthToken)
			}
			if e1.User != e2.User {
				t.Errorf("host %q user: got %q, want %q", host, e2.User, e1.User)
			}
			// git_protocol defaults to "https" on marshal when empty.
			gp1 := e1.GitProtocol
			if gp1 == "" {
				gp1 = "https"
			}
			gp2 := e2.GitProtocol
			if gp2 == "" {
				gp2 = "https"
			}
			if gp1 != gp2 {
				t.Errorf("host %q git_protocol: got %q, want %q", host, gp2, gp1)
			}
		}
	})
}

// FuzzGLabConfigRoundtrip verifies glab config parse/marshal roundtrip.
func FuzzGLabConfigRoundtrip(f *testing.F) {
	f.Add("hosts:\n    gitlab.com:\n        token: glpat-abc\n        user: bob\n        git_protocol: https\n        api_host: gitlab.com\n")
	f.Add("")
	f.Add("editor: vim\nhosts:\n    self.host:\n        token: tk\n")

	f.Fuzz(func(t *testing.T, data string) {
		path := t.TempDir() + "/config.yml"
		if err := writeYAML(path, data); err != nil {
			return
		}
		cfg1, err := loadGLabConfig(path)
		if err != nil {
			return
		}
		marshaled := marshalGLabConfig(cfg1)
		path2 := t.TempDir() + "/config2.yml"
		if err := writeYAML(path2, marshaled); err != nil {
			t.Fatalf("writeYAML roundtrip: %v", err)
		}
		cfg2, err := loadGLabConfig(path2)
		if err != nil {
			t.Fatalf("loadGLabConfig roundtrip: %v", err)
		}
		for host, e1 := range cfg1.Hosts {
			if e1.Token == "" {
				continue
			}
			e2, ok := cfg2.Hosts[host]
			if !ok {
				t.Errorf("host %q lost after roundtrip", host)
				continue
			}
			if e1.Token != e2.Token {
				t.Errorf("host %q token mismatch", host)
			}
			if e1.User != e2.User {
				t.Errorf("host %q user mismatch", host)
			}
		}
	})
}

// FuzzTeaConfigRoundtrip verifies tea config parse/marshal roundtrip.
func FuzzTeaConfigRoundtrip(f *testing.F) {
	f.Add("logins:\n    - name: codeberg.org\n      url: https://codeberg.org\n      token: tok1\n      user: alice\n      default: true\n")
	f.Add("")
	f.Add("logins:\n    - name: gitea.local\n      url: https://gitea.local\n      token: abc\n")

	f.Fuzz(func(t *testing.T, data string) {
		path := t.TempDir() + "/config.yml"
		if err := writeYAML(path, data); err != nil {
			return
		}
		cfg1, err := loadTeaConfig(path)
		if err != nil {
			return
		}
		marshaled := marshalTeaConfig(cfg1)
		path2 := t.TempDir() + "/config2.yml"
		if err := writeYAML(path2, marshaled); err != nil {
			t.Fatalf("writeYAML roundtrip: %v", err)
		}
		cfg2, err := loadTeaConfig(path2)
		if err != nil {
			t.Fatalf("loadTeaConfig roundtrip: %v", err)
		}
		if len(cfg1.Logins) != len(cfg2.Logins) {
			// Logins with empty names may be dropped; only compare
			// named entries.
			named1 := 0
			for _, l := range cfg1.Logins {
				if l.Name != "" {
					named1++
				}
			}
			named2 := 0
			for _, l := range cfg2.Logins {
				if l.Name != "" {
					named2++
				}
			}
			if named1 != named2 {
				t.Errorf("login count: got %d, want %d", named2, named1)
			}
		}
		for i, l1 := range cfg1.Logins {
			if l1.Name == "" {
				continue
			}
			if i >= len(cfg2.Logins) {
				break
			}
			l2 := cfg2.Logins[i]
			if l1.Name != l2.Name {
				t.Errorf("login[%d] name: got %q, want %q", i, l2.Name, l1.Name)
			}
			if l1.Token != l2.Token {
				t.Errorf("login[%d] token mismatch", i)
			}
		}
	})
}
