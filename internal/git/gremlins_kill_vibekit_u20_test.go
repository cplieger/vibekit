package git

// Mutant-killing tests for unit vibekit-u20 (package internal/git).
//
// Each test targets a specific surviving gremlins mutant by asserting an
// observable whose expected value depends on the exact operator at the
// mutated site. Helpers/identifiers are prefixed gk_vibekit_u20_ to avoid
// collisions with sibling units sharing this package.
//
// Mutant map (file:line TYPE -> test):
//   ai_prompts.go:145:55      CONDITIONALS_BOUNDARY -> ExtractCommitMessage_WordBreakBoundary
//   errors.go:37:12           CONDITIONALS_NEGATION -> WriteGitError_DetailField
//   errors.go:73:82           CONDITIONALS_NEGATION -> GitShowCmd_MissingPathInRealRepo
//   handlers.go:143:37        CONDITIONALS_BOUNDARY -> HandlePRFetch_NumberBoundary
//   handlers_ai.go:77:43      CONDITIONALS_NEGATION -> HandleCommitMessage_StagedGate
//   handlers_ai.go:136:43     CONDITIONALS_NEGATION -> HandlePRDescription_DiffGate
//   handlers_ai.go:139:44     CONDITIONALS_NEGATION -> HandlePRDescription_FallbackDiffGate
//   handlers_repo.go:148:70   CONDITIONALS_NEGATION -> HandleLog_RefResolution
//   handlers_repo.go:148:87   CONDITIONALS_NEGATION -> HandleLog_RefResolution
//   handlers_repo.go:166:10   CONDITIONALS_NEGATION -> HandleLog_RemoteErrorLog
//   handlers_repo.go:264:35   CONDITIONALS_NEGATION -> HandleRemove_SuccessBody
//   handlers_staging.go:25:87 CONDITIONALS_NEGATION -> HandleStatus_QuickControlsFetch
//   handlers_staging.go:42:70 CONDITIONALS_NEGATION -> CollectStatus_RemoteSet
//   handlers_staging.go:82:58 CONDITIONALS_NEGATION -> CollectStatus_StashCount
//   handlers_staging.go:82:72 CONDITIONALS_NEGATION -> CollectStatus_StashCount
//   handlers_staging.go:93:40 CONDITIONALS_NEGATION -> CollectStatus_HasGH
//   parse.go:54:16            CONDITIONALS_BOUNDARY -> ParseGitStatusOutput_LenBoundary
//   repos.go:42:18            CONDITIONALS_BOUNDARY -> DiscoverRepos_EntryCapBoundary
//   validate.go:34:16         CONDITIONALS_BOUNDARY -> SanitizeRepoPaths_CountBoundary
//   validate.go:66:56         CONDITIONALS_BOUNDARY -> ValidateFilePath_ControlCharBoundary
//
// Equivalent (documented, not killed):
//   handlers_repo.go:170:99   / handlers_staging.go:64:101  -- rev-list is NOT in
//     gitexec's allowedSubcommands, so gitCmd(...,"rev-list",...) is gated to
//     /bin/false and always errors with empty output. The `err == nil` success
//     branch is dead; the mutation `err != nil` enters a block that parses ""
//     (strings.Fields("") is empty, len != 2) so ahead/behind stay 0 either way.
//     Verified empirically: collectStatus / handleLog report Behind=0, Ahead=0.
//   handlers_repo.go:200:27   -- `git branch ... --format=%(refname:short)\t%(HEAD)`
//     emits a literal tab in every line, so SplitN(line,"\t",2) always yields
//     len(parts)==2 for the non-empty lines the loop processes. `len(parts) > 1`
//     and `>= 1` differ only at len==1, which is unreachable; result identical.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/gitexec"
	"golang.org/x/sync/singleflight"
)

// --- helpers ---

func gk_vibekit_u20_skipNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// gk_vibekit_u20_git runs a git command in dir with an isolated config
// environment (mirrors the existing initFixtureRepo helper), failing the
// test on a non-zero exit.
func gk_vibekit_u20_git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gk_vibekit_u20_writeCommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gk_vibekit_u20_git(t, dir, "add", file)
	gk_vibekit_u20_git(t, dir, "commit", "-q", "-m", msg)
}

