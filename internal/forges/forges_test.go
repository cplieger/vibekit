package forges

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGHHostsRoundtrip verifies that writing a gh hosts.yml file and
// then reading it back returns the same data.
func TestGHHostsRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	SetConfigHome(tmp)
	t.Cleanup(func() { SetConfigHome("") })

	if err := writeGHHosts("github.com", "ghp_test123", "alice"); err != nil {
		t.Fatalf("write: %v", err)
	}
	hosts, err := loadGHHosts(filepath.Join(tmp, "gh", "hosts.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, ok := hosts["github.com"]
	if !ok {
		t.Fatalf("github.com not in loaded hosts: %+v", hosts)
	}
	if got.OAuthToken != "ghp_test123" {
		t.Errorf("oauth_token = %q, want %q", got.OAuthToken, "ghp_test123")
	}
	if got.User != "alice" {
		t.Errorf("user = %q, want alice", got.User)
	}
	if got.GitProtocol != "https" {
		t.Errorf("git_protocol = %q, want https", got.GitProtocol)
	}
}

// TestGHHostsMultiHost ensures multiple hosts coexist and one can be
// removed without affecting the others.
func TestGHHostsMultiHost(t *testing.T) {
	tmp := t.TempDir()
	SetConfigHome(tmp)
	t.Cleanup(func() { SetConfigHome("") })

	if err := writeGHHosts("github.com", "tok1", "alice"); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := writeGHHosts("github.enterprise.example", "tok2", "bob"); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if err := removeGHHost("github.com"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	hosts, err := loadGHHosts(filepath.Join(tmp, "gh", "hosts.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := hosts["github.com"]; ok {
		t.Errorf("github.com should be removed")
	}
	ent, ok := hosts["github.enterprise.example"]
	if !ok || ent.OAuthToken != "tok2" {
		t.Errorf("enterprise host missing or corrupted: %+v", ent)
	}
}

// TestGLabConfigRoundtrip verifies the glab config writer.
func TestGLabConfigRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	SetConfigHome(tmp)
	t.Cleanup(func() { SetConfigHome("") })

	if err := writeGLabConfig("gitlab.com", "glpat-xxx", "alice"); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadGLabConfig(filepath.Join(tmp, "glab-cli", "config.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, ok := cfg.Hosts["gitlab.com"]
	if !ok {
		t.Fatalf("gitlab.com not in hosts: %+v", cfg.Hosts)
	}
	if got.Token != "glpat-xxx" {
		t.Errorf("token = %q", got.Token)
	}
	if got.User != "alice" {
		t.Errorf("user = %q", got.User)
	}
}

// TestTeaConfigRoundtrip verifies the tea config writer.
func TestTeaConfigRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	SetConfigHome(tmp)
	t.Cleanup(func() { SetConfigHome("") })

	if err := writeTeaConfig("codeberg.org", "abc123", "alice"); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadTeaConfig(filepath.Join(tmp, "tea", "config.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Logins) != 1 {
		t.Fatalf("logins len = %d, want 1", len(cfg.Logins))
	}
	got := cfg.Logins[0]
	if got.Name != "codeberg.org" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Token != "abc123" {
		t.Errorf("token = %q", got.Token)
	}
	if !got.Default {
		t.Errorf("first login should be default=true")
	}
}

// TestRemoveTokenIdempotent verifies that removing a non-existent
// token doesn't error.
func TestRemoveTokenIdempotent(t *testing.T) {
	tmp := t.TempDir()
	SetConfigHome(tmp)
	t.Cleanup(func() { SetConfigHome("") })

	for _, kind := range []Kind{KindGitHub, KindGitLab, KindGitea, KindCodeberg} {
		if err := RemoveToken(context.Background(), kind, ""); err != nil {
			t.Errorf("RemoveToken(%s) without prior login: %v", kind, err)
		}
	}
}

// TestWriteYAMLAtomic ensures the write is atomic (tmp file is renamed,
// not visible mid-write).
func TestWriteYAMLAtomic(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "test.yml")
	if err := writeYAML(dest, "hello\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello\n" {
		t.Errorf("content = %q", data)
	}
	// .tmp file should not be left behind.
	if _, err := os.Stat(dest + ".tmp"); err == nil {
		t.Errorf(".tmp file leaked")
	}
}

// TestGHHostsParse_RealWorldShape parses a hosts.yml shape that gh
// itself produces (verified by hand on a test installation).
func TestGHHostsParse_RealWorldShape(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "hosts.yml")
	body := `github.com:
    git_protocol: https
    oauth_token: ghu_xxxx
    user: alice
github.enterprise.example:
    git_protocol: https
    oauth_token: tk_yyyy
    user: bob
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hosts, err := loadGHHosts(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(hosts) != 2 {
		t.Errorf("len = %d, want 2", len(hosts))
	}
	if hosts["github.com"].User != "alice" {
		t.Errorf("alice missing")
	}
	if hosts["github.enterprise.example"].OAuthToken != "tk_yyyy" {
		t.Errorf("enterprise token missing")
	}
}

// TestExtractPRNumber covers the URL → number extraction across forges.
func TestExtractPRNumber(t *testing.T) {
	cases := []struct {
		url  string
		want int
	}{
		{"https://github.com/owner/repo/pull/42", 42},
		{"https://gitlab.com/group/proj/-/merge_requests/123", 123},
		{"https://codeberg.org/user/repo/pulls/7", 7},
		{"https://gitea.example/u/r/pulls/99", 99},
		{"https://no-match.example", 0},
	}
	for _, c := range cases {
		got := extractPRNumberFromURL(c.url)
		if got != c.want {
			t.Errorf("extractPRNumberFromURL(%q) = %d, want %d", c.url, got, c.want)
		}
	}
}

// TestExtractIssueNumber covers issue URL parsing.
func TestExtractIssueNumber(t *testing.T) {
	cases := []struct {
		url  string
		want int
	}{
		{"https://github.com/owner/repo/issues/42", 42},
		{"https://gitlab.com/group/proj/-/issues/123", 123},
		{"https://codeberg.org/user/repo/issues/7", 7},
		{"https://no-match.example", 0},
	}
	for _, c := range cases {
		got := extractIssueNumberFromURL(c.url)
		if got != c.want {
			t.Errorf("extractIssueNumberFromURL(%q) = %d, want %d", c.url, got, c.want)
		}
	}
}

// TestParseRepo covers the owner/name splitter.
func TestParseRepo(t *testing.T) {
	cases := []struct {
		in        string
		owner     string
		name      string
		wantError bool
	}{
		{"owner/repo", "owner", "repo", false},
		{"  owner/repo  ", "owner", "repo", false}, // trim
		{"group/sub/repo", "group", "sub/repo", false},
		{"justone", "", "", true},
		{"", "", "", true},
		{"/", "", "", true},
		{"owner/", "", "", true},
	}
	for _, c := range cases {
		o, n, err := ParseRepo(c.in)
		if (err != nil) != c.wantError {
			t.Errorf("ParseRepo(%q) error = %v, want error=%v", c.in, err, c.wantError)
			continue
		}
		if !c.wantError && (o != c.owner || n != c.name) {
			t.Errorf("ParseRepo(%q) = %q, %q; want %q, %q", c.in, o, n, c.owner, c.name)
		}
	}
}

// TestKindHelpers locks in the Kind metadata.
func TestKindHelpers(t *testing.T) {
	cases := []struct {
		kind  Kind
		cli   string
		host  string
		title string
		valid bool
	}{
		{KindGitHub, "gh", "github.com", "GitHub", true},
		{KindGitLab, "glab", "gitlab.com", "GitLab", true},
		{KindCodeberg, "tea", "codeberg.org", "Codeberg", true},
		{KindGitea, "tea", "", "Gitea", true},
		{Kind("nope"), "", "", "nope", false},
	}
	for _, c := range cases {
		if got := c.kind.Valid(); got != c.valid {
			t.Errorf("%s.Valid() = %v", c.kind, got)
		}
		if got := c.kind.CLI(); got != c.cli {
			t.Errorf("%s.CLI() = %q", c.kind, got)
		}
		if got := c.kind.DefaultHost(); got != c.host {
			t.Errorf("%s.DefaultHost() = %q", c.kind, got)
		}
		if got := c.kind.Title(); got != c.title {
			t.Errorf("%s.Title() = %q", c.kind, got)
		}
	}
}

// TestSplitID locks in the ID parser used by HTTP routing.
func TestSplitID(t *testing.T) {
	cases := []struct {
		id   string
		kind Kind
		host string
	}{
		{"github:github.com", KindGitHub, "github.com"},
		{"gitlab:self.example", KindGitLab, "self.example"},
		{"codeberg:codeberg.org", KindCodeberg, "codeberg.org"},
		{"malformed", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		k, h := splitID(c.id)
		if k != c.kind || h != c.host {
			t.Errorf("splitID(%q) = %q, %q; want %q, %q", c.id, k, h, c.kind, c.host)
		}
	}
}

// TestSanitizeEnv strips credential env vars to prevent leak-through.
func TestSanitizeEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GH_TOKEN=leaked",
		"GITHUB_TOKEN=leaked2",
		"GITLAB_TOKEN=leaked3",
		"GLAB_TOKEN=leaked4",
		"GITEA_TOKEN=leaked5",
		"TEA_TOKEN=leaked6",
		"USER=alice",
	}
	out := sanitizeEnv(in)
	for _, kv := range out {
		if strings.Contains(kv, "TOKEN") || strings.Contains(kv, "GITHUB_TOKEN") {
			t.Errorf("sanitized env still contains: %q", kv)
		}
	}
	// Non-token vars should pass through.
	hasPath, hasUser := false, false
	for _, kv := range out {
		if kv == "PATH=/usr/bin" {
			hasPath = true
		}
		if kv == "USER=alice" {
			hasUser = true
		}
	}
	if !hasPath {
		t.Errorf("PATH dropped")
	}
	if !hasUser {
		t.Errorf("USER dropped")
	}
}

// setConfigHomeTemp points the per-CLI config root at a fresh temp dir
// and restores the default when the test finishes.
func setConfigHomeTemp(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	SetConfigHome(tmp)
	t.Cleanup(func() { SetConfigHome("") })
	return tmp
}

// TestTeaConfig_UpsertLoginReplacesMatchInPlace verifies that upserting a
// login whose name matches an existing entry replaces that entry in place
// and leaves the other logins untouched.
func TestTeaConfig_UpsertLoginReplacesMatchInPlace(t *testing.T) {
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

// TestTeaConfig_UpsertLoginAppendsNewName verifies that upserting a
// brand-new login name appends it and leaves existing logins intact.
func TestTeaConfig_UpsertLoginAppendsNewName(t *testing.T) {
	c := &teaConfig{Logins: []teaLogin{{Name: "github", URL: "https://github.com"}}}
	c.upsertLogin(teaLogin{Name: "codeberg", URL: "https://codeberg.org"})
	if len(c.Logins) != 2 || c.Logins[1].Name != "codeberg" {
		t.Fatalf("upsertLogin(new name) = %+v, want [github, codeberg]", c.Logins)
	}
	if c.Logins[0].Name != "github" {
		t.Errorf("existing login clobbered on append: %+v", c.Logins[0])
	}
}

// TestRemoveToken_HostDefaulting verifies RemoveToken targets the kind's
// default host when host is empty, and the exact host when one is given.
func TestRemoveToken_HostDefaulting(t *testing.T) {
	t.Run("empty_host_uses_default", func(t *testing.T) {
		tmp := setConfigHomeTemp(t)
		if err := writeGHHosts("github.com", "tok", "alice"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// host == "" defaults to github.com, so that entry is removed.
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
		tmp := setConfigHomeTemp(t)
		if err := writeGHHosts("github.com", "t1", "alice"); err != nil {
			t.Fatalf("seed 1: %v", err)
		}
		if err := writeGHHosts("github.enterprise.example", "t2", "bob"); err != nil {
			t.Fatalf("seed 2: %v", err)
		}
		// An explicit host is removed verbatim; the default host remains.
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

// TestMakeID_HostDefaulting verifies MakeID fills in the kind's default
// host for an empty host and uses an explicit host verbatim.
func TestMakeID_HostDefaulting(t *testing.T) {
	if got := MakeID(KindGitHub, ""); got != "github:github.com" {
		t.Errorf("MakeID(KindGitHub, \"\") = %q, want %q", got, "github:github.com")
	}
	if got := MakeID(KindGitHub, "self.example"); got != "github:self.example" {
		t.Errorf("MakeID(KindGitHub, %q) = %q, want %q", "self.example", got, "github:self.example")
	}
}

// TestWriteGLabConfig_PreservesExistingHosts verifies a second writeGLabConfig
// keeps the host stored by the first write (the loaded map is reused, never
// wiped).
func TestWriteGLabConfig_PreservesExistingHosts(t *testing.T) {
	tmp := setConfigHomeTemp(t)
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

// TestRemoveGLabHost_PerformsRemoval verifies removeGLabHost deletes the
// named host while leaving the others in place.
func TestRemoveGLabHost_PerformsRemoval(t *testing.T) {
	tmp := setConfigHomeTemp(t)
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

// TestMarshalGLabConfig_EditorLine verifies the editor line is emitted only
// when an editor is configured.
func TestMarshalGLabConfig_EditorLine(t *testing.T) {
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

// TestMarshalGLabConfig_HostsHeader verifies the hosts header is omitted when
// there are no hosts and present when there is at least one.
func TestMarshalGLabConfig_HostsHeader(t *testing.T) {
	empty := marshalGLabConfig(&glabConfig{Hosts: map[string]glabHostEntry{}})
	if strings.Contains(empty, "hosts:") {
		t.Errorf("marshalGLabConfig(no hosts) = %q, want no hosts header", empty)
	}
	populated := marshalGLabConfig(&glabConfig{Hosts: map[string]glabHostEntry{"gitlab.com": {Token: "t"}}})
	if !strings.Contains(populated, "hosts:") {
		t.Errorf("marshalGLabConfig(one host) = %q, want hosts header", populated)
	}
}

// TestMarshalGLabConfig_ProtocolDefault verifies an empty git protocol
// defaults to https while a set protocol is preserved.
func TestMarshalGLabConfig_ProtocolDefault(t *testing.T) {
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

// TestRemoveTeaHost_PerformsRemoval verifies removeTeaHost deletes the named
// login while leaving the others in place.
func TestRemoveTeaHost_PerformsRemoval(t *testing.T) {
	tmp := setConfigHomeTemp(t)
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

// TestMarshalTeaConfig_LoginsHeader verifies the logins header is omitted when
// there are no logins and present when there is at least one.
func TestMarshalTeaConfig_LoginsHeader(t *testing.T) {
	empty := marshalTeaConfig(&teaConfig{})
	if strings.Contains(empty, "logins:") {
		t.Errorf("marshalTeaConfig(no logins) = %q, want no logins header", empty)
	}
	populated := marshalTeaConfig(&teaConfig{Logins: []teaLogin{{Name: "codeberg.org", Token: "t"}}})
	if !strings.Contains(populated, "logins:") {
		t.Errorf("marshalTeaConfig(one login) = %q, want logins header", populated)
	}
}
