package forges

// Round-2 mutant-killing tests for package internal/forges.
// Test-only; identifiers prefixed gk_vibekit_r2_ / TestGKVibekitR2_.

import "testing"

// Kills inject_tea.go:79:23 (CONDITIONALS_NEGATION on
// `c.Logins[i].Name == l.Name` in upsertLogin). With two logins,
// upserting a login whose Name matches the SECOND entry must replace
// that entry in place and leave the first untouched. The `!=` mutant
// matches the FIRST non-matching login instead, overwriting "github"
// and returning before it ever reaches the real match.
func TestGKVibekitR2_TeaConfig_UpsertLoginReplacesMatchInPlace(t *testing.T) {
	c := &teaConfig{Logins: []teaLogin{
		{Name: "github", URL: "https://github.com", Token: "tok-gh"},
		{Name: "gitea", URL: "https://old.example.com", Token: "tok-old"},
	}}

	c.upsertLogin(teaLogin{Name: "gitea", URL: "https://new.example.com", Token: "tok-new"})

	if len(c.Logins) != 2 {
		t.Fatalf("upsertLogin(existing name) changed login count to %d, want 2: %+v", len(c.Logins), c.Logins)
	}
	if c.Logins[0] != (teaLogin{Name: "github", URL: "https://github.com", Token: "tok-gh"}) {
		t.Errorf("first (non-matching) login was mutated: %+v", c.Logins[0])
	}
	if c.Logins[1] != (teaLogin{Name: "gitea", URL: "https://new.example.com", Token: "tok-new"}) {
		t.Errorf("matching login not replaced in place: %+v", c.Logins[1])
	}
}

// Companion (append path): upserting a brand-new name appends. Keeps the
// loop honest under the original — no entry matches, so it must fall
// through to the append rather than replacing anything.
func TestGKVibekitR2_TeaConfig_UpsertLoginAppendsNewName(t *testing.T) {
	c := &teaConfig{Logins: []teaLogin{{Name: "github", URL: "https://github.com"}}}
	c.upsertLogin(teaLogin{Name: "codeberg", URL: "https://codeberg.org"})
	if len(c.Logins) != 2 || c.Logins[1].Name != "codeberg" {
		t.Fatalf("upsertLogin(new name) = %+v, want [github, codeberg]", c.Logins)
	}
	if c.Logins[0].Name != "github" {
		t.Errorf("existing login clobbered on append: %+v", c.Logins[0])
	}
}