// gk_vibekit_u20_captureLogs swaps the slog default to a buffer-backed
// debug handler for the duration of the test and restores it on cleanup.
// Safe because the git package's tests never run in parallel.
func gk_vibekit_u20_captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// gk_vibekit_u20_behindRepo builds a work tree whose HEAD is at C1 while
// origin/main has been advanced to C2 (then fetched). The local branch
// "main" tracks origin/main. Returns the work tree path.
func gk_vibekit_u20_behindRepo(t *testing.T) string {
	t.Helper()
	gk_vibekit_u20_skipNoGit(t)
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	work := filepath.Join(base, "work")
	if err := os.Mkdir(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, origin) // origin main @ C1 ("initial commit")
	gk_vibekit_u20_git(t, base, "clone", "-q", origin, work)
	gk_vibekit_u20_writeCommit(t, origin, "README.md", "second\n", "second commit") // C2
	gk_vibekit_u20_git(t, work, "fetch", "-q", "origin")                            // origin/main -> C2
	return work
}

// --- ai_prompts.go:145:55 CONDITIONALS_BOUNDARY (idx > 30 vs idx >= 30) ---

func Test_gk_vibekit_u20_ExtractCommitMessage_WordBreakBoundary(t *testing.T) {
	// Subject longer than 72 chars whose only space within subject[:69]
	// sits at index exactly 30, so LastIndex == 30 (the boundary).
	input := strings.Repeat("A", 30) + " " + strings.Repeat("B", 45)
	got := extractCommitMessage(input)
	// original `idx > 30`: 30 is NOT > 30 -> else branch -> subject[:69]+"...".
	want := strings.Repeat("A", 30) + " " + strings.Repeat("B", 38) + "..."
	// mutant `idx >= 30`: 30 >= 30 -> subject[:30]+"..." -> "A"*30+"...", which differs.
	if got != want {
		t.Errorf("extractCommitMessage(boundary idx==30) = %q, want %q (CONDITIONALS_BOUNDARY idx>30 vs idx>=30)", got, want)
	}
}

// --- errors.go:37:12 CONDITIONALS_NEGATION (detail != "" vs detail == "") ---

func Test_gk_vibekit_u20_WriteGitError_DetailField(t *testing.T) {
	// Non-empty detail must be present (original sets it; mutant `==` drops it).
	rec := httptest.NewRecorder()
	writeGitError(rec, KindShowFailed, "boundary-detail")
	var m map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["error"] != string(KindShowFailed) {
		t.Errorf("error = %q, want %q", m["error"], string(KindShowFailed))
	}
	if m["detail"] != "boundary-detail" {
		t.Errorf("detail = %q, want %q (CONDITIONALS_NEGATION detail!=\"\" vs ==\"\")", m["detail"], "boundary-detail")
	}

	// Empty detail must be absent (original skips it; mutant `==` sets detail="").
	rec2 := httptest.NewRecorder()
	writeGitError(rec2, KindShowFailed, "")
	var m2 map[string]string
	if err := json.Unmarshal(rec2.Body.Bytes(), &m2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m2["detail"]; ok {
		t.Errorf("detail key present for empty detail (CONDITIONALS_NEGATION sets detail on empty input)")
	}
}

// --- errors.go:73:82 CONDITIONALS_NEGATION (ExitCode() == 128 vs != 128) ---

func Test_gk_vibekit_u20_GitShowCmd_MissingPathInRealRepo(t *testing.T) {
	gk_vibekit_u20_skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir) // real repo, only README.md committed
	// `git show HEAD:does-not-exist.txt` exits 128 and the output does NOT
	// contain "not a git repository".
	out, err := gitShowCmd(context.Background(), dir, refHEAD, "does-not-exist.txt")
	// original `== 128`: classified as ErrPathNotInRef with empty output.
	// mutant `!= 128`: falls through, returning the raw *exec.ExitError + output.
	if !errors.Is(err, ErrPathNotInRef) {
		t.Errorf("err = %v, want ErrPathNotInRef (CONDITIONALS_NEGATION ExitCode()==128 vs !=128)", err)
	}
	if out != "" {
		t.Errorf("out = %q, want empty for path-not-in-ref", out)
	}
}

