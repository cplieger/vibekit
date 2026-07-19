package forges

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLILogin_GH_PipesTokenAndSetsUpGit verifies the gh login verb:
// the token travels on stdin to `auth login --with-token`, and the
// same call registers gh as git's credential helper via setup-git.
func TestCLILogin_GH_PipesTokenAndSetsUpGit(t *testing.T) {
	dir := stubPath(t)
	rec := filepath.Join(t.TempDir(), "rec")
	stubCLI(t, dir, "gh", recordingScript(rec))

	if err := cliLogin(context.Background(), KindGitHub, "github.com", "gho_secret"); err != nil {
		t.Fatalf("cliLogin: %v", err)
	}

	record := readRecord(t, rec)
	argv := recordLines(record, "argv:")
	want := []string{
		"auth login --hostname github.com --with-token",
		"auth setup-git --hostname github.com",
	}
	if len(argv) != len(want) || argv[0] != want[0] || argv[1] != want[1] {
		t.Errorf("gh argv = %q, want %q", argv, want)
	}
	if !strings.Contains(record, "stdin:gho_secret") {
		t.Errorf("token not piped on stdin; record:\n%s", record)
	}
	if strings.Contains(strings.Join(argv, " "), "gho_secret") {
		t.Errorf("token leaked into argv: %q", argv)
	}
}

// TestCLILogin_GLab verifies the glab login verb: token on stdin via
// --stdin, then the git-credential configure call.
func TestCLILogin_GLab(t *testing.T) {
	dir := stubPath(t)
	rec := filepath.Join(t.TempDir(), "rec")
	stubCLI(t, dir, "glab", recordingScript(rec))

	if err := cliLogin(context.Background(), KindGitLab, "gitlab.com", "glpat-secret"); err != nil {
		t.Fatalf("cliLogin: %v", err)
	}

	record := readRecord(t, rec)
	argv := recordLines(record, "argv:")
	want := []string{
		"auth login --hostname gitlab.com --stdin",
		"auth git-credential configure --hostname gitlab.com",
	}
	if len(argv) != len(want) || argv[0] != want[0] || argv[1] != want[1] {
		t.Errorf("glab argv = %q, want %q", argv, want)
	}
	if !strings.Contains(record, "stdin:glpat-secret") {
		t.Errorf("token not piped on stdin; record:\n%s", record)
	}
}

// TestCLILogin_Tea_TokenViaEnvNotArgv verifies the tea login verb: one
// `login add` call carrying --git-credentials (tea registers itself as
// git's helper), with the token in $GITEA_SERVER_TOKEN — never argv.
func TestCLILogin_Tea_TokenViaEnvNotArgv(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the ~/.git-credentials scrub
	dir := stubPath(t)
	rec := filepath.Join(t.TempDir(), "rec")
	stubCLI(t, dir, "tea", recordingScript(rec))

	if err := cliLogin(context.Background(), KindGitea, "gitea.example.com", "tea_secret"); err != nil {
		t.Fatalf("cliLogin: %v", err)
	}

	record := readRecord(t, rec)
	argv := recordLines(record, "argv:")
	want := "login add --name gitea.example.com --url https://gitea.example.com --git-credentials --no-version-check"
	if len(argv) != 1 || argv[0] != want {
		t.Errorf("tea argv = %q, want [%q]", argv, want)
	}
	if strings.Contains(strings.Join(argv, " "), "tea_secret") {
		t.Errorf("token leaked into argv: %q", argv)
	}
	if !strings.Contains(record, "env:tea_secret") {
		t.Errorf("token not delivered via GITEA_SERVER_TOKEN; record:\n%s", record)
	}
}

// TestCLILogin_Tea_ScrubsStaleStoreEntry verifies the pre-login scrub:
// a leftover cleartext ~/.git-credentials line for the host (written by
// the pre-CLI-native integration, and consulted by git BEFORE tea's
// helper) is removed; unrelated lines survive.
func TestCLILogin_Tea_ScrubsStaleStoreEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	credFile := filepath.Join(home, ".git-credentials")
	stale := "https://oauth2:oldtok@gitea.example.com/\nhttps://user:tok@other.example/\n"
	if err := os.WriteFile(credFile, []byte(stale), 0o600); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	dir := stubPath(t)
	stubCLI(t, dir, "tea", "exit 0")

	if err := cliLogin(context.Background(), KindGitea, "gitea.example.com", "newtok"); err != nil {
		t.Fatalf("cliLogin: %v", err)
	}

	data, err := os.ReadFile(credFile)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if strings.Contains(string(data), "gitea.example.com") {
		t.Errorf("stale host line survived the scrub: %q", data)
	}
	if !strings.Contains(string(data), "other.example") {
		t.Errorf("unrelated line was scrubbed: %q", data)
	}
}

