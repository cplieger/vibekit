package git

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/gitexec"
	"pgregory.net/rapid"
)

func TestRepoDir(t *testing.T) {
	workDir := filepath.FromSlash("/home/user/work")
	h := &Handler{workDir: workDir}

	tests := []struct {
		name string
		repo string
		want string
	}{
		{"empty string falls back to workDir", "", workDir},
		{"dot falls back to workDir", ".", workDir},
		{"parent traversal rejected", "..", workDir},
		{"nested parent traversal rejected", "foo/../../etc", workDir},
		{"any parent segment rejected", "child/../sibling", workDir},
		{"absolute unix path rejected", "/etc/passwd", workDir},
		{"normal subdir joined", "project", filepath.Join(workDir, "project")},
		{"nested subdir joined", "a/b/c", filepath.Join(workDir, filepath.Clean("a/b/c"))},
		{"trailing slash cleaned", "project/", filepath.Join(workDir, "project")},
		{"multi-slash collapsed", "a//b", filepath.Join(workDir, filepath.Clean("a/b"))},
		{"single-dot segment cleaned", "a/./b", filepath.Join(workDir, filepath.Clean("a/b"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.repoDir(tt.repo)
			if got != tt.want {
				t.Errorf("repoDir(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}

func TestRepoDir_NeverEscapesWorkDir(t *testing.T) {
	// Fuzzy invariant: for any input the parser accepts as a subdir, the
	// resolved path must be lexically rooted at workDir.
	workDir := filepath.FromSlash("/home/user/work")
	h := &Handler{workDir: workDir}
	inputs := []string{
		"", ".", "..", "../../etc/passwd", "/absolute", "/etc",
		"project", "a/b", "a/../b", "./legit", "legit/./ok",
		"legit//double", "trailing/",
	}
	for _, in := range inputs {
		got := h.repoDir(in)
		// Must either equal workDir or have workDir as a lexical prefix.
		if got != workDir {
			rel, err := filepath.Rel(workDir, got)
			if err != nil {
				t.Errorf("repoDir(%q) = %q: not relative to workDir: %v", in, got, err)
				continue
			}
			if rel == ".." || (len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)) {
				t.Errorf("repoDir(%q) = %q: escapes workDir (rel=%q)", in, got, rel)
			}
		}
	}
}

func TestStatusLabel(t *testing.T) {
	tests := []struct {
		want string
		in   byte
	}{
		{in: 'M', want: "Modified"},
		{in: 'A', want: "Added"},
		{in: 'D', want: "Deleted"},
		{in: 'R', want: "Renamed"},
		{in: 'C', want: "Copied"},
		{in: 'U', want: "Unmerged"},
		{in: '?', want: "Untracked"},
		{in: ' ', want: "Unknown"},
		{in: 'X', want: "Unknown"},
		{in: 'Z', want: "Unknown"},
		{in: 0, want: "Unknown"},
	}
	for _, tt := range tests {
		got := statusLabel(tt.in)
		if got != tt.want {
			t.Errorf("statusLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseGitStatusOutput(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want []gitFile
	}{
		{"nil input", nil, nil},
		{"empty input", []byte(""), nil},
		{"whitespace only", []byte("  \n\t\r\n"), nil},
		{"unstaged modified", []byte(" M file.go\n"), []gitFile{
			{Path: "file.go", Status: "M", Display: "Modified"},
		}},
		{"staged added", []byte("A  new.go\n"), []gitFile{
			{Path: "new.go", Status: "A", Display: "Added", Staged: true},
		}},
		{"staged and unstaged emits two entries", []byte("MM both.go\n"), []gitFile{
			{Path: "both.go", Status: "M", Display: "Modified", Staged: true},
			{Path: "both.go", Status: "M", Display: "Modified", Staged: false},
		}},
		{"untracked", []byte("?? newfile.go\n"), []gitFile{
			{Path: "newfile.go", Status: "?", Display: "Untracked"},
		}},
		{"rename", []byte("R  old.go -> new.go\n"), []gitFile{
			{Path: "new.go", Status: "R", Display: "Renamed", Staged: true},
		}},
		{"directory entry skipped", []byte("?? somedir/\n?? real.txt\n"), []gitFile{
			{Path: "real.txt", Status: "?", Display: "Untracked"},
		}},
		{"short lines skipped", []byte("XY\n M file.go\n?\n"), []gitFile{
			{Path: "file.go", Status: "M", Display: "Modified"},
		}},
		{"multiple entries", []byte("M  staged.go\n M unstaged.go\n?? untracked.go\nA  added.go\n"), []gitFile{
			{Path: "staged.go", Status: "M", Display: "Modified", Staged: true},
			{Path: "unstaged.go", Status: "M", Display: "Modified"},
			{Path: "untracked.go", Status: "?", Display: "Untracked"},
			{Path: "added.go", Status: "A", Display: "Added", Staged: true},
		}},
		{"trailing whitespace trimmed", []byte(" M file.go\n\n\t\r\n"), []gitFile{
			{Path: "file.go", Status: "M", Display: "Modified"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGitStatusOutput(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseGitStatusOutput(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRepoFromQuery(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"missing query param", "/api/git/status", ""},
		{"empty query param", "/api/git/status?repo=", ""},
		{"populated query param", "/api/git/status?repo=project", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			got := repoFromQuery(req)
			if got != tt.want {
				t.Errorf("repoFromQuery(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestNewHandler_Fields(t *testing.T) {
	h := NewHandler("/work")
	if h.workDir != "/work" {
		t.Errorf("workDir = %q, want %q", h.workDir, "/work")
	}
}

func TestHandleShow_Rejections(t *testing.T) {
	type testCase struct {
		name    string
		path    string
		ref     string
		rawPath bool // if true, set path via RawQuery to bypass net/url escaping
		rawRef  bool // if true, set ref via RawQuery to bypass net/url escaping
	}
	cases := []testCase{
		// Missing/invalid path
		{name: "missing_path", path: "", ref: ""},
		// Invalid refs
		{name: "ref_with_space", path: "foo", ref: "bad ref"},
		{name: "ref_starting_with_dash", path: "foo", ref: "-exec"},
		{name: "ref_carriage_return", path: "foo", ref: "HEAD\rmalicious", rawRef: true},
		{name: "ref_null_byte", path: "foo", ref: "HEAD\x00abc", rawRef: true},
		{name: "ref_colon_metachar", path: "foo", ref: "HEAD:inject", rawRef: true},
		{name: "ref_asterisk_metachar", path: "foo", ref: "HEAD*", rawRef: true},
		// Path traversal
		{name: "path_traversal_parent", path: "../../etc/passwd"},
		{name: "path_traversal_dotdot", path: ".."},
		{name: "path_traversal_nested", path: "a/../../x"},
		{name: "path_absolute", path: "/etc/passwd"},
		{name: "path_leading_dash", path: "-evil"},
		// Path control bytes
		{name: "path_null_byte", path: "has\x00null", rawPath: true},
		{name: "path_newline", path: "has\nnewline", rawPath: true},
		{name: "path_cr", path: "has\rcr", rawPath: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(t.TempDir())
			req := httptest.NewRequest(http.MethodGet, "/api/git/show", nil)
			q := req.URL.Query()
			if tc.path != "" {
				q.Set("path", tc.path)
			}
			if tc.ref != "" {
				q.Set("ref", tc.ref)
			}
			if tc.rawPath || tc.rawRef {
				req.URL.RawQuery = q.Encode()
			} else {
				req.URL.RawQuery = q.Encode()
			}
			rec := httptest.NewRecorder()
			h.handleShow(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("code = %d, want 400", rec.Code)
			}
		})
	}
}

func TestHandleShow_MissingFileReturnsEmptyContent(t *testing.T) {
	// Non-repo tempdir: git errors with "fatal: not a git repository",
	// which no longer matches any "missing file" marker (we dropped
	// the broad "fatal: path " prefix in cycle u10c1 because it
	// swallowed unrelated path errors). It now falls through to the
	// slog.Warn + show-failed branch. Both branches still emit
	// `{"content":""}`; the assertion pins the branch via the
	// `error` field so a future regression that re-widens the missing-
	// file allowlist doesn't silently hide a real failure.
	h := NewHandler(t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/api/git/show?path=nonexistent", nil)
	rec := httptest.NewRecorder()
	h.handleShow(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error":"show_failed"`) {
		t.Errorf("body = %q, want show-failed error marker", body)
	}
}

// --- pure function coverage ---

func TestExtractCommitMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty input yields empty", "", ""},
		{"plain single line passes through", "feat: add widget", "feat: add widget"},
		{"strips triple-backtick fences", "```\nfeat: add widget\n```", "feat: add widget"},
		{"strips uppercase prefix", "COMMIT MESSAGE: feat: add widget", "feat: add widget"},
		{"strips lowercase prefix", "commit message: feat: add widget", "feat: add widget"},
		{"strips titlecase prefix", "Commit Message: feat: add widget", "feat: add widget"},
		{"strips surrounding double quotes", `"feat: add widget"`, "feat: add widget"},
		{"strips surrounding single quotes", `'feat: add widget'`, "feat: add widget"},
		{"mismatched quotes preserved", `"feat: add widget'`, `"feat: add widget'`},
		{"trims leading/trailing whitespace", "   feat: add widget  \n", "feat: add widget"},
		{
			"short subject + body preserved",
			"feat: add widget\n\n- does the thing",
			"feat: add widget\n\n- does the thing",
		},
		{
			"empty body drops to subject only",
			"feat: add widget\n\n   \n  ",
			"feat: add widget",
		},
		{
			"subject exactly 72 chars unchanged",
			strings.Repeat("a", 72),
			strings.Repeat("a", 72),
		},
		{
			"subject >72 chars with no word boundary in first 30 hard-cuts at 69",
			strings.Repeat("x", 100),
			strings.Repeat("x", 69) + "...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCommitMessage(tt.in)
			if got != tt.want {
				t.Errorf("extractCommitMessage(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractCommitMessage_SubjectLineBounded(t *testing.T) {
	// Subject line must always be <=72 chars after truncation.
	inputs := []string{
		"",
		"short",
		strings.Repeat("a", 72),
		strings.Repeat("a", 73),
		strings.Repeat("x", 1000),
		"feat: " + strings.Repeat("word ", 40),
	}
	for _, in := range inputs {
		out := extractCommitMessage(in)
		firstLine, _, _ := strings.Cut(out, "\n")
		if len(firstLine) > 72 {
			t.Errorf("extractCommitMessage(%q): subject %q is %d chars, want <=72",
				in, firstLine, len(firstLine))
		}
	}
}

func TestPRRefShape(t *testing.T) {
	tests := []struct {
		name, remote, want string
	}{
		{"github https", "https://github.com/owner/repo.git", "refs/pull/%d/head"},
		{"github ssh", "git@github.com:owner/repo.git", "refs/pull/%d/head"},
		{"gitea self-hosted", "https://gitea.example.com/owner/repo.git", "refs/pull/%d/head"},
		{"codeberg", "https://codeberg.org/owner/repo.git", "refs/pull/%d/head"},
		{"gitlab cloud https", "https://gitlab.com/owner/repo.git", "refs/merge-requests/%d/head"},
		{"gitlab self-hosted", "https://gitlab.internal.example.com/o/r.git", "refs/merge-requests/%d/head"},
		{"gitlab ssh", "git@gitlab.com:owner/repo.git", "refs/merge-requests/%d/head"},
		{"empty remote falls back to github shape", "", "refs/pull/%d/head"},
		{"unknown remote falls back to github shape", "https://bitbucket.org/o/r.git", "refs/pull/%d/head"},
		// Host-only matching: path segments containing "gitlab" on a
		// non-GitLab host must not trigger the merge-requests shape.
		{"github path contains gitlab", "https://github.com/gitlab/tooling.git", "refs/pull/%d/head"},
		{"codeberg path contains gitlab", "https://codeberg.org/o/gitlab-clone.git", "refs/pull/%d/head"},
		{"gitea scp path contains gitlab", "git@gitea.example.com:org/gitlab-runner.git", "refs/pull/%d/head"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prRefShape(tt.remote)
			if got != tt.want {
				t.Errorf("prRefShape(%q) = %q, want %q", tt.remote, got, tt.want)
			}
		})
	}
}

// --- helpers.go coverage ---

func TestRequirePOST_AcceptsPOST(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	rec := httptest.NewRecorder()
	if !requirePOST(rec, req) {
		t.Errorf("requirePOST(POST) = false, want true")
	}
}

func TestRequirePOST_RejectsOtherMethods(t *testing.T) {
	methods := []string{
		http.MethodGet, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodHead,
	}
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			req := httptest.NewRequest(m, "/x", nil)
			rec := httptest.NewRecorder()
			if requirePOST(rec, req) {
				t.Errorf("requirePOST(%s) = true, want false", m)
			}
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("code = %d, want 405", rec.Code)
			}
		})
	}
}

func TestDecodePostBody_SuccessPopulatesStruct(t *testing.T) {
	body := strings.NewReader(`{"repo":"foo"}`)
	req := httptest.NewRequest(http.MethodPost, "/x", body)
	rec := httptest.NewRecorder()

	var got repoBody
	ok := decodePostBody(rec, req, &got, "bad")

	if !ok {
		t.Fatalf("decodePostBody returned false, want true")
	}
	if got.Repo != "foo" {
		t.Errorf("Repo = %q, want %q", got.Repo, "foo")
	}
}

func TestDecodePostBody_MalformedJSONRejected(t *testing.T) {
	body := strings.NewReader(`{not-json`)
	req := httptest.NewRequest(http.MethodPost, "/x", body)
	rec := httptest.NewRecorder()

	var got repoBody
	ok := decodePostBody(rec, req, &got, "custom error msg")

	if ok {
		t.Fatalf("decodePostBody returned true on malformed JSON")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "custom error msg") {
		t.Errorf("body %q does not contain custom error message", rec.Body.String())
	}
}

func TestDecodePostBodyOptional_MalformedJSONIgnored(t *testing.T) {
	body := strings.NewReader(`{not-json`)
	req := httptest.NewRequest(http.MethodPost, "/x", body)
	rec := httptest.NewRecorder()

	var got repoBody
	decodePostBodyOptional(rec, req, &got)

	// No response was written; caller continues as if body were zero.
	if got.Repo != "" {
		t.Errorf("Repo = %q, want empty on malformed optional body", got.Repo)
	}
}

func TestDecodePostBodyOptional_EmptyBodyIgnored(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	rec := httptest.NewRecorder()

	var got repoBody
	decodePostBodyOptional(rec, req, &got)
	if got.Repo != "" {
		t.Errorf("Repo = %q, want empty on nil body", got.Repo)
	}
}

func TestWriteCmdResult_SuccessEmitsOutputOnly(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCmdResult(rec, "hello world", nil)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"output":"hello world"`) {
		t.Errorf("body %q missing output field", body)
	}
	if strings.Contains(body, `"error"`) {
		t.Errorf("body %q contains error field on success", body)
	}
}

func TestWriteCmdResult_ErrorSetsErrorFieldNotOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCmdResult(rec, "fatal: not a git repo", errors.New("exit 128"))

	body := rec.Body.String()
	if !strings.Contains(body, `"error":"fatal: not a git repo"`) {
		t.Errorf("body %q missing error field", body)
	}
	// Post-fix: on failure we emit {error: ...} only; no output field.
	if strings.Contains(body, `"output"`) {
		t.Errorf("body %q must not contain output on failure", body)
	}
}

func TestWriteCmdResult_EmptyOutputFallsBackToErrMessage(t *testing.T) {
	// When a subprocess fails to start (git missing, EACCES), `out` is
	// empty and the err carries the useful message. The fix swaps to
	// err.Error() so the response isn't identity-indistinguishable
	// from a silent no-op.
	rec := httptest.NewRecorder()
	writeCmdResult(rec, "", errors.New(`exec: "git": executable file not found`))

	body := rec.Body.String()
	if !strings.Contains(body, `exec: \"git\"`) {
		t.Errorf("body %q does not surface err.Error() fallback", body)
	}
}

func TestWriteCmdResult_ScrubsAuthInErrorOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCmdResult(rec,
		"fatal: authentication failed for https://alice:ghp_TOKEN123@github.com/org/repo.git",
		errors.New("exit 128"))
	body := rec.Body.String()
	if strings.Contains(body, "ghp_TOKEN123") {
		t.Errorf("body %q leaked token", body)
	}
	if strings.Contains(body, "alice") {
		t.Errorf("body %q leaked username in userinfo", body)
	}
	if !strings.Contains(body, "github.com") {
		t.Errorf("body %q scrubbed too aggressively (host missing)", body)
	}
}

// --- gitExec env hardening regression pin ---

// TestGitExec_ScrubsInheritedEnv pins the env block + cmdline -c
// hardening in gitExec that defends against credential-prompt
// hijacking, runtime gitconfig injection (GIT_CONFIG_COUNT/KEY/VALUE
// + GIT_CONFIG_PARAMETERS), and ext:: transport re-enabling. Each
// guarantee is load-bearing; dropping any of them re-opens a
// CVE-class exposure (CVE-2017-1000117 and kin).
//
// Note on shape: an earlier version of this code pinned
// GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_SYSTEM=/dev/null. That
// also disabled the credential.helper line `gh auth setup-git`
// writes to ~/.gitconfig, which broke HTTPS clones of private repos.
// The fix moved the ext:: hardening to a command-line `-c
// protocol.ext.allow=never` flag (which always wins over gitconfig)
// and let gitconfig files load again.
func TestGitExec_ScrubsInheritedEnv(t *testing.T) {
	// Simulate a compromised parent env attempting every known
	// runtime-injection path. The scrub must win via os/exec's
	// last-wins duplicate-key semantics, OR the cmdline -c flag
	// must override (whichever applies).
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.ext.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'protocol.ext.allow=always'")

	want := map[string]string{
		"GIT_TERMINAL_PROMPT":    "0",
		"GIT_ASKPASS":            "",
		"SSH_ASKPASS":            "",
		"GIT_PROTOCOL_FROM_USER": "0",
		"GIT_CONFIG_COUNT":       "",
		"GIT_CONFIG_PARAMETERS":  "",
	}
	cmd := gitExec(context.Background(), t.TempDir(), "status")

	// Build a map of cmd.Env entries; later duplicates (our appends)
	// overwrite inherited values in the map lookup, matching the
	// last-wins semantics os/exec follows at spawn time.
	got := make(map[string]string, len(cmd.Env))
	for _, kv := range cmd.Env {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			got[k] = v
		}
	}
	for k, wantVal := range want {
		gotVal, ok := got[k]
		if !ok {
			t.Errorf("gitExec env missing %q (security regression: CVE-class exposure)", k)
			continue
		}
		if gotVal != wantVal {
			t.Errorf("gitExec env %q = %q, want %q", k, gotVal, wantVal)
		}
	}

	// gitconfig FILES must remain loadable so credential helpers
	// (gh auth git-credential, glab, etc.) work for HTTPS clones of
	// private repos. Pinning them to /dev/null was the previous
	// behavior and broke private clones — guard against the
	// regression returning.
	for _, k := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if v, ok := got[k]; ok {
			t.Errorf("gitExec env %q = %q must not be set; pinning it disables credential helpers from gitconfig", k, v)
		}
	}

	// Args must contain the cmdline -c hardening that takes
	// priority over gitconfig. This is what blocks ext:: even when
	// user gitconfig (or env-injected GIT_CONFIG_*) tries to enable
	// it. Drop this and CVE-2017-1000117 class issues come back.
	wantArgPair := []string{"-c", "protocol.ext.allow=never"}
	foundExt := false
	for i := range len(cmd.Args) - 1 {
		if cmd.Args[i] == wantArgPair[0] && cmd.Args[i+1] == wantArgPair[1] {
			foundExt = true
			break
		}
	}
	if !foundExt {
		t.Errorf("gitExec args missing `-c protocol.ext.allow=never` (security regression: ext:: transport may be re-enabled by gitconfig); got %v", cmd.Args)
	}

	// Belt-and-braces: args[0] must be "git" (or end in "git" if
	// the runner uses absolute path), and the requested subcommand
	// must appear after the hardening prefix.
	if len(cmd.Args) < 2 || cmd.Args[0] == "" {
		t.Fatalf("gitExec args = %v, want [git, -c, ..., status, ...]", cmd.Args)
	}
	if !strings.HasSuffix(cmd.Args[0], "git") {
		t.Errorf("gitExec arg[0] = %q, want binary ending in 'git'", cmd.Args[0])
	}
	// "status" must appear somewhere after the prefix.
	foundStatus := slices.Contains(cmd.Args[1:], "status")
	if !foundStatus {
		t.Errorf("gitExec args missing 'status' subcommand: %v", cmd.Args)
	}
}

// --- scrubAuth + sanitizeRepoPaths ---

func TestScrubAuth(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"no creds passes through", "plain error", "plain error"},
		{
			"https userinfo stripped",
			"fatal: https://user:token@github.com/org/repo.git",
			"fatal: https://github.com/org/repo.git",
		},
		{
			"token-only userinfo stripped",
			"https://ghp_ABC123@github.com/org/repo.git",
			"https://github.com/org/repo.git",
		},
		{
			"chained @ segments stripped fully",
			"http://a@b:c@host/path",
			"http://host/path",
		},
		{
			"query token redacted",
			"https://gitea.example/repo?access_token=secret",
			"https://gitea.example/repo?access_token=[REDACTED]",
		},
		{
			"authorization header redacted",
			"Authorization: Bearer ghp_TOKEN123",
			"Authorization: Bearer [REDACTED]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gitexec.ScrubAuth(tt.in)
			if got != tt.want {
				t.Errorf("gitexec.ScrubAuth(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestScrubAuth_Idempotent(t *testing.T) {
	inputs := []string{
		"https://user:pwd@host/path",
		"http://a@b@c@host/",
		"?token=secret&other=ok",
		"Authorization: Bearer abc",
	}
	for _, in := range inputs {
		once := gitexec.ScrubAuth(in)
		twice := gitexec.ScrubAuth(once)
		if once != twice {
			t.Errorf("scrubAuth not idempotent: f(%q)=%q, f(f(x))=%q", in, once, twice)
		}
	}
}

func TestSanitizeRepoPaths_Accepts(t *testing.T) {
	got, err := sanitizeRepoPaths([]string{"a/b.txt", "./c", "d/e/f.go", "single"})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	want := []string{"a/b.txt", "c", filepath.Clean("d/e/f.go"), "single"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got = %v, want %v", got, want)
	}
}

func TestSanitizeRepoPaths_SkipsEmpty(t *testing.T) {
	got, err := sanitizeRepoPaths([]string{"", "a", ""})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("got = %v, want [a]", got)
	}
}

func TestSanitizeRepoPaths_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		wantEr string
		in     []string
	}{
		{name: "parent traversal", in: []string{".."}, wantEr: "escapes"},
		{name: "prefix traversal", in: []string{"../etc/passwd"}, wantEr: "escapes"},
		{
			name: "mid-path traversal normalises to ..",
			in:   []string{"a/../../x"}, wantEr: "escapes",
		},
		{name: "absolute path", in: []string{"/etc/passwd"}, wantEr: "absolute"},
		{name: "null byte", in: []string{"a\x00b"}, wantEr: "null byte"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sanitizeRepoPaths(tt.in)
			if err == nil {
				t.Fatalf("err = nil, want error containing %q", tt.wantEr)
			}
			if !strings.Contains(err.Error(), tt.wantEr) {
				t.Errorf("err = %v, want error containing %q", err, tt.wantEr)
			}
		})
	}
}

func TestSanitizeRepoPaths_TooManyPathsRejected(t *testing.T) {
	huge := make([]string, maxRepoPaths+1)
	for i := range huge {
		huge[i] = "f"
	}
	_, err := sanitizeRepoPaths(huge)
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("err = %v, want 'too many paths'", err)
	}
}

// --- handleClone ---

func TestHandleClone_MalformedBodyRejected(t *testing.T) {
	h := NewHandler(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/git/clone", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	h.handleClone(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestHandleClone_EmptyURLRejected(t *testing.T) {
	h := NewHandler(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/git/clone", strings.NewReader(`{"url":""}`))
	rec := httptest.NewRecorder()
	h.handleClone(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "url required") {
		t.Errorf("body %q missing expected error", rec.Body.String())
	}
}

func TestHandleClone_RejectsInvalidSchemes(t *testing.T) {
	cases := []string{
		"http://example.com/repo.git",
		"ftp://example.com/repo.git",
		"file:///etc/passwd",
		"--upload-pack=evil",
		"ssh://evil",
		"git://no-scheme-allowed",
	}
	for _, url := range cases {
		t.Run(url, func(t *testing.T) {
			h := NewHandler(t.TempDir())
			body := `{"url":` + strconv.Quote(url) + `}`
			req := httptest.NewRequest(http.MethodPost, "/api/git/clone", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.handleClone(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("scheme %q: code = %d, want 400", url, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "only https:// and git@ URLs allowed") {
				t.Errorf("scheme %q: body %q missing expected error", url, rec.Body.String())
			}
		})
	}
}

// --- handleCheckout ---

func TestHandleCheckout_MalformedBodyRejected(t *testing.T) {
	h := NewHandler(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/git/checkout", strings.NewReader("{"))
	rec := httptest.NewRecorder()
	h.handleCheckout(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestHandleCheckout_InvalidBranchNames(t *testing.T) {
	tests := []struct {
		name, branch, wantErr string
	}{
		{"empty branch", "", "branch required"},
		{"leading dash looks like flag", "-rf", "invalid branch name"},
		{"double-dash flag", "--force", "invalid branch name"},
		{"space in name", "feature branch", "invalid branch name"},
		{"tab in name", "feature\tbranch", "invalid branch name"},
		{"newline in name", "feature\nbranch", "invalid branch name"},
		{"cr in name", "feature\rbranch", "invalid branch name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(t.TempDir())
			body := `{"branch":` + strconv.Quote(tt.branch) + `}`
			req := httptest.NewRequest(http.MethodPost, "/api/git/checkout", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.handleCheckout(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("branch %q: code = %d, want 400", tt.branch, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tt.wantErr) {
				t.Errorf("branch %q: body %q missing %q", tt.branch, rec.Body.String(), tt.wantErr)
			}
		})
	}
}

// --- handleRemove + handleReclone ---

func TestHandleRemove_WorkspaceRootRejected(t *testing.T) {
	for _, repo := range []string{"", "."} {
		t.Run("repo="+repo, func(t *testing.T) {
			h := NewHandler(t.TempDir())
			body := `{"repo":` + strconv.Quote(repo) + `}`
			req := httptest.NewRequest(http.MethodPost, "/api/git/remove", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.handleRemove(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("repo=%q: code = %d, want 400", repo, rec.Code)
			}
		})
	}
}

func TestHandleRemove_TraversalResolvesToWorkDirAndRejected(t *testing.T) {
	workDir := t.TempDir()
	h := NewHandler(workDir)
	body := `{"repo":"../other"}`
	req := httptest.NewRequest(http.MethodPost, "/api/git/remove", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleRemove(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cannot remove workspace root") {
		t.Errorf("body %q missing guard message", rec.Body.String())
	}
	if _, err := os.Stat(workDir); err != nil {
		t.Errorf("workDir vanished after traversal attempt: %v", err)
	}
}

func TestHandleRemove_RealSubdirRemoved(t *testing.T) {
	workDir := t.TempDir()
	sub := filepath.Join(workDir, "to-delete")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(workDir)
	body := `{"repo":"to-delete"}`
	req := httptest.NewRequest(http.MethodPost, "/api/git/remove", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleRemove(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("subdir still exists: err=%v", err)
	}
	if _, err := os.Stat(workDir); err != nil {
		t.Errorf("workDir vanished: %v", err)
	}
}

func TestHandleReclone_WorkspaceRootRejected(t *testing.T) {
	for _, repo := range []string{"", "."} {
		t.Run("repo="+repo, func(t *testing.T) {
			h := NewHandler(t.TempDir())
			body := `{"repo":` + strconv.Quote(repo) + `}`
			req := httptest.NewRequest(http.MethodPost, "/api/git/reclone", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.handleReclone(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("repo=%q: code = %d, want 400", repo, rec.Code)
			}
		})
	}
}

func TestHandleReclone_NonGitRepoRejected(t *testing.T) {
	workDir := t.TempDir()
	sub := filepath.Join(workDir, "not-a-repo")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(workDir)
	body := `{"repo":"not-a-repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/git/reclone", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleReclone(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not a git repo") {
		t.Errorf("body %q missing 'not a git repo'", rec.Body.String())
	}
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("subdir vanished on rejection: %v", err)
	}
}

// --- handleCommit + handlePRFetch ---

func TestHandleCommit_EmptyMessageRejected(t *testing.T) {
	h := NewHandler(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/git/commit", strings.NewReader(`{"message":""}`))
	rec := httptest.NewRecorder()
	h.handleCommit(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "message required") {
		t.Errorf("body %q missing 'message required'", rec.Body.String())
	}
}

func TestHandlePRFetch_InvalidNumberRejected(t *testing.T) {
	for _, n := range []int{0, -1, -42, 1_000_000_000} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			h := NewHandler(t.TempDir())
			body := `{"number":` + strconv.Itoa(n) + `}`
			req := httptest.NewRequest(http.MethodPost, "/api/git/pr-fetch", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.handlePRFetch(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("n=%d: code = %d, want 400", n, rec.Code)
			}
		})
	}
}

func TestHandlePRFetch_InvalidHeadRejected(t *testing.T) {
	cases := []string{"-rf", "--force", "feat branch", "has\ttab", "has\nnewline"}
	for _, head := range cases {
		t.Run(head, func(t *testing.T) {
			h := NewHandler(t.TempDir())
			body := `{"number":42,"head":` + strconv.Quote(head) + `}`
			req := httptest.NewRequest(http.MethodPost, "/api/git/pr-fetch", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.handlePRFetch(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("head=%q: code = %d, want 400", head, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "invalid head name") {
				t.Errorf("head=%q: body %q missing expected error", head, rec.Body.String())
			}
		})
	}
}

// --- handleShow: path validation ---

// --- handleCommitMessage: nil guard ---

func TestHandleCommitMessage_NilUtilityPromptReturnsError(t *testing.T) {
	// With the AIHandler design, the prompter is non-nil by
	// construction (NewAIHandler requires it). A nil-prompter
	// AIHandler hits "no staged changes" before reaching the
	// prompter call (tempdir isn't a repo), so we verify that
	// the handler doesn't panic and returns the expected git error.
	a := &AIHandler{workDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/api/git/commit-message", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked with nil utilityPrompt: %v", r)
		}
	}()
	a.handleCommitMessage(rec, req)
	// Tempdir isn't a repo → "no staged changes" before prompter is called.
	if !strings.Contains(rec.Body.String(), "no_staged_changes") {
		t.Errorf("body %q missing 'no_staged_changes'", rec.Body.String())
	}
}

// --- handleRepos (fixture) ---

func TestHandleRepos_ListsReposSkippingDotAndNonRepos(t *testing.T) {
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".hidden", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workDir, "plaindir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "repoA", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/repos", nil)
	rec := httptest.NewRecorder()
	h.handleRepos(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var resp struct {
		Repos []string `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Repos) != 1 || resp.Repos[0] != "repoA" {
		t.Errorf("repos = %v, want [repoA]", resp.Repos)
	}
}

func TestHandleRepos_IncludesDotWhenWorkDirIsRepo(t *testing.T) {
	workDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/repos", nil)
	rec := httptest.NewRecorder()
	h.handleRepos(rec, req)
	var resp struct {
		Repos []string `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Repos) != 1 || resp.Repos[0] != "." {
		t.Errorf("repos = %v, want [.]", resp.Repos)
	}
}

// --- real git fixture tests ---

// initFixtureRepo creates a minimal git repo at dir with one file + one
// commit on "main". Skips the test if git isn't on PATH.
func initFixtureRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	run := func(args ...string) {
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
	run("init", "--initial-branch=main", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "initial commit")
}

func TestHandleStatus_NonRepoReturnsIsRepoFalse(t *testing.T) {
	workDir := t.TempDir()
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/status", nil)
	rec := httptest.NewRecorder()
	h.handleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var resp gitStatusResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.IsRepo {
		t.Errorf("IsRepo = true, want false for empty dir")
	}
}

func TestHandleStatusAll_EmptyWorkspaceReturnsEmptyArray(t *testing.T) {
	workDir := t.TempDir()
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/status-all", nil)
	rec := httptest.NewRecorder()
	h.handleStatusAll(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Repos []allRepoStatus `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Repos) != 0 {
		t.Errorf("Repos = %d entries, want 0 for empty workspace", len(resp.Repos))
	}
}

func TestHandleStatusAll_CleanRepoListedOnce(t *testing.T) {
	workDir := t.TempDir()
	repoDir := filepath.Join(workDir, "myrepo")
	if err := os.Mkdir(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initFixtureRepo(t, repoDir)
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/status-all", nil)
	rec := httptest.NewRecorder()
	h.handleStatusAll(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var resp struct {
		Repos []allRepoStatus `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Repos) != 1 {
		t.Fatalf("Repos = %d entries, want 1 (the myrepo subdir)", len(resp.Repos))
	}
	got := resp.Repos[0]
	if got.Repo != "myrepo" {
		t.Errorf("Repo = %q, want \"myrepo\"", got.Repo)
	}
	if !got.IsRepo {
		t.Errorf("IsRepo = false, want true")
	}
	if got.HasDirty {
		t.Errorf("HasDirty = true, want false for clean fixture")
	}
}

func TestHandleStatus_CleanRepoReportsBranchAndNoFiles(t *testing.T) {
	workDir := t.TempDir()
	initFixtureRepo(t, workDir)
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/status?quick=1", nil)
	rec := httptest.NewRecorder()
	h.handleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp gitStatusResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.IsRepo {
		t.Errorf("IsRepo = false, want true")
	}
	if resp.Branch != "main" {
		t.Errorf("Branch = %q, want %q", resp.Branch, "main")
	}
	if resp.HasDirty {
		t.Errorf("HasDirty = true, want false on clean repo")
	}
}

func TestHandleStatus_DirtyRepoReportsFiles(t *testing.T) {
	workDir := t.TempDir()
	initFixtureRepo(t, workDir)
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/status?quick=1", nil)
	rec := httptest.NewRecorder()
	h.handleStatus(rec, req)
	var resp gitStatusResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.HasDirty {
		t.Errorf("HasDirty = false, want true")
	}
	if len(resp.Files) < 2 {
		t.Errorf("Files = %+v, want >=2 entries", resp.Files)
	}
}

func TestHandleLog_RepoReturnsCommitLines(t *testing.T) {
	workDir := t.TempDir()
	initFixtureRepo(t, workDir)
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/log", nil)
	rec := httptest.NewRecorder()
	h.handleLog(rec, req)
	var resp struct {
		Entries []string `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Entries) < 1 {
		t.Fatalf("entries = %+v, want >=1", resp.Entries)
	}
	if !strings.Contains(resp.Entries[0], "initial commit") {
		t.Errorf("first entry %q missing 'initial commit'", resp.Entries[0])
	}
}

func TestHandleBranches_RepoReturnsCurrentBranch(t *testing.T) {
	workDir := t.TempDir()
	initFixtureRepo(t, workDir)
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/branches", nil)
	rec := httptest.NewRecorder()
	h.handleBranches(rec, req)
	var resp struct {
		Current  string `json:"current"`
		Branches []struct {
			Name    string `json:"name"`
			Current bool   `json:"current"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Current != "main" {
		t.Errorf("current = %q, want main", resp.Current)
	}
}

func TestHandleShow_ExistingFileAtHEADReturnsContent(t *testing.T) {
	workDir := t.TempDir()
	initFixtureRepo(t, workDir)
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/show?path=README.md", nil)
	rec := httptest.NewRecorder()
	h.handleShow(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Content != "hi" {
		t.Errorf("content = %q, want %q", resp.Content, "hi")
	}
}

func TestSplitTrackedUntracked_ParsesFixture(t *testing.T) {
	workDir := t.TempDir()
	initFixtureRepo(t, workDir)
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	tracked, untracked := splitTrackedUntracked(ctx, workDir, []string{"README.md", "new.txt"})
	if len(tracked) != 1 || tracked[0] != "README.md" {
		t.Errorf("tracked = %v, want [README.md]", tracked)
	}
	if len(untracked) != 1 || untracked[0] != "new.txt" {
		t.Errorf("untracked = %v, want [new.txt]", untracked)
	}

	tracked, untracked = splitTrackedUntracked(ctx, workDir, []string{"ghost.go"})
	if len(tracked) != 0 || len(untracked) != 0 {
		t.Errorf("unknown-file: tracked=%v untracked=%v, want both empty", tracked, untracked)
	}

	tracked, untracked = splitTrackedUntracked(ctx, t.TempDir(), []string{"anything"})
	if tracked != nil || untracked != nil {
		t.Errorf("non-repo: tracked=%v untracked=%v, want nil,nil", tracked, untracked)
	}
}

func TestIsValidGitRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"HEAD", true},
		{"main", true},
		{"origin/main", true},
		{"release/1.2", true},
		{"feature/v1.2.3", true},
		{"", false},
		{"-exec", false},
		{"--upload-pack=/tmp/x", false},
		{"has space", false},
		{"has\ttab", false},
		{"has\nnewline", false},
		{"has\rcarriage", false},
		{"has\x00nul", false},
		{"has:colon", false},
		{"has?question", false},
		{"has*asterisk", false},
		{"has[bracket", false},
		{"has\\backslash", false},
		{"HEAD~3", false}, // ref-expression, not a plain ref
		{"main^", false},  // ref-expression, not a plain ref
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			if got := isValidGitRef(tt.ref); got != tt.want {
				t.Errorf("isValidGitRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestScrubAuth_LongUserInfoChain(t *testing.T) {
	// 16-segment chain: scrubAuth must consume every userinfo
	// segment in a single call (true fixed-point iteration), not
	// bail out at 8 iterations and leak the residual head.
	in := "http://a@b@c@d@e@f@g@h@i@j@k@l@m@n@o@p@host/path"
	want := "http://host/path"
	if got := gitexec.ScrubAuth(in); got != want {
		t.Errorf("gitexec.ScrubAuth(long chain) = %q, want %q", got, want)
	}
	// Idempotency: a second call must not change the output.
	if got := gitexec.ScrubAuth(gitexec.ScrubAuth(in)); got != want {
		t.Errorf("scrubAuth idempotent = %q, want %q", got, want)
	}
}

func TestScrubAuth_DeeplyChainedUserinfo(t *testing.T) {
	// Five @ segments before the host exercise the fixpoint loop
	// at a depth realistic adversaries might actually try. Sits
	// between the common 1-2 pass case and the 16-segment worst
	// case pinned by TestScrubAuth_LongUserInfoChain.
	in := "http://a@b@c@d@e@host/path"
	want := "http://host/path"
	if got := gitexec.ScrubAuth(in); got != want {
		t.Errorf("gitexec.ScrubAuth(%q) = %q, want %q", in, got, want)
	}
}

func TestHandleReclone_RejectsNonStandardScheme(t *testing.T) {
	// Requires a real git binary to set up the fixture.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH")
	}
	workDir := t.TempDir()
	repoDir := filepath.Join(workDir, "evil")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", "file:///tmp/attacker")

	h := NewHandler(workDir)
	body := strings.NewReader(`{"repo":"evil"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/git/reclone", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handleReclone(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (json error envelope)", rec.Code)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.Error, "unsupported scheme") {
		t.Errorf("error = %q, want substring 'unsupported scheme'", resp.Error)
	}
	// Tree must be preserved (reclone rejected BEFORE os.RemoveAll).
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		t.Errorf("repo dir destroyed by rejected reclone: %v", err)
	}
}

// --- handleStage validation (no git subprocess needed) ---

func TestStagingHandlers_InputValidation(t *testing.T) {
	type testCase struct {
		name     string
		method   string
		endpoint string
		handler  string
		body     string
		wantBody string
		wantCode int
	}
	cases := []testCase{
		// handleStage
		{"stage_malformed_body", http.MethodPost, "/api/git/stage", "stage", "{not", "bad request", http.StatusBadRequest},
		{"stage_absolute_path", http.MethodPost, "/api/git/stage", "stage", `{"files":["/etc/passwd"]}`, "absolute", http.StatusBadRequest},
		{"stage_traversal_path", http.MethodPost, "/api/git/stage", "stage", `{"files":["../etc/passwd"]}`, "escapes", http.StatusBadRequest},
		// handleUnstage
		{"unstage_malformed_body", http.MethodPost, "/api/git/unstage", "unstage", "garbage", "", http.StatusBadRequest},
		{"unstage_traversal_path", http.MethodPost, "/api/git/unstage", "unstage", `{"files":["../../escape"]}`, "", http.StatusBadRequest},
		// handleDiscard
		{"discard_malformed_body", http.MethodPost, "/api/git/discard", "discard", "not json", "files required", http.StatusBadRequest},
		{"discard_empty_files", http.MethodPost, "/api/git/discard", "discard", `{"files":[]}`, "files required", http.StatusBadRequest},
		{"discard_null_byte_in_path", http.MethodPost, "/api/git/discard", "discard", "{\"files\":[\"bad\\u0000file\"]}", "null byte", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(t.TempDir())
			req := httptest.NewRequest(tc.method, tc.endpoint, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			switch tc.handler {
			case "stage":
				h.handleStage(rec, req)
			case "unstage":
				h.handleUnstage(rec, req)
			case "discard":
				h.handleDiscard(rec, req)
			}
			if rec.Code != tc.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body %q missing %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

// --- handlePRDescription validation (no git subprocess needed) ---

func TestHandlePRDescription_MalformedBodyRejected(t *testing.T) {
	a := &AIHandler{workDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-description", strings.NewReader("{not"))
	rec := httptest.NewRecorder()
	a.handlePRDescription(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bad request") {
		t.Errorf("body %q missing 'bad request'", rec.Body.String())
	}
}

func TestHandlePRDescription_NilUtilityPromptReturnsError(t *testing.T) {
	// With the AIHandler design, the prompter is non-nil by
	// construction. A nil-prompter AIHandler hits "no changes"
	// before reaching the prompter call (tempdir isn't a repo).
	a := &AIHandler{workDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-description", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked with nil utilityPrompt: %v", r)
		}
	}()
	a.handlePRDescription(rec, req)
	// Tempdir isn't a repo → "no changes" before prompter is called.
	if !strings.Contains(rec.Body.String(), "no_changes") {
		t.Errorf("body %q missing 'no_changes'", rec.Body.String())
	}
}

// --- method enforcement on thin wrapper handlers ---

func TestSimpleHandlers_NonPostRejected(t *testing.T) {
	// POST-only endpoints must 405 without reaching the git subprocess
	// layer — otherwise a misconfigured reverse proxy or a stray GET
	// could trigger side effects.
	tests := []struct {
		handler func(h *Handler) http.HandlerFunc
		name    string
	}{
		{func(h *Handler) http.HandlerFunc { return h.handleClone }, "clone"},
		{func(h *Handler) http.HandlerFunc { return h.handleCheckout }, "checkout"},
		{func(h *Handler) http.HandlerFunc { return h.handleRemove }, "remove"},
		{func(h *Handler) http.HandlerFunc { return h.handleCommit }, "commit"},
		{func(h *Handler) http.HandlerFunc { return h.handlePRFetch }, "pr-fetch"},
		{func(h *Handler) http.HandlerFunc { return h.handleReclone }, "reclone"},
		{func(h *Handler) http.HandlerFunc { return h.handleStage }, "stage"},
		{func(h *Handler) http.HandlerFunc { return h.handleUnstage }, "unstage"},
		{func(h *Handler) http.HandlerFunc { return h.handleDiscard }, "discard"},
		{func(h *Handler) http.HandlerFunc { return h.handlePush }, "push"},
		{func(h *Handler) http.HandlerFunc { return h.handlePull }, "pull"},
		{func(h *Handler) http.HandlerFunc { return h.handleStash }, "stash"},
		{func(h *Handler) http.HandlerFunc { return h.handleStashPop }, "stash-pop"},
	}
	methods := []string{
		http.MethodGet, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions,
	}
	for _, tt := range tests {
		for _, m := range methods {
			t.Run(tt.name+"/"+m, func(t *testing.T) {
				h := NewHandler(t.TempDir())
				req := httptest.NewRequest(m, "/api/git/"+tt.name, nil)
				rec := httptest.NewRecorder()
				tt.handler(h)(rec, req)
				if rec.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s: code = %d, want 405", m, tt.name, rec.Code)
				}
			})
		}
	}
}

// --- WithUtilityPrompt wiring contract ---

// mockPrompter implements UtilityPrompter for tests.
type mockPrompter struct {
	err    error
	result string
	called bool
}

func (m *mockPrompter) UtilityPrompt(_ context.Context, _ string) (string, error) {
	m.called = true
	return m.result, m.err
}

func TestWithUtilityPrompt_WiresCallback(t *testing.T) {
	// After constructing AIHandler with a prompter, handleCommitMessage
	// must proceed past the nil guard. In a non-repo tempdir it
	// short-circuits at the "no staged changes" branch.
	mp := &mockPrompter{result: "feat: stub"}
	a := NewAIHandler(t.TempDir(), mp)
	req := httptest.NewRequest(http.MethodPost, "/api/git/commit-message",
		strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	a.handleCommitMessage(rec, req)
	// Tempdir isn't a repo so we hit "no staged changes" before the
	// callback fires.
	if mp.called {
		t.Errorf("utilityPrompt called before staged-changes check (guard order changed)")
	}
	if !strings.Contains(rec.Body.String(), "no_staged_changes") {
		t.Errorf("body %q missing 'no_staged_changes'; guard order may have changed", rec.Body.String())
	}
}

// --- prRemoteHost ---

func TestPRRemoteHost(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"whitespace", "   ", ""},
		{"github https", "https://github.com/owner/repo.git", "github.com"},
		{"github https with port", "https://github.com:8443/owner/repo.git", "github.com"},
		{"gitlab https", "https://gitlab.example.com/o/r.git", "gitlab.example.com"},
		{"github scp", "git@github.com:owner/repo.git", "github.com"},
		{"gitea scp", "git@gitea.example.com:org/repo.git", "gitea.example.com"},
		{"scp missing user", ":host:path", ""},
		{"scp missing host", "user@:path", ""},
		{"scp ext helper rejected", "ext::foo@host:pwn", ""},
		{"scp with slash in user rejected", "user@ho/st:path", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gitexec.ParseRemoteHost(tt.in)
			if got != tt.want {
				t.Errorf("ParseRemoteHost(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPRRemoteHost_NeverContainsPathOrUserinfo(t *testing.T) {
	// Property: for any input prRemoteHost accepts, the output must
	// not contain '/' or '@'. Empty string is always acceptable
	// (unknown-remote signal). Guards against a regression that swaps
	// url.Hostname for url.Host (port leak) or drops the userinfo
	// strip.
	inputs := []string{
		"https://user:token@github.com/repo.git",
		"https://a@b@c@host/path",
		"git@gitlab.com:org/repo.git",
		"https://gitea.example.com:8080/o/r.git",
		"file:///etc/passwd",
		"ssh://u@h:22/p",
		"not a url",
		"",
		"/just/a/path",
	}
	for _, in := range inputs {
		got := gitexec.ParseRemoteHost(in)
		if got == "" {
			continue
		}
		if strings.ContainsAny(got, "/@") {
			t.Errorf("ParseRemoteHost(%q) = %q: contains / or @", in, got)
		}
	}
}

// --- fuzz targets ---

func FuzzParseGitStatusOutput(f *testing.F) {
	// Seed corpus from the table-driven test cases.
	seeds := [][]byte{
		nil,
		[]byte(""),
		[]byte("  \n\t\r\n"),
		[]byte(" M file.go\n"),
		[]byte("A  new.go\n"),
		[]byte("MM both.go\n"),
		[]byte("?? newfile.go\n"),
		[]byte("R  old.go -> new.go\n"),
		[]byte("?? somedir/\n?? real.txt\n"),
		[]byte("XY\n M file.go\n?\n"),
		[]byte("M  staged.go\n M unstaged.go\n?? untracked.go\nA  added.go\n"),
		[]byte(" M file.go\n\n\t\r\n"),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic on any input.
		_ = parseGitStatusOutput(data)
	})
}

func FuzzScrubAuth(f *testing.F) {
	// Seed corpus from existing test cases.
	seeds := []string{
		"",
		"plain error",
		"fatal: https://user:token@github.com/org/repo.git",
		"https://ghp_ABC123@github.com/org/repo.git",
		"http://a@b:c@host/path",
		"https://gitea.example/repo?access_token=secret",
		"Authorization: Bearer ghp_TOKEN123",
		"https://user:pwd@host/path",
		"http://a@b@c@host/",
		"?token=secret&other=ok",
		"http://a@b@c@d@e@f@g@h@i@j@k@l@m@n@o@p@host/path",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		result := gitexec.ScrubAuth(data)
		// Post-condition 1: no panic (implicit).
		// Post-condition 2: idempotent.
		if twice := gitexec.ScrubAuth(result); twice != result {
			t.Errorf("scrubAuth not idempotent: f(%q)=%q, f(f(x))=%q", data, result, twice)
		}
		// Post-condition 3: no userinfo between :// and the next /.
		if _, rest, ok := strings.Cut(result, "://"); ok {
			if hostPart, _, hasSlash := strings.Cut(rest, "/"); hasSlash {
				if strings.Contains(hostPart, "@") {
					t.Errorf("gitexec.ScrubAuth(%q) = %q: userinfo '@' remains between :// and /", data, result)
				}
			}
		}
	})
}

func FuzzPRRemoteHost(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"https://github.com/owner/repo.git",
		"https://github.com:8443/owner/repo.git",
		"https://gitlab.example.com/o/r.git",
		"git@github.com:owner/repo.git",
		"git@gitea.example.com:org/repo.git",
		":host:path",
		"user@:path",
		"ext::foo@host:pwn",
		"user@ho/st:path",
		"https://user:token@github.com/repo.git",
		"https://a@b@c@host/path",
		"ssh://u@h:22/p",
		"file:///etc/passwd",
		"not a url",
		"/just/a/path",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		got := gitexec.ParseRemoteHost(data)
		if got == "" {
			return
		}
		// Invariant: result must not contain '/', '@', ':', or control characters.
		for _, c := range got {
			if c == '/' || c == '@' || c == ':' {
				t.Errorf("ParseRemoteHost(%q) = %q: contains forbidden char %q", data, got, string(c))
				return
			}
			if c < 0x20 || c == 0x7f {
				t.Errorf("ParseRemoteHost(%q) = %q: contains control char %U", data, got, c)
				return
			}
		}
	})
}

func FuzzExtractCommitMessage(f *testing.F) {
	seeds := []string{
		"",
		"feat: add widget",
		"```\nfeat: add widget\n```",
		"COMMIT MESSAGE: feat: add widget",
		"commit message: feat: add widget",
		"Commit Message: feat: add widget",
		`"feat: add widget"`,
		`'feat: add widget'`,
		"   feat: add widget  \n",
		"feat: add widget\n\n- does the thing",
		"feat: add widget\n\n   \n  ",
		strings.Repeat("a", 72),
		strings.Repeat("x", 100),
		"feat: " + strings.Repeat("word ", 40),
		"```go\nfeat: refactor\n```",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		result := extractCommitMessage(data)
		// Invariant 1: subject line <= 72 chars.
		firstLine, _, _ := strings.Cut(result, "\n")
		if len(firstLine) > 72 {
			t.Errorf("extractCommitMessage(%q): subject %q is %d chars, want <=72",
				data, firstLine, len(firstLine))
		}
		// Invariant 2: no surrounding quotes in output.
		trimmed := strings.TrimSpace(result)
		if len(trimmed) >= 2 {
			if (trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"') ||
				(trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'') {
				// Only flag if the input didn't have mismatched quotes
				// (mismatched quotes are preserved by design).
				in := strings.TrimSpace(data)
				if len(in) >= 2 &&
					((in[0] == '"' && in[len(in)-1] == '"') ||
						(in[0] == '\'' && in[len(in)-1] == '\'')) {
					t.Errorf("extractCommitMessage(%q) = %q: surrounding quotes not stripped",
						data, result)
				}
			}
		}
		// Invariant 3: no "COMMIT MESSAGE:" prefix in output.
		upper := strings.ToUpper(strings.TrimSpace(result))
		if strings.HasPrefix(upper, "COMMIT MESSAGE:") {
			t.Errorf("extractCommitMessage(%q) = %q: prefix not stripped", data, result)
		}
	})
}

func BenchmarkParseGitStatusOutput(b *testing.B) {
	// Generate synthetic porcelain output for sub-benchmarks.
	generate := func(n int) []byte {
		var buf strings.Builder
		for i := range n {
			switch i % 4 {
			case 0:
				buf.WriteString(" M file" + strconv.Itoa(i) + ".go\n")
			case 1:
				buf.WriteString("A  new" + strconv.Itoa(i) + ".go\n")
			case 2:
				buf.WriteString("?? untracked" + strconv.Itoa(i) + ".txt\n")
			case 3:
				buf.WriteString("MM both" + strconv.Itoa(i) + ".go\n")
			}
		}
		return []byte(buf.String())
	}

	for _, n := range []int{10, 100, 1000} {
		data := generate(n)
		b.Run(strconv.Itoa(n)+"_files", func(b *testing.B) {
			for range b.N {
				_ = parseGitStatusOutput(data)
			}
		})
	}
}

func BenchmarkScrubAuth(b *testing.B) {
	clean := "fatal: repository '/home/user/repo' does not appear to be a git repository"
	singleUserinfo := "fatal: https://user:ghp_TOKEN123@github.com/org/repo.git not found"
	chained16 := "http://a@b@c@d@e@f@g@h@i@j@k@l@m@n@o@p@host/path"
	longOutput := strings.Repeat("Receiving objects: 100% (1234/1234), 512.00 KiB | 1.02 MiB/s, done.\n", 16)

	for _, tc := range []struct {
		name  string
		input string
	}{
		{"clean", clean},
		{"single_userinfo", singleUserinfo},
		{"chained_16", chained16},
		{"long_output_1KB", longOutput},
	} {
		b.Run(tc.name, func(b *testing.B) {
			for range b.N {
				_ = gitexec.ScrubAuth(tc.input)
			}
		})
	}
}

func FuzzIsValidGitRef(f *testing.F) {
	seeds := []string{
		"",
		"HEAD",
		"main",
		"refs/heads/feature",
		"-exec",
		"bad ref",
		"has\ttab",
		"has\nnewline",
		"has\x00null",
		"has:colon",
		"has?question",
		"has*star",
		"has[bracket",
		"has\\backslash",
		"has~tilde",
		"has^caret",
		"v1.2.3",
		"feature/branch-name",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		ok := isValidGitRef(data)
		if !ok {
			return
		}
		// Post-condition: if valid, must not start with '-'.
		if strings.HasPrefix(data, "-") {
			t.Errorf("isValidGitRef(%q) = true, but starts with '-'", data)
		}
		// Post-condition: if valid, must not contain forbidden chars.
		if strings.ContainsAny(data, " \t\n\r\x00:?*[\\~^") {
			t.Errorf("isValidGitRef(%q) = true, but contains forbidden char", data)
		}
		// Post-condition: must not be empty.
		if data == "" {
			t.Errorf("isValidGitRef(%q) = true, but is empty", data)
		}
	})
}

func FuzzSanitizeRepoPaths(f *testing.F) {
	seeds := []string{
		"a/b.txt", "./c", "d/e/f.go", "single",
		"", "..", "../etc/passwd", "a/../../x",
		"/etc/passwd", "has\x00null",
		"normal/path", "a/./b", "a//b",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		got, err := sanitizeRepoPaths([]string{data})
		if err != nil {
			return
		}
		for _, p := range got {
			// Invariant 1: every returned path must be Clean.
			if filepath.Clean(p) != p {
				t.Errorf("sanitizeRepoPaths(%q): result %q is not Clean", data, p)
			}
			// Invariant 2: no returned path starts with ".." or contains ".."+separator.
			if p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
				t.Errorf("sanitizeRepoPaths(%q): result %q escapes via ..", data, p)
			}
			// Invariant 3: no returned path is absolute.
			if filepath.IsAbs(p) {
				t.Errorf("sanitizeRepoPaths(%q): result %q is absolute", data, p)
			}
			// Invariant 4: no returned path contains a null byte.
			if strings.ContainsRune(p, '\x00') {
				t.Errorf("sanitizeRepoPaths(%q): result %q contains null byte", data, p)
			}
		}
	})
}

func FuzzRepoDir(f *testing.F) {
	seeds := []string{
		"", ".", "..", "../../etc/passwd", "/absolute", "/etc",
		"project", "a/b", "a/../b", "./legit", "legit/./ok",
		"legit//double", "trailing/",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		workDir := "/home/user/work"
		h := &Handler{workDir: workDir}
		got := h.repoDir(data)
		// Invariant 1: must not panic (implicit).
		// Invariant 2: output must equal workDir OR be lexically rooted at workDir.
		if got == workDir {
			return
		}
		rel, err := filepath.Rel(workDir, got)
		if err != nil {
			t.Errorf("repoDir(%q) = %q: not relative to workDir: %v", data, got, err)
			return
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("repoDir(%q) = %q: escapes workDir (rel=%q)", data, got, rel)
		}
	})
}

// --- tarch-b11-c7-p1: property-based test for extractCommitMessage ---

func TestExtractCommitMessage_PropertyInvariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a random subject (0-200 chars) and optional body.
		subject := rapid.StringMatching(`[a-z :(){}\-]{0,200}`).Draw(t, "subject")

		hasBody := rapid.Bool().Draw(t, "hasBody")
		var input string
		if hasBody {
			body := rapid.StringMatching(`[a-z \-\n]{1,100}`).Draw(t, "body")
			input = subject + "\n\n" + body
		} else {
			input = subject
		}

		// Optionally wrap with fence/prefix/quotes.
		wrapper := rapid.IntRange(0, 3).Draw(t, "wrapper")
		switch wrapper {
		case 1:
			input = "```\n" + input + "\n```"
		case 2:
			input = "COMMIT MESSAGE: " + input
		case 3:
			input = `"` + input + `"`
		}

		result := extractCommitMessage(input)

		// Invariant 1: subject line <= 72 chars.
		firstLine, _, _ := strings.Cut(result, "\n")
		if len(firstLine) > 72 {
			t.Fatalf("subject %q is %d chars, want <=72", firstLine, len(firstLine))
		}

		// Invariant 2: if truncation fired, output ends with "...".
		trimmedSubject := strings.TrimSpace(subject)
		if wrapper == 0 && len(trimmedSubject) > 72 && result != "" {
			subjectOut, _, _ := strings.Cut(result, "\n")
			if !strings.HasSuffix(subjectOut, "...") {
				t.Fatalf("truncated subject %q does not end with '...'", subjectOut)
			}
		}

		// Invariant 3: output never starts or ends with whitespace.
		if result != strings.TrimSpace(result) {
			t.Fatalf("result has leading/trailing whitespace: %q", result)
		}
	})
}

// --- tarch-b11-c7-p3: table-driven TestHandlePush_ValidationMatrix ---

func TestHandlePush_ValidationMatrix(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"empty_body_proceeds", "", http.StatusOK},
		{"malformed_json_proceeds", "{not-json", http.StatusOK},
		{"valid_repo_proceeds", `{"repo":"myrepo"}`, http.StatusOK},
		{"traversal_repo_clamped_to_workdir", `{"repo":"../escape"}`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			initFixtureRepo(t, workDir)
			h := NewHandler(workDir)
			var req *http.Request
			if tt.body == "" {
				req = httptest.NewRequest(http.MethodPost, "/api/git/push", nil)
			} else {
				req = httptest.NewRequest(http.MethodPost, "/api/git/push", strings.NewReader(tt.body))
			}
			rec := httptest.NewRecorder()
			h.handlePush(rec, req)
			// Push to a repo with no remote always fails at the git level
			// but the handler itself returns 200 with an error envelope.
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d, body = %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			// Verify the response is valid JSON.
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Errorf("response is not valid JSON: %v, body = %s", err, rec.Body.String())
			}
		})
	}
}

// --- tarch-b11-c7-p4: FuzzCloneURLValidation ---

func FuzzCloneURLValidation(f *testing.F) {
	seeds := []string{
		"",
		"https://github.com/owner/repo.git",
		"git@github.com:owner/repo.git",
		"http://example.com/repo.git",
		"ftp://example.com/repo.git",
		"file:///etc/passwd",
		"--upload-pack=evil",
		"ssh://evil",
		"git://no-scheme-allowed",
		"HTTPS://GITHUB.COM/repo.git",
		"https%3A//github.com/repo.git",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, url string) {
		accepted := isAllowedRemoteScheme(url)
		// Invariant 1: never panics (implicit).
		// Invariant 2: if accepted, must start with "https://" or "git@".
		if accepted {
			if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "git@") {
				t.Errorf("isAllowedRemoteScheme(%q) = true but doesn't start with https:// or git@", url)
			}
		}
	})
}

// --- tarch-b11-c7-p6: table-driven TestHandlePull_ValidationMatrix ---

func TestHandlePull_ValidationMatrix(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"empty_body_proceeds", "", http.StatusOK},
		{"malformed_json_proceeds", "{not-json", http.StatusOK},
		{"valid_repo_proceeds", `{"repo":"myrepo"}`, http.StatusOK},
		{"traversal_repo_clamped_to_workdir", `{"repo":"../escape"}`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			initFixtureRepo(t, workDir)
			h := NewHandler(workDir)
			var req *http.Request
			if tt.body == "" {
				req = httptest.NewRequest(http.MethodPost, "/api/git/pull", nil)
			} else {
				req = httptest.NewRequest(http.MethodPost, "/api/git/pull", strings.NewReader(tt.body))
			}
			rec := httptest.NewRecorder()
			h.handlePull(rec, req)
			// Pull on a repo with no remote always fails at the git level
			// but the handler itself returns 200 with an error envelope.
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d, body = %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			// Verify the response is valid JSON.
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Errorf("response is not valid JSON: %v, body = %s", err, rec.Body.String())
			}
		})
	}
}

func TestExtractCommitMessage_RapidUTF8(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate arbitrary UTF-8 strings (0-500 chars).
		subject := rapid.String().Draw(rt, "subject")
		if len(subject) > 500 {
			subject = subject[:500]
		}

		hasBody := rapid.Bool().Draw(rt, "hasBody")
		var input string
		if hasBody {
			body := rapid.String().Draw(rt, "body")
			if len(body) > 200 {
				body = body[:200]
			}
			input = subject + "\n\n" + body
		} else {
			input = subject
		}

		// Optionally wrap with fence/prefix/quotes.
		wrapper := rapid.IntRange(0, 4).Draw(rt, "wrapper")
		switch wrapper {
		case 1:
			lang := rapid.StringMatching(`[a-z]{0,10}`).Draw(rt, "lang")
			input = "```" + lang + "\n" + input + "\n```"
		case 2:
			input = "COMMIT MESSAGE: " + input
		case 3:
			input = `"` + input + `"`
		case 4:
			input = "  \n\t" + input + "\n  "
		}

		result := extractCommitMessage(input)

		// Invariant 1: subject line <= 72 chars.
		firstLine, _, _ := strings.Cut(result, "\n")
		if len(firstLine) > 72 {
			rt.Fatalf("subject %q is %d chars, want <=72", firstLine, len(firstLine))
		}

		// Invariant 2: no leading/trailing whitespace.
		if result != strings.TrimSpace(result) {
			rt.Fatalf("result has leading/trailing whitespace: %q", result)
		}
	})
}
