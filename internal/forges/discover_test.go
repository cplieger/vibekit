package forges

import (
	"errors"
	"path/filepath"
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
