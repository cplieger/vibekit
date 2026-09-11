package forges

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ghStatusFixture is the shape gh 2.94 actually prints for
// `gh auth status --json hosts` (captured live; login anonymized).
const ghStatusFixture = `{"hosts":{"github.com":[{"state":"success","active":true,"host":"github.com","login":"alice","tokenSource":"keyring","scopes":"repo, workflow","gitProtocol":"https"}]}}`

// TestGHAuthHosts_DecodesRealShape decodes the captured real-world
// status JSON and derives Connected purely from presence.
func TestGHAuthHosts_DecodesRealShape(t *testing.T) {
	dir := stubPath(t)
	stubCLI(t, dir, "gh", "echo '"+ghStatusFixture+"'")

	hosts, err := ghAuthHosts(t.Context())
	if err != nil {
		t.Fatalf("ghAuthHosts: %v", err)
	}
	if hosts["github.com"] != "alice" {
		t.Errorf("hosts = %v, want github.com→alice", hosts)
	}
}

// TestGHAuthHosts_ParsesOutputOnNonZeroExit pins the load-bearing
// detail: gh exits non-zero when any host's connection check errors
// (e.g. offline) but still prints the JSON — the account must still be
// discovered. Presence, never gh's network-tested "state", drives the
// forge list; Probe stays the only network-verification path.
func TestGHAuthHosts_ParsesOutputOnNonZeroExit(t *testing.T) {
	dir := stubPath(t)
	offline := `{"hosts":{"github.com":[{"state":"error","active":true,"host":"github.com","login":"alice"}]}}`
	stubCLI(t, dir, "gh", "echo '"+offline+"'; echo 'connection error' >&2; exit 1")

	hosts, err := ghAuthHosts(t.Context())
	if err != nil {
		t.Fatalf("ghAuthHosts: %v", err)
	}
	if hosts["github.com"] != "alice" {
		t.Errorf("offline account must still be discovered by presence, got %v", hosts)
	}
}

// TestGHAuthHosts_ActiveAccountWins picks the active account when a
// host carries several.
func TestGHAuthHosts_ActiveAccountWins(t *testing.T) {
	dir := stubPath(t)
	multi := `{"hosts":{"github.com":[{"active":false,"login":"old"},{"active":true,"login":"current"}]}}`
	stubCLI(t, dir, "gh", "echo '"+multi+"'")

	hosts, err := ghAuthHosts(t.Context())
	if err != nil {
		t.Fatalf("ghAuthHosts: %v", err)
	}
	if hosts["github.com"] != "current" {
		t.Errorf("active account should win, got %v", hosts)
	}
}

// TestGHAuthHosts_NotLoggedIn maps gh's no-hosts reply to an empty set.
func TestGHAuthHosts_NotLoggedIn(t *testing.T) {
	dir := stubPath(t)
	stubCLI(t, dir, "gh", `echo "You are not logged into any GitHub hosts." >&2; exit 1`)

	hosts, err := ghAuthHosts(t.Context())
	if err != nil {
		t.Fatalf("ghAuthHosts: %v", err)
	}
	if len(hosts) != 0 {
		t.Errorf("want empty set, got %v", hosts)
	}
}

// TestGHAuthHosts_NotInstalled propagates the missing-binary sentinel
// (the manager turns it into a cli_missing row).
func TestGHAuthHosts_NotInstalled(t *testing.T) {
	stubPath(t) // empty PATH
	_, err := ghAuthHosts(t.Context())
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("want ErrNotInstalled, got %v", err)
	}
}

// teaListFixture is the shape tea (gitea.dev/tea line) actually prints
// for `tea logins list -o json` (captured live against a seeded store;
// note "default" is a STRING and ssh_host rides along).
const teaListFixture = `[
  {
    "name": "myforge",
    "url": "https://gitea.example.com",
    "ssh_host": "gitea.example.com",
    "user": "alice",
    "default": "true"
  }
]`

