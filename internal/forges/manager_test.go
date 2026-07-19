package forges

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// seedGLabConfig writes a glab config.yml fixture (raw file — glab's
// store is discovered via the read-only parser, the one file-based
// exception; see glab_config.go).
func seedGLabConfig(t *testing.T, configHome, body string) {
	t.Helper()
	writeFixture(t, filepath.Join(configHome, "glab-cli", "config.yml"), body)
}

// writeFixture writes a file creating parents.
func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

// TestManagerList_AggregatesConfiguredForges verifies List discovers
// every CLI's connections through the CLI's own status output (gh
// status JSON, tea list JSON, glab's read-only parser), including the
// Codeberg-vs-Gitea classification by URL host and a tea login whose
// NAME differs from its host.
func TestManagerList_AggregatesConfiguredForges(t *testing.T) {
	tmp := setConfigHomeTemp(t)
	dir := stubPath(t)
	stubCLI(t, dir, "gh",
		`echo '{"hosts":{"github.com":[{"state":"success","active":true,"login":"alice"}]}}'`)
	stubCLI(t, dir, "tea",
		`echo '[{"name":"codeberg.org","url":"https://codeberg.org","user":"carol"},{"name":"myforge","url":"https://gitea.example","user":"dave"}]'`)
	stubCLI(t, dir, "glab", "exit 0")
	seedGLabConfig(t, tmp, "hosts:\n    gitlab.com:\n        token: glpat-tok\n        user: bob\n")

	list := NewManager().List(context.Background())

	byHost := make(map[string]ConfiguredForge, len(list))
	for _, f := range list {
		byHost[f.Host] = f
	}
	if len(byHost) != 4 {
		t.Fatalf("List() returned %d forges, want 4: %+v", len(list), list)
	}
	cases := []struct {
		host string
		kind Kind
		user string
	}{
		{"github.com", KindGitHub, "alice"},
		{"gitlab.com", KindGitLab, "bob"},
		{"codeberg.org", KindCodeberg, "carol"},
		{"gitea.example", KindGitea, "dave"}, // host from URL, not the login name
	}
	for _, c := range cases {
		f, ok := byHost[c.host]
		if !ok {
			t.Errorf("host %q missing from List(): %+v", c.host, list)
			continue
		}
		if f.Kind != c.kind {
			t.Errorf("host %q kind = %q, want %q", c.host, f.Kind, c.kind)
		}
		if f.Username != c.user {
			t.Errorf("host %q username = %q, want %q", c.host, f.Username, c.user)
		}
		if !f.Connected || f.CLIMissing {
			t.Errorf("host %q should be Connected with its CLI present: %+v", c.host, f)
		}
	}
}

// TestManagerList_SkipsTokenlessGLabEntries verifies a glab config
// entry with no token is treated as not-configured.
func TestManagerList_SkipsTokenlessGLabEntries(t *testing.T) {
	tmp := setConfigHomeTemp(t)
	dir := stubPath(t)
	stubCLI(t, dir, "glab", "exit 0")
	seedGLabConfig(t, tmp,
		"hosts:\n    gitlab.com:\n        token: glpat-tok\n        user: alice\n"+
			"    tokenless.example:\n        user: nobody\n")

	list := NewManager().List(context.Background())

	var hasGitLab bool
	for _, f := range list {
		if f.Host == "tokenless.example" {
			t.Errorf("tokenless host should be skipped, got %+v", f)
		}
		if f.Host == "gitlab.com" {
			hasGitLab = true
		}
	}
	if !hasGitLab {
		t.Errorf("gitlab.com with a token should be listed: %+v", list)
	}
}

// TestManagerList_SortsByKindThenHost pins List's ordering contract:
// Kind first, Host as tiebreaker. Two GitHub hosts exercise the
// same-kind tiebreaker; a GitLab host that sorts before both proves the
// primary key is Kind.
func TestManagerList_SortsByKindThenHost(t *testing.T) {
	tmp := setConfigHomeTemp(t)
	dir := stubPath(t)
	stubCLI(t, dir, "gh",
		`echo '{"hosts":{"bbb.example":[{"active":true,"login":"alice"}],"ccc.example":[{"active":true,"login":"bob"}]}}'`)
	stubCLI(t, dir, "glab", "exit 0")
	seedGLabConfig(t, tmp, "hosts:\n    aaa.example:\n        token: t3\n        user: carol\n")

	list := NewManager().List(context.Background())

	got := make([]string, len(list))
	for i, f := range list {
		got[i] = string(f.Kind) + "|" + f.Host
	}
	want := []string{
		"github|bbb.example", // Kind primary: all github before gitlab...
		"github|ccc.example", // ...Host secondary: bbb before ccc
		"gitlab|aaa.example", // gitlab last despite its host sorting first
	}
	if len(got) != len(want) {
		t.Fatalf("List() returned %d forges, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("List()[%d] = %q, want %q (full order: %v)", i, got[i], want[i], got)
		}
	}
}

// TestManagerRefresh_CLIMissingRows verifies the degraded state: a CLI
// binary that is gone (disabled in Settings → Tools, or a fresh tools
// volume against a kept config volume) while its configuration
// survives yields a warning row — kind-level (stat probe) for gh/tea,
// host-level (parser) for glab — never a silent disappearance.
func TestManagerRefresh_CLIMissingRows(t *testing.T) {
	tmp := setConfigHomeTemp(t)
	stubPath(t) // empty PATH: no CLIs at all
	writeFixture(t, filepath.Join(tmp, "gh", "hosts.yml"), "github.com:\n    user: alice\n")
	writeFixture(t, filepath.Join(tmp, "tea", "config.yml"), "logins:\n- name: x\n")
	seedGLabConfig(t, tmp, "hosts:\n    gitlab.com:\n        token: glpat-tok\n        user: bob\n")

	list := NewManager().List(context.Background())

	byID := make(map[string]ConfiguredForge, len(list))
	for _, f := range list {
		byID[f.ID] = f
	}
	if len(byID) != 3 {
		t.Fatalf("List() returned %d rows, want 3: %+v", len(list), list)
	}
	for _, id := range []string{"github:cli-missing", "gitea:cli-missing", "gitlab:gitlab.com"} {
		f, ok := byID[id]
		if !ok {
			t.Errorf("row %q missing: %+v", id, list)
			continue
		}
		if !f.CLIMissing {
			t.Errorf("row %q should be CLIMissing: %+v", id, f)
		}
		if f.Connected {
			t.Errorf("row %q must not report Connected without its CLI: %+v", id, f)
		}
		if f.LastError == "" {
			t.Errorf("row %q should carry the reinstall pointer: %+v", id, f)
		}
	}
	// The glab row keeps its real host (parser-backed discovery).
	if byID["gitlab:gitlab.com"].Host != "gitlab.com" {
		t.Errorf("glab cli-missing row should keep its host: %+v", byID["gitlab:gitlab.com"])
	}
}

// TestManagerRefresh_NoConfigsNoRows verifies a never-connected forge
// stays absent even when its CLI is missing (the stat probe gates the
// cli_missing row on an existing configuration).
func TestManagerRefresh_NoConfigsNoRows(t *testing.T) {
	setConfigHomeTemp(t)
	stubPath(t) // empty PATH, empty config home

	if list := NewManager().List(context.Background()); len(list) != 0 {
		t.Errorf("want empty list, got %+v", list)
	}
}
