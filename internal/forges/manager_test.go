package forges

import (
	"context"
	"testing"
)

// TestManagerList_AggregatesConfiguredForges verifies List reads every CLI's
// own config file and classifies each connected forge, including the
// Codeberg-vs-Gitea distinction tea logins fall under (codeberg.org logins
// are Codeberg, every other tea login is Gitea).
func TestManagerList_AggregatesConfiguredForges(t *testing.T) {
	setConfigHomeTemp(t)
	if err := writeGHHosts("github.com", "gho_tok", "alice"); err != nil {
		t.Fatalf("seed gh: %v", err)
	}
	if err := writeGLabConfig("gitlab.com", "glpat-tok", "bob"); err != nil {
		t.Fatalf("seed glab: %v", err)
	}
	if err := writeTeaConfig("codeberg.org", "cb_tok", "carol"); err != nil {
		t.Fatalf("seed codeberg: %v", err)
	}
	if err := writeTeaConfig("gitea.example", "gt_tok", "dave"); err != nil {
		t.Fatalf("seed gitea: %v", err)
	}

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
		{"gitea.example", KindGitea, "dave"},
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
		if !f.Connected {
			t.Errorf("host %q should be Connected", c.host)
		}
	}
}

// TestManagerList_SkipsTokenlessEntries verifies a config entry with no token
// is treated as not-configured and omitted from the aggregated list.
func TestManagerList_SkipsTokenlessEntries(t *testing.T) {
	tmp := setConfigHomeTemp(t)
	if err := writeGHHosts("github.com", "gho_tok", "alice"); err != nil {
		t.Fatalf("seed gh: %v", err)
	}
	// Hand-write a gh host with no oauth_token: it must be skipped.
	if err := writeYAML(tmp+"/gh/hosts.yml",
		"github.com:\n    oauth_token: gho_tok\n    user: alice\n    git_protocol: https\n"+
			"tokenless.example:\n    user: nobody\n    git_protocol: https\n"); err != nil {
		t.Fatalf("seed tokenless: %v", err)
	}

	list := NewManager().List(context.Background())

	for _, f := range list {
		if f.Host == "tokenless.example" {
			t.Errorf("tokenless host should be skipped, got %+v", f)
		}
	}
	var hasGitHub bool
	for _, f := range list {
		if f.Host == "github.com" {
			hasGitHub = true
		}
	}
	if !hasGitHub {
		t.Errorf("github.com with a token should be listed: %+v", list)
	}
}