// TestTeaLogins_DecodesRealShape decodes the captured real-world list
// output, including the URL→host derivation.
func TestTeaLogins_DecodesRealShape(t *testing.T) {
	dir := stubPath(t)
	fixture := filepath.Join(t.TempDir(), "fixture.json")
	writeFixture(t, fixture, teaListFixture)
	stubCLI(t, dir, "tea", "cat "+fixture)

	logins, err := teaLogins(t.Context())
	if err != nil {
		t.Fatalf("teaLogins: %v", err)
	}
	if len(logins) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(logins), logins)
	}
	l := logins[0]
	if l.Name != "myforge" || l.User != "alice" {
		t.Errorf("decoded login = %+v", l)
	}
	if l.host() != "gitea.example.com" {
		t.Errorf("host() = %q, want gitea.example.com", l.host())
	}
}

// TestTeaLogins_EmptyList maps an empty store to nil.
func TestTeaLogins_EmptyList(t *testing.T) {
	dir := stubPath(t)
	stubCLI(t, dir, "tea", "echo '[]'")

	logins, err := teaLogins(t.Context())
	if err != nil {
		t.Fatalf("teaLogins: %v", err)
	}
	if len(logins) != 0 {
		t.Errorf("want empty, got %+v", logins)
	}
}

// TestTeaHelperToken_ParsesAndCaches verifies the API-fallback token
// mint: tea's git-credential protocol output is parsed for password=,
// and the value is cached per host so repeated API calls don't spawn a
// subprocess each (the counter file counts invocations).
func TestTeaHelperToken_ParsesAndCaches(t *testing.T) {
	const host = "cache-test.example"
	t.Cleanup(func() { teaTokenCache.Delete(host) })
	dir := stubPath(t)
	counter := filepath.Join(t.TempDir(), "count")
	stubCLI(t, dir, "tea", `echo run >> `+counter+`
printf 'protocol=https\nhost=`+host+`\nusername=alice\npassword=tok123\n'`)

	for i := range 2 {
		tok, err := teaHelperToken(t.Context(), host)
		if err != nil {
			t.Fatalf("teaHelperToken call %d: %v", i+1, err)
		}
		if tok != "tok123" {
			t.Fatalf("token = %q, want tok123", tok)
		}
	}
	if runs := strings.Count(readRecord(t, counter), "run"); runs != 1 {
		t.Errorf("helper subprocess ran %d times, want 1 (cached)", runs)
	}
}

// TestCLIConfigExists exercises the stat-only probe behind cli_missing
// rows: no parsing, just presence of the CLI's well-known config file.
func TestCLIConfigExists(t *testing.T) {
	tmp := setConfigHomeTemp(t)
	if cliConfigExists(KindGitHub) || cliConfigExists(KindGitea) {
		t.Error("empty config home should probe false")
	}
	writeFixture(t, filepath.Join(tmp, "gh", "hosts.yml"), "anything: at all\n")
	writeFixture(t, filepath.Join(tmp, "tea", "config.yml"), "logins: []\n")
	if !cliConfigExists(KindGitHub) {
		t.Error("gh config should probe true")
	}
	if !cliConfigExists(KindGitea) || !cliConfigExists(KindCodeberg) {
		t.Error("tea config should probe true for gitea AND codeberg")
	}
}

// gh prints its hosts JSON even when one host's check errored, which is why the
// stdout is decoded regardless of the exit status. The case that must NOT be
// swallowed is the other one: output that does not decode at all AND a real
// failure. Answering with an empty list there tells the user they have no
// forges configured when what actually happened is that gh broke.
func TestGHAuthHosts_UndecodableOutputWithARealErrorPropagates(t *testing.T) {
	dir := stubPath(t)
	stubCLI(t, dir, "gh", `echo 'not json at all'
echo 'gh: something broke' >&2
exit 2`)

	hosts, err := ghAuthHosts(t.Context())

	if err == nil {
		t.Errorf("ghAuthHosts = %v, nil; want the failure reported rather than an empty list", hosts)
	}
}

