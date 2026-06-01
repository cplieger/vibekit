package forges

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// FuzzLoadGHHosts feeds arbitrary bytes through the gh hosts.yml parser
// and asserts no panic. If parsing succeeds, a round-trip through
// marshal+load must produce equivalent data.
func FuzzLoadGHHosts(f *testing.F) {
	f.Add([]byte("github.com:\n    oauth_token: tok\n    user: alice\n    git_protocol: https\n"))
	f.Add([]byte(""))
	f.Add([]byte("host:\n"))

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
