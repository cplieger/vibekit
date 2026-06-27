package filehandler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// For tests, temporarily remove "tmp" from the blacklist so we can use t.TempDir().
func init() {
	delete(blacklist, "tmp")
}

func testDir(t *testing.T) (h *Handler, dir, prefix string) {
	t.Helper()
	dir = t.TempDir() // e.g. /tmp/TestXxx123
	var err error
	h, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// prefix is the request path relative to /
	prefix = strings.TrimPrefix(dir, "/")
	return h, dir, prefix
}

func getReq(t *testing.T, h *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func postReq(t *testing.T, h *Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func putReq(t *testing.T, h *Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// multipartUpload builds an /api/file/upload multipart body targeting
// dir. Returns the request ready to ServeHTTP against the handler's
// mux. Files map order is non-deterministic; callers that care about
// upload order should assert on set membership.
func multipartUpload(t *testing.T, targetDir string, files map[string][]byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("dir", targetDir); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		fw, err := w.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/file/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// --- resolvePath tests ---

func TestResolvePath_Blacklisted(t *testing.T) {
	for _, dir := range []string{"etc/passwd", "proc/1/status", "var/log/syslog"} {
		_, err := resolvePath(dir)
		if err == nil {
			t.Errorf("expected error for blacklisted path %q", dir)
		}
	}
}

func TestResolvePath_Allowed(t *testing.T) {
	got, err := resolvePath("workspace/myrepo/file.go")
	if err != nil {
		t.Fatal(err)
	}
	// workspace doesn't exist so EvalSymlinks returns ENOENT on the
	// leaf; we fall through to the lexical form.
	if got != "/workspace/myrepo/file.go" {
		t.Errorf("got %q, want /workspace/myrepo/file.go", got)
	}
}

func TestResolvePath_Root(t *testing.T) {
	got, err := resolvePath("/")
	if err != nil {
		t.Fatalf("resolvePath(\"/\") error: %v", err)
	}
	if got != "/" {
		t.Errorf("got %q, want /", got)
	}
}

func TestResolvePath_TraversalIntoBlacklisted(t *testing.T) {
	_, err := resolvePath("workspace/../../etc/passwd")
	if err == nil {
		t.Error("expected error for traversal into blacklisted dir")
	}
}

// Normalisation: resolvePath cleans common noisy input forms.
func TestResolvePath_Normalisation(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"workspace/myrepo", "/workspace/myrepo"},
		{"workspace//myrepo", "/workspace/myrepo"},
		{"workspace/./myrepo", "/workspace/myrepo"},
		{"workspace/myrepo/", "/workspace/myrepo"},
		{"workspace/a/../b", "/workspace/b"},
		{"/workspace/a", "/workspace/a"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := resolvePath(tc.in)
			if err != nil {
				t.Fatalf("resolvePath(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("resolvePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// S1 regression: a symlink planted inside an allowed directory must
// not grant access to a blacklisted target.
func TestResolvePath_SymlinkToBlacklistedRejected(t *testing.T) {
	dir := t.TempDir()
	prefix := strings.TrimPrefix(dir, "/")

	link := filepath.Join(dir, "evil-link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Lexical form is /tmp/.../evil-link which passes the lexical
	// blacklist (top segment = "tmp" which we removed for tests).
	// EvalSymlinks resolves to /etc, which must trip the blacklist.
	_, err := resolvePath(prefix + "/evil-link")
	if err == nil {
		t.Errorf("resolvePath via symlink to /etc returned nil error; expected rejection")
	}
}

// S1 regression: reading through a symlink that points at a sensitive
// path is blocked by the symlink-aware resolver. We can't create
// /config/push-subs.json in a test, so we inject a sensitive entry
// targeting an actual file inside the temp dir and symlink another
// name onto it. EvalSymlinks must resolve the link to the real
// sensitive path and the enforceAccess re-check must 403.
func TestReadFile_SymlinkToSensitive_Blocked(t *testing.T) {
	h, _ := New("/")
	dir := t.TempDir()
	prefix := strings.TrimPrefix(dir, "/")

	secret := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(secret, []byte("top"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "peek")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	orig := sensitivePrefixes
	t.Cleanup(func() { sensitivePrefixes = orig })
	// Register the real target as sensitive — the lexical form of
	// "peek" itself is not in the list, so without EvalSymlinks the
	// read would succeed.
	sensitivePrefixes = append([]sensitivePath{{Path: secret, IsDir: false}}, orig...)

	rec := getReq(t, h, "/api/file?path="+prefix+"/peek")
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// S1 regression: writing through a symlink whose parent escapes the
// workspace is refused at resolve time (not absorbed by O_NOFOLLOW on
// the wrong side of the check).
func TestWriteFile_SymlinkedParent_Blocked(t *testing.T) {
	h, _ := New("/")
	dir := t.TempDir()
	prefix := strings.TrimPrefix(dir, "/")

	link := filepath.Join(dir, "evil-parent")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rec := putReq(t, h, "/api/file?path="+prefix+"/evil-parent/foo.txt",
		`{"content":"pwned"}`)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// --- isSensitive tests (direct coverage) ---

func TestIsSensitive(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"exact_vibekit_md", "/config/kiro/steering/vibekit.md", true},
		{"exact_env_md", "/config/kiro/steering/environment.md", true},
		{"dir_agents_match", "/config/kiro/agents/foo.json", true},
		{"unrelated_file", "/workspace/repo/main.go", false},
		{"sibling_steering_ok", "/config/kiro/steering/other.md", false},
		{"chats_dir_deep", "/config/chats/deep/nested.json", true},
		{"exact_push_subs", "/config/push-subs.json", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSensitive(tc.path); got != tc.want {
				t.Errorf("isSensitive(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// isProtectedDir: blocks a container of any sensitive path, whether
// the sensitive entry is a dir prefix (trailing /) or an exact file.
func TestIsProtectedDir(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Protected: dir itself listed (or ancestor of listed dir).
		{"/config/chats", true},
		{"/config/kiro/agents", true},
		{"/config/kiro/steering", true}, // contains sensitive files
		{"/config/kiro", true},          // encloses multiple sensitive dirs
		{"/config", true},               // encloses push-subs.json
		// Not protected: leaves and unrelated paths.
		{"/workspace", false},
		{"/workspace/repo", false},
		// Note: a path below a sensitive dir prefix still matches
		// because callers already ran it through isSensitive (which
		// would also block it). isProtectedDir is the defense for
		// the CONTAINER case, not a substitute for isSensitive.
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := isProtectedDir(tc.path); got != tc.want {
				t.Errorf("isProtectedDir(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// --- looksBinary boundaries ---

func TestLooksBinary_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", []byte{}, false},
		{"ascii_only", []byte("hello world"), false},
		{"leading_nul", []byte{0x00, 'a'}, true},
		{"trailing_nul_within_sniff", append(bytes.Repeat([]byte("a"), binarySniffN-1), 0x00), true},
		{"nul_past_sniff_window", append(bytes.Repeat([]byte("a"), binarySniffN+100), 0x00), false},
		{"utf8_no_nul", []byte("café — ÿ"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksBinary(tc.data); got != tc.want {
				t.Errorf("looksBinary(len=%d) = %v, want %v", len(tc.data), got, tc.want)
			}
		})
	}
}

// --- File read/write tests ---

func TestReadWriteFile(t *testing.T) {
	h, dir, prefix := testDir(t)

	rec := putReq(t, h, "/api/file?path="+prefix+"/hello.txt", `{"content":"world"}`)
	if rec.Code != 200 {
		t.Fatalf("write status %d: %s", rec.Code, rec.Body.String())
	}

	rec = getReq(t, h, "/api/file?path="+prefix+"/hello.txt")
	if rec.Code != 200 {
		t.Fatalf("read status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ Content string }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Content != "world" {
		t.Errorf("content = %q, want %q", resp.Content, "world")
	}

	data, _ := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if string(data) != "world" {
		t.Errorf("disk content = %q", string(data))
	}
}

func TestReadFile_Blacklisted(t *testing.T) {
	h, _ := New("/")
	rec := getReq(t, h, "/api/file?path=etc/passwd")
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestReadFile_Binary(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0x89, 0x50, 0x4E, 0x47, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file?path="+prefix+"/bin.dat")
	if rec.Code != 415 {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestReadFile_MissingPath(t *testing.T) {
	h, _ := New("/")
	rec := getReq(t, h, "/api/file")
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestReadFile_MissingOnDisk(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := getReq(t, h, "/api/file?path="+prefix+"/missing.txt")
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestReadFile_IsDirectory(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file?path="+prefix+"/sub")
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestReadFile_TooLarge(t *testing.T) {
	h, dir, prefix := testDir(t)
	big := bytes.Repeat([]byte("a"), maxFileSize+1)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file?path="+prefix+"/big.txt")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

// Q5 regression: writing onto a directory returns a clean 400, not a
// generic 500 leaking the raw EISDIR path.
func TestWriteFile_TargetIsDirectory(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := putReq(t, h, "/api/file?path="+prefix+"/sub", `{"content":"x"}`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// Body should be the generic sentinel, not the OS error text.
	if strings.Contains(rec.Body.String(), "is a directory") == false {
		t.Errorf("body = %s, want \"path is a directory\"", rec.Body.String())
	}
}

func TestWriteFile_InvalidJSON(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := putReq(t, h, "/api/file?path="+prefix+"/x.txt", `{not json`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFile_MethodNotAllowed(t *testing.T) {
	h, _, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodDelete, "/api/file?path="+prefix+"/x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// --- Directory listing tests ---

func TestListFiles_Root(t *testing.T) {
	h, _ := New("/")
	rec := getReq(t, h, "/api/files?path=.")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ Files []fileEntry }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, f := range resp.Files {
		if blacklist[f.Name] {
			t.Errorf("blacklisted dir %q in root listing", f.Name)
		}
		if strings.HasPrefix(f.Name, ".") {
			t.Errorf("dotfile %q leaked into root listing", f.Name)
		}
	}
}

func TestListFiles_Subdir(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := getReq(t, h, "/api/files?path="+prefix)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Files    []fileEntry
		Writable bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Files) != 2 {
		t.Fatalf("files count = %d, want 2", len(resp.Files))
	}
	if !resp.Writable {
		t.Error("expected writable=true for temp dir")
	}
}

func TestListFiles_EmptyPath_TreatedAsRoot(t *testing.T) {
	h, _ := New("/")
	rec := getReq(t, h, "/api/files?path=")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Path != "/" {
		t.Errorf("path = %q, want %q", resp.Path, "/")
	}
}

func TestListFiles_NotFound(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := getReq(t, h, "/api/files?path="+prefix+"/does-not-exist")
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestListFiles_MethodNotAllowed(t *testing.T) {
	h, _ := New("/")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/files?path=.", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// --- Action tests ---

func TestAction_Mkdir(t *testing.T) {
	h, dir, prefix := testDir(t)
	rec := postReq(t, h, "/api/files/action", `{"action":"mkdir","path":"`+prefix+`/newdir"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "newdir"))
	if err != nil || !info.IsDir() {
		t.Error("directory not created")
	}
}

func TestAction_Touch(t *testing.T) {
	h, dir, prefix := testDir(t)
	rec := postReq(t, h, "/api/files/action", `{"action":"touch","path":"`+prefix+`/new.txt"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Error("file not created")
	}
}

func TestAction_Delete(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "del.txt"), []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postReq(t, h, "/api/files/action", `{"action":"delete","path":"`+prefix+`/del.txt"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "del.txt")); err == nil {
		t.Error("file still exists")
	}
}

// S3 regression: shallow-path guard.
// TestAction_SecurityRejections consolidates all action-rejection tests
// that verify 403 responses for sensitive/protected/blacklisted paths.
// Each sub-test injects a sensitive prefix (when needed), creates any
// required filesystem state, POSTs an action, asserts 403, and verifies
// the filesystem was not mutated.
func TestAction_SecurityRejections(t *testing.T) {
	type secCase struct {
		checkSourceExists  string
		action             string
		pathSuffix         string
		dest               string
		renameName         string
		sensitivePrefix    string
		setupFile          string
		setupDir           string
		name               string
		checkDestAbsent    string
		wantBodyContains   string
		useTempDir         bool
		checkNoTempOrphans bool
	}

	cases := []secCase{
		{
			name:       "delete/root",
			action:     "delete",
			pathSuffix: "/",
		},
		{
			name:       "delete/top_level_segment",
			action:     "delete",
			pathSuffix: "workspace",
		},
		{
			// Symmetric with delete/top_level_segment: refuse mkdir of
			// `/foo` so the UI can't create entries it can't undo.
			name:       "mkdir/top_level_segment",
			action:     "mkdir",
			pathSuffix: "newdir",
		},
		{
			// Same symmetry guard for touch.
			name:       "touch/top_level_segment",
			action:     "touch",
			pathSuffix: "new.txt",
		},
		{
			name:             "delete/protected_dir",
			action:           "delete",
			pathSuffix:       "config/chats",
			wantBodyContains: "",
		},
		{
			name:              "rename/sensitive_dest",
			action:            "rename",
			useTempDir:        true,
			pathSuffix:        "attacker.txt",
			renameName:        "locked.md",
			sensitivePrefix:   "locked.md", // exact file
			setupFile:         "attacker.txt",
			checkSourceExists: "attacker.txt",
			checkDestAbsent:   "locked.md",
		},
		{
			name:              "move/protected_dir_source",
			action:            "move",
			useTempDir:        true,
			pathSuffix:        "chats",
			dest:              "stolen",
			sensitivePrefix:   "chats/", // directory prefix
			setupDir:          "chats",
			checkSourceExists: "chats",
		},
		{
			name:              "rename/protected_dir_source",
			action:            "rename",
			useTempDir:        true,
			pathSuffix:        "chats",
			renameName:        "junk",
			sensitivePrefix:   "chats/", // directory prefix
			setupDir:          "chats",
			checkSourceExists: "chats",
			checkDestAbsent:   "junk",
		},
		{
			name:               "copy/sensitive_dest",
			action:             "copy",
			useTempDir:         true,
			pathSuffix:         "attacker.json",
			dest:               "locked.json",
			sensitivePrefix:    "locked.json", // exact file
			setupFile:          "attacker.json",
			checkSourceExists:  "attacker.json",
			checkDestAbsent:    "locked.json",
			checkNoTempOrphans: true,
			wantBodyContains:   "protected",
		},
		{
			name:             "copy/protected_dir_dest",
			action:           "copy",
			useTempDir:       true,
			pathSuffix:       "src.json",
			dest:             "chats",
			sensitivePrefix:  "chats/", // directory prefix
			setupFile:        "src.json",
			wantBodyContains: "copy target is protected",
		},
		{
			name:              "move/sensitive_dest",
			action:            "move",
			useTempDir:        true,
			pathSuffix:        "attacker.json",
			dest:              "locked.json",
			sensitivePrefix:   "locked.json", // exact file
			setupFile:         "attacker.json",
			checkSourceExists: "attacker.json",
			checkDestAbsent:   "locked.json",
			wantBodyContains:  "protected",
		},
		{
			name:              "move/protected_dir_dest",
			action:            "move",
			useTempDir:        true,
			pathSuffix:        "src.json",
			dest:              "chats",
			sensitivePrefix:   "chats/", // directory prefix
			setupFile:         "src.json",
			checkSourceExists: "src.json",
			wantBodyContains:  "move target is protected",
		},
		{
			name:            "mkdir/protected_dir",
			action:          "mkdir",
			useTempDir:      true,
			pathSuffix:      "chats",
			sensitivePrefix: "chats/", // directory prefix
			checkDestAbsent: "chats",
		},
		{
			name:            "touch/protected_path",
			action:          "touch",
			useTempDir:      true,
			pathSuffix:      "chats",
			sensitivePrefix: "chats/", // directory prefix
			checkDestAbsent: "chats",
		},
		{
			name:            "touch/sensitive_exact_file",
			action:          "touch",
			useTempDir:      true,
			pathSuffix:      "secret.json",
			sensitivePrefix: "secret.json", // exact file
			checkDestAbsent: "secret.json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var h *Handler
			var dir, prefix string

			if tc.useTempDir {
				h, dir, prefix = testDir(t)
			} else {
				h, _ = New("/")
			}

			// Inject sensitive prefix if specified.
			if tc.sensitivePrefix != "" && tc.useTempDir {
				orig := sensitivePrefixes
				t.Cleanup(func() { sensitivePrefixes = orig })
				var entry string
				isDir := strings.HasSuffix(tc.sensitivePrefix, "/")
				if isDir {
					// directory prefix — filepath.Join strips trailing slash
					entry = filepath.Join("/", prefix, tc.sensitivePrefix) + "/"
				} else {
					// exact file
					entry = filepath.Join("/", prefix, tc.sensitivePrefix)
				}
				sensitivePrefixes = append([]sensitivePath{{Path: entry, IsDir: isDir}}, orig...)
			}

			// Create setup file/dir.
			if tc.setupFile != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.setupFile), []byte("payload"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.setupDir != "" {
				if err := os.Mkdir(filepath.Join(dir, tc.setupDir), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			// Build JSON body.
			var reqPath string
			if tc.useTempDir {
				reqPath = prefix + "/" + tc.pathSuffix
			} else {
				reqPath = tc.pathSuffix
			}

			var body string
			switch {
			case tc.dest != "":
				destPath := prefix + "/" + tc.dest
				body = `{"action":"` + tc.action + `","path":"` + reqPath + `","dest":"` + destPath + `"}`
			case tc.renameName != "":
				body = `{"action":"` + tc.action + `","path":"` + reqPath + `","name":"` + tc.renameName + `"}`
			default:
				body = `{"action":"` + tc.action + `","path":"` + reqPath + `"}`
			}

			rec := postReq(t, h, "/api/files/action", body)
			if rec.Code != 403 {
				t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}

			// Optional body content assertion.
			if tc.wantBodyContains != "" {
				if !strings.Contains(rec.Body.String(), tc.wantBodyContains) {
					t.Errorf("body = %s, want substring %q", rec.Body.String(), tc.wantBodyContains)
				}
			}

			// Verify source still exists.
			if tc.checkSourceExists != "" {
				if _, err := os.Stat(filepath.Join(dir, tc.checkSourceExists)); err != nil {
					t.Errorf("source %q removed despite rejection: %v", tc.checkSourceExists, err)
				}
			}

			// Verify destination does not exist.
			if tc.checkDestAbsent != "" {
				if _, err := os.Stat(filepath.Join(dir, tc.checkDestAbsent)); err == nil {
					t.Errorf("destination %q created despite rejection", tc.checkDestAbsent)
				}
			}

			// Verify no temp orphans.
			if tc.checkNoTempOrphans {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range entries {
					if strings.Contains(e.Name(), ".copy-") {
						t.Errorf("leftover copy temp file %q", e.Name())
					}
				}
			}
		})
	}
}

func TestAction_Rename(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postReq(t, h, "/api/files/action", `{"action":"rename","path":"`+prefix+`/old.txt","name":"new.txt"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil || string(data) != "data" {
		t.Error("renamed file missing or wrong content")
	}
}

func TestAction_Rename_InvalidNames(t *testing.T) {
	bad := []string{"", ".", "..", "a/b", "a\\b", "with\x00nul"}
	for _, name := range bad {
		t.Run("name="+name, func(t *testing.T) {
			h, dir, prefix := testDir(t)
			if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			nameJSON, _ := json.Marshal(name)
			body := `{"action":"rename","path":"` + prefix + `/old.txt","name":` + string(nameJSON) + `}`
			rec := postReq(t, h, "/api/files/action", body)
			if rec.Code != 400 {
				t.Errorf("rename name=%q: status = %d, want 400", name, rec.Code)
			}
			if _, err := os.Stat(filepath.Join(dir, "old.txt")); err != nil {
				t.Errorf("rename name=%q: original file missing: %v", name, err)
			}
		})
	}
}

func TestAction_Blacklisted(t *testing.T) {
	h, _ := New("/")
	rec := postReq(t, h, "/api/files/action", `{"action":"touch","path":"etc/evil.txt"}`)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAction_UnknownAction(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := postReq(t, h, "/api/files/action", `{"action":"nope","path":"`+prefix+`/x"}`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAction_MethodNotAllowed(t *testing.T) {
	h, _ := New("/")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/files/action", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestAction_InvalidJSON(t *testing.T) {
	h, _ := New("/")
	rec := postReq(t, h, "/api/files/action", `not json at all`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAction_Copy_MissingDest(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postReq(t, h, "/api/files/action",
		`{"action":"copy","path":"`+prefix+`/src.txt"}`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAction_Copy_DestBlacklisted(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postReq(t, h, "/api/files/action",
		`{"action":"copy","path":"`+prefix+`/src.txt","dest":"etc/evil"}`)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAction_Copy(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"action":"copy","path":"` + prefix + `/src.txt","dest":"` + prefix + `/dst.txt"}`
	rec := postReq(t, h, "/api/files/action", body)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	src, _ := os.ReadFile(filepath.Join(dir, "src.txt"))
	dst, err := os.ReadFile(filepath.Join(dir, "dst.txt"))
	if err != nil {
		t.Fatalf("dst missing: %v", err)
	}
	if string(src) != "payload" || string(dst) != "payload" {
		t.Errorf("src=%q dst=%q, want both %q", string(src), string(dst), "payload")
	}
}

func TestAction_Copy_SourceMissing(t *testing.T) {
	h, _, prefix := testDir(t)
	body := `{"action":"copy","path":"` + prefix + `/missing.txt","dest":"` + prefix + `/dst.txt"}`
	rec := postReq(t, h, "/api/files/action", body)
	if rec.Code != 500 {
		t.Errorf("status = %d, want 500 (source open fails)", rec.Code)
	}
}

// FH-C2-01 regression: the stat-based pre-check in actionCopy rejects
// oversize sources with 413 before opening the destination. Uses
// os.Truncate so the 100 MiB source is a sparse file (no tmpfs
// pressure, <10 ms). Before this guard the LimitReader tail check
// still catches oversize but only after burning destination
// allocation + partial IO, which is the bug the fix set out to
// prevent.
func TestAction_Copy_SourceTooLarge(t *testing.T) {
	h, dir, prefix := testDir(t)

	srcPath := filepath.Join(dir, "huge.bin")
	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(srcPath, maxCopySize+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	body := `{"action":"copy","path":"` + prefix + `/huge.bin","dest":"` + prefix + `/dst.bin"}`
	rec := postReq(t, h, "/api/files/action", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "source file too large to copy") {
		t.Errorf("body = %s, want \"source file too large to copy\"", rec.Body.String())
	}
	// Destination must NOT exist — the whole point of the pre-stat
	// guard is to reject before CreateTemp on the dest side.
	if _, err := os.Stat(filepath.Join(dir, "dst.bin")); err == nil {
		t.Error("destination file created despite 413 reject")
	}
}

// F2 / S7 / ops #1 regression: actionCopy now streams into a
// `.copy-*` sibling and renames into place. A mid-copy failure must
// leave the pre-existing destination intact (the old behaviour was
// O_TRUNC then partial-write, destroying the user's file even on
// failure). Drive the failure via an oversize cap trip and assert
// the dest is either untouched (existing content preserved) or never
// created (fresh copy).
func TestAction_Copy_FailurePreservesExistingDest(t *testing.T) {
	h, dir, prefix := testDir(t)
	// Pre-existing destination must survive a failed copy.
	original := []byte("ORIGINAL CONTENT THAT MUST SURVIVE A FAILED COPY")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(dst, original, 0o644); err != nil {
		t.Fatal(err)
	}
	// Oversize source via sparse file (no actual allocation).
	src := filepath.Join(dir, "src.bin")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Truncate(src, maxCopySize+1); err != nil {
		t.Fatal(err)
	}

	body := `{"action":"copy","path":"` + prefix + `/src.bin","dest":"` + prefix + `/dst.txt"}`
	rec := postReq(t, h, "/api/files/action", body)
	if rec.Code == 200 {
		t.Fatalf("unexpected 200 on oversize copy; body=%s", rec.Body.String())
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("dst missing after failed copy: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("dst corrupted by failed copy: got %d bytes, want original %d bytes",
			len(got), len(original))
	}
	// No `.copy-*` orphan left behind on the failure path.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".copy-") {
			t.Errorf("leftover copy temp file %q", e.Name())
		}
	}
}

// S6 regression: actionMove must refuse to relocate a protected
// directory as its source. Simulated via an injected sensitive
// prefix pointing inside the temp dir — without the source guard,
// mv /<tmp>/chats /<tmp>/stolen would succeed and detach the
// (simulated) chat store from its expected location.
func TestAction_Move(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"action":"move","path":"` + prefix + `/src.txt","dest":"` + prefix + `/moved.txt"}`
	rec := postReq(t, h, "/api/files/action", body)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "src.txt")); err == nil {
		t.Error("source still exists after move")
	}
	data, err := os.ReadFile(filepath.Join(dir, "moved.txt"))
	if err != nil || string(data) != "payload" {
		t.Errorf("moved file missing or wrong: err=%v data=%q", err, string(data))
	}
}

// --- handleDownload tests ---

func TestHandleDownload_File(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file/download?path="+prefix+"/doc.txt")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "hello" {
		t.Errorf("body = %q, want %q", got, "hello")
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename="doc.txt"`) {
		t.Errorf("Content-Disposition = %q, want attachment filename", cd)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain*", ct)
	}
}

func TestHandleDownload_UnknownExtension(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "blob"), []byte{0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file/download?path="+prefix+"/blob")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct == "" {
		t.Errorf("Content-Type unexpectedly empty")
	}
}

// TestHandleDownload_ErrorPaths consolidates the 5 error-path download
// tests into a single table-driven test. The 2 happy-path tests (File,
// UnknownExtension) remain separate because they assert response headers
// and body content.
func TestHandleDownload_ErrorPaths(t *testing.T) {
	type dlCase struct {
		name       string
		path       string // query param value (empty = omit param)
		setupDir   string // create this subdir in temp dir before request
		method     string // HTTP method; defaults to GET
		wantStatus int
	}

	h, dir, prefix := testDir(t)

	cases := []dlCase{
		{
			name:       "missing_path_param",
			path:       "", // no ?path= at all
			wantStatus: 400,
		},
		{
			name:       "missing_file",
			path:       prefix + "/missing.txt",
			wantStatus: 404,
		},
		{
			name:       "directory_rejected",
			path:       prefix + "/sub",
			setupDir:   "sub",
			wantStatus: 400,
		},
		{
			name:       "blacklisted",
			path:       "etc/passwd",
			wantStatus: 403,
		},
		{
			name:       "method_not_allowed",
			path:       prefix,
			method:     http.MethodPost,
			wantStatus: 405,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setupDir != "" {
				if err := os.MkdirAll(filepath.Join(dir, tc.setupDir), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			url := "/api/file/download"
			if tc.path != "" {
				url += "?path=" + tc.path
			}

			method := http.MethodGet
			if tc.method != "" {
				method = tc.method
			}

			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(method, url, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// --- handleUpload tests ---

func TestHandleUpload_SingleFile(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := multipartUpload(t, prefix, map[string][]byte{"hello.txt": []byte("world")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Uploaded []string `json:"uploaded"`
		OK       bool     `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.OK || len(resp.Uploaded) != 1 || resp.Uploaded[0] != "hello.txt" {
		t.Errorf("resp = %+v, want {OK:true Uploaded:[hello.txt]}", resp)
	}
	data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil || string(data) != "world" {
		t.Errorf("disk content = %q err=%v, want %q", string(data), err, "world")
	}
}

// Ops F1 regression: partial writes never surface under the user's
// filename. The temp-rename pattern leaves `.upload-*` siblings on
// error, never a truncated file at the expected path.
func TestHandleUpload_NoPartialFileOnSuccess(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := multipartUpload(t, prefix, map[string][]byte{"ok.txt": []byte("contents")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	// No leftover .upload-* sibling on the happy path.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".upload-") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
}

func TestHandleUpload_MultipleFiles(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := multipartUpload(t, prefix, map[string][]byte{
		"a.txt": []byte("A"),
		"b.txt": []byte("B"),
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("file %s missing: %v", name, err)
		}
	}
}

func TestHandleUpload_StripsPathPrefixInFilename(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Filename with embedded directory component must land as
	// filepath.Base; no escape out of the target dir.
	req := multipartUpload(t, prefix,
		map[string][]byte{"subdir/hidden.txt": []byte("naughty")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "hidden.txt")); err != nil {
		t.Errorf("expected basename-only file, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "subdir")); err == nil {
		t.Error("handler escaped target dir via path prefix")
	}
}

// Q4 regression: invalid filenames surface as 400 with a descriptive
// error instead of silently succeeding with a subset `uploaded` array.
func TestHandleUpload_DotDotFilenameReturns400(t *testing.T) {
	h, _, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := multipartUpload(t, prefix, map[string][]byte{"..": []byte("skip")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid filename") {
		t.Errorf("body = %s, want \"invalid filename\"", rec.Body.String())
	}
}

func TestHandleUpload_NoFiles(t *testing.T) {
	h, _, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("dir", prefix)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/file/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleUpload_BlacklistedDir(t *testing.T) {
	h, _ := New("/")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := multipartUpload(t, "etc", map[string][]byte{"x.txt": []byte("x")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// S5 regression: upload must refuse a dir= that names a protected
// directory (or a container of one). Without isProtectedDir on the
// resolved target, dir=/config/chats would silently land arbitrary
// files inside the chat store. Simulated via injected sensitive
// prefix pointing inside the temp dir so we don't need a real
// /config mount.
func TestHandleUpload_RefusesProtectedDir(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	orig := sensitivePrefixes
	t.Cleanup(func() { sensitivePrefixes = orig })
	sensitivePrefixes = append([]sensitivePath{{Path: filepath.Join("/", prefix, "chats") + "/", IsDir: true}}, orig...)

	if err := os.Mkdir(filepath.Join(dir, "chats"), 0o755); err != nil {
		t.Fatal(err)
	}

	req := multipartUpload(t, prefix+"/chats",
		map[string][]byte{"fake-chat.json": []byte("[]")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "chats", "fake-chat.json")); err == nil {
		t.Error("file written despite protected-dir rejection")
	}
}

// S5 regression: upload into a non-sensitive parent must refuse per-
// file overwrites of sensitive exact-match entries. When the parent
// also encloses a sensitive file, the directory gate trips first
// (upload target is a container of a sensitive file); when the
// parent is non-enclosing but the filename happens to match a
// sensitive entry elsewhere, the per-file gate trips with
// "invalid filename". Either 403 or 400 is acceptable — the critical
// invariant is that the sensitive file is never materialised.
func TestHandleUpload_RefusesSensitiveFilename(t *testing.T) {
	h, dir, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Register a specific file as sensitive while leaving the parent
	// dir writable — mirrors /config/push-subs.json where /config is
	// a mount point but push-subs.json is protected.
	secret := filepath.Join(dir, "keys.json")
	orig := sensitivePrefixes
	t.Cleanup(func() { sensitivePrefixes = orig })
	sensitivePrefixes = append([]sensitivePath{{Path: secret, IsDir: false}}, orig...)

	req := multipartUpload(t, prefix,
		map[string][]byte{"keys.json": []byte("[]")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 && rec.Code != 403 {
		t.Fatalf("status = %d, want 400 or 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(secret); err == nil {
		t.Error("sensitive file written despite sensitive-path gates")
	}
}

// F1 regression: writeFile now uses O_NOFOLLOW. A dangling symlink
// planted between resolvePath's EvalSymlinks and the write must not
// create the target. resolvePath returns the lexical form when the
// symlink target doesn't exist; without O_NOFOLLOW on the open,
// os.WriteFile would follow the symlink and materialise the
// sensitive target. ELOOP surfaces as a 500 "write failed" — the
// important post-condition is the sensitive target is not created.
func TestWriteFile_DanglingSymlinkToSensitive_Blocked(t *testing.T) {
	h, _ := New("/")
	dir := t.TempDir()
	prefix := strings.TrimPrefix(dir, "/")

	target := filepath.Join(dir, "protected.json")
	orig := sensitivePrefixes
	t.Cleanup(func() { sensitivePrefixes = orig })
	sensitivePrefixes = append([]sensitivePath{{Path: target, IsDir: false}}, orig...)

	link := filepath.Join(dir, "trojan")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rec := putReq(t, h, "/api/file?path="+prefix+"/trojan",
		`{"content":"pwn"}`)
	// 403 (resolve layer catches it via the real-path enforceAccess
	// against the sensitive target) or 500 (O_NOFOLLOW trips ELOOP)
	// are both acceptable; the critical invariant is the target is
	// never materialised.
	if rec.Code != 403 && rec.Code != 500 {
		t.Errorf("status = %d, want 403 or 500; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("sensitive target file was created despite symlink guard")
	}
}

func TestHandleUpload_MethodNotAllowed(t *testing.T) {
	h, _ := New("/")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/file/upload", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHandleUpload_InvalidMultipart(t *testing.T) {
	h, _ := New("/")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/file/upload",
		strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// Ops F5 regression: oversize upload returns 413, not a generic 400.
func TestHandleUpload_TooLargeReturns413(t *testing.T) {
	h, _, prefix := testDir(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Build a body that exceeds maxUploadSize. MaxBytesReader should
	// trigger the 413 branch before ParseMultipartForm completes.
	huge := bytes.Repeat([]byte("a"), maxUploadSize+1024)
	req := multipartUpload(t, prefix, map[string][]byte{"big.bin": huge})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}

// FH-C3-01 (S5): actionCopy must refuse a sensitive destination even
// when the source is a benign file. Parallels the rename-dest guard
// (TestAction_Rename_RejectsSensitiveDest). Without this the copy
// action would let `cp attacker.json /config/push-subs.json` slip
// through because the source resolve layer doesn't know the dest is
// sensitive and actionCopy's dest-guard is the only line between the
// attacker and the protected file.
// Q1 (S1 deeper): a symlinked ancestor + two-or-more nonexistent
// trailing components must not bypass the blacklist. The earlier S1
// tests only pinned the single-missing-leaf case where
// EvalSymlinks(parent) resolves the symlink itself. With two missing
// components (evil/newdir/sub where newdir doesn't exist in /etc),
// EvalSymlinks(parent)=EvalSymlinks(.../evil/newdir) returns ENOENT
// internally — only the ancestor walk surfaces the symlink crossing.
func TestResolvePath_SymlinkAncestorWithDeepMissingLeaf_Rejected(t *testing.T) {
	dir := t.TempDir()
	prefix := strings.TrimPrefix(dir, "/")

	link := filepath.Join(dir, "evil")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// `newdir/sub` do not exist either in the temp dir or in /etc.
	// Pre-fix: resolveRealPath returned the lexical form, enforceAccess
	// saw top="tmp" (removed from blacklist for tests), request passed.
	// Post-fix: the ancestor walk resolves `evil` to `/etc`, recomposes
	// `/etc/newdir/sub`, enforceAccess sees top="etc", rejects.
	_, err := resolvePath(prefix + "/evil/newdir/sub")
	if err == nil {
		t.Errorf("resolvePath through symlinked ancestor + deep missing leaf returned nil error; expected rejection")
	}
}

// Q1 cross-handler regression: actionMkdir must not create
// directories under a symlinked ancestor's blacklisted target.
// Using a hermetic temp dir registered as sensitive (proxy for /etc
// which we don't want to actually write into during tests) means the
// test is side-effect-free even if the guard ever regresses.
func TestAction_Mkdir_ThroughSymlinkedAncestor_Rejected(t *testing.T) {
	h, dir, prefix := testDir(t)

	sink := t.TempDir()
	link := filepath.Join(dir, "evil")
	if err := os.Symlink(sink, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	orig := sensitivePrefixes
	t.Cleanup(func() { sensitivePrefixes = orig })
	sensitivePrefixes = append([]sensitivePath{{Path: sink + "/", IsDir: true}}, orig...)

	rec := postReq(t, h, "/api/files/action",
		`{"action":"mkdir","path":"`+prefix+`/evil/newdir/sub"}`)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(sink, "newdir")); err == nil {
		t.Error("created dir under symlink target despite rejection")
	}
}

// --- ctxReader tests (direct unit coverage of the cancellation guard) ---
//
// The cancellation branch is only exercised indirectly by actionCopy,
// where a successful copy masks a regression of the `ctx.Err()` guard.
// A mutation that removes the guard must be caught here.

// errReader always returns its configured error on Read.
type errReader struct{ err error }

func (e *errReader) Read(_ []byte) (int, error) { return 0, e.err }

func TestCtxReader_Read(t *testing.T) {
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	expiredCtx, expiredCancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer expiredCancel()

	sentinel := errors.New("inner io failure")

	cases := []struct {
		ctx     context.Context
		inner   io.Reader
		wantErr error
		name    string
		wantN   int
	}{
		{
			name:    "cancelled_context",
			ctx:     cancelledCtx,
			inner:   strings.NewReader("should not be read"),
			wantN:   0,
			wantErr: context.Canceled,
		},
		{
			name:    "deadline_exceeded",
			ctx:     expiredCtx,
			inner:   strings.NewReader("unused"),
			wantN:   0,
			wantErr: context.DeadlineExceeded,
		},
		{
			name:    "live_context_forwards_read",
			ctx:     context.Background(),
			inner:   strings.NewReader("hello"),
			wantN:   5,
			wantErr: nil,
		},
		{
			name:    "live_context_forwards_inner_error",
			ctx:     context.Background(),
			inner:   &errReader{err: sentinel},
			wantN:   0,
			wantErr: sentinel,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := &ctxReader{ctx: tc.ctx, r: tc.inner}
			buf := make([]byte, 8)
			n, err := cr.Read(buf)

			if n != tc.wantN {
				t.Errorf("ctxReader.Read() n = %d, want %d", n, tc.wantN)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("ctxReader.Read() err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil && !errors.Is(err, io.EOF) {
				t.Errorf("ctxReader.Read() unexpected err = %v", err)
			}
		})
	}
}

// --- actionRename destination resolvePath coverage ---
//
// Pins that rename's destination runs through resolvePath (blacklist
// + real-path) instead of only the lexical sensitive/protected checks.
// Without the fix, renaming a file into a name that matches a sensitive
// PREFIX directory (e.g. `chats`) on cold boot could slip past the
// per-call guards. This mirrors the rename/copy asymmetry security
// review flagged.
func TestAction_Rename_DestRunsResolvePath(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "old.txt"),
		[]byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := sensitivePrefixes
	t.Cleanup(func() { sensitivePrefixes = orig })
	// Name `chats` would, after Join, resolve to <prefix>/chats which
	// is a protected directory per the sensitivePrefixes injection.
	sensitivePrefixes = append([]sensitivePath{{Path: filepath.Join("/", prefix, "chats") + "/", IsDir: true}}, orig...)

	rec := postReq(t, h, "/api/files/action",
		`{"action":"rename","path":"`+prefix+`/old.txt","name":"chats"}`)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); err != nil {
		t.Errorf("source file disappeared after rejected rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "chats")); err == nil {
		t.Error("destination created despite rejection")
	}
}

func TestWriteUploads_ContextCancelled_AbortsEarly(t *testing.T) {
	h, dir, _ := testDir(t)

	// Build a multipart body with 3 files.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		fw, err := w.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte("content-" + name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Parse the multipart form to get FileHeaders.
	reader := multipart.NewReader(&buf, w.Boundary())
	form, err := reader.ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer form.RemoveAll()

	files := form.File["files"]
	if len(files) != 3 {
		t.Fatalf("expected 3 file headers, got %d", len(files))
	}

	// Cancel the context before calling writeUploads.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	uploaded, _, wErr := h.writeUploads(ctx, dir, files)
	if wErr == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(wErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", wErr)
	}
	// No files should have been written because the context was
	// already cancelled at the top of the first iteration.
	if len(uploaded) != 0 {
		t.Errorf("expected 0 uploaded files, got %d", len(uploaded))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected empty dir, got %d entries", len(entries))
	}
}

// g78: actionCopy context cancellation mid-stream cleans up the temp
// file. Uses a pre-cancelled context so the ctxReader returns
// context.Canceled on the first Read, triggering the defer cleanup
// that removes the .copy-* temp file.
func TestAction_Copy_ContextCancelled_CleansUpTemp(t *testing.T) {
	h, dir, prefix := testDir(t)

	// Create a source file with enough content to exercise the copy path.
	src := filepath.Join(dir, "cancel-src.txt")
	if err := os.WriteFile(src, []byte("some content to copy"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build the request with a pre-cancelled context so the copy
	// aborts on the first ctxReader.Read call.
	body := `{"action":"copy","path":"` + prefix + `/cancel-src.txt","dest":"` + prefix + `/cancel-dst.txt"}`
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/files/action", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the copy starts
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// The handler should return 500 because actionCopy returns the
	// context error and handleFilesAction wraps it as a generic failure.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}

	// The destination file must not exist.
	if _, err := os.Stat(filepath.Join(dir, "cancel-dst.txt")); err == nil {
		t.Error("destination file should not exist after cancelled copy")
	}

	// No .copy-* temp files should remain in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".copy-") {
			t.Errorf("temp file %q not cleaned up after cancelled copy", e.Name())
		}
	}

	// Source must be untouched.
	got, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("source read failed: %v", err)
	}
	if string(got) != "some content to copy" {
		t.Errorf("source content = %q, want %q", string(got), "some content to copy")
	}
}

// --- BenchmarkResolvePath (hot path called on every HTTP request) ---

func BenchmarkResolvePath(b *testing.B) {
	// Create a temp dir with a nested structure for the deep-path case.
	dir := b.TempDir()
	prefix := strings.TrimPrefix(dir, "/")

	// Create a nested directory for the deep path case.
	deep := filepath.Join(dir, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		b.Fatal(err)
	}

	// Create a symlink for the symlink case.
	linkTarget := filepath.Join(dir, "a", "b")
	link := filepath.Join(dir, "linked")
	if err := os.Symlink(linkTarget, link); err != nil {
		b.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"allowed_path", prefix + "/a/b/c/d"},
		{"blacklisted_path", "etc/passwd"},
		{"deep_nested_path", prefix + "/a/b/c/d/../../../b/c/d"},
		{"symlink_path", prefix + "/linked/c/d"},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				resolvePath(tc.path)
			}
		})
	}
}

// --- Fuzz target for resolvePath (security-critical path resolution) ---

func FuzzResolvePath(f *testing.F) {
	// Seed corpus: known attack shapes and edge cases.
	seeds := []string{
		"workspace/myrepo/file.go",
		"../../../etc/passwd",
		"workspace/../../etc/shadow",
		"etc/passwd",
		"proc/1/status",
		"var/log/syslog",
		"/",
		".",
		"..",
		"workspace/./../../etc/passwd",
		"workspace/%2e%2e/etc/passwd",
		"workspace/\x00/etc/passwd",
		"workspace/symlink/../../../etc",
		strings.Repeat("../", 50) + "etc/passwd",
		"workspace/a/b/c/../../../../etc/passwd",
		"config/home/.kiro/steering/vibekit.md",
		"config/chats/deep/nested.json",
		"config/push-subs.json",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		result, err := resolvePath(input)
		if err != nil {
			// Rejected path — that's fine, security working as intended.
			return
		}

		// Assertion 1: no panic (implicit — reaching here means no panic).

		// Assertion 2: result is always an absolute clean path.
		if !filepath.IsAbs(result) {
			t.Errorf("resolvePath(%q) = %q is not absolute", input, result)
		}
		if cleaned := filepath.Clean(result); cleaned != result {
			t.Errorf("resolvePath(%q) = %q is not clean (Clean = %q)", input, result, cleaned)
		}

		// Assertion 3: if err == nil, result must not start with any
		// blacklisted prefix.
		top := strings.SplitN(strings.TrimPrefix(result, "/"), "/", 2)[0]
		if blacklist[top] {
			t.Errorf("resolvePath(%q) = %q starts with blacklisted segment %q", input, result, top)
		}

		// Assertion 4: result must not be a sensitive path.
		if isSensitive(result) {
			t.Errorf("resolvePath(%q) = %q is a sensitive path", input, result)
		}
	})
}

// --- FuzzIsSensitive (security-critical sensitive-path predicate) ---

func FuzzIsSensitive(f *testing.F) {
	// Seed corpus: known sensitive paths, near-misses, and adversarial shapes.
	seeds := []string{
		"/config/home/.kiro/steering/vibekit.md",
		"/config/home/.kiro/steering/environment.md",
		"/config/home/.kiro/agents/foo.json",
		"/config/chats/deep/nested.json",
		"/config/push-subs.json",
		"/config/vapid-keys.json",
		"/workspace/repo/main.go",
		"/config/home/.kiro/steering/other.md",
		"/config/chats",
		"/config/chats/",
		"/config/home/.kiro/agents/",
		"/config/home/.kiro/agents",
		"",
		"/",
		"/config",
		"/config/home/.kiro/steering/vibekit.md/extra",
		"/config/push-subs.json/",
		"/config/push-subs.jsonx",
		"\x00",
		"/config/chats/\x00/evil",
		"//config//chats//foo",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		result := isSensitive(input)

		// Assertion 1: if isSensitive returns true, the input must match
		// at least one sensitivePrefixes entry (directory prefix or exact file).
		if result {
			matched := false
			for _, sp := range sensitivePrefixes {
				if sp.IsDir {
					if strings.HasPrefix(input, sp.Path) {
						matched = true
						break
					}
				} else if input == sp.Path {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("isSensitive(%q) = true but no sensitivePrefixes entry matches", input)
			}
		}

		// Assertion 2: if no sensitivePrefixes entry matches, result must be false.
		if !result {
			for _, sp := range sensitivePrefixes {
				if sp.IsDir {
					if strings.HasPrefix(input, sp.Path) {
						t.Errorf("isSensitive(%q) = false but matches dir prefix %q", input, sp.Path)
					}
				} else if input == sp.Path {
					t.Errorf("isSensitive(%q) = false but matches exact path %q", input, sp.Path)
				}
			}
		}

		// Assertion 3: no panic (implicit — reaching here means no panic).
	})
}

// --- BenchmarkFileAction_Copy (IO-intensive copy path) ---

func BenchmarkFileAction_Copy(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1KiB", 1 << 10},
		{"1MiB", 1 << 20},
		{"10MiB", 10 << 20},
	}

	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			dir := b.TempDir()
			prefix := strings.TrimPrefix(dir, "/")

			srcPath := filepath.Join(dir, "src.bin")
			if err := os.WriteFile(srcPath, bytes.Repeat([]byte("x"), sz.size), 0o644); err != nil {
				b.Fatal(err)
			}

			h, _ := New("/")
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			body := `{"action":"copy","path":"` + prefix + `/src.bin","dest":"` + prefix + `/dst.bin"}`

			b.SetBytes(int64(sz.size))
			b.ReportAllocs()
			b.ResetTimer()

			for range b.N {
				req := httptest.NewRequest(http.MethodPost, "/api/files/action", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != 200 {
					b.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				// Remove dest for next iteration.
				os.Remove(filepath.Join(dir, "dst.bin"))
			}
		})
	}
}

// --- Boundary and error-propagation tests -----------------------------
//
// These pin observable outcomes at size boundaries (mkdir/touch/delete
// depth guards, streamCopy/readFile/download size caps) and on error
// paths (action functions must propagate the raw OS error, never
// swallow it or return errHandled).

// discardStatusRecorder captures the HTTP status code without buffering
// the response body, so the maxCopySize download-boundary test never
// allocates the served payload.
type discardStatusRecorder struct {
	hdr     http.Header
	code    int
	written bool
}

func (s *discardStatusRecorder) Header() http.Header {
	if s.hdr == nil {
		s.hdr = http.Header{}
	}
	return s.hdr
}

func (s *discardStatusRecorder) WriteHeader(code int) {
	if !s.written {
		s.code = code
		s.written = true
	}
}

func (s *discardStatusRecorder) Write(p []byte) (int, error) {
	if !s.written {
		s.code = http.StatusOK
		s.written = true
	}
	return len(p), nil
}

// actionMkdir's depth guard rejects only paths with fewer than two
// segments, so "/a/b" (exactly two slashes) is allowed and created.
func TestAction_Mkdir_BoundaryAtTwoSegments(t *testing.T) {
	h, dir, _ := testDir(t)
	resolved := "/a/b" // count("/a/b") == 2
	err := actionMkdir(context.Background(), httptest.NewRecorder(), fileAction{}, resolved, h)
	if err != nil {
		t.Fatalf("actionMkdir(%q) = %v, want nil (a 2-segment path must be allowed to mkdir)", resolved, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "a", "b")); statErr != nil {
		t.Errorf("actionMkdir(%q): expected dir created at <root>/a/b, stat err: %v", resolved, statErr)
	}
}

// actionMkdir returns the raw MkdirAll error (MkdirAll under a regular
// file is ENOTDIR) rather than swallowing it or returning errHandled.
func TestAction_Mkdir_PropagatesError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved := "/afile/sub" // relPath "afile/sub"; "afile" is a file -> MkdirAll ENOTDIR
	err := actionMkdir(context.Background(), httptest.NewRecorder(), fileAction{}, resolved, h)
	if err == nil {
		t.Fatalf("actionMkdir(%q) = nil, want non-nil (MkdirAll under a regular file must error)", resolved)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionMkdir(%q) returned errHandled; expected the raw MkdirAll error", resolved)
	}
}

// actionTouch shares the mkdir depth boundary: "/x/y" (two slashes) is
// allowed and the file is created under an existing parent.
func TestAction_Touch_BoundaryAtTwoSegments(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.Mkdir(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := "/x/y" // count == 2; parent "x" exists so OpenFile can create
	err := actionTouch(context.Background(), httptest.NewRecorder(), fileAction{}, resolved, h)
	if err != nil {
		t.Fatalf("actionTouch(%q) = %v, want nil (a 2-segment path must be allowed to touch)", resolved, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "x", "y")); statErr != nil {
		t.Errorf("actionTouch(%q): expected file created at <root>/x/y, stat err: %v", resolved, statErr)
	}
}

// actionTouch returns the raw OpenFile error (OpenFile under a regular
// file is ENOTDIR) rather than swallowing it or closing a nil file.
func TestAction_Touch_PropagatesOpenError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "pfile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved := "/pfile/child" // parent "pfile" is a file -> OpenFile ENOTDIR
	err := actionTouch(context.Background(), httptest.NewRecorder(), fileAction{}, resolved, h)
	if err == nil {
		t.Fatalf("actionTouch(%q) = nil, want non-nil (OpenFile under a regular file must error)", resolved)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionTouch(%q) returned errHandled; expected the raw OpenFile error", resolved)
	}
}

// actionDelete allows a 2-segment path ("/p/q") and removes the target;
// the depth guard rejects only fewer-than-two-segment paths.
func TestAction_Delete_BoundaryAtTwoSegments(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "p", "q"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := "/p/q" // segments == 2
	err := actionDelete(context.Background(), httptest.NewRecorder(), fileAction{}, resolved, h)
	if err != nil {
		t.Fatalf("actionDelete(%q) = %v, want nil (a 2-segment path must be deletable)", resolved, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "p", "q")); !os.IsNotExist(statErr) {
		t.Errorf("actionDelete(%q): expected <root>/p/q removed, stat err = %v", resolved, statErr)
	}
}

// actionDelete returns the raw RemoveAll error when the path traverses
// an escaping symlink (os.Root refuses the operation).
func TestAction_Delete_PropagatesRemoveError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.Symlink("/etc", filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	resolved := filepath.Join(dir, "escape", "leaf") // traverses an escaping symlink
	err := actionDelete(context.Background(), httptest.NewRecorder(), fileAction{}, resolved, h)
	if err == nil {
		t.Fatalf("actionDelete(%q) = nil, want non-nil (RemoveAll through an escaping symlink must error)", resolved)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionDelete(%q) returned errHandled; expected the raw RemoveAll error", resolved)
	}
}

// actionRename surfaces the raw Rename error (renaming a missing source)
// past every guard rather than returning errHandled.
func TestAction_Rename_PropagatesError(t *testing.T) {
	h, dir, _ := testDir(t)
	resolved := filepath.Join(dir, "ghost.txt") // source does not exist
	err := actionRename(context.Background(), httptest.NewRecorder(),
		fileAction{Name: "renamed.txt"}, resolved, h)
	if err == nil {
		t.Fatalf("actionRename(%q -> renamed.txt) = nil, want non-nil (renaming a missing source must error)", resolved)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionRename returned errHandled; expected the raw Rename error (a guard fired before Rename)")
	}
}

// actionMove surfaces the raw Rename error for a missing source, same
// shape as rename.
func TestAction_Move_PropagatesError(t *testing.T) {
	h, dir, _ := testDir(t)
	resolved := filepath.Join(dir, "ghost.txt") // source does not exist
	err := actionMove(context.Background(), httptest.NewRecorder(),
		fileAction{Dest: filepath.Join(dir, "moved.txt")}, resolved, h)
	if err == nil {
		t.Fatalf("actionMove(%q) = nil, want non-nil (moving a missing source must error)", resolved)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionMove returned errHandled; expected the raw Rename error")
	}
}

// streamCopy copies a file whose size exactly equals the size limit
// without an oversize error or truncation: the oversize guard and the
// post-copy length check are both strictly-greater-than, and the
// LimitReader reads limit+1 bytes.
func TestStreamCopy_AtExactCap(t *testing.T) {
	h, dir, _ := testDir(t)
	src := []byte("0123456789") // size passed as the per-call limit below
	if err := os.WriteFile(filepath.Join(dir, "in"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dir, "in")
	destPath := filepath.Join(dir, "out")

	n, err := streamCopy(context.Background(), srcPath, destPath, int64(len(src)), h)
	if err != nil {
		t.Fatalf("streamCopy(size == cap) error = %v, want nil (a file exactly at the cap is not oversize)", err)
	}
	if n != int64(len(src)) {
		t.Fatalf("streamCopy(size == cap) n = %d, want %d (the full file must be copied)", n, len(src))
	}
	got, readErr := os.ReadFile(destPath)
	if readErr != nil {
		t.Fatalf("reading copied dest: %v", readErr)
	}
	if !bytes.Equal(got, src) {
		t.Errorf("streamCopy dest = %q, want %q (content must not be truncated)", got, src)
	}
}

// streamCopy returns the rename error when the temp file cannot replace
// the destination (renaming a file over a non-empty directory fails).
func TestStreamCopy_PropagatesRenameError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "src"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "destdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "destdir", "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := streamCopy(context.Background(),
		filepath.Join(dir, "src"), filepath.Join(dir, "destdir"), maxCopySize, h)
	if err == nil {
		t.Fatalf("streamCopy(dest is a non-empty dir) = (n=%d, nil), want non-nil error (rename over a directory must fail)", n)
	}
}

// listEntries at root keeps visible files and filters dotfiles.
func TestListEntries_FiltersDotfilesAtRoot(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "visible.txt"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".hidden"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	got := listEntries(context.Background(), entries, "/")
	names := map[string]bool{}
	for _, f := range got {
		names[f.Name] = true
	}
	if !names["visible.txt"] {
		t.Errorf("listEntries at root dropped %q; non-dotfiles must be kept", "visible.txt")
	}
	if names[".hidden"] {
		t.Errorf("listEntries at root kept %q; dotfiles must be filtered", ".hidden")
	}
}

// Reading a file whose size exactly equals maxFileSize returns 200 with
// the full content: the size guard and the post-read length check are
// both strictly-greater-than, and the LimitReader reads maxFileSize+1.
func TestReadFile_AtExactMaxSize(t *testing.T) {
	h, dir, prefix := testDir(t)
	content := bytes.Repeat([]byte("a"), maxFileSize) // exactly the cap; not binary
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file?path="+prefix+"/big.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET big.txt (size == maxFileSize) status = %d, want 200 (a file exactly at the cap is readable)", rec.Code)
	}
	var resp struct {
		Content string `json:"content"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal read response: %v", err)
	}
	if len(resp.Content) != maxFileSize {
		t.Errorf("read content length = %d, want %d (the full file must be returned, not truncated)",
			len(resp.Content), maxFileSize)
	}
}

// Downloading a file whose size exactly equals maxCopySize returns 200;
// the download size guard is strictly-greater-than. A sparse file + a
// discarding ResponseWriter keep this cheap.
func TestHandleDownload_AtExactMaxCopySize(t *testing.T) {
	h, dir, prefix := testDir(t)
	f, err := os.Create(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxCopySize); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/file/download?path="+prefix+"/big.bin", nil)
	sw := &discardStatusRecorder{}
	h.handleDownload(sw, req)
	if sw.code != http.StatusOK {
		t.Fatalf("download (size == maxCopySize) status = %d, want 200 (a file exactly at the cap is downloadable)", sw.code)
	}
}

// writeOneUpload propagates the write error when the destination's
// parent directory is missing.
func TestWriteOneUpload_PropagatesError(t *testing.T) {
	req := multipartUpload(t, "ignored", map[string][]byte{"a.txt": []byte("hi")})
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	fhs := req.MultipartForm.File["files"]
	if len(fhs) == 0 {
		t.Fatal("no multipart file parsed")
	}
	h, dir, _ := testDir(t)
	dest := filepath.Join(dir, "missing-dir", "x.txt") // parent does not exist
	n, err := h.writeOneUpload(context.Background(), dest, fhs[0])
	if err == nil {
		t.Fatalf("writeOneUpload(dest with missing parent) = (n=%d, nil), want non-nil error", n)
	}
}

// --- /api/files/download (POST zip stream) ---------------------------

// handleDownloadZip streams a workspace dir/file selection as a zip:
// top-level entries are named by their base, and a selected directory
// recurses with the directory base as the zip-path prefix. Every path
// is resolved through the workspace-containment guard before any bytes
// are written.
func TestHandleDownloadZip_StreamsFilesAndDirs(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("bravo"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := `{"paths":["` + prefix + `/a.txt","` + prefix + `/sub"]}`
	rec := postReq(t, h, "/api/files/download", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %q: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(data)
	}
	if got["a.txt"] != "alpha" {
		t.Errorf("zip entry a.txt = %q, want %q", got["a.txt"], "alpha")
	}
	if want := filepath.Join("sub", "b.txt"); got[want] != "bravo" {
		t.Errorf("zip entry %q = %q, want %q (directory must recurse)", want, got[want], "bravo")
	}
}

// handleDownloadZip rejects a non-POST method and an empty path list
// before streaming anything.
func TestHandleDownloadZip_Rejects(t *testing.T) {
	h, _, _ := testDir(t)

	if rec := getReq(t, h, "/api/files/download"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
	if rec := postReq(t, h, "/api/files/download", `{"paths":[]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty paths status = %d, want 400", rec.Code)
	}
}