// --- handlers.go:143:37 CONDITIONALS_BOUNDARY (Number > 10_000_000 vs >=) ---

func Test_gk_vibekit_u20_HandlePRFetch_NumberBoundary(t *testing.T) {
	h := NewHandler(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-fetch", strings.NewReader(`{"number":10000000}`))
	rec := httptest.NewRecorder()
	h.handlePRFetch(rec, req)
	// 10_000_000 is the exact cap. original `> 10_000_000` accepts it (passes the
	// guard, then fails the later remote lookup -> 200, not a BadRequest). mutant
	// `>= 10_000_000` rejects it -> 400 "invalid PR number".
	if rec.Code == http.StatusBadRequest {
		t.Errorf("code = 400 at number==10_000_000 (CONDITIONALS_BOUNDARY > vs >=); body = %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "invalid PR number") {
		t.Errorf("boundary number rejected as invalid (CONDITIONALS_BOUNDARY > vs >=); body = %s", rec.Body.String())
	}
}

// --- handlers_ai.go:77:43 CONDITIONALS_NEGATION (TrimSpace(diff) == "" vs !=) ---

func Test_gk_vibekit_u20_HandleCommitMessage_StagedGate(t *testing.T) {
	gk_vibekit_u20_skipNoGit(t)

	t.Run("clean_repo_reports_no_staged", func(t *testing.T) {
		dir := t.TempDir()
		initFixtureRepo(t, dir) // clean: nothing staged, real repo (err==nil)
		mp := &mockPrompter{result: "feat: gk commit"}
		a := NewAIHandler(dir, mp)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/git/commit-message", strings.NewReader(`{}`))
		a.handleCommitMessage(rec, req)
		// original `== ""`: empty staged diff -> no_staged_changes, prompter NOT called.
		// mutant `!= ""`: condition false -> proceeds and calls the prompter.
		if mp.called {
			t.Errorf("prompter called on clean repo (CONDITIONALS_NEGATION TrimSpace(diff)==\"\" vs !=\"\")")
		}
		if !strings.Contains(rec.Body.String(), "no_staged_changes") {
			t.Errorf("body = %q, want no_staged_changes", rec.Body.String())
		}
	})

	t.Run("staged_repo_generates_message", func(t *testing.T) {
		dir := t.TempDir()
		initFixtureRepo(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gk_vibekit_u20_git(t, dir, "add", "README.md") // stage a real change
		mp := &mockPrompter{result: "feat: gk commit"}
		a := NewAIHandler(dir, mp)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/git/commit-message", strings.NewReader(`{}`))
		a.handleCommitMessage(rec, req)
		// original `== ""`: non-empty staged diff -> proceeds, prompter called, output returned.
		// mutant `!= ""`: condition true -> no_staged_changes, prompter NOT called.
		if !mp.called {
			t.Errorf("prompter not called on staged repo (CONDITIONALS_NEGATION TrimSpace(diff)==\"\" vs !=\"\")")
		}
		var resp map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp["output"] != "feat: gk commit" {
			t.Errorf("output = %q, want %q; body = %s", resp["output"], "feat: gk commit", rec.Body.String())
		}
	})
}

// --- handlers_ai.go:136:43 CONDITIONALS_NEGATION (TrimSpace(diff) == "" vs !=) ---

func Test_gk_vibekit_u20_HandlePRDescription_DiffGate(t *testing.T) {
	gk_vibekit_u20_skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir) // main @ C1
	gk_vibekit_u20_git(t, dir, "checkout", "-q", "-b", "feature")
	gk_vibekit_u20_writeCommit(t, dir, "README.md", "changed\n", "feature commit") // C2 on feature
	mp := &mockPrompter{result: "My PR description"}
	a := NewAIHandler(dir, mp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-description", strings.NewReader(`{}`))
	a.handlePRDescription(rec, req)
	// base="main"; `git diff main...HEAD` is non-empty (feature ahead), err==nil.
	// original `== ""`: false -> skip fallback -> proceed -> output, prompter called.
	// mutant `!= ""`: true -> enter fallback -> `git diff origin/main...HEAD` errors
	//   (no origin) -> no_changes, prompter NOT called.
	if !mp.called {
		t.Errorf("prompter not called with a non-empty base diff (CONDITIONALS_NEGATION ==\"\" vs !=\"\")")
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["output"] != "My PR description" {
		t.Errorf("output = %q, want %q; body = %s", resp["output"], "My PR description", rec.Body.String())
	}
}

// --- handlers_ai.go:139:44 CONDITIONALS_NEGATION (fallback TrimSpace(diff) == "" vs !=) ---

func Test_gk_vibekit_u20_HandlePRDescription_FallbackDiffGate(t *testing.T) {
	gk_vibekit_u20_skipNoGit(t)
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	work := filepath.Join(base, "work")
	if err := os.Mkdir(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, origin)                                                   // origin main @ C1
	gk_vibekit_u20_git(t, base, "clone", "-q", origin, work)                     // work main @ C1, origin/main @ C1
	gk_vibekit_u20_writeCommit(t, work, "README.md", "changed\n", "work commit") // work main @ C2 == HEAD
	mp := &mockPrompter{result: "My PR description"}
	a := NewAIHandler(work, mp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-description", strings.NewReader(`{}`))
	a.handlePRDescription(rec, req)
	// base="main" == HEAD -> `git diff main...HEAD` empty -> outer block entered.
	// fallback `git diff origin/main...HEAD` is non-empty (origin/main @ C1 differs), err==nil.
	// original line-139 `== ""`: false -> skip no_changes -> proceed -> output, prompter called.
	// mutant `!= ""`: true -> no_changes, prompter NOT called.
	if !mp.called {
		t.Errorf("prompter not called with non-empty fallback diff (CONDITIONALS_NEGATION ==\"\" vs !=\"\")")
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["output"] != "My PR description" {
		t.Errorf("output = %q, want %q; body = %s", resp["output"], "My PR description", rec.Body.String())
	}
}

// --- handlers_repo.go:148:70 (err == nil) + 148:87 (branch != "") CONDITIONALS_NEGATION ---

func Test_gk_vibekit_u20_HandleLog_RefResolution(t *testing.T) {
	work := gk_vibekit_u20_behindRepo(t) // HEAD @ "initial commit"; origin/main @ "second commit"
	h := NewHandler(work)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/git/log", nil)
	h.handleLog(rec, req)
	var resp struct {
		Entries []string `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// original: branch resolves AND origin/main verifies -> ref = origin/main ->
	//   log shows both commits (2 entries, incl. "second commit").
	// mutant 148:70 (err==nil -> err!=nil) OR 148:87 (branch!="" -> branch=="")
	//   skips the block -> ref = HEAD -> log shows only "initial commit" (1 entry).
	if len(resp.Entries) != 2 {
		t.Fatalf("entries = %v (len %d), want 2 (CONDITIONALS_NEGATION on branch-resolution guard)", resp.Entries, len(resp.Entries))
	}
	if !strings.Contains(strings.Join(resp.Entries, "\n"), "second commit") {
		t.Errorf("entries %v missing 'second commit' (origin/main ref not used)", resp.Entries)
	}
}

// --- handlers_repo.go:166:10 CONDITIONALS_NEGATION (rErr != nil vs == nil) ---

func Test_gk_vibekit_u20_HandleLog_RemoteErrorLog(t *testing.T) {
	t.Run("no_origin_logs_failure", func(t *testing.T) {
		gk_vibekit_u20_skipNoGit(t)
		dir := t.TempDir()
		initFixtureRepo(t, dir) // no remote configured -> get-url fails (rErr != nil)
		buf := gk_vibekit_u20_captureLogs(t)
		h := NewHandler(dir)
		h.handleLog(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/git/log", nil))
		// original `rErr != nil`: true -> debug log emitted.
		// mutant `rErr == nil`: false -> no log.
		if !strings.Contains(buf.String(), "git remote get-url failed during log") {
			t.Errorf("expected debug log for failed remote get-url (CONDITIONALS_NEGATION rErr!=nil vs ==nil)")
		}
	})
	t.Run("with_origin_no_failure_log", func(t *testing.T) {
		gk_vibekit_u20_skipNoGit(t)
		dir := t.TempDir()
		initFixtureRepo(t, dir)
		gk_vibekit_u20_git(t, dir, "remote", "add", "origin", "https://example.com/x.git")
		buf := gk_vibekit_u20_captureLogs(t)
		h := NewHandler(dir)
		h.handleLog(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/git/log", nil))
		// original `rErr != nil`: false (get-url succeeds) -> no log.
		// mutant `rErr == nil`: true -> log emitted.
		if strings.Contains(buf.String(), "git remote get-url failed during log") {
			t.Errorf("unexpected failure log when remote get-url succeeds (CONDITIONALS_NEGATION rErr!=nil vs ==nil)")
		}
	})
}

// --- handlers_repo.go:264:35 CONDITIONALS_NEGATION (err != nil vs == nil) ---

func Test_gk_vibekit_u20_HandleRemove_SuccessBody(t *testing.T) {
	workDir := t.TempDir()
	sub := filepath.Join(workDir, "to-remove")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(workDir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/remove", strings.NewReader(`{"repo":"to-remove"}`))
	h.handleRemove(rec, req)
	body := rec.Body.String()
	// os.RemoveAll succeeds (the removal runs in the init statement either way).
	// original `err != nil`: false -> Ok {"ok":true}.
	// mutant `err == nil`: true -> writes {"error":"remove failed"}.
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("body = %q, want ok:true on successful remove (CONDITIONALS_NEGATION err!=nil vs ==nil)", body)
	}
	if strings.Contains(body, "remove failed") {
		t.Errorf("body = %q contains 'remove failed' on a successful remove (CONDITIONALS_NEGATION err!=nil vs ==nil)", body)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("subdir still exists after remove: %v", err)
	}
}

// --- handlers_staging.go:25:87 CONDITIONALS_NEGATION (Get("quick") == "" vs !=) ---

func Test_gk_vibekit_u20_HandleStatus_QuickControlsFetch(t *testing.T) {
	// A repo with a bogus local-path remote: collectStatus's `git fetch --quiet`
	// errors ("transport 'file' not allowed") and logs "git fetch during status
	// failed" -- but ONLY when doFetch is true.
	mk := func(t *testing.T) string {
		t.Helper()
		gk_vibekit_u20_skipNoGit(t)
		dir := t.TempDir()
		initFixtureRepo(t, dir)
		gk_vibekit_u20_git(t, dir, "remote", "add", "origin", "/nonexistent-gk-vibekit-u20")
		return dir
	}

	t.Run("no_quick_fetches", func(t *testing.T) {
		dir := mk(t)
		buf := gk_vibekit_u20_captureLogs(t)
		h := NewHandler(dir)
		h.handleStatus(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/git/status", nil))
		// no "quick" -> original doFetch=("" == "")=true -> fetch attempted -> log.
		// mutant doFetch=("" != "")=false -> no fetch -> no log.
		if !strings.Contains(buf.String(), "git fetch during status failed") {
			t.Errorf("expected a fetch attempt without quick param (CONDITIONALS_NEGATION Get(\"quick\")==\"\" vs !=\"\")")
		}
	})

	t.Run("quick_skips_fetch", func(t *testing.T) {
		dir := mk(t)
		buf := gk_vibekit_u20_captureLogs(t)
		h := NewHandler(dir)
		h.handleStatus(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/git/status?quick=1", nil))
		// quick=1 -> original doFetch=("1" == "")=false -> no fetch -> no log.
		// mutant doFetch=("1" != "")=true -> fetch attempted -> log.
		if strings.Contains(buf.String(), "git fetch during status failed") {
			t.Errorf("unexpected fetch with quick param (CONDITIONALS_NEGATION Get(\"quick\")==\"\" vs !=\"\")")
		}
	})
}

// --- handlers_staging.go:42:70 CONDITIONALS_NEGATION (get-url err == nil vs !=) ---

func Test_gk_vibekit_u20_CollectStatus_RemoteSet(t *testing.T) {
	gk_vibekit_u20_skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	const url = "https://example.com/x.git"
	gk_vibekit_u20_git(t, dir, "remote", "add", "origin", url)
	st := collectStatus(context.Background(), dir, gitexec.DefaultTimeouts(), &singleflight.Group{}, false)
	// original `err == nil`: get-url succeeds -> Remote set to the URL.
	// mutant `err != nil`: condition false -> Remote left "".
	if st.Remote != url {
		t.Errorf("Remote = %q, want %q (CONDITIONALS_NEGATION get-url err==nil vs !=nil)", st.Remote, url)
	}
}

// --- handlers_staging.go:82:58 (err == nil) + 82:72 (out != "") CONDITIONALS_NEGATION ---

func Test_gk_vibekit_u20_CollectStatus_StashCount(t *testing.T) {
	gk_vibekit_u20_skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gk_vibekit_u20_git(t, dir, "stash") // exactly one stash, clean working tree
	st := collectStatus(context.Background(), dir, gitexec.DefaultTimeouts(), &singleflight.Group{}, false)
	// `git stash list` succeeds (err==nil) with one non-empty line -> stashes = 1.
	// mutant 82:58 (err==nil -> err!=nil): condition false -> stashes stays 0.
	// mutant 82:72 (out!="" -> out==""): condition false -> stashes stays 0.
	if st.Stashes != 1 {
		t.Errorf("Stashes = %d, want 1 (CONDITIONALS_NEGATION stash-list err==nil / out!=\"\")", st.Stashes)
	}
}

// --- handlers_staging.go:93:40 CONDITIONALS_NEGATION (LookPath gh err == nil vs !=) ---

func Test_gk_vibekit_u20_CollectStatus_HasGH(t *testing.T) {
	mkRepo := func(t *testing.T) string {
		t.Helper()
		repo := t.TempDir()
		// A bare .git directory is enough for IsGitRepo (os.Stat-based);
		// the gh lookup is reached regardless of git availability.
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		return repo
	}

	t.Run("gh_absent", func(t *testing.T) {
		repo := mkRepo(t)
		t.Setenv("PATH", t.TempDir()) // empty bin dir: gh not found
		st := collectStatus(context.Background(), repo, gitexec.DefaultTimeouts(), &singleflight.Group{}, false)
		// original `err == nil`: gh not found -> HasGH false.
		// mutant `err != nil`: gh not found -> HasGH true.
		if st.HasGH {
			t.Errorf("HasGH = true with gh absent (CONDITIONALS_NEGATION LookPath err==nil vs !=nil)")
		}
	})

	t.Run("gh_present", func(t *testing.T) {
		repo := mkRepo(t)
		binDir := t.TempDir()
		gh := filepath.Join(binDir, "gh")
		if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir)
		st := collectStatus(context.Background(), repo, gitexec.DefaultTimeouts(), &singleflight.Group{}, false)
		// original: gh found -> HasGH true. mutant: HasGH false.
		if !st.HasGH {
			t.Errorf("HasGH = false with gh present (CONDITIONALS_NEGATION LookPath err==nil vs !=nil)")
		}
	})
}

// --- parse.go:54:16 CONDITIONALS_BOUNDARY (len(line) < 3 vs <= 3) ---

func Test_gk_vibekit_u20_ParseGitStatusOutput_LenBoundary(t *testing.T) {
	// "??x" is exactly 3 bytes and survives TrimRight (ends in non-space).
	// original `< 3`: 3 is NOT < 3 -> the line is processed (path = line[3:] = "").
	// mutant `<= 3`: 3 <= 3 -> the line is skipped -> zero entries.
	got := parseGitStatusOutput([]byte("??x"))
	if len(got) != 1 {
		t.Fatalf("parseGitStatusOutput(\"??x\") len = %d, want 1 (CONDITIONALS_BOUNDARY len(line)<3 vs <=3)", len(got))
	}
	if got[0].Status != "?" || got[0].Display != "Untracked" || got[0].Path != "" {
		t.Errorf("entry = %+v, want {Path:\"\" Status:\"?\" Display:\"Untracked\"}", got[0])
	}
}

// --- repos.go:42:18 CONDITIONALS_BOUNDARY (len(entries) > maxRepoEntries vs >=) ---

func Test_gk_vibekit_u20_DiscoverRepos_EntryCapBoundary(t *testing.T) {
	workDir := t.TempDir() // not a git repo
	for i := 0; i < maxRepoEntries; i++ {
		f := filepath.Join(workDir, fmt.Sprintf("e%05d", i))
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	buf := gk_vibekit_u20_captureLogs(t)
	_ = discoverRepos(context.Background(), workDir)
	// At exactly maxRepoEntries the result is identical either way (the slice
	// cap is a no-op), but the warning differs:
	// original `len > cap`: 1024 > 1024 is false -> no warning.
	// mutant `len >= cap`: 1024 >= 1024 is true -> "entry count exceeds cap" warning.
	if strings.Contains(buf.String(), "entry count exceeds cap") {
		t.Errorf("cap warning fired at exactly maxRepoEntries (CONDITIONALS_BOUNDARY len>cap vs len>=cap)")
	}
}

// --- validate.go:34:16 CONDITIONALS_BOUNDARY (len(paths) > maxRepoPaths vs >=) ---

func Test_gk_vibekit_u20_SanitizeRepoPaths_CountBoundary(t *testing.T) {
	paths := make([]string, maxRepoPaths) // exactly the cap
	for i := range paths {
		paths[i] = "f" + strconv.Itoa(i)
	}
	got, err := sanitizeRepoPaths(paths)
	// original `len > cap`: 1024 > 1024 is false -> no error, all paths returned.
	// mutant `len >= cap`: 1024 >= 1024 is true -> "too many paths" error.
	if err != nil {
		t.Fatalf("err = %v at exactly maxRepoPaths (CONDITIONALS_BOUNDARY len>cap vs len>=cap)", err)
	}
	if len(got) != maxRepoPaths {
		t.Errorf("len(got) = %d, want %d", len(got), maxRepoPaths)
	}
	// One over the cap errors under both versions (confirms the cap fires above
	// the boundary; not a distinguishing assertion).
	if _, err := sanitizeRepoPaths(make([]string, maxRepoPaths+1)); err == nil {
		t.Errorf("expected an error for maxRepoPaths+1 paths")
	}
}

// --- validate.go:66:56 CONDITIONALS_BOUNDARY (r < 0x20 vs r <= 0x20) ---

func Test_gk_vibekit_u20_ValidateFilePath_ControlCharBoundary(t *testing.T) {
	// rune 0x20 (space) is the boundary:
	// original `r < 0x20`: a space is NOT a control char -> path accepted.
	// mutant `r <= 0x20`: a space is treated as a control char -> path rejected.
	if !validateFilePath("foo bar") {
		t.Errorf("validateFilePath(\"foo bar\") = false, want true (CONDITIONALS_BOUNDARY r<0x20 vs r<=0x20)")
	}
	// Genuine control chars (< 0x20) and DEL (0x7f) are rejected by both versions.
	if validateFilePath("foo\x1fbar") {
		t.Errorf("validateFilePath with 0x1f control char = true, want false")
	}
	if validateFilePath("foo\x7fbar") {
		t.Errorf("validateFilePath with 0x7f (DEL) = true, want false")
	}
}