// TestActiveAccount pins the one selection rule the login and the
// scopes readers share: without it a host with several accounts could
// report one account's login beside another's scopes.
func TestActiveAccount(t *testing.T) {
	tests := []struct {
		name     string
		accounts []ghStatusAccount
		wantOK   bool
		want     string
	}{
		{name: "none", accounts: nil, wantOK: false},
		{name: "single", accounts: []ghStatusAccount{{Login: "alice"}}, wantOK: true, want: "alice"},
		{
			name:     "active_wins_over_first",
			accounts: []ghStatusAccount{{Login: "old"}, {Login: "current", Active: true}},
			wantOK:   true,
			want:     "current",
		},
		{
			name:     "no_active_falls_back_to_first",
			accounts: []ghStatusAccount{{Login: "first"}, {Login: "second"}},
			wantOK:   true,
			want:     "first",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := activeAccount(tt.accounts)
			if ok != tt.wantOK {
				t.Fatalf("activeAccount(%+v) ok = %v, want %v", tt.accounts, ok, tt.wantOK)
			}
			if ok && got.Login != tt.want {
				t.Errorf("activeAccount(%+v).Login = %q, want %q", tt.accounts, got.Login, tt.want)
			}
		})
	}
}

// TestGHGrantedScopes_DecodesTheCapturedShape reads the scopes off the
// same captured status JSON the login discovery uses. Without this
// field decoded, a reconnect cannot know what it is about to replace.
func TestGHGrantedScopes_DecodesTheCapturedShape(t *testing.T) {
	dir := stubPath(t)
	stubCLI(t, dir, "gh", "echo '"+ghStatusFixture+"'")

	got := ghGrantedScopes(t.Context(), "github.com")
	want := []string{"repo", "workflow"}
	if !slices.Equal(got, want) {
		t.Errorf("ghGrantedScopes(github.com) = %v, want %v", got, want)
	}
}

// TestGHGrantedScopes_ReadsTheActiveAccount: the scopes must come from
// the account whose token gh actually stores.
func TestGHGrantedScopes_ReadsTheActiveAccount(t *testing.T) {
	dir := stubPath(t)
	multi := `{"hosts":{"github.com":[` +
		`{"active":false,"login":"old","scopes":"repo"},` +
		`{"active":true,"login":"current","scopes":"repo, gist"}]}}`
	stubCLI(t, dir, "gh", "echo '"+multi+"'")

	got := ghGrantedScopes(t.Context(), "github.com")
	want := []string{"repo", "gist"}
	if !slices.Equal(got, want) {
		t.Errorf("ghGrantedScopes(github.com) = %v, want the active account's %v", got, want)
	}
}

// TestGHGrantedScopes_UnreadableAnswersNil covers every way the read can
// come back empty. All of them must answer nil rather than an error, so
// a caller falls back to its own floor instead of failing the login.
func TestGHGrantedScopes_UnreadableAnswersNil(t *testing.T) {
	tests := []struct {
		name   string
		script string
		host   string
	}{
		{name: "gh_not_installed", script: "", host: "github.com"},
		{
			name:   "not_logged_in",
			script: `echo "You are not logged into any GitHub hosts." >&2; exit 1`,
			host:   "github.com",
		},
		{name: "undecodable_output", script: `echo 'not json'; exit 2`, host: "github.com"},
		{
			name:   "host_absent",
			script: "echo '" + ghStatusFixture + "'",
			host:   "gitlab.example.com",
		},
		{
			name:   "offline_check_reports_no_scopes",
			script: `echo '{"hosts":{"github.com":[{"state":"error","active":true,"login":"alice"}]}}'; exit 1`,
			host:   "github.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := stubPath(t)
			if tt.script != "" {
				stubCLI(t, dir, "gh", tt.script)
			}
			if got := ghGrantedScopes(t.Context(), tt.host); got != nil {
				t.Errorf("ghGrantedScopes(%q) = %v, want nil", tt.host, got)
			}
		})
	}
}
