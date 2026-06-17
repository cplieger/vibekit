package git

// Round-2 mutant-killing test for package internal/git.
// Test-only; identifiers prefixed gk_vibekit_r2_ / TestGKVibekitR2_.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gk_vibekit_r2_git runs git in dir with an isolated, deterministic
// identity (mirrors the u20 helper) and fails the test on a non-zero exit.
func gk_vibekit_r2_git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Kills handlers_ai.go:46:9 (NEGATION `err != nil`) and :46:42
// (NEGATION `strings.TrimSpace(out) == ""`) in getRecentCommits. The
// guard is `if err != nil || strings.TrimSpace(out) == ""`. Against a
// real repo with a commit, gitCmd succeeds (err==nil) and the log is
// non-empty, so the whole guard is false and the commit subject is
// returned. Flipping either operand makes the guard true and returns
// the "No commit history available" sentinel — caught here.
func TestGKVibekitR2_GetRecentCommits_ReturnsHistoryForRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	gk_vibekit_r2_git(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	gk_vibekit_r2_git(t, dir, "add", "f.txt")
	gk_vibekit_r2_git(t, dir, "commit", "-q", "-m", "seed commit subject")

	got := getRecentCommits(context.Background(), dir, 10)
	if !strings.Contains(got, "seed commit subject") {
		t.Errorf("getRecentCommits(repo with a commit) = %q, want it to contain the commit subject", got)
	}
	if got == "No commit history available" {
		t.Errorf("getRecentCommits(repo with a commit) returned the empty-history sentinel")
	}

	// Negative direction (keeps the guard honest): a non-repo directory
	// makes gitCmd error, so the sentinel is the correct result.
	if got := getRecentCommits(context.Background(), t.TempDir(), 10); got != "No commit history available" {
		t.Errorf("getRecentCommits(non-repo dir) = %q, want the sentinel", got)
	}
}
