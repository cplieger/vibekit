package git

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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
	"golang.org/x/sync/singleflight"
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
		{"adjacent dots in a name are a name", "foo..bar", filepath.Join(workDir, "foo..bar")},
		{"leading double dots in a name are a name", "..drafts", filepath.Join(workDir, "..drafts")},
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
		{"unstaged modified", []byte(" M file.go\x00"), []gitFile{
			{Path: "file.go", Status: "M", Display: "Modified"},
		}},
		{"staged added", []byte("A  new.go\x00"), []gitFile{
			{Path: "new.go", Status: "A", Display: "Added", Staged: true},
		}},
		{"staged and unstaged emits two entries", []byte("MM both.go\x00"), []gitFile{
			{Path: "both.go", Status: "M", Display: "Modified", Staged: true},
			{Path: "both.go", Status: "M", Display: "Modified", Staged: false},
		}},
		{"untracked", []byte("?? newfile.go\x00"), []gitFile{
			{Path: "newfile.go", Status: "?", Display: "Untracked"},
		}},
		// -z rename: the ` -> ` is dropped and the order is reversed, so
		// the new (current) path comes first and the origin path is a
		// second NUL field that must be consumed, not parsed.
		{"rename keeps new path, consumes origin field", []byte("R  new.go\x00old.go\x00"), []gitFile{
			{Path: "new.go", Status: "R", Display: "Renamed", Staged: true},
		}},
		{"rename plus worktree modify emits two entries", []byte("RM renamed.go\x00orig.go\x00"), []gitFile{
			{Path: "renamed.go", Status: "R", Display: "Renamed", Staged: true},
			{Path: "renamed.go", Status: "M", Display: "Modified", Staged: false},
		}},
		{"copy consumes origin field", []byte("C  copy.go\x00src.go\x00"), []gitFile{
			{Path: "copy.go", Status: "C", Display: "Copied", Staged: true},
		}},
		// -z never quotes: non-ASCII bytes arrive verbatim as UTF-8.
		{"non-ascii filename unquoted", []byte(" M café.txt\x00"), []gitFile{
			{Path: "café.txt", Status: "M", Display: "Modified"},
		}},
		{"staged non-ascii rename", []byte("R  café-new.txt\x00café-old.txt\x00"), []gitFile{
			{Path: "café-new.txt", Status: "R", Display: "Renamed", Staged: true},
		}},
		// A literal " -> " inside a filename is no longer mistaken for a
		// rename separator (the old newline parser split on it).
		{"spaced filename with arrow-like substring", []byte(" M foo -> bar.txt\x00"), []gitFile{
			{Path: "foo -> bar.txt", Status: "M", Display: "Modified"},
		}},
		{"rename of spaced paths", []byte("R  new name.txt\x00old name.txt\x00"), []gitFile{
			{Path: "new name.txt", Status: "R", Display: "Renamed", Staged: true},
		}},
		{"directory entry skipped", []byte("?? somedir/\x00?? real.txt\x00"), []gitFile{
			{Path: "real.txt", Status: "?", Display: "Untracked"},
		}},
		{"short records skipped", []byte("XY\x00 M file.go\x00?\x00"), []gitFile{
			{Path: "file.go", Status: "M", Display: "Modified"},
		}},
		{"multiple entries", []byte("M  staged.go\x00 M unstaged.go\x00?? untracked.go\x00A  added.go\x00"), []gitFile{
			{Path: "staged.go", Status: "M", Display: "Modified", Staged: true},
			{Path: "unstaged.go", Status: "M", Display: "Modified"},
			{Path: "untracked.go", Status: "?", Display: "Untracked"},
			{Path: "added.go", Status: "A", Display: "Added", Staged: true},
		}},
		{"no trailing NUL still parses final record", []byte(" M file.go"), []gitFile{
			{Path: "file.go", Status: "M", Display: "Modified"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGitStatusOutput(tt.in)
			if !slices.Equal(got, tt.want) {
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

// BF15, half one. A workspace root that is not itself a repository has no
// committed revision of anything, and that is not a git FAILURE — it is the
// absence of a base. The two used to be one kind: `git show` ran in the
// non-repo directory, errored with "fatal: not a git repository", and the
// handler reported show_failed. A client cannot act on that: the honest answer
// is "no repo owns this path", which renders as an all-add diff.
//
// The distinction is what keeps a REAL git failure legible. Fold them and a
// broken object database renders as "this file is brand new", silently claiming
// every line was added.
func TestHandleShow_PathOutsideEveryRepoIsNotAFailure(t *testing.T) {
	h := NewHandler(t.TempDir()) // no .git anywhere
	req := httptest.NewRequest(http.MethodGet, "/api/git/show?path=nonexistent", nil)
	rec := httptest.NewRecorder()
	h.handleShow(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error":"`+string(KindNotInRepo)+`"`) {
		t.Errorf("body = %q, want the not-in-repo marker: no repository owns the path", body)
	}
	if strings.Contains(body, string(KindShowFailed)) {
		t.Errorf("body = %q, want the absence of a base reported as such rather than as a git failure", body)
	}
}

// BF15, half two, and the one with a user-visible wrong ANSWER rather than a
// wrong label. An absent `repo` used to default to the workspace root, so a file
// living in a SUBDIRECTORY repo was shown from the wrong repository: `git show
// HEAD:sub/tracked.txt` finds nothing there, the base came back empty, and the
// diff claimed every line had just been added. Every caller that holds only a
// workspace-relative path is in this shape — a turn's changed-file ledger, a
// tool card's filename — because translate.relPath strips the workspace prefix
// and knows nothing about repos.
func TestHandleShow_ResolvesTheOwningRepoWhenNoneIsNamed(t *testing.T) {
	work := t.TempDir()
	// The workspace root is a repo too, so this cannot pass by accident: the
	// old default WOULD find a repository here, just not the right one.
	initFixtureRepo(t, work)
	sub := filepath.Join(work, "sub")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, sub)
	writeCommit(t, sub, "tracked.txt", "from the subrepo\n", "add tracked")

	h := NewHandler(work)
	req := httptest.NewRequest(http.MethodGet, "/api/git/show?path=sub/tracked.txt", nil)
	rec := httptest.NewRecorder()
	h.handleShow(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Content string `json:"content"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	if got.Error != "" {
		t.Fatalf("error = %q, want the subrepo resolved", got.Error)
	}
	// gitCmd trims the trailing newline off every git invocation's output; that
	// is pre-existing and shared with the repo-named path.
	if got.Content != "from the subrepo" {
		t.Errorf("content = %q, want the committed base from the owning repo."+
			" Empty means the base was read from the workspace root, so the diff"+
			" claims every line was added", got.Content)
	}
}

// A file that genuinely does not exist at the ref, inside a real repo, is still
// the empty-base case: the diff renders as all-add, which is correct for a new
// file. This is the branch KindNotInRepo must NOT swallow.
func TestHandleShow_MissingFileInARepoReturnsEmptyContent(t *testing.T) {
	work := t.TempDir()
	initFixtureRepo(t, work)
	h := NewHandler(work)
	req := httptest.NewRequest(http.MethodGet, "/api/git/show?path=never-committed.txt", nil)
	rec := httptest.NewRecorder()
	h.handleShow(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"content":""`) {
		t.Errorf("body = %q, want empty content for a file absent at the ref", body)
	}
}

// An explicitly named repo still wins: the resolution only fills in a MISSING
// answer, it never overrides a caller that knows which repository it means.
func TestHandleShow_ExplicitRepoIsNotReResolved(t *testing.T) {
	work := t.TempDir()
	sub := filepath.Join(work, "sub")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, sub)
	writeCommit(t, sub, "tracked.txt", "named\n", "add tracked")

	h := NewHandler(work)
	req := httptest.NewRequest(http.MethodGet,
		"/api/git/show?repo=sub&path=tracked.txt", nil)
	rec := httptest.NewRecorder()
	h.handleShow(rec, req)
	if body := rec.Body.String(); !strings.Contains(body, "named") {
		t.Errorf("body = %q, want the named repo's content (path stays repo-relative)", body)
	}
}

// ownerOf is the rule the handler leans on, and it has three shapes worth
// pinning independently of an HTTP round trip.
func TestOwnerOf(t *testing.T) {
	work := t.TempDir()
	initFixtureRepo(t, work)
	for _, name := range []string{"sub", "sub-longer"} {
		dir := filepath.Join(work, name)
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		initFixtureRepo(t, dir)
	}
	h := NewHandler(work)

	tests := []struct {
		name      string
		path      string
		wantRepo  string
		wantInner string
		wantOK    bool
		reason    string
	}{{
		name: "SubdirRepoWins", path: "sub/a.go",
		wantRepo: "sub", wantInner: "a.go", wantOK: true,
		reason: "the nearest repo owns the file, not the workspace root",
	}, {
		name: "RootRepoOwnsTheRest", path: "top.go",
		wantRepo: ".", wantInner: "top.go", wantOK: true,
		reason: "the workspace-root repo is the fallback, which reproduces the old default",
	}, {
		name: "LongerRepoNameWins", path: "sub-longer/b.go",
		wantRepo: "sub-longer", wantInner: "b.go", wantOK: true,
		reason: "of two matching names the longer one is the nearer repo",
	}, {
		// The case a string-prefix test gets WRONG, and it is not exotic: a
		// sibling directory whose name merely begins with a repo's name.
		// strings.HasPrefix says "sub" owns "subx/file.go" and hands git the
		// path "x/file.go", which resolves to nothing.
		name: "ASiblingBeginningWithARepoNameIsNotInIt", path: "subx/file.go",
		wantRepo: ".", wantInner: "subx/file.go", wantOK: true,
		reason: "ownership is separator-precise, not a string prefix",
	}, {
		name: "RepoDirectoryItselfIsNotAFile", path: "sub",
		wantRepo: ".", wantInner: "sub", wantOK: true,
		reason: "naming the repo dir is not naming a file in it, so it falls back to the root",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, inner, ok := h.ownerOf(t.Context(), tt.path)
			if repo != tt.wantRepo || inner != tt.wantInner || ok != tt.wantOK {
				t.Errorf("ownerOf(%q) = (%q, %q, %v), want (%q, %q, %v) — %s",
					tt.path, repo, inner, ok, tt.wantRepo, tt.wantInner, tt.wantOK, tt.reason)
			}
		})
	}
}

// With no repository anywhere there is no owner, which is what makes the
// handler's not-in-repo answer reachable.
func TestOwnerOf_NoRepositoryMeansNoOwner(t *testing.T) {
	h := NewHandler(t.TempDir())
	if repo, inner, ok := h.ownerOf(t.Context(), "anything.go"); ok {
		t.Errorf("ownerOf = (%q, %q, true), want no owner in a workspace with no repos", repo, inner)
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
	// Subject line must always be <=subjectMaxRunes RUNES after truncation, and
	// must always be valid UTF-8. The unit matters: this assertion used to count
	// bytes while its message said "chars", which is the same conflation
	// capSubject itself had — so a non-ASCII subject cut mid-rune satisfied it.
	inputs := []string{
		"",
		"short",
		strings.Repeat("a", 72),
		strings.Repeat("a", 73),
		strings.Repeat("x", 1000),
		"feat: " + strings.Repeat("word ", 40),
		// Multi-byte, no spaces: the branch that truncates without a word break.
		strings.Repeat("日", 200),
		// Multi-byte with a word break past the minimum.
		"feat: " + strings.Repeat("変更 ", 60),
		// A 4-byte rune, so a byte-slice cut lands mid-sequence at more offsets.
		strings.Repeat("𝄞", 100),
	}
	for _, in := range inputs {
		out := extractCommitMessage(in)
		firstLine, _, _ := strings.Cut(out, "\n")
		if n := utf8.RuneCountInString(firstLine); n > subjectMaxRunes {
			t.Errorf("extractCommitMessage(%q...): subject is %d runes, want <=%d",
				in[:min(len(in), 20)], n, subjectMaxRunes)
		}
		if !utf8.ValidString(firstLine) {
			t.Errorf("extractCommitMessage(%q...): subject %q is not valid UTF-8",
				in[:min(len(in), 20)], firstLine)
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
	if !decodePostBodyOptional(rec, req, &got) {
		t.Fatalf("decodePostBodyOptional(%q) = false, want true", `{not-json`)
	}

	// No response was written; caller continues as if body were zero.
	if got.Repo != "" {
		t.Errorf("Repo = %q, want empty on malformed optional body", got.Repo)
	}
}

func TestDecodePostBodyOptional_EmptyBodyIgnored(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	rec := httptest.NewRecorder()

	var got repoBody
	if !decodePostBodyOptional(rec, req, &got) {
		t.Fatalf("decodePostBodyOptional(nil body) = false, want true")
	}
	if got.Repo != "" {
		t.Errorf("Repo = %q, want empty on nil body", got.Repo)
	}
}

// TestSyncHandlers_OversizeBodyRefusedBeforeGit pins the refusal on the four
// handlers whose body is advisory. An oversize body means the server stopped
// reading before Repo arrived, and a zero Repo resolves to the WORKSPACE ROOT
// (resolveRepoDir), so waving it through runs push/pull/stash against the wrong
// tree and answers 200 with a success shape.
//
// The status is what proves git was not reached: every git result on these paths
// goes out through writeCmdResult, which writes 200 whether the command
// succeeded or failed, so a 413 can only come from the decode refusal.
func TestSyncHandlers_OversizeBodyRefusedBeforeGit(t *testing.T) {
	body := `{"repo":"` + strings.Repeat("A", int(webhttp.MaxJSONBody)) + `"}`

	tests := []struct {
		handler func(h *Handler) http.HandlerFunc
		name    string
	}{
		{func(h *Handler) http.HandlerFunc { return h.handlePush }, "push"},
		{func(h *Handler) http.HandlerFunc { return h.handlePull }, "pull"},
		{func(h *Handler) http.HandlerFunc { return h.handleStash }, "stash"},
		{func(h *Handler) http.HandlerFunc { return h.handleStashPop }, "stash-pop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(t.TempDir())
			req := httptest.NewRequest(http.MethodPost, "/api/git/"+tt.name, strings.NewReader(body))
			rec := httptest.NewRecorder()

			tt.handler(h)(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("%s with a %d-byte body (cap %d): code = %d, want %d",
					tt.name, len(body), webhttp.MaxJSONBody, rec.Code, http.StatusRequestEntityTooLarge)
			}
			if !strings.Contains(rec.Body.String(), "request body too large") {
				t.Errorf("%s with an oversize body: body = %q, want it to name the refusal",
					tt.name, rec.Body.String())
			}
		})
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
	cmd := gitExec(t.Context(), t.TempDir(), "status")

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

func TestScrubAuth_Idempotent(t *testing.T) {
	inputs := []string{
		"https://user:pwd@host/path",
		"http://a@b@c@host/",
		// A git:// URL with a userinfo chain: the scheme the deleted
		// exec-side target contributed, and the one shape the seeds
		// above do not reach (they are all http/https).
		"git://a@b@c@host/repo",
		"?token=secret&other=ok",
		"Authorization: Bearer abc",
	}
	for _, in := range inputs {
		once := scrubAuth(in)
		twice := scrubAuth(once)
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
	if !slices.Equal(got, want) {
		t.Errorf("got = %v, want %v", got, want)
	}
}

func TestSanitizeRepoPaths_SkipsEmpty(t *testing.T) {
	got, err := sanitizeRepoPaths([]string{"", "a", ""})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(got, []string{"a"}) {
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

func TestCloneDirName(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"https with .git suffix", "https://github.com/cplieger/vibekit.git", "vibekit"},
		{"https without .git suffix", "https://github.com/cplieger/vibekit", "vibekit"},
		{"dot-named repo", "https://github.com/cplieger/.kiro.git", ".kiro"},
		{"trailing slash", "https://github.com/cplieger/vibekit.git/", "vibekit"},
		{"scp style", "git@github.com:cplieger/.kiro.git", ".kiro"},
		{"scp style without suffix", "git@github.com:cplieger/vibekit", "vibekit"},
		{"query stripped", "https://example.com/o/r.git?ref=main", "r"},
		{"host only", "https://github.com", ""},
		{"root path only", "https://github.com/", ""},
		{"repo named .git refused", "https://github.com/o/.git.git", ""},
		{"dot refused", "https://github.com/o/.", ""},
		{"parent traversal refused", "https://github.com/o/..", ""},
		{"flag-shaped name refused", "https://github.com/o/-x.git", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cloneDirName(tc.url); got != tc.want {
				t.Errorf("cloneDirName(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestInspectCloneDest(t *testing.T) {
	stateName := func(s destState) string {
		switch s {
		case destAbsent:
			return "destAbsent"
		case destEmpty:
			return "destEmpty"
		case destRepo:
			return "destRepo"
		case destOccupied:
			return "destOccupied"
		}
		return "unknown"
	}

	base := t.TempDir()
	mkdir := func(rel string) string {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	empty := mkdir("empty")
	repo := mkdir("repo/.git")
	_ = repo
	write(filepath.Join(mkdir("occupied/settings"), "lsp.json"))
	plainFile := filepath.Join(base, "afile")
	write(plainFile)
	link := filepath.Join(base, "link")
	if err := os.Symlink(empty, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		dir  string
		want destState
	}{
		{"absent", filepath.Join(base, "nope"), destAbsent},
		{"plain file", plainFile, destAbsent},
		{"symlink to a directory", link, destAbsent},
		{"empty directory", empty, destEmpty},
		{"git repository", filepath.Join(base, "repo"), destRepo},
		{"directory with content", filepath.Join(base, "occupied"), destOccupied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inspectCloneDest(t.Context(), tc.dir)
			if got != tc.want {
				t.Errorf("inspectCloneDest(%q) = %s, want %s", tc.dir, stateName(got), stateName(tc.want))
			}
		})
	}
}

// TestClone_ExistingRepoIsReportedByName replaces git's opaque "already
// exists and is not an empty directory" with a message naming the repo and
// the way out. Reaches no network: the destination check short-circuits
// before any git subprocess runs.
func TestClone_ExistingRepoIsReportedByName(t *testing.T) {
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "vibekit", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(workDir)
	out, err := h.clone(t.Context(), "https://github.com/cplieger/vibekit.git")
	if err == nil {
		t.Fatalf("clone into an existing repo: err = nil, want a refusal (out %q)", out)
	}
	if !strings.Contains(err.Error(), "vibekit") || !strings.Contains(err.Error(), "re-clone") {
		t.Errorf("err = %q, want it to name the repo and point at re-clone", err)
	}
}

// serveFixtureRepo builds a fixture repo under work and publishes it over
// git's dumb HTTP protocol, returning the remote URL.
//
// A local path cannot serve as the remote here: gitExec sets
// GIT_PROTOCOL_FROM_USER=0, which is exactly what makes git refuse the
// `file` transport ("fatal: transport 'file' not allowed"). Reaching the
// real gitExec path therefore needs a real HTTP remote. The repo tracks
// one file, README.md, on branch main.
func serveFixtureRepo(t *testing.T, work string) string {
	t.Helper()
	skipNoGit(t)
	src := filepath.Join(work, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, src)
	runGit(t, work, "clone", "--bare", "-q", "src", "srv.git")
	runGit(t, filepath.Join(work, "srv.git"), "update-server-info")
	srv := httptest.NewServer(http.FileServer(http.Dir(work)))
	t.Cleanup(srv.Close)
	return srv.URL + "/srv.git"
}

// TestClone_AdoptsAnOccupiedDestination covers the routing rather than the
// mechanism: h.clone must send an occupied destination to adoptDestination
// instead of to the plain `git clone` that refused it.
func TestClone_AdoptsAnOccupiedDestination(t *testing.T) {
	base := t.TempDir()
	remote := serveFixtureRepo(t, base)
	// serveFixtureRepo publishes the repo as srv.git, so the destination
	// git would derive, and that h.clone must inspect, is "srv".
	const name = "srv"

	workDir := filepath.Join(base, "work")
	dest := filepath.Join(workDir, name)
	if err := os.MkdirAll(filepath.Join(dest, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "settings", "lsp.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(workDir)
	out, err := h.clone(t.Context(), remote)
	if err != nil {
		t.Fatalf("clone into an occupied destination = %v, want nil\n%s", err, out)
	}
	if !IsRepo(t.Context(), dest) {
		t.Errorf("IsRepo(%q) = false, want true", dest)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "README.md")); statErr != nil {
		t.Errorf("tracked file not checked out: %v", statErr)
	}
}

// TestAdoptDestination_ClonesIntoAnOccupiedDirectory is the regression test
// for the reported defect. Cloning a repo NAMED .kiro failed because vibekit
// had already written <workspace>/.kiro/settings/lsp.json when it activated
// code intelligence, so plain `git clone` refused the non-empty destination
// in a few milliseconds and the repo could never be cloned at all.
func TestAdoptDestination_ClonesIntoAnOccupiedDirectory(t *testing.T) {
	base := t.TempDir()
	remote := serveFixtureRepo(t, base)

	// The destination exactly as vibekit leaves it: present, non-empty,
	// not a git repository.
	dest := filepath.Join(base, "work", ".kiro")
	if err := os.MkdirAll(filepath.Join(dest, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	lsp := filepath.Join(dest, "settings", "lsp.json")
	if err := os.WriteFile(lsp, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := adoptDestination(t.Context(), dest, remote)
	if err != nil {
		t.Fatalf("adoptDestination(%q, %q) = %v, want nil\n%s", dest, remote, err, out)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "README.md")); statErr != nil {
		t.Errorf("tracked file not checked out: %v", statErr)
	}
	// Without this the Sources row keeps offering Clone forever, which is
	// the second half of the reported symptom.
	if !IsRepo(t.Context(), dest) {
		t.Error("IsRepo(dest) = false, want true")
	}
	// The pre-existing file is untouched. This is the whole safety claim.
	got, readErr := os.ReadFile(lsp)
	if readErr != nil {
		t.Fatalf("read %s: %v", lsp, readErr)
	}
	if string(got) != "{}" {
		t.Errorf("lsp.json = %q, want %q", got, "{}")
	}
	// A tracking branch, exactly what a plain clone leaves behind.
	branch, revErr := gitCmd(t.Context(), dest, "rev-parse", "--abbrev-ref", "HEAD")
	if revErr != nil {
		t.Fatalf("rev-parse --abbrev-ref HEAD: %v", revErr)
	}
	if branch != "main" {
		t.Errorf("checked-out branch = %q, want %q", branch, "main")
	}
}

// TestAdoptDestination_RefusesToOverwriteUntrackedContent pins the property
// that makes adoption acceptable at all: git's own checkout refuses a
// collision, so pre-existing content is never overwritten silently.
func TestAdoptDestination_RefusesToOverwriteUntrackedContent(t *testing.T) {
	base := t.TempDir()
	remote := serveFixtureRepo(t, base)

	dest := filepath.Join(base, "work", "repo")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same name as a tracked file, different content.
	readme := filepath.Join(dest, "README.md")
	if err := os.WriteFile(readme, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := adoptDestination(t.Context(), dest, remote)
	if err == nil {
		t.Fatalf("adoptDestination(%q, %q) = nil, want a checkout refusal\n%s", dest, remote, out)
	}
	if !strings.Contains(out, "README.md") {
		t.Errorf("output %q does not name the colliding file", out)
	}
	got, readErr := os.ReadFile(readme)
	if readErr != nil {
		t.Fatalf("read %s: %v", readme, readErr)
	}
	if string(got) != "mine\n" {
		t.Errorf("README.md = %q, want %q: adoption overwrote it", got, "mine\n")
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
	// Dot-NAMED repos (".github", ".kiro") are legitimate clone targets
	// and must be discovered; only ".git" itself is excluded.
	if err := os.MkdirAll(filepath.Join(workDir, ".hidden", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A workspace-root .git dir must not be listed as a repo named ".git"
	// (the root repo itself is reported as "." by the workDir check).
	if err := os.MkdirAll(filepath.Join(workDir, ".git"), 0o755); err != nil {
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
	// workDir itself has .git → "." leads; then subdir repos sorted.
	want := []string{".", ".hidden", "repoA"}
	if len(resp.Repos) != len(want) {
		t.Fatalf("repos = %v, want %v", resp.Repos, want)
	}
	for i, w := range want {
		if resp.Repos[i] != w {
			t.Errorf("repos[%d] = %q, want %q (full: %v)", i, resp.Repos[i], w, resp.Repos)
		}
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

// skipNoGit skips the test when the git binary isn't on PATH.
func skipNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// runGit runs a git command in dir with an isolated, deterministic
// identity and config (no global/system gitconfig), failing the test on
// a non-zero exit. Shared by the fixture builders below.
func runGit(t *testing.T, dir string, args ...string) {
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

// initFixtureRepo creates a minimal git repo at dir with one file + one
// commit on "main". Skips the test if git isn't on PATH.
func initFixtureRepo(t *testing.T, dir string) {
	t.Helper()
	skipNoGit(t)
	runGit(t, dir, "init", "--initial-branch=main", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-q", "-m", "initial commit")
}

// writeCommit writes file with content in dir and commits it with msg.
func writeCommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", file)
	runGit(t, dir, "commit", "-q", "-m", msg)
}

// captureLogs swaps the slog default to a buffer-backed debug handler for
// the duration of the test and restores it on cleanup. Safe because the
// git package's tests never run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// behindRepo builds a work tree whose HEAD is at C1 while origin/main has
// been advanced to C2 (then fetched). The local "main" tracks
// origin/main. Returns the work tree path.
func behindRepo(t *testing.T) string {
	t.Helper()
	skipNoGit(t)
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	work := filepath.Join(base, "work")
	if err := os.Mkdir(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, origin) // origin main @ C1 ("initial commit")
	runGit(t, base, "clone", "-q", origin, work)
	writeCommit(t, origin, "README.md", "second\n", "second commit") // C2
	runGit(t, work, "fetch", "-q", "origin")                         // origin/main -> C2
	return work
}

// aheadBehind must report real upstream divergence. The counts come from
// `git rev-list --left-right --count HEAD...@{upstream}`, so rev-list has to
// be permitted by the allowedSubcommands allowlist; if it is dropped, the command is
// rigged to fail and the counts silently collapse to 0/0 (a dead ahead/behind
// indicator in the git panel).
func TestAheadBehind_ReportsUpstreamDivergence(t *testing.T) {
	work := behindRepo(t) // HEAD at C1, origin/main at C2 -> behind 1, ahead 0
	if ahead, behind := aheadBehind(t.Context(), work); ahead != 0 || behind != 1 {
		t.Fatalf("aheadBehind on a behind-by-one work tree = (%d, %d), want (0, 1)", ahead, behind)
	}
	// A local commit on top of C1 leaves the work tree both ahead and behind.
	writeCommit(t, work, "local.txt", "local\n", "local commit")
	if ahead, behind := aheadBehind(t.Context(), work); ahead != 1 || behind != 1 {
		t.Fatalf("aheadBehind after a local commit = (%d, %d), want (1, 1)", ahead, behind)
	}
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

// parseGitStatus round-trips real `git status --porcelain=v1 -z` output
// for filenames git C-quotes in the default (newline) format: non-ASCII
// bytes, embedded spaces, and a staged rename to such a name. The -z
// format never quotes, so every returned Path is the exact on-disk path
// — which is what stage/unstage/discard/diff feed back to git. This is
// the end-to-end proof of the quoting-bug fix (GIT-PANEL): under the old
// newline + line[3:] parser these paths came back wrapped in double
// quotes with C-escapes (café.txt → "caf\303\251.txt") and no subsequent
// git op could resolve them.
func TestParseGitStatus_UnquotesSpecialFilenames(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir) // commits README.md on main

	// Untracked files whose names the default porcelain format would quote.
	const nonASCII = "café.txt"
	const spaced = "with space.txt"
	for _, name := range []string{nonASCII, spaced} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A staged rename to a non-ASCII + spaced name (git mv stages it).
	const renamedTo = "rénamed doc.md"
	runGit(t, dir, "mv", "README.md", renamedTo)

	files := parseGitStatus(t.Context(), dir)

	byPath := make(map[string]gitFile, len(files))
	for _, f := range files {
		byPath[f.Path] = f
		// Regression guard: the old parser surfaced C-quoted paths
		// (e.g. "caf\303\251.txt"). -z output is verbatim, so no
		// returned path may be double-quote-wrapped or carry a
		// backslash escape.
		if strings.HasPrefix(f.Path, `"`) || strings.Contains(f.Path, `\`) {
			t.Errorf("path %q looks C-quoted; -z output must be verbatim", f.Path)
		}
	}

	if f, ok := byPath[nonASCII]; !ok {
		t.Errorf("non-ASCII untracked file %q missing from %+v", nonASCII, files)
	} else if f.Status != "?" {
		t.Errorf("%q status = %q, want untracked", nonASCII, f.Status)
	}
	if _, ok := byPath[spaced]; !ok {
		t.Errorf("spaced untracked file %q missing from %+v", spaced, files)
	}
	// The rename's new (current) path must be present, staged, and
	// un-quoted regardless of whether git reports it as R (rename
	// detected, origin field consumed) or A (add) — both stage the new
	// path under this exact name.
	if f, ok := byPath[renamedTo]; !ok {
		t.Errorf("renamed path %q missing from %+v", renamedTo, files)
	} else if !f.Staged {
		t.Errorf("renamed path %q Staged = false, want true", renamedTo)
	}
}

// "Discard all" sends every dirty path in one request, so the handler must
// clear tracked and untracked files together. Only the tracked half ever had a
// test: `clean` was absent from the exec allowlist, so the untracked half ran
// /bin/false and reported `clean:` with nothing after the colon while the
// tracked half genuinely succeeded. The three file states below are what one
// real "Discard all" click carries.
func TestHandleDiscard_ClearsTrackedAndUntracked(t *testing.T) {
	dir := t.TempDir()
	initFixtureRepo(t, dir) // commits README.md ("hi\n") on main

	// A modified tracked file, an untracked file, and a STAGED new file —
	// the last lands in the untracked bucket because the handler unstages
	// before discarding, so it also exercises the clean path.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staged-new.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "staged-new.txt")

	h := NewHandler(dir)
	body := `{"files":["README.md","untracked.txt","staged-new.txt"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/git/discard", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleDiscard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	// The failure is a 200 carrying an error envelope, so the status alone
	// proves nothing — this is the assertion the bug would have tripped.
	if strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("body = %q, want no error envelope", rec.Body.String())
	}

	if got, err := os.ReadFile(filepath.Join(dir, "README.md")); err != nil {
		t.Errorf("README.md after discard: %v", err)
	} else if string(got) != "hi\n" {
		t.Errorf("README.md = %q, want the committed %q restored", got, "hi\n")
	}
	for _, name := range []string{"untracked.txt", "staged-new.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still present after discard (stat err = %v), want removed", name, err)
		}
	}
}

// A discard failure must name a cause. The message is built by joining
// per-subcommand failures, so an empty subprocess output used to render as a
// bare "<subcommand>:" that told the user nothing.
func TestHandleDiscard_FailureNamesACause(t *testing.T) {
	dir := t.TempDir()
	initFixtureRepo(t, dir)

	h := NewHandler(dir)
	// A tracked path that does not exist: checkout fails with a real message.
	body := `{"files":["no-such-file.txt"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/git/discard", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleDiscard(rec, req)

	got := rec.Body.String()
	// Either it succeeded (nothing to do) or it failed with a stated cause;
	// what it may never do is fail with a message ending at its own colon.
	for _, sub := range []string{subCheckout, subClean} {
		if strings.Contains(got, sub+`: "`) || strings.Contains(got, sub+`:\n`) {
			t.Errorf("body = %q, want a cause after %q, not an empty one", got, sub+":")
		}
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
	ctx := t.Context()

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
	if got := scrubAuth(in); got != want {
		t.Errorf("scrubAuth(long chain) = %q, want %q", got, want)
	}
	// Idempotency: a second call must not change the output.
	if got := scrubAuth(scrubAuth(in)); got != want {
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
	if got := scrubAuth(in); got != want {
		t.Errorf("scrubAuth(%q) = %q, want %q", in, got, want)
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

// TestHandleReclone_RefusesAnIntermediateSymlinkEscape is the handler-level
// counterpart to TestRepoDirForDelete_RefusesAnIntermediateSymlinkEscape: it
// pins that reclone actually CALLS the resolved variant.
//
// The bait is one the unguarded code takes. With the lexical resolver,
// {"repo":"link/victim"} passes every guard reclone has — it is not empty, not
// ".", not the workspace root, `<dir>/.git` exists through the symlink, origin
// resolves, and the scheme is allowed — and os.RemoveAll then deletes a repo
// outside the workspace. So the surviving victim tree is the assertion that
// matters: revert handleReclone to h.repoDir and this test fails on it.
//
// The positive control lives next door: TestHandleReclone_RejectsNonStandardScheme
// drives a plain in-workspace repo all the way to the scheme check, so the guard
// added here is not refusing everything.
func TestHandleReclone_RefusesAnIntermediateSymlinkEscape(t *testing.T) {
	// Requires a real git binary to set up the fixture.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH")
	}
	outside := t.TempDir()
	workDir := t.TempDir()

	victim := filepath.Join(outside, "victim")
	if err := os.MkdirAll(victim, 0o750); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = victim
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	// An ALLOWED scheme deliberately: a rejected scheme would stop the request
	// before os.RemoveAll for a reason that has nothing to do with the escape,
	// and the test would pass with the guard removed.
	run("remote", "add", "origin", "https://example.invalid/victim.git")

	if err := os.Symlink(outside, filepath.Join(workDir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodPost, "/api/git/reclone",
		strings.NewReader(`{"repo":"link/victim"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handleReclone(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("handleReclone({\"repo\":\"link/victim\"}) code = %d, want %d",
			rec.Code, http.StatusBadRequest)
	}
	if body := rec.Body.String(); !strings.Contains(body, "not inside the workspace") {
		t.Errorf("handleReclone({\"repo\":\"link/victim\"}) body = %q, want substring %q",
			body, "not inside the workspace")
	}
	// The assertion the guard exists for: the repo outside the workspace is intact.
	if _, err := os.Stat(filepath.Join(victim, ".git")); err != nil {
		t.Errorf("reclone deleted a repo outside the workspace: os.Stat(%q) = %v, want nil",
			filepath.Join(victim, ".git"), err)
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

func (m *mockPrompter) UtilityPrompt(_ context.Context, _ string, _ vibekit.EffortLevel) (string, error) {
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
			got := parseRemoteHost(tt.in)
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
		got := parseRemoteHost(in)
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
	// Seed corpus from the table-driven test cases (NUL-delimited -z format).
	seeds := [][]byte{
		nil,
		[]byte(""),
		[]byte(" M file.go\x00"),
		[]byte("A  new.go\x00"),
		[]byte("MM both.go\x00"),
		[]byte("?? newfile.go\x00"),
		[]byte("R  new.go\x00old.go\x00"),
		[]byte("RM renamed.go\x00orig.go\x00"),
		[]byte("C  copy.go\x00src.go\x00"),
		[]byte(" M café.txt\x00"),
		[]byte(" M foo -> bar.txt\x00"),
		[]byte("?? somedir/\x00?? real.txt\x00"),
		[]byte("XY\x00 M file.go\x00?\x00"),
		[]byte("M  staged.go\x00 M unstaged.go\x00?? untracked.go\x00A  added.go\x00"),
		[]byte(" M file.go"),
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
		// A git:// URL with a userinfo chain: the scheme the deleted
		// exec-side target contributed, and the one shape the seeds
		// above do not reach (they are all http/https).
		"git://a@b@c@host/repo",
		"?token=secret&other=ok",
		"http://a@b@c@d@e@f@g@h@i@j@k@l@m@n@o@p@host/path",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		result := scrubAuth(data)
		// Post-condition 1: no panic (implicit).
		// Post-condition 2: idempotent.
		if twice := scrubAuth(result); twice != result {
			t.Errorf("scrubAuth not idempotent: f(%q)=%q, f(f(x))=%q", data, result, twice)
		}
		// Post-condition 3: no userinfo between :// and the next /.
		if _, rest, ok := strings.Cut(result, "://"); ok {
			if hostPart, _, hasSlash := strings.Cut(rest, "/"); hasSlash {
				if strings.Contains(hostPart, "@") {
					t.Errorf("scrubAuth(%q) = %q: userinfo '@' remains between :// and /", data, result)
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
		got := parseRemoteHost(data)
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
		// Rune-boundary seeds. The cap cuts at subjectMaxRunes-len(ellipsis) =
		// 69, so a multi-byte rune STARTING at byte 68 straddles that index:
		// slicing bytes there emits half a rune, slicing runes keeps it whole.
		// No space anywhere, so the word-break arm cannot mask the cut.
		strings.Repeat("a", 68) + "\u00e9" + strings.Repeat("a", 10),
		// Same straddle with a 3-byte rune, and with a space early enough that
		// the word-break arm is rejected (<= subjectWordBreakMin), so this too
		// reaches the ellipsis arm with the cut inside the rune.
		"a " + strings.Repeat("b", 66) + "\u0c0b" + strings.Repeat("b", 10),
		// A 4-byte rune straddling the same index.
		strings.Repeat("a", 68) + "\U0001f680" + strings.Repeat("a", 10),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		result := extractCommitMessage(data)
		// Invariant 1: the subject is capped in RUNES — the unit capSubject
		// documents and slices in. Counting BYTES here is what the weekly fuzz
		// caught: a 72-rune subject carrying one 3-byte rune measures 74 bytes,
		// so a byte count reports a correct cap as a violation. Read the bound
		// off the production constant so the two cannot desync.
		firstLine, _, _ := strings.Cut(result, "\n")
		if n := utf8.RuneCountInString(firstLine); n > subjectMaxRunes {
			t.Errorf("extractCommitMessage(%q): subject %q is %d runes, want <=%d",
				data, firstLine, n, subjectMaxRunes)
		}
		// Invariant 1b: capping never splits a rune. capSubject used to slice
		// bytes while its doc said "chars", which cut a multi-byte rune in half;
		// the JSON encoder then replaced the fragment with U+FFFD and the user
		// saw a replacement character in a suggested commit message. Moving the
		// cap to runes closed that, and this is what holds it closed.
		if utf8.ValidString(data) && !utf8.ValidString(result) {
			t.Errorf("extractCommitMessage(%q) = %q: valid UTF-8 in, invalid UTF-8 out",
				data, result)
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
	// Generate synthetic porcelain -z output (NUL-delimited) for sub-benchmarks.
	generate := func(n int) []byte {
		var buf strings.Builder
		for i := range n {
			switch i % 4 {
			case 0:
				buf.WriteString(" M file" + strconv.Itoa(i) + ".go\x00")
			case 1:
				buf.WriteString("A  new" + strconv.Itoa(i) + ".go\x00")
			case 2:
				buf.WriteString("?? untracked" + strconv.Itoa(i) + ".txt\x00")
			case 3:
				buf.WriteString("MM both" + strconv.Itoa(i) + ".go\x00")
			}
		}
		return []byte(buf.String())
	}

	for _, n := range []int{10, 100, 1000} {
		data := generate(n)
		b.Run(strconv.Itoa(n)+"_files", func(b *testing.B) {
			for b.Loop() {
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
			for b.Loop() {
				_ = scrubAuth(tc.input)
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

		// Invariant 1: the subject line is capped in RUNES. This generator draws
		// from an ASCII alphabet, so a byte count agreed here by accident; it is
		// the cap's own unit that decides the assertion, not the alphabet a
		// sibling draw happens to use.
		firstLine, _, _ := strings.Cut(result, "\n")
		if n := utf8.RuneCountInString(firstLine); n > subjectMaxRunes {
			t.Fatalf("subject %q is %d runes, want <=%d", firstLine, n, subjectMaxRunes)
		}

		// Invariant 2: if truncation fired, output ends with the ellipsis.
		trimmedSubject := strings.TrimSpace(subject)
		if wrapper == 0 && utf8.RuneCountInString(trimmedSubject) > subjectMaxRunes && result != "" {
			subjectOut, _, _ := strings.Cut(result, "\n")
			if !strings.HasSuffix(subjectOut, subjectEllipsis) {
				t.Fatalf("truncated subject %q does not end with %q", subjectOut, subjectEllipsis)
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

		// Invariant 1: the subject line is capped in RUNES, matching the
		// contract capSubject implements. Counting bytes here contradicts
		// that cap: a compliant 72-rune subject of multi-byte runes is far
		// longer than 72 bytes, which rapid finds on most seeds.
		firstLine, _, _ := strings.Cut(result, "\n")
		if n := utf8.RuneCountInString(firstLine); n > subjectMaxRunes {
			rt.Fatalf("subject %q is %d runes, want <=%d", firstLine, n, subjectMaxRunes)
		}

		// Invariant 2: no leading/trailing whitespace.
		if result != strings.TrimSpace(result) {
			rt.Fatalf("result has leading/trailing whitespace: %q", result)
		}
	})
}

// --- conditional boundary / branch regression guards ---
// These pin observable behaviour at the exact edge of conditionals that
// general-purpose tests leave ambiguous (off-by-one boundaries, negated
// guards). Each asserts an output whose value depends on the precise
// operator at the site under test.

// A subject longer than 72 chars whose only space within subject[:69]
// sits at exactly column 30: extractCommitMessage's strict ">30"
// word-boundary rule treats column 30 as too early, so it breaks at
// column 69 rather than at the space.
func TestExtractCommitMessage_WordBreakBoundary(t *testing.T) {
	input := strings.Repeat("A", 30) + " " + strings.Repeat("B", 45)
	got := extractCommitMessage(input)
	want := strings.Repeat("A", 30) + " " + strings.Repeat("B", 38) + "..."
	if got != want {
		t.Errorf("extractCommitMessage(boundary idx==30) = %q, want %q", got, want)
	}
}

// writeGitError includes the detail field only when it is non-empty.
func TestWriteGitError_DetailField(t *testing.T) {
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
		t.Errorf("detail = %q, want %q", m["detail"], "boundary-detail")
	}

	rec2 := httptest.NewRecorder()
	writeGitError(rec2, KindShowFailed, "")
	var m2 map[string]string
	if err := json.Unmarshal(rec2.Body.Bytes(), &m2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m2["detail"]; ok {
		t.Errorf("detail key present for empty detail; want it omitted")
	}
}

// In a real repo, `git show HEAD:<missing>` exits 128 without "not a git
// repository"; gitShowCmd classifies that as ErrPathNotInRef with empty
// output rather than surfacing the raw exec error.
func TestGitShowCmd_MissingPathInRealRepo(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	out, err := gitShowCmd(t.Context(), dir, refHEAD, "does-not-exist.txt")
	if !errors.Is(err, ErrPathNotInRef) {
		t.Errorf("err = %v, want ErrPathNotInRef", err)
	}
	if out != "" {
		t.Errorf("out = %q, want empty for path-not-in-ref", out)
	}
}

// PR number 10_000_000 is the exact accepted cap: handlePRFetch passes the
// validation guard (failing only later at the remote lookup), so it must
// not be rejected as an invalid number.
func TestHandlePRFetch_NumberBoundary(t *testing.T) {
	h := NewHandler(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-fetch", strings.NewReader(`{"number":10000000}`))
	rec := httptest.NewRecorder()
	h.handlePRFetch(rec, req)
	if rec.Code == http.StatusBadRequest {
		t.Errorf("code = 400 at number==10_000_000; body = %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "invalid PR number") {
		t.Errorf("boundary number rejected as invalid; body = %s", rec.Body.String())
	}
}

// handleCommitMessage calls the prompter only when there are staged
// changes: a clean repo reports no_staged_changes without invoking it; a
// repo with a staged change generates a message.
func TestHandleCommitMessage_StagedGate(t *testing.T) {
	skipNoGit(t)

	t.Run("clean_repo_reports_no_staged", func(t *testing.T) {
		dir := t.TempDir()
		initFixtureRepo(t, dir)
		mp := &mockPrompter{result: "feat: gk commit"}
		a := NewAIHandler(dir, mp)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/git/commit-message", strings.NewReader(`{}`))
		a.handleCommitMessage(rec, req)
		if mp.called {
			t.Errorf("prompter called on clean repo; want it skipped")
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
		runGit(t, dir, "add", "README.md")
		mp := &mockPrompter{result: "feat: gk commit"}
		a := NewAIHandler(dir, mp)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/git/commit-message", strings.NewReader(`{}`))
		a.handleCommitMessage(rec, req)
		if !mp.called {
			t.Errorf("prompter not called on staged repo; want it invoked")
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

// handlePRDescription proceeds (and calls the prompter) when
// `git diff base...HEAD` is non-empty, without falling through to the
// origin fallback.
func TestHandlePRDescription_DiffGate(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	runGit(t, dir, "checkout", "-q", "-b", "feature")
	writeCommit(t, dir, "README.md", "changed\n", "feature commit")
	mp := &mockPrompter{result: "My PR description"}
	a := NewAIHandler(dir, mp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-description", strings.NewReader(`{}`))
	a.handlePRDescription(rec, req)
	if !mp.called {
		t.Errorf("prompter not called with a non-empty base diff; want it invoked")
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["output"] != "My PR description" {
		t.Errorf("output = %q, want %q; body = %s", resp["output"], "My PR description", rec.Body.String())
	}
}

// When `git diff base...HEAD` is empty, handlePRDescription falls back to
// `git diff origin/base...HEAD`; a non-empty fallback diff proceeds to
// generate the description.
func TestHandlePRDescription_FallbackDiffGate(t *testing.T) {
	skipNoGit(t)
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	work := filepath.Join(base, "work")
	if err := os.Mkdir(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, origin)
	runGit(t, base, "clone", "-q", origin, work)
	writeCommit(t, work, "README.md", "changed\n", "work commit")
	mp := &mockPrompter{result: "My PR description"}
	a := NewAIHandler(work, mp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-description", strings.NewReader(`{}`))
	a.handlePRDescription(rec, req)
	if !mp.called {
		t.Errorf("prompter not called with non-empty fallback diff; want it invoked")
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["output"] != "My PR description" {
		t.Errorf("output = %q, want %q; body = %s", resp["output"], "My PR description", rec.Body.String())
	}
}

// handleLog resolves the log ref to origin/<branch> when the upstream
// verifies, so a work tree behind its origin still shows the origin
// commits.
func TestHandleLog_RefResolution(t *testing.T) {
	work := behindRepo(t)
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
	if len(resp.Entries) != 2 {
		t.Fatalf("entries = %v (len %d), want 2", resp.Entries, len(resp.Entries))
	}
	if !strings.Contains(strings.Join(resp.Entries, "\n"), "second commit") {
		t.Errorf("entries %v missing 'second commit' (origin/main ref not used)", resp.Entries)
	}
}

// handleLog logs a debug breadcrumb when `git remote get-url` fails (no
// origin) and stays silent when it succeeds.
func TestHandleLog_RemoteErrorLog(t *testing.T) {
	t.Run("no_origin_logs_failure", func(t *testing.T) {
		skipNoGit(t)
		dir := t.TempDir()
		initFixtureRepo(t, dir)
		buf := captureLogs(t)
		h := NewHandler(dir)
		h.handleLog(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/git/log", nil))
		if !strings.Contains(buf.String(), "git remote get-url failed during log") {
			t.Errorf("expected debug log for a failed remote get-url")
		}
	})
	t.Run("with_origin_no_failure_log", func(t *testing.T) {
		skipNoGit(t)
		dir := t.TempDir()
		initFixtureRepo(t, dir)
		runGit(t, dir, "remote", "add", "origin", "https://example.com/x.git")
		buf := captureLogs(t)
		h := NewHandler(dir)
		h.handleLog(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/git/log", nil))
		if strings.Contains(buf.String(), "git remote get-url failed during log") {
			t.Errorf("unexpected failure log when remote get-url succeeds")
		}
	})
}

// handleRemove returns {"ok":true} (not an error body) after removing an
// existing subdirectory.
func TestHandleRemove_SuccessBody(t *testing.T) {
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
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("body = %q, want ok:true on successful remove", body)
	}
	if strings.Contains(body, "remove failed") {
		t.Errorf("body = %q contains 'remove failed' on a successful remove", body)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("subdir still exists after remove: %v", err)
	}
}

// handleStatus fetches from the remote only when ?quick is absent: the
// default path attempts a fetch (logging its failure for a bogus remote);
// ?quick=1 skips it.
func TestHandleStatus_QuickControlsFetch(t *testing.T) {
	mk := func(t *testing.T) string {
		t.Helper()
		skipNoGit(t)
		dir := t.TempDir()
		initFixtureRepo(t, dir)
		runGit(t, dir, "remote", "add", "origin", "/nonexistent-remote")
		return dir
	}

	t.Run("no_quick_fetches", func(t *testing.T) {
		dir := mk(t)
		buf := captureLogs(t)
		h := NewHandler(dir)
		h.handleStatus(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/git/status", nil))
		if !strings.Contains(buf.String(), "git fetch during status failed") {
			t.Errorf("expected a fetch attempt without the quick param")
		}
	})

	t.Run("quick_skips_fetch", func(t *testing.T) {
		dir := mk(t)
		buf := captureLogs(t)
		h := NewHandler(dir)
		h.handleStatus(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/git/status?quick=1", nil))
		if strings.Contains(buf.String(), "git fetch during status failed") {
			t.Errorf("unexpected fetch with the quick param")
		}
	})
}

// collectStatus populates Remote from `git remote get-url origin` when a
// remote is configured.
func TestCollectStatus_RemoteSet(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	const url = "https://example.com/x.git"
	runGit(t, dir, "remote", "add", "origin", url)
	st := collectStatus(t.Context(), dir, defaultTimeouts(), &singleflight.Group{}, false)
	if st.Remote != url {
		t.Errorf("Remote = %q, want %q", st.Remote, url)
	}
}

// collectStatus counts stash entries from `git stash list`.
func TestCollectStatus_StashCount(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "stash")
	st := collectStatus(t.Context(), dir, defaultTimeouts(), &singleflight.Group{}, false)
	if st.Stashes != 1 {
		t.Errorf("Stashes = %d, want 1", st.Stashes)
	}
}

// collectStatus.HasGH reflects whether the gh CLI is on PATH.
func TestCollectStatus_HasGH(t *testing.T) {
	mkRepo := func(t *testing.T) string {
		t.Helper()
		repo := t.TempDir()
		// A bare .git directory is enough for IsRepo (os.Stat-based);
		// the gh lookup is reached regardless of git availability.
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		return repo
	}

	// hasGH is memoized (sync.OnceValue) — the answer is process-global —
	// so each subtest swaps in a fresh once-value bound to its PATH.
	freshHasGH := func(t *testing.T) {
		t.Helper()
		orig := hasGH
		hasGH = sync.OnceValue(func() bool {
			_, err := exec.LookPath("gh")
			return err == nil
		})
		t.Cleanup(func() { hasGH = orig })
	}

	t.Run("gh_absent", func(t *testing.T) {
		repo := mkRepo(t)
		t.Setenv("PATH", t.TempDir()) // empty bin dir: gh not found
		freshHasGH(t)
		st := collectStatus(t.Context(), repo, defaultTimeouts(), &singleflight.Group{}, false)
		if st.HasGH {
			t.Errorf("HasGH = true with gh absent")
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
		freshHasGH(t)
		st := collectStatus(t.Context(), repo, defaultTimeouts(), &singleflight.Group{}, false)
		if !st.HasGH {
			t.Errorf("HasGH = false with gh present")
		}
	})
}

// parseGitStatusOutput's inclusive lower bound for a parseable record is
// 4 bytes ("XY P": 2 status bytes, a space, a >=1-char path). A record of
// exactly 4 bytes yields one entry; a 3-byte record (no path) is dropped
// rather than emitting a useless empty-path entry.
func TestParseGitStatusOutput_LenBoundary(t *testing.T) {
	got := parseGitStatusOutput([]byte("?? x\x00"))
	if len(got) != 1 {
		t.Fatalf("parseGitStatusOutput(\"?? x\") len = %d, want 1", len(got))
	}
	if got[0].Status != "?" || got[0].Display != "Untracked" || got[0].Path != "x" {
		t.Errorf("entry = %+v, want {Path:\"x\" Status:\"?\" Display:\"Untracked\"}", got[0])
	}

	// One byte short of a path: dropped, not emitted as an empty-path entry.
	if got := parseGitStatusOutput([]byte("?? \x00")); len(got) != 0 {
		t.Errorf("parseGitStatusOutput(\"?? \") = %+v, want no entries", got)
	}
}

// discoverRepos does not emit the cap warning at exactly maxRepoEntries
// entries (the warning fires only strictly above the cap).
func TestDiscoverRepos_EntryCapBoundary(t *testing.T) {
	workDir := t.TempDir()
	for i := range maxRepoEntries {
		f := filepath.Join(workDir, fmt.Sprintf("e%05d", i))
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	buf := captureLogs(t)
	_ = discoverRepos(t.Context(), workDir)
	if strings.Contains(buf.String(), "entry count exceeds cap") {
		t.Errorf("cap warning fired at exactly maxRepoEntries")
	}
}

// sanitizeRepoPaths accepts exactly maxRepoPaths paths and rejects one
// more (maxRepoPaths itself is within the allowed count).
func TestSanitizeRepoPaths_CountBoundary(t *testing.T) {
	paths := make([]string, maxRepoPaths)
	for i := range paths {
		paths[i] = "f" + strconv.Itoa(i)
	}
	got, err := sanitizeRepoPaths(paths)
	if err != nil {
		t.Fatalf("err = %v at exactly maxRepoPaths", err)
	}
	if len(got) != maxRepoPaths {
		t.Errorf("len(got) = %d, want %d", len(got), maxRepoPaths)
	}
	if _, err := sanitizeRepoPaths(make([]string, maxRepoPaths+1)); err == nil {
		t.Errorf("expected an error for maxRepoPaths+1 paths")
	}
}

// validateFilePath's traversal rule is COMPONENT-precise
// (pathinside.HasDotDot), not a ".." substring search. This test pins
// the behaviour change that came with adopting it: a name that merely
// contains two adjacent dots is a name, and is accepted, while every
// real `..` component is still refused.
func TestValidateFilePath_DotDotIsAComponentNotASubstring(t *testing.T) {
	accepted := []string{
		"v1..v2.txt",     // the shape the old strings.Contains refused
		"a..b/main.go",   // two adjacent dots mid-name, in a directory
		"..extras/x.mkv", // first component merely BEGINS with two dots
		"...",            // a legal directory name
		"a/./b",          // unclean but not traversing: canonicality is not tested
	}
	for _, p := range accepted {
		t.Run("accept/"+p, func(t *testing.T) {
			if !validateFilePath(p) {
				t.Errorf("validateFilePath(%q) = false, want true (no `..` component)", p)
			}
		})
	}
	refused := []string{
		"..",             // the component alone
		"../x",           // leading traversal
		"a/../b",         // buried traversal: RelEscapes would CLEAN this to "b"
		"a/..",           // trailing traversal
		"a/../../etc/pw", // multi-level
	}
	for _, p := range refused {
		t.Run("refuse/"+p, func(t *testing.T) {
			if validateFilePath(p) {
				t.Errorf("validateFilePath(%q) = true, want false (`..` component)", p)
			}
		})
	}
}

// validateFilePath accepts a space (0x20, the boundary) but rejects
// genuine control chars (< 0x20) and DEL (0x7f).
func TestValidateFilePath_ControlCharBoundary(t *testing.T) {
	if !validateFilePath("foo bar") {
		t.Errorf("validateFilePath(\"foo bar\") = false, want true")
	}
	if validateFilePath("foo\x1fbar") {
		t.Errorf("validateFilePath with a 0x1f control char = true, want false")
	}
	if validateFilePath("foo\x7fbar") {
		t.Errorf("validateFilePath with 0x7f (DEL) = true, want false")
	}
}

// getRecentCommits returns the commit subjects for a real repo and the
// "No commit history available" sentinel for a non-repo directory.
func TestGetRecentCommits_ReturnsHistoryForRealRepo(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-q", "-m", "seed commit subject")

	got := getRecentCommits(t.Context(), dir, 10)
	if !strings.Contains(got, "seed commit subject") {
		t.Errorf("getRecentCommits(repo with a commit) = %q, want it to contain the commit subject", got)
	}
	if got == "No commit history available" {
		t.Errorf("getRecentCommits(repo with a commit) returned the empty-history sentinel")
	}

	// A non-repo directory makes the git command error, so the sentinel
	// is the correct result.
	if got := getRecentCommits(t.Context(), t.TempDir(), 10); got != "No commit history available" {
		t.Errorf("getRecentCommits(non-repo dir) = %q, want the sentinel", got)
	}
}

// capturePrompter records the prompt it was handed so a test can assert
// on the text the AI git handlers build (the diff body, the commit-log
// section). It always succeeds.
type capturePrompter struct {
	prompt string
	result string
}

func (c *capturePrompter) UtilityPrompt(_ context.Context, prompt string, _ vibekit.EffortLevel) (string, error) {
	c.prompt = prompt
	return c.result, nil
}

// handleCommitMessage feeds the AI the full staged diff (unified-diff
// hunk bodies), not the `--stat` summary and not a size-truncated stub:
// the prompt must carry the actual changed line and a hunk header.
func TestHandleCommitMessage_PromptCarriesFullStagedDiff(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\nUNIQUE_DIFF_MARKER_LINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")

	cp := &capturePrompter{result: "feat: x"}
	a := NewAIHandler(dir, cp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/commit-message", strings.NewReader(`{}`))
	a.handleCommitMessage(rec, req)

	if !strings.Contains(cp.prompt, "UNIQUE_DIFF_MARKER_LINE") {
		t.Errorf("commit prompt missing the staged diff body; prompt:\n%s", cp.prompt)
	}
	if !strings.Contains(cp.prompt, "@@") {
		t.Errorf("commit prompt missing unified-diff hunk header (got the --stat summary instead of the full diff?); prompt:\n%s", cp.prompt)
	}
}

// handlePRDescription caps the branch diff at 12 KB but must still feed
// the AI the actual diff content for normal-sized branches; a cap that
// collapses to zero would truncate everything down to the size-stub
// suffix.
func TestHandlePRDescription_PromptCarriesBranchDiff(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	runGit(t, dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\nPR_DIFF_MARKER_LINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-q", "-m", "feature change")

	cp := &capturePrompter{result: "desc"}
	a := NewAIHandler(dir, cp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-description", strings.NewReader(`{}`))
	a.handlePRDescription(rec, req)

	if !strings.Contains(cp.prompt, "PR_DIFF_MARKER_LINE") {
		t.Errorf("PR prompt missing the branch diff body; prompt:\n%s", cp.prompt)
	}
}

// handlePRDescription uses the local `base..HEAD` commit log verbatim
// when it is non-empty; it must NOT fall through to the
// `origin/base..HEAD` fallback. When local main is ahead of origin/main,
// the fallback would wrongly fold in commits that aren't part of this
// branch's own history.
func TestHandlePRDescription_UsesLocalLogWhenNonEmpty(t *testing.T) {
	skipNoGit(t)
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	work := filepath.Join(base, "work")
	if err := os.Mkdir(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, origin)                   // origin main @ C1
	runGit(t, base, "clone", "-q", origin, work) // work main @ C1, origin/main @ C1
	// Advance local main beyond origin/main with a recognizable commit.
	writeCommit(t, work, "main.txt", "m\n", "MAIN_ONLY_COMMIT") // work main @ C2
	// Branch off and add the feature commit (HEAD @ C3).
	runGit(t, work, "checkout", "-q", "-b", "feature")
	writeCommit(t, work, "feat.txt", "f\n", "FEATURE_ONLY_COMMIT")

	cp := &capturePrompter{result: "desc"}
	a := NewAIHandler(work, cp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-description", strings.NewReader(`{}`))
	a.handlePRDescription(rec, req)

	if !strings.Contains(cp.prompt, "FEATURE_ONLY_COMMIT") {
		t.Fatalf("PR prompt log missing the branch's own commit; prompt:\n%s", cp.prompt)
	}
	if strings.Contains(cp.prompt, "MAIN_ONLY_COMMIT") {
		t.Errorf("PR prompt log folded in origin/main fallback commits; the non-empty local log should win; prompt:\n%s", cp.prompt)
	}
}

// When the local `base..HEAD` log is empty (HEAD == base), the handler
// falls back to `origin/base..HEAD` so a freshly-pushed-from-clone
// branch still gets a commit list in its PR description.
func TestHandlePRDescription_FallsBackToOriginLogWhenLocalEmpty(t *testing.T) {
	skipNoGit(t)
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	work := filepath.Join(base, "work")
	if err := os.Mkdir(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	initFixtureRepo(t, origin)
	runGit(t, base, "clone", "-q", origin, work)
	// Commit on work's main (HEAD == main): `main..HEAD` is empty, but
	// `origin/main..HEAD` carries this commit.
	writeCommit(t, work, "README.md", "changed\n", "FALLBACK_LOG_COMMIT")

	cp := &capturePrompter{result: "desc"}
	a := NewAIHandler(work, cp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-description", strings.NewReader(`{}`))
	a.handlePRDescription(rec, req)

	if !strings.Contains(cp.prompt, "FALLBACK_LOG_COMMIT") {
		t.Errorf("PR prompt log missing the origin-fallback commit; prompt:\n%s", cp.prompt)
	}
}

// handlePRFetch proceeds to the fetch when `git remote get-url origin`
// succeeds; an unreachable origin must surface as an error envelope, not
// a success response that echoes the origin URL back as output.
func TestHandlePRFetch_UnreachableOriginReportsError(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	runGit(t, dir, "remote", "add", "origin", "/nonexistent-origin-path")

	h := NewHandler(dir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/pr-fetch", strings.NewReader(`{"number":42}`))
	h.handlePRFetch(rec, req)

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if _, isError := resp["error"]; !isError {
		t.Errorf("pr-fetch against an unreachable origin returned %v, want an error envelope", resp)
	}
	if out, ok := resp["output"]; ok {
		t.Errorf("pr-fetch returned success output %q; a successful origin lookup must not short-circuit to echoing the URL", out)
	}
}

// sanitizeBranchName must turn arbitrary model output into a valid,
// compact git branch name (or "" when nothing usable remains).
func TestSanitizeBranchName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean passthrough", "feat/tab-status-badges", "feat/tab-status-badges"},
		{"uppercase + spaces", "Feat/Tab Status Badges", "feat/tab-status-badges"},
		{"quoted + fenced", "```\n\"fix/retry-test\"\n```", "fix/retry-test"},
		{"explanation line dropped", "feat/exif-rename\nThis name reflects...", "feat/exif-rename"},
		{"underscores to dashes", "chore/update_deps_now", "chore/update-deps-now"},
		{"exotic chars dropped", "feat/caf\u00e9-\u2728launch!", "feat/caf-launch"},
		{"dash runs collapsed", "fix/--double--dash--", "fix/double-dash"},
		{"empty segments removed", "feat//name/", "feat/name"},
		{"dotdot segment removed", "feat/../escape", "feat/escape"},
		{"cap at 60 bytes", "feat/" + strings.Repeat("a", 80), "feat/" + strings.Repeat("a", 55)},
		{"nothing usable", "\u2728\u2728 !!", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeBranchName(tc.in); got != tc.want {
				t.Errorf("sanitizeBranchName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Each prompt template renders against the data map its builder passes.
// template.Must only proves the template PARSES; a field reference the
// builder does not supply fails at execute time, and the builder answers
// that by logging and returning whatever it had rendered so far — so a
// silently truncated prompt reaching the model is visible only in the log.
func TestPromptBuilders_renderAgainstTheirData(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		logs := captureLogs(t)
		got := buildCommitPrompt("recent history", "the staged diff")
		if strings.Contains(logs.String(), "commit prompt template execute failed") {
			t.Errorf("buildCommitPrompt(%q, %q) logged a template failure: %s",
				"recent history", "the staged diff", logs.String())
		}
		if !strings.Contains(got, "the staged diff") {
			t.Errorf("buildCommitPrompt(%q, %q) = %q, want it to carry the diff",
				"recent history", "the staged diff", got)
		}
	})

	t.Run("pr", func(t *testing.T) {
		logs := captureLogs(t)
		got := buildPRPrompt("the commit log", "the branch diff")
		if strings.Contains(logs.String(), "pr prompt template execute failed") {
			t.Errorf("buildPRPrompt(%q, %q) logged a template failure: %s",
				"the commit log", "the branch diff", logs.String())
		}
		if !strings.Contains(got, "the branch diff") {
			t.Errorf("buildPRPrompt(%q, %q) = %q, want it to carry the diff",
				"the commit log", "the branch diff", got)
		}
	})

	t.Run("branch", func(t *testing.T) {
		logs := captureLogs(t)
		got := buildBranchPrompt("main\nfeat/old", "the work in progress")
		if strings.Contains(logs.String(), "branch prompt template execute failed") {
			t.Errorf("buildBranchPrompt(%q, %q) logged a template failure: %s",
				"main\nfeat/old", "the work in progress", logs.String())
		}
		if !strings.Contains(got, "the work in progress") {
			t.Errorf("buildBranchPrompt(%q, %q) = %q, want it to carry the context",
				"main\nfeat/old", "the work in progress", got)
		}
	})
}

// The branch-name endpoint: the model's raw output is sanitized into the
// response, and the prompt carries the repo's uncommitted context.
func TestHandleBranchName(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\nBRANCH_CONTEXT_MARKER\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cp := &capturePrompter{result: "  Feat/Say Hello World  "}
	a := NewAIHandler(dir, cp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/branch-name", strings.NewReader(`{}`))
	a.handleBranchName(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad body: %v", err)
	}
	if out["output"] != "feat/say-hello-world" {
		t.Errorf("output = %q, want sanitized feat/say-hello-world", out["output"])
	}
	if !strings.Contains(cp.prompt, "BRANCH_CONTEXT_MARKER") {
		t.Errorf("prompt missing uncommitted diff context; prompt:\n%s", cp.prompt)
	}
}

// A repo with no uncommitted changes falls back to recent commits as the
// naming context instead of erroring.
func TestHandleBranchName_CleanTreeUsesCommits(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)

	cp := &capturePrompter{result: "chore/initial-commit-follow-up"}
	a := NewAIHandler(dir, cp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/git/branch-name", strings.NewReader(`{}`))
	a.handleBranchName(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(cp.prompt, "Recent commits:") {
		t.Errorf("clean-tree prompt should carry the commit fallback; prompt:\n%s", cp.prompt)
	}
}

// TestRepoDirForDelete_RefusesAnIntermediateSymlinkEscape pins the containment
// check the DESTRUCTIVE callers need.
//
// repoDir is deliberately lexical-only so a user can address a symlinked repo by
// its link name, and its doc justifies that on the ground that this package
// performs "read operations". Two handlers are the exception, because they call
// os.RemoveAll: handleRemove and handleReclone. That unlinks a final symlink
// rather than following it, but the kernel resolves every INTERMEDIATE
// component, so a lexically-clean "link/victim" deletes outside the workspace.
// No `..`, not absolute, and the dir == workDir guard does not fire.
//
// Both directions are asserted, because a fix that simply refused symlinks would
// break the feature the lexical rule exists for.
func TestRepoDirForDelete_RefusesAnIntermediateSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	workDir := t.TempDir()

	// The escape: a symlink inside the workspace pointing at a tree outside it,
	// with a real victim directory under the target.
	if err := os.MkdirAll(filepath.Join(outside, "victim"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, "link")); err != nil {
		t.Fatal(err)
	}
	// The feature: a symlink inside the workspace pointing at a real repo, also
	// outside it. Addressing THIS by its link name must keep working for reads,
	// and resolving it for a delete must land on the resolved target rather than
	// be refused for being a link.
	realRepo := filepath.Join(outside, "realrepo")
	if err := os.MkdirAll(realRepo, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRepo, filepath.Join(workDir, "repolink")); err != nil {
		t.Fatal(err)
	}
	// An ordinary repo directly inside the workspace: the common case.
	if err := os.MkdirAll(filepath.Join(workDir, "plain"), 0o750); err != nil {
		t.Fatal(err)
	}

	h := &Handler{workDir: workDir}

	if got := h.repoDirForDelete("link/victim"); got != "" {
		t.Errorf("repoDirForDelete(%q) = %q, want \"\" (it resolves outside the workspace)",
			"link/victim", got)
	}
	// `..` falls back to workDir in the lexical resolver, so it arrives here as
	// the root and the caller refuses it as "cannot remove workspace root" -- a
	// different refusal from an escape, which is why it is not "" .
	if got := h.repoDirForDelete("../escape"); got != workDir {
		t.Errorf("repoDirForDelete(%q) = %q, want the workspace root %q", "../escape", got, workDir)
	}
	// A repo that does not exist is NOT an escape: its parent is the workspace, so
	// it passes, and os.RemoveAll is a no-op on a missing path. Demanding existence
	// here would add a TOCTOU between the check and the delete for no benefit.
	if got := h.repoDirForDelete("nope"); got != filepath.Join(workDir, "nope") {
		t.Errorf("repoDirForDelete(%q) = %q, want the lexical join", "nope", got)
	}
	// The lexical resolver still names it, which is what makes the escape
	// reachable without this check and is why the test asserts on both.
	if lexical := h.repoDir("link/victim"); lexical == "" {
		t.Error("repoDir(\"link/victim\") = \"\", want the lexical join (the premise of this test)")
	}

	wantPlain := filepath.Join(workDir, "plain")
	if got := h.repoDirForDelete("plain"); got != wantPlain {
		t.Errorf("repoDirForDelete(%q) = %q, want %q", "plain", got, wantPlain)
	}
	// A symlinked repo stays addressable, and the LEXICAL path is returned so
	// os.RemoveAll unlinks the link rather than deleting the repo it points at.
	wantLink := filepath.Join(workDir, "repolink")
	if got := h.repoDirForDelete("repolink"); got != wantLink {
		t.Errorf("repoDirForDelete(%q) = %q, want %q (a symlinked repo stays addressable)",
			"repolink", got, wantLink)
	}
}

// handleLog carries the forge commit-URL prefix so the client can link each
// hash without deriving a web location itself, and carries an empty one when
// the repo has no origin — the client renders a plain hash then.
func TestHandleLog_CommitURLPrefix(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{name: "github_origin", origin: "https://github.com/foo/bar.git", want: "https://github.com/foo/bar/commit/"},
		{name: "gitlab_origin", origin: "git@gitlab.com:foo/bar.git", want: "https://gitlab.com/foo/bar/-/commit/"},
		{name: "no_origin", origin: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skipNoGit(t)
			dir := t.TempDir()
			initFixtureRepo(t, dir)
			if tt.origin != "" {
				runGit(t, dir, "remote", "add", "origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			NewHandler(dir).handleLog(rec, httptest.NewRequest(http.MethodGet, "/api/git/log", nil))
			var resp struct {
				CommitURLPrefix string `json:"commit_url_prefix"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("Setup: unmarshal handleLog body %q: %v", rec.Body.String(), err)
			}
			if resp.CommitURLPrefix != tt.want {
				t.Errorf("handleLog(origin=%q) commit_url_prefix = %q, want %q", tt.origin, resp.CommitURLPrefix, tt.want)
			}
		})
	}
}
