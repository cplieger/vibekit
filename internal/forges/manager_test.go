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

// TestManagerList_SortsByKindThenHost pins List's ordering contract: forges
// are ordered by Kind first, then Host as a tiebreaker. Two GitHub hosts
// exercise the same-kind Host tiebreaker; a GitLab login whose host name sorts
// before both GitHub hosts proves the primary sort key is Kind, not Host. A
// comparator that sorted by Host alone, reversed the Kind ordering, or
// reversed the Host tiebreaker would each reorder the result observably.
func TestManagerList_SortsByKindThenHost(t *testing.T) {
	setConfigHomeTemp(t)
	if err := writeGHHosts("bbb.example", "t1", "alice"); err != nil {
		t.Fatalf("seed gh bbb: %v", err)
	}
	if err := writeGHHosts("ccc.example", "t2", "bob"); err != nil {
		t.Fatalf("seed gh ccc: %v", err)
	}
	if err := writeGLabConfig("aaa.example", "t3", "carol"); err != nil {
		t.Fatalf("seed glab aaa: %v", err)
	}

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