// TestCLILogout_GH_And_GLab verifies the native logout argv per kind.
func TestCLILogout_GH_And_GLab(t *testing.T) {
	dir := stubPath(t)
	recGH := filepath.Join(t.TempDir(), "gh.rec")
	recGLab := filepath.Join(t.TempDir(), "glab.rec")
	stubCLI(t, dir, "gh", recordingScript(recGH))
	stubCLI(t, dir, "glab", recordingScript(recGLab))

	if err := cliLogout(context.Background(), KindGitHub, "github.com"); err != nil {
		t.Fatalf("gh logout: %v", err)
	}
	if err := cliLogout(context.Background(), KindGitLab, "gitlab.com"); err != nil {
		t.Fatalf("glab logout: %v", err)
	}

	if got := recordLines(readRecord(t, recGH), "argv:"); len(got) != 1 || got[0] != "auth logout --hostname github.com" {
		t.Errorf("gh logout argv = %q", got)
	}
	if got := recordLines(readRecord(t, recGLab), "argv:"); len(got) != 1 || got[0] != "auth logout --hostname gitlab.com" {
		t.Errorf("glab logout argv = %q", got)
	}
}

// TestCLILogout_Tea_ResolvesNameFromURL verifies tea disconnect: the
// login NAME is resolved from tea's own list by URL host (a login added
// by hand in the shell can carry any name), then deleted by that name.
func TestCLILogout_Tea_ResolvesNameFromURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := stubPath(t)
	rec := filepath.Join(t.TempDir(), "rec")
	stubCLI(t, dir, "tea", `case "$1 $2" in
"logins list") echo '[{"name":"myforge","url":"https://gitea.example.com","user":"dave"}]' ;;
"login delete") printf 'deleted:%s\n' "$3" >> `+rec+` ;;
esac`)

	if err := cliLogout(context.Background(), KindGitea, "gitea.example.com"); err != nil {
		t.Fatalf("cliLogout: %v", err)
	}
	if got := readRecord(t, rec); !strings.Contains(got, "deleted:myforge") {
		t.Errorf("expected delete of login name %q, record:\n%s", "myforge", got)
	}
}

// TestCLILogout_Tea_NoMatchingLogin_NoOp verifies disconnect of a host
// tea has never stored is an idempotent no-op (no delete invoked).
func TestCLILogout_Tea_NoMatchingLogin_NoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := stubPath(t)
	rec := filepath.Join(t.TempDir(), "rec")
	stubCLI(t, dir, "tea", `case "$1 $2" in
"logins list") echo '[]' ;;
"login delete") printf 'deleted:%s\n' "$3" >> `+rec+` ;;
esac`)

	if err := cliLogout(context.Background(), KindGitea, "unknown.example"); err != nil {
		t.Fatalf("cliLogout: %v", err)
	}
	if got := readRecord(t, rec); got != "" {
		t.Errorf("delete should not run for an unknown host, record:\n%s", got)
	}
}

// TestCLILogout_NotLoggedIn_Idempotent verifies the "already logged
// out" CLI reply maps to success (disconnect is idempotent).
func TestCLILogout_NotLoggedIn_Idempotent(t *testing.T) {
	dir := stubPath(t)
	stubCLI(t, dir, "gh", `echo "you are not logged in to any hosts" >&2; exit 1`)

	if err := cliLogout(context.Background(), KindGitHub, "github.com"); err != nil {
		t.Errorf("cliLogout on not-logged-in should be nil, got %v", err)
	}
}

// TestCLILogin_EmptyToken_And_MissingHost pins the validation prelude.
func TestCLILogin_EmptyToken_And_MissingHost(t *testing.T) {
	if err := cliLogin(context.Background(), KindGitHub, "github.com", ""); err == nil {
		t.Error("empty token should error")
	}
	if err := cliLogin(context.Background(), KindGitea, "", "tok"); err == nil {
		t.Error("gitea without a host should error (no default host)")
	}
}

// TestErrSentinelsAliasCliexec pins the sentinel aliasing: the package
// previously declared SEPARATE errors.New values with the same message
// as cliexec's, so errors.Is against the forges symbols never matched
// what runCmd actually returns.
func TestErrSentinelsAliasCliexec(t *testing.T) {
	dir := stubPath(t) // empty PATH: no CLIs
	_ = dir
	err := cliLogin(context.Background(), KindGitHub, "github.com", "tok")
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("missing CLI should map to forges.ErrNotInstalled, got %v", err)
	}
}
