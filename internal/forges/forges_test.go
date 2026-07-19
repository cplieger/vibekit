package forges

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadGLabConfig_RealWorldShape parses a config.yml shape that glab
// itself produces (the read-only discovery parser's contract; vibekit
// never writes this file).
func TestLoadGLabConfig_RealWorldShape(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yml")
	body := `git_protocol: https
editor:
hosts:
    gitlab.com:
        token: glpat-xxx
        user: alice
        git_protocol: https
        api_host: gitlab.com
    self.example:
        token: glpat-yyy
        user: bob
`
	writeFixture(t, path, body)
	cfg, err := loadGLabConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Hosts) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(cfg.Hosts), cfg.Hosts)
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
	if cfg.Hosts["self.example"].Token != "glpat-yyy" {
		t.Errorf("self-hosted entry missing: %+v", cfg.Hosts)
	}
}

// TestLoadGLabConfig_MissingFile returns an empty config.
func TestLoadGLabConfig_MissingFile(t *testing.T) {
	cfg, err := loadGLabConfig(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Hosts) != 0 {
		t.Errorf("want empty hosts, got %+v", cfg.Hosts)
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
