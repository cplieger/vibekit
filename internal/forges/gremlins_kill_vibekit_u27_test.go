package forges

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// gk_vibekit_u27_setHome points the per-CLI config root at a temp dir and
// restores the default on cleanup.
func gk_vibekit_u27_setHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	SetConfigHome(tmp)
	t.Cleanup(func() { SetConfigHome("") })
	return tmp
}

// TestGkVibekitU27_RemoveToken_HostDefaulting kills the
// CONDITIONALS_NEGATION mutant at inject.go:93 (`if host == ""` in
// RemoveToken). When host is empty the kind's default host must be used;
// when host is provided it must be used verbatim. Flipping == to !=
// inverts which host is targeted.
func TestGkVibekitU27_RemoveToken_HostDefaulting(t *testing.T) {
	t.Run("empty_host_uses_default", func(t *testing.T) {
		tmp := gk_vibekit_u27_setHome(t)
		if err := writeGHHosts("github.com", "tok", "alice"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// host == "" → defaults to github.com → that entry is removed.
		// Mutated (!=) keeps host="" and removes nothing.
		if err := RemoveToken(context.Background(), KindGitHub, ""); err != nil {
			t.Fatalf("RemoveToken: %v", err)
		}
		hosts, err := loadGHHosts(filepath.Join(tmp, "gh", "hosts.yml"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if _, ok := hosts["github.com"]; ok {
			t.Errorf("RemoveToken(KindGitHub, \"\") left github.com present: %+v", hosts)
		}
	})

	t.Run("explicit_host_used_verbatim", func(t *testing.T) {
		tmp := gk_vibekit_u27_setHome(t)
		if err := writeGHHosts("github.com", "t1", "alice"); err != nil {
			t.Fatalf("seed 1: %v", err)
		}
		if err := writeGHHosts("github.enterprise.example", "t2", "bob"); err != nil {
			t.Fatalf("seed 2: %v", err)
		}
		// host != "" → that exact host is removed; github.com remains.
		// Mutated (!=) replaces host with the default and removes github.com.
		if err := RemoveToken(context.Background(), KindGitHub, "github.enterprise.example"); err != nil {
			t.Fatalf("RemoveToken: %v", err)
		}
		hosts, err := loadGHHosts(filepath.Join(tmp, "gh", "hosts.yml"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if _, ok := hosts["github.enterprise.example"]; ok {
			t.Errorf("explicit host github.enterprise.example should be removed: %+v", hosts)
		}
		if _, ok := hosts["github.com"]; !ok {
			t.Errorf("github.com should remain after removing a different host: %+v", hosts)
		}
	})
}

// TestGkVibekitU27_MakeID_HostDefaulting kills the CONDITIONALS_NEGATION
// mutant at manager.go:51 (`if host == ""` in MakeID).
func TestGkVibekitU27_MakeID_HostDefaulting(t *testing.T) {
	if got := MakeID(KindGitHub, ""); got != "github:github.com" {
		t.Errorf("MakeID(KindGitHub, \"\") = %q, want %q", got, "github:github.com")
	}
	if got := MakeID(KindGitHub, "self.example"); got != "github:self.example" {
		t.Errorf("MakeID(KindGitHub, %q) = %q, want %q", "self.example", got, "github:self.example")
	}
}

// TestGkVibekitU27_WriteGLabConfig_PreservesExistingHosts kills the
// CONDITIONALS_NEGATION mutant at inject_glab.go:28 (`if cfg.Hosts == nil`).
// loadGLabConfig always returns a non-nil Hosts map, so the original guard
// never re-initializes it. Flipping == to != re-makes (wipes) the loaded
// map on every write, dropping previously-stored hosts.
func TestGkVibekitU27_WriteGLabConfig_PreservesExistingHosts(t *testing.T) {
	tmp := gk_vibekit_u27_setHome(t)
	if err := writeGLabConfig("gitlab.com", "glpat-1", "alice"); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := writeGLabConfig("self.example", "glpat-2", "bob"); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	cfg, err := loadGLabConfig(filepath.Join(tmp, "glab-cli", "config.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := cfg.Hosts["gitlab.com"]; !ok {
		t.Errorf("first host gitlab.com lost after second write: %+v", cfg.Hosts)
	}
	if _, ok := cfg.Hosts["self.example"]; !ok {
		t.Errorf("second host self.example missing: %+v", cfg.Hosts)
	}
}

// TestGkVibekitU27_RemoveGLabHost_PerformsRemoval kills the
// CONDITIONALS_NEGATION mutant at inject_glab.go:45 (`if err != nil` from
// configHome). With a config-home override configHome returns a nil error,
// so the original proceeds with the removal. Flipping != to == returns
// early (nil) and the host is never removed.
func TestGkVibekitU27_RemoveGLabHost_PerformsRemoval(t *testing.T) {
	tmp := gk_vibekit_u27_setHome(t)
	if err := writeGLabConfig("gitlab.com", "glpat-1", "alice"); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := writeGLabConfig("self.example", "glpat-2", "bob"); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if err := removeGLabHost("gitlab.com"); err != nil {
		t.Fatalf("removeGLabHost: %v", err)
	}
	cfg, err := loadGLabConfig(filepath.Join(tmp, "glab-cli", "config.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := cfg.Hosts["gitlab.com"]; ok {
		t.Errorf("removeGLabHost did not remove gitlab.com: %+v", cfg.Hosts)
	}
	if _, ok := cfg.Hosts["self.example"]; !ok {
		t.Errorf("self.example should remain: %+v", cfg.Hosts)
	}
}

// TestGkVibekitU27_MarshalGLab_EditorLine kills the CONDITIONALS_NEGATION
// mutant at inject_glab.go:146 (`if cfg.Editor != ""`).
func TestGkVibekitU27_MarshalGLab_EditorLine(t *testing.T) {
	withEditor := marshalGLabConfig(&glabConfig{Editor: "vim", Hosts: map[string]glabHostEntry{}})
	if !strings.Contains(withEditor, "editor: vim") {
		t.Errorf("marshalGLabConfig(Editor=vim) = %q, want it to contain %q", withEditor, "editor: vim")
	}
	noEditor := marshalGLabConfig(&glabConfig{
		Editor: "",
		Hosts:  map[string]glabHostEntry{"gitlab.com": {Token: "t"}},
	})
	if strings.Contains(noEditor, "editor:") {
		t.Errorf("marshalGLabConfig(Editor=\"\") = %q, want no editor line", noEditor)
	}
}

// TestGkVibekitU27_MarshalGLab_HostsHeader kills the CONDITIONALS_BOUNDARY
// mutant at inject_glab.go:149 (`if len(cfg.Hosts) > 0`). At the boundary
// (len == 0) the hosts header must be omitted; `>= 0` would always emit it.
func TestGkVibekitU27_MarshalGLab_HostsHeader(t *testing.T) {
	empty := marshalGLabConfig(&glabConfig{Hosts: map[string]glabHostEntry{}})
	if strings.Contains(empty, "hosts:") {
		t.Errorf("marshalGLabConfig(no hosts) = %q, want no hosts header", empty)
	}
	populated := marshalGLabConfig(&glabConfig{Hosts: map[string]glabHostEntry{"gitlab.com": {Token: "t"}}})
	if !strings.Contains(populated, "hosts:") {
		t.Errorf("marshalGLabConfig(one host) = %q, want hosts header", populated)
	}
}

// TestGkVibekitU27_MarshalGLab_ProtocolDefault kills the
// CONDITIONALS_NEGATION mutant at inject_glab.go:160 (`if gp == ""`). An
// empty protocol must default to https; a set protocol must be preserved.
// Flipping == to != inverts both.
func TestGkVibekitU27_MarshalGLab_ProtocolDefault(t *testing.T) {
	ssh := marshalGLabConfig(&glabConfig{
		Hosts: map[string]glabHostEntry{"gitlab.com": {Token: "t", Protocol: "ssh"}},
	})
	if !strings.Contains(ssh, "git_protocol: ssh") {
		t.Errorf("marshalGLabConfig(Protocol=ssh) = %q, want it to preserve %q", ssh, "git_protocol: ssh")
	}
	def := marshalGLabConfig(&glabConfig{
		Hosts: map[string]glabHostEntry{"gitlab.com": {Token: "t", Protocol: ""}},
	})
	if !strings.Contains(def, "git_protocol: https") {
		t.Errorf("marshalGLabConfig(Protocol=\"\") = %q, want default %q", def, "git_protocol: https")
	}
}

// TestGkVibekitU27_RemoveTeaHost_PerformsRemoval kills the
// CONDITIONALS_NEGATION mutant at inject_tea.go:44 (`if err != nil` from
// configHome). Same shape as the glab removal: == returns early and skips
// the removal.
func TestGkVibekitU27_RemoveTeaHost_PerformsRemoval(t *testing.T) {
	tmp := gk_vibekit_u27_setHome(t)
	if err := writeTeaConfig("codeberg.org", "tok-1", "alice"); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := writeTeaConfig("gitea.example", "tok-2", "bob"); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if err := removeTeaHost("codeberg.org"); err != nil {
		t.Fatalf("removeTeaHost: %v", err)
	}
	cfg, err := loadTeaConfig(filepath.Join(tmp, "tea", "config.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, l := range cfg.Logins {
		if l.Name == "codeberg.org" {
			t.Errorf("removeTeaHost did not remove codeberg.org: %+v", cfg.Logins)
		}
	}
	var hasGitea bool
	for _, l := range cfg.Logins {
		if l.Name == "gitea.example" {
			hasGitea = true
		}
	}
	if !hasGitea {
		t.Errorf("gitea.example should remain: %+v", cfg.Logins)
	}
}

// TestGkVibekitU27_MarshalTea_LoginsHeader kills the CONDITIONALS_BOUNDARY
// mutant at inject_tea.go:165 (`if len(cfg.Logins) > 0`). At the boundary
// (len == 0) the logins header must be omitted; `>= 0` would always emit it.
func TestGkVibekitU27_MarshalTea_LoginsHeader(t *testing.T) {
	empty := marshalTeaConfig(&teaConfig{})
	if strings.Contains(empty, "logins:") {
		t.Errorf("marshalTeaConfig(no logins) = %q, want no logins header", empty)
	}
	populated := marshalTeaConfig(&teaConfig{Logins: []teaLogin{{Name: "codeberg.org", Token: "t"}}})
	if !strings.Contains(populated, "logins:") {
		t.Errorf("marshalTeaConfig(one login) = %q, want logins header", populated)
	}
}
