package filebrowse

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testDir(t *testing.T) (h *Handler, dir, prefix string) {
	t.Helper()
	dir = t.TempDir() // e.g. /tmp/TestXxx123 — the handler's single granted mount
	var err error
	h, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// prefix is the request path relative to /
	prefix = strings.TrimPrefix(dir, "/")
	return h, dir, prefix
}

// testHandlerAt builds a handler whose single mount claims policyDir
// (e.g. "/config") while its os.Root is backed by a throwaway temp
// dir. Lexical-layer tests exercise the REAL sensitive prefixes
// without touching (or requiring) the actual policy path on the host;
// no filesystem op ever reaches the backing dir in these tests.
func testHandlerAt(t *testing.T, policyDir string) *Handler {
	t.Helper()
	backing, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{mounts: []mount{{
		root: backing,
		dir:  policyDir,
		name: strings.TrimPrefix(policyDir, "/"),
	}}}
}

// locAt builds a loc for abs inside the handler's first (only) mount,
// for tests that call action funcs directly, bypassing resolvePath.
func locAt(h *Handler, abs string) loc {
	return loc{m: &h.mounts[0], abs: abs}
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

func TestResolvePath_OutsideRoots(t *testing.T) {
	h, _, _ := testDir(t)
	for _, dir := range []string{"etc/passwd", "proc/1/status", "var/log/syslog", "workspace2/x"} {
		_, err := h.resolvePath(dir)
		if err == nil {
			t.Errorf("expected error for ungranted path %q", dir)
		}
	}
}

func TestResolvePath_Allowed(t *testing.T) {
	h, dir, prefix := testDir(t)
	got, err := h.resolvePath(prefix + "/myrepo/file.go")
	if err != nil {
		t.Fatal(err)
	}
	// myrepo doesn't exist so EvalSymlinks returns ENOENT on the
	// leaf; we fall through to the lexical form.
	if want := dir + "/myrepo/file.go"; got.abs != want {
		t.Errorf("got %q, want %q", got.abs, want)
	}
	if got.m == nil || got.m.dir != dir {
		t.Errorf("loc mount = %+v, want mount at %q", got.m, dir)
	}
	if want := "myrepo/file.go"; got.rel() != want {
		t.Errorf("rel() = %q, want %q", got.rel(), want)
	}
}

// "/" is not resolvable — it is the synthetic mount listing, handled
// before resolvePath in handleFiles. Every other route 403s on it.
func TestResolvePath_Root_Denied(t *testing.T) {
	h, _, _ := testDir(t)
	if _, err := h.resolvePath("/"); err == nil {
		t.Fatal("resolvePath(\"/\") = nil error, want denial (no mount is /)")
	}
}

func TestResolvePath_TraversalOutOfMount(t *testing.T) {
	h, _, prefix := testDir(t)
	_, err := h.resolvePath(prefix + "/../../etc/passwd")
	if err == nil {
		t.Error("expected error for traversal out of the granted mount")
	}
}

// Normalisation: resolvePath cleans common noisy input forms.
func TestResolvePath_Normalisation(t *testing.T) {
	h, dir, prefix := testDir(t)
	tests := []struct {
		in   string
		want string // relative to the mount dir
	}{
		{prefix + "/myrepo", "/myrepo"},
		{prefix + "//myrepo", "/myrepo"},
		{prefix + "/./myrepo", "/myrepo"},
		{prefix + "/myrepo/", "/myrepo"},
		{prefix + "/a/../b", "/b"},
		{"/" + prefix + "/a", "/a"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := h.resolvePath(tc.in)
			if err != nil {
				t.Fatalf("resolvePath(%q) error: %v", tc.in, err)
			}
			if want := dir + tc.want; got.abs != want {
				t.Errorf("resolvePath(%q) = %q, want %q", tc.in, got.abs, want)
			}
		})
	}
}

// S1 regression: a symlink planted inside a granted mount must not
// grant access to an out-of-mount target.
func TestResolvePath_SymlinkOutOfMountRejected(t *testing.T) {
	h, dir, prefix := testDir(t)

	link := filepath.Join(dir, "evil-link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Lexical form is <mount>/evil-link which passes the mount match.
	// EvalSymlinks resolves to /etc, which is outside every grant.
	_, err := h.resolvePath(prefix + "/evil-link")
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
	h, dir, prefix := testDir(t)

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
	h, dir, prefix := testDir(t)

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

// --- IsSensitive tests (direct coverage) ---

func TestIsSensitive(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// kiro-cli state lives inside HOME (KIRO_HOME=$HOME/.kiro); the
		// whole /config/home/ tree is blocked.
		{"home_env_md", "/config/home/.kiro/steering/environment.md", true},
		{"home_agents", "/config/home/.kiro/agents/foo.json", true},
		{"home_ssh_key", "/config/home/.ssh/id_ed25519", true},
		// Legacy pre-relocation KIRO_HOME tree: blocked wholesale.
		{"legacy_env_md", "/config/kiro/steering/environment.md", true},
		{"legacy_agents", "/config/kiro/agents/foo.json", true},
		{"legacy_any_file", "/config/kiro/steering/other.md", true},
		{"unrelated_file", "/workspace/repo/main.go", false},
		{"chats_dir_deep", "/config/chats/deep/nested.json", true},
		{"exact_push_subs", "/config/push-subs.json", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSensitive(tc.path); got != tc.want {
				t.Errorf("IsSensitive(%q) = %v, want %v", tc.path, got, tc.want)
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
		{"/config/home/.kiro/agents", true}, // inside the blocked HOME tree
		{"/config/kiro/steering", true},     // inside the blocked legacy tree
		{"/config/kiro", true},              // the legacy tree itself
		{"/config/home", true},              // the HOME tree itself
		{"/config", true},                   // encloses push-subs.json
		// Not protected: leaves and unrelated paths.
		{"/workspace", false},
		{"/workspace/repo", false},
		// Note: a path below a sensitive dir prefix still matches
		// because callers already ran it through IsSensitive (which
		// would also block it). isProtectedDir is the defense for
		// the CONTAINER case, not a substitute for IsSensitive.
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

func TestReadFile_OutsideRoots(t *testing.T) {
	h, _, _ := testDir(t)
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
	h, _, _ := testDir(t)
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

// The root listing is synthetic: exactly the granted mounts, sorted by
// name, never writable. Nothing else on the host filesystem leaks in.
func TestListFiles_Root_ListsMounts(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	h, err := New(dirB, dirA) // deliberately unsorted
	if err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/files?path=.")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path     string `json:"path"`
		Files    []fileEntry
		Writable bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Writable {
		t.Error("synthetic root must not be writable")
	}
	wantNames := []string{strings.TrimPrefix(dirA, "/"), strings.TrimPrefix(dirB, "/")}
	slices.Sort(wantNames)
	if len(resp.Files) != 2 {
		t.Fatalf("files = %+v, want exactly the 2 mounts", resp.Files)
	}
	for i, want := range wantNames {
		f := resp.Files[i]
		if f.Name != want || !f.IsDir {
			t.Errorf("files[%d] = %+v, want dir %q", i, f, want)
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
	h, _, _ := testDir(t)
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
	h, _, _ := testDir(t)
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

// swappedAncestor stages the race a pinned parent exists for: a path whose
// directory component was a real, empty directory when the policy check resolved
// it, and is a symlink to a sensitive tree by the time the operation runs.
//
// The loc is built before the swap, which is what makes the window deterministic.
// It returns the request-path form of the named entry, the on-disk path of the
// file that must survive, and the loc the action is called with.
func swappedAncestor(t *testing.T, h *Handler, dir string) (victim string, l loc) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	victim = filepath.Join(dir, "store", "keep.txt")
	if err := os.WriteFile(victim, []byte("the chat store"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The named entry does not exist inside the real x, so anything the operation
	// touches is a file it was never pointed at.
	l = locAt(h, filepath.Join(dir, "x", "keep.txt"))
	if err := os.Remove(filepath.Join(dir, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("store", filepath.Join(dir, "x")); err != nil {
		t.Fatal(err)
	}
	return victim, l
}

// TestActionDelete_RefusesASwappedAncestor: the sensitive-path check is
// exact-prefix over the resolved path, so /config/x/chats matches no sensitive
// prefix and passes — and with x swapped for an in-mount symlink, the mount's
// os.Root would FOLLOW it (a root confines but does not pin) and the unlink would
// land on the protected tree through a check that said the path was not
// protected. Naming only the final element through a pinned parent is what closes
// it.
func TestActionDelete_RefusesASwappedAncestor(t *testing.T) {
	h, dir, _ := testDir(t)
	victim, l := swappedAncestor(t, h, dir)

	err := actionDelete(context.Background(), httptest.NewRecorder(), fileAction{Action: "delete"}, l, h)
	if err == nil {
		t.Error("delete through a symlinked ancestor was accepted, want a refusal")
	}
	if _, statErr := os.Lstat(victim); statErr != nil {
		t.Errorf("the protected file was deleted through the symlinked ancestor: %v", statErr)
	}
}

// TestActionMove_RefusesASwappedAncestor is actionDelete's case on the source
// side of a move, which cannot leave the mount but can carry the protected tree
// out from under its own guard.
func TestActionMove_RefusesASwappedAncestor(t *testing.T) {
	h, dir, prefix := testDir(t)
	victim, l := swappedAncestor(t, h, dir)

	body := fileAction{Action: "move", Dest: prefix + "/stolen.txt"}
	err := actionMove(context.Background(), httptest.NewRecorder(), body, l, h)
	if err == nil {
		t.Error("move through a symlinked ancestor was accepted, want a refusal")
	}
	if _, statErr := os.Lstat(victim); statErr != nil {
		t.Errorf("the protected file was moved through the symlinked ancestor: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, "stolen.txt")); statErr == nil {
		t.Error("the move landed, want nothing at the destination")
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
			// "/" is not a mount; every action on it dies at resolve.
			name:       "delete/root",
			action:     "delete",
			pathSuffix: "/",
		},
		{
			// Ungranted top-level segments are outside the allow-list.
			name:       "delete/ungranted_segment",
			action:     "delete",
			pathSuffix: "workspace2",
		},
		{
			name:       "mkdir/ungranted_segment",
			action:     "mkdir",
			pathSuffix: "newdir",
		},
		{
			name:       "touch/ungranted_segment",
			action:     "touch",
			pathSuffix: "new.txt",
		},
		{
			// The granted root itself is boot-time configuration: no
			// action may delete, shadow, or recreate it.
			name:             "delete/mount_point",
			action:           "delete",
			useTempDir:       true,
			pathSuffix:       ".",
			wantBodyContains: "granted root",
		},
		{
			name:             "mkdir/mount_point",
			action:           "mkdir",
			useTempDir:       true,
			pathSuffix:       ".",
			wantBodyContains: "granted root",
		},
		{
			name:             "touch/mount_point",
			action:           "touch",
			useTempDir:       true,
			pathSuffix:       ".",
			wantBodyContains: "granted root",
		},
		{
			name:             "rename/mount_point",
			action:           "rename",
			useTempDir:       true,
			pathSuffix:       ".",
			renameName:       "stolen",
			wantBodyContains: "granted root",
		},
		{
			name:             "move/mount_point",
			action:           "move",
			useTempDir:       true,
			pathSuffix:       ".",
			dest:             "stolen",
			wantBodyContains: "granted root",
		},
		{
			// isProtectedDir on delete: the container of a sensitive
			// tree is not deletable even though IsSensitive only
			// matches its contents.
			name:              "delete/protected_dir",
			action:            "delete",
			useTempDir:        true,
			pathSuffix:        "chats",
			sensitivePrefix:   "chats/", // directory prefix
			setupDir:          "chats",
			checkSourceExists: "chats",
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
			// Every case runs against a temp-dir mount; useTempDir=false
			// cases simply target paths outside it (absolute suffixes).
			h, dir, prefix := testDir(t)

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

func TestAction_OutsideRoots(t *testing.T) {
	h, _, _ := testDir(t)
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
	h, _, _ := testDir(t)
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
	h, _, _ := testDir(t)
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

func TestAction_Copy_DestOutsideRoots(t *testing.T) {
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

// condGet issues a conditional download the way a browser revalidating a
// no-cache response does: both validators from the previous 200.
func condGet(t *testing.T, h *Handler, path, etag, lastMod string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastMod != "" {
		req.Header.Set("If-Modified-Since", lastMod)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHandleDownload_ETagSurvivesASameSecondRewrite pins the strong validator.
//
// Last-Modified is an HTTP-date truncated to one second, so with it as the only
// validator a rewrite inside one second of the client's last Last-Modified
// answers 304 and the browser serves the previous bytes. The agent's screenshot
// loop is that consumer: a re-shot frame keeps its filename and therefore its
// URL, so nothing busts the cache. Both revalidations here carry both headers,
// which is what a browser sends, and the SIZE is held constant so the assertion
// rests on the mtime-nanoseconds leg rather than passing for a second reason.
func TestHandleDownload_ETagSurvivesASameSecondRewrite(t *testing.T) {
	h, dir, prefix := testDir(t)
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, []byte("frame-one"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Both mtimes are pinned into ONE wall-clock second so the test does not
	// depend on how coarse the host's inode clock happens to be.
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, base, base.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Nanosecond() == 0 {
		t.Skipf("%s stores mtime at second granularity, so sub-second rewrites are indistinguishable here", dir)
	}

	url := "/api/file/download?path=" + prefix + "/shot.png"
	first := getReq(t, h, url)
	if first.Code != 200 {
		t.Fatalf("first GET: status %d: %s", first.Code, first.Body.String())
	}
	etag, lastMod := first.Header().Get("ETag"), first.Header().Get("Last-Modified")
	if len(etag) < 3 || !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag = %q, want a quoted strong validator (an unquoted one ServeContent never matches)", etag)
	}

	// The validator must still validate, or the fix would be "always 200".
	if rec := condGet(t, h, url, etag, lastMod); rec.Code != http.StatusNotModified {
		t.Errorf("unchanged file: status = %d, want 304", rec.Code)
	}

	if err := os.WriteFile(path, []byte("frame-two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, base, base.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	rec := condGet(t, h, url, etag, lastMod)
	if rec.Code != 200 {
		t.Fatalf("same-second rewrite: status = %d, want 200 with the new bytes", rec.Code)
	}
	if got := rec.Body.String(); got != "frame-two" {
		t.Errorf("same-second rewrite: body = %q, want %q", got, "frame-two")
	}
	if next := rec.Header().Get("ETag"); next == etag {
		t.Errorf("ETag = %q for both generations, want it to change", next)
	}
}

// TestHandleDownload_SVGIsAttachment pins the one download header that is a
// SECURITY control rather than a convenience.
//
// mime.TypeByExtension(".svg") is "image/svg+xml", and an SVG document rendered
// at a same-origin URL executes its own script with this origin's privileges —
// which would mean access to vibekit's cookies and its whole same-origin API
// surface. Every `<img src=…>` pointed at this route is safe on its own (an SVG
// referenced AS AN IMAGE may not fetch, script, or reach the embedding document),
// but the file browser also renders a real anchor to this URL, and CSP does not
// close the gap: `frame-src` falls back to `default-src 'self'`, which PERMITS a
// same-origin frame.
//
// `Content-Disposition: attachment` is the whole control. It was untested, and a
// plausible future change — "serve images inline so the viewer can use them" —
// would have silently turned the existing download anchor into stored XSS. The
// `<img>` path needs no `inline` to work, which is exactly why relaxing this
// would buy nothing.
func TestHandleDownload_SVGIsAttachment(t *testing.T) {
	h, dir, prefix := testDir(t)
	const svg = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	if err := os.WriteFile(filepath.Join(dir, "arch.svg"), []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/file/download?path="+prefix+"/arch.svg")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	// The type is script-capable, which is precisely why the disposition matters.
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("Content-Type = %q, want image/svg+xml*", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want to start with attachment", cd)
	}
	if strings.Contains(cd, "inline") {
		t.Errorf("Content-Disposition = %q, must never be inline for image/svg+xml", cd)
	}
	// The bytes are served verbatim; nothing sanitizes them, and nothing should —
	// inertness comes from HOW the response is consumed, not from rewriting it.
	if got := rec.Body.String(); got != svg {
		t.Errorf("body = %q, want the file verbatim", got)
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

func TestHandleUpload_OutsideRootsDir(t *testing.T) {
	h, _, _ := testDir(t)
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

// A symlink planted between resolvePath's EvalSymlinks and the write must not
// materialise its target.
//
// The premise this test used to carry was wrong: it credited
// syscall.O_NOFOLLOW on the old root.OpenFile, and that flag never did anything
// here (see TestWriteFile_RelativeSymlinkCannotClobberVictim for the
// measurement). What blocked THIS case was os.Root's own rule that a symlink
// must not be absolute, and the target here is absolute. The write now refuses
// a non-regular target outright, so the answer is a 400 naming it; the
// post-condition is unchanged and is what matters.
func TestWriteFile_DanglingSymlinkToSensitive_Blocked(t *testing.T) {
	h, dir, prefix := testDir(t)

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
	if rec.Code != 400 && rec.Code != 403 && rec.Code != 500 {
		t.Errorf("status = %d, want 400, 403 or 500; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("sensitive target file was created despite symlink guard")
	}
}

// The case the deleted syscall.O_NOFOLLOW claimed to cover and did not: a
// RELATIVE symlink swapped in AFTER resolvePath accepted a regular file.
//
// A symlink that already exists is not the exposure — resolvePath EvalSymlinks
// the leaf, so it names the target and IsSensitive judges the target. The
// exposure is the race, so the race is what this stages: resolve, swap, write.
//
// Measured on go1.27.0: os.Root.OpenFile ORs O_NOFOLLOW in itself and then
// re-resolves the link on the resulting ELOOP, so a caller-supplied O_NOFOLLOW
// is ignored and the open lands on the target. The red check performs the exact
// deleted open and shows the victim clobbered; the handler then refuses. An
// ABSOLUTE-target link is a different case that os.Root refuses on its own
// (symlinks must not be absolute), which is why the sibling test above passed
// for a reason unrelated to the flag.
func TestWriteFile_RelativeSymlinkSwappedAfterResolve(t *testing.T) {
	h, dir, _ := testDir(t)

	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("KEEP"), 0o600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	decoy := filepath.Join(dir, "decoy.txt")
	if err := os.WriteFile(decoy, []byte("decoy"), 0o600); err != nil {
		t.Fatalf("seed decoy: %v", err)
	}

	// The resolve the handler performs, on the regular file.
	l, err := h.resolvePath(strings.TrimPrefix(decoy, "/"))
	if err != nil {
		t.Fatalf("resolvePath(%q) = %v, want nil", decoy, err)
	}

	swap := func() {
		t.Helper()
		if rErr := os.Remove(decoy); rErr != nil && !errors.Is(rErr, os.ErrNotExist) {
			t.Fatalf("remove decoy: %v", rErr)
		}
		if sErr := os.Symlink("victim.txt", decoy); sErr != nil {
			t.Fatalf("symlink decoy -> victim: %v", sErr)
		}
	}

	// Red check: the deleted open writes through the link.
	swap()
	f, err := l.m.root.OpenFile(l.rel(),
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		t.Fatalf("Root.OpenFile with O_NOFOLLOW refused a relative in-root symlink (%v); "+
			"the exposure this test exists to close is not reachable, so the refusal "+
			"below proves nothing", err)
	}
	if _, err := f.WriteString("CLOBBERED"); err != nil {
		t.Fatalf("write through the link: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "CLOBBERED" {
		t.Fatalf("victim after the ambient write = %q, want %q", got, "CLOBBERED")
	}

	// Reset and run the handler's own write against the same stale loc.
	if err := os.WriteFile(victim, []byte("KEEP"), 0o600); err != nil {
		t.Fatalf("restore victim: %v", err)
	}
	swap()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/file", strings.NewReader(`{"content":"pwn"}`))
	writeFile(rec, req, l)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(victim); string(got) != "KEEP" {
		t.Errorf("victim after the refused write = %q, want %q", got, "KEEP")
	}
}

func TestHandleUpload_MethodNotAllowed(t *testing.T) {
	h, _, _ := testDir(t)
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
	h, _, _ := testDir(t)
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
// trailing components must not bypass the allow-list. The earlier S1
// tests only pinned the single-missing-leaf case where
// EvalSymlinks(parent) resolves the symlink itself. With two missing
// components (evil/newdir/sub where newdir doesn't exist in /etc),
// EvalSymlinks(parent)=EvalSymlinks(.../evil/newdir) returns ENOENT
// internally — only the ancestor walk surfaces the symlink crossing.
func TestResolvePath_SymlinkAncestorWithDeepMissingLeaf_Rejected(t *testing.T) {
	h, dir, prefix := testDir(t)

	link := filepath.Join(dir, "evil")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// `newdir/sub` do not exist either in the temp dir or in /etc.
	// Pre-fix: resolveRealPath returned the lexical form, which stays
	// inside the granted mount, and the request passed. Post-fix: the
	// ancestor walk resolves `evil` to `/etc`, recomposes
	// `/etc/newdir/sub`, and enforce rejects it as outside every mount.
	_, err := h.resolvePath(prefix + "/evil/newdir/sub")
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
	cancelledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	expiredCtx, expiredCancel := context.WithDeadline(t.Context(), time.Now().Add(-1*time.Second))
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
			ctx:     t.Context(),
			inner:   strings.NewReader("hello"),
			wantN:   5,
			wantErr: nil,
		},
		{
			name:    "live_context_forwards_inner_error",
			ctx:     t.Context(),
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
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	uploaded, _, wErr := writeUploads(ctx, locAt(h, dir), files)
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

	ctx, cancel := context.WithCancel(t.Context())
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
	h, err := New(dir)
	if err != nil {
		b.Fatal(err)
	}

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
		{"outside_roots_path", "etc/passwd"},
		{"deep_nested_path", prefix + "/a/b/c/d/../../../b/c/d"},
		{"symlink_path", prefix + "/linked/c/d"},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				h.resolvePath(tc.path)
			}
		})
	}
}

// --- Fuzz target for resolvePath (security-critical path resolution) ---

func FuzzResolvePath(f *testing.F) {
	// Two mounts: a real temp dir (filesystem-backed resolution) and a
	// policy-level "/config" mount so the sensitive-prefix seeds
	// exercise the sensitive layer rather than dying at mount match.
	dir := f.TempDir()
	backing, err := os.OpenRoot(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	h, err := New(dir)
	if err != nil {
		f.Fatal(err)
	}
	h.mounts = append(h.mounts, mount{root: backing, dir: "/config", name: "config"})
	prefix := strings.TrimPrefix(dir, "/")

	// Seed corpus: known attack shapes and edge cases.
	seeds := []string{
		prefix + "/myrepo/file.go",
		"../../../etc/passwd",
		prefix + "/../../etc/shadow",
		"etc/passwd",
		"proc/1/status",
		"var/log/syslog",
		"/",
		".",
		"..",
		prefix + "/./../../etc/passwd",
		prefix + "/%2e%2e/etc/passwd",
		prefix + "/\x00/etc/passwd",
		prefix + "/symlink/../../../etc",
		strings.Repeat("../", 50) + "etc/passwd",
		prefix + "/a/b/c/../../../../etc/passwd",
		"config/home/.kiro/steering/vibekit.md",
		"config/chats/deep/nested.json",
		"config/push-subs.json",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		result, err := h.resolvePath(input)
		if err != nil {
			// Rejected path — that's fine, security working as intended.
			return
		}

		// Assertion 1: no panic (implicit — reaching here means no panic).

		// Assertion 2: the result is always an absolute clean path.
		if !filepath.IsAbs(result.abs) {
			t.Errorf("resolvePath(%q) = %q is not absolute", input, result.abs)
		}
		if cleaned := filepath.Clean(result.abs); cleaned != result.abs {
			t.Errorf("resolvePath(%q) = %q is not clean (Clean = %q)", input, result.abs, cleaned)
		}

		// Assertion 3 (allow-list invariant): the result lies inside a
		// granted mount, and the returned mount is that owner.
		owner := h.mountFor(result.abs)
		if owner == nil {
			t.Errorf("resolvePath(%q) = %q is outside every granted mount", input, result.abs)
		} else if result.m != owner {
			t.Errorf("resolvePath(%q) returned mount %q, want owner %q", input, result.m.dir, owner.dir)
		}

		// Assertion 4: the result is never a sensitive path.
		if IsSensitive(result.abs) {
			t.Errorf("resolvePath(%q) = %q is a sensitive path", input, result.abs)
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
		result := IsSensitive(input)

		// Assertion 1: if IsSensitive returns true, the input must match
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
				t.Errorf("IsSensitive(%q) = true but no sensitivePrefixes entry matches", input)
			}
		}

		// Assertion 2: if no sensitivePrefixes entry matches, result must be false.
		if !result {
			for _, sp := range sensitivePrefixes {
				if sp.IsDir {
					if strings.HasPrefix(input, sp.Path) {
						t.Errorf("IsSensitive(%q) = false but matches dir prefix %q", input, sp.Path)
					}
				} else if input == sp.Path {
					t.Errorf("IsSensitive(%q) = false but matches exact path %q", input, sp.Path)
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

			h, err := New(dir)
			if err != nil {
				b.Fatal(err)
			}
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			body := `{"action":"copy","path":"` + prefix + `/src.bin","dest":"` + prefix + `/dst.bin"}`

			b.SetBytes(int64(sz.size))
			b.ReportAllocs()

			for b.Loop() {
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

// actionMkdir creates any path inside a mount; only the mount point
// itself is refused (the mount boundary replaced the old depth guard).
func TestAction_Mkdir_InsideMount(t *testing.T) {
	h, dir, _ := testDir(t)
	l := locAt(h, filepath.Join(dir, "a", "b"))
	err := actionMkdir(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err != nil {
		t.Fatalf("actionMkdir(%q) = %v, want nil (paths inside a mount must be creatable)", l.abs, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "a", "b")); statErr != nil {
		t.Errorf("actionMkdir(%q): expected dir created at <mount>/a/b, stat err: %v", l.abs, statErr)
	}
}

// actionMkdir returns the raw MkdirAll error (MkdirAll under a regular
// file is ENOTDIR) rather than swallowing it or returning errHandled.
func TestAction_Mkdir_PropagatesError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "afile", "sub")) // "afile" is a file -> MkdirAll ENOTDIR
	err := actionMkdir(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err == nil {
		t.Fatalf("actionMkdir(%q) = nil, want non-nil (MkdirAll under a regular file must error)", l.abs)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionMkdir(%q) returned errHandled; expected the raw MkdirAll error", l.abs)
	}
}

// actionTouch creates a file anywhere inside the mount with an
// existing parent.
func TestAction_Touch_InsideMount(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.Mkdir(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "x", "y"))
	err := actionTouch(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err != nil {
		t.Fatalf("actionTouch(%q) = %v, want nil (paths inside a mount must be touchable)", l.abs, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "x", "y")); statErr != nil {
		t.Errorf("actionTouch(%q): expected file created at <mount>/x/y, stat err: %v", l.abs, statErr)
	}
}

// actionTouch returns the raw OpenFile error (OpenFile under a regular
// file is ENOTDIR) rather than swallowing it or closing a nil file.
func TestAction_Touch_PropagatesOpenError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "pfile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "pfile", "child")) // parent is a file -> OpenFile ENOTDIR
	err := actionTouch(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err == nil {
		t.Fatalf("actionTouch(%q) = nil, want non-nil (OpenFile under a regular file must error)", l.abs)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionTouch(%q) returned errHandled; expected the raw OpenFile error", l.abs)
	}
}

// actionDelete removes anything inside the mount; only the mount point
// itself is refused.
func TestAction_Delete_InsideMount(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "p", "q"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "p", "q"))
	err := actionDelete(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err != nil {
		t.Fatalf("actionDelete(%q) = %v, want nil (paths inside a mount must be deletable)", l.abs, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "p", "q")); !os.IsNotExist(statErr) {
		t.Errorf("actionDelete(%q): expected <mount>/p/q removed, stat err = %v", l.abs, statErr)
	}
}

// actionDelete returns the raw RemoveAll error when the path traverses
// an escaping symlink (os.Root refuses the operation).
func TestAction_Delete_PropagatesRemoveError(t *testing.T) {
	h, dir, _ := testDir(t)
	if err := os.Symlink("/etc", filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	l := locAt(h, filepath.Join(dir, "escape", "leaf")) // traverses an escaping symlink
	err := actionDelete(t.Context(), httptest.NewRecorder(), fileAction{}, l, h)
	if err == nil {
		t.Fatalf("actionDelete(%q) = nil, want non-nil (RemoveAll through an escaping symlink must error)", l.abs)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionDelete(%q) returned errHandled; expected the raw RemoveAll error", l.abs)
	}
}

// actionRename surfaces the raw Rename error (renaming a missing source)
// past every guard rather than returning errHandled.
func TestAction_Rename_PropagatesError(t *testing.T) {
	h, dir, _ := testDir(t)
	l := locAt(h, filepath.Join(dir, "ghost.txt")) // source does not exist
	err := actionRename(t.Context(), httptest.NewRecorder(),
		fileAction{Name: "renamed.txt"}, l, h)
	if err == nil {
		t.Fatalf("actionRename(%q -> renamed.txt) = nil, want non-nil (renaming a missing source must error)", l.abs)
	}
	if errors.Is(err, errHandled) {
		t.Fatalf("actionRename returned errHandled; expected the raw Rename error (a guard fired before Rename)")
	}
}

// actionMove surfaces the raw Rename error for a missing source, same
// shape as rename.
func TestAction_Move_PropagatesError(t *testing.T) {
	h, dir, _ := testDir(t)
	l := locAt(h, filepath.Join(dir, "ghost.txt")) // source does not exist
	err := actionMove(t.Context(), httptest.NewRecorder(),
		fileAction{Dest: filepath.Join(dir, "moved.txt")}, l, h)
	if err == nil {
		t.Fatalf("actionMove(%q) = nil, want non-nil (moving a missing source must error)", l.abs)
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
	srcLoc := locAt(h, filepath.Join(dir, "in"))
	destLoc := locAt(h, filepath.Join(dir, "out"))

	n, err := streamCopy(t.Context(), srcLoc, destLoc, int64(len(src)))
	if err != nil {
		t.Fatalf("streamCopy(size == cap) error = %v, want nil (a file exactly at the cap is not oversize)", err)
	}
	if n != int64(len(src)) {
		t.Fatalf("streamCopy(size == cap) n = %d, want %d (the full file must be copied)", n, len(src))
	}
	got, readErr := os.ReadFile(destLoc.abs)
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
	n, err := streamCopy(t.Context(),
		locAt(h, filepath.Join(dir, "src")), locAt(h, filepath.Join(dir, "destdir")), maxCopySize)
	if err == nil {
		t.Fatalf("streamCopy(dest is a non-empty dir) = (n=%d, nil), want non-nil error (rename over a directory must fail)", n)
	}
}

// listEntries keeps dotfiles (the synthetic mount listing replaced the
// old real-root special case) and hides sensitive paths.
func TestListEntries_KeepsDotfiles_HidesSensitive(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"visible.txt", ".hidden", "secret.json"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	orig := sensitivePrefixes
	t.Cleanup(func() { sensitivePrefixes = orig })
	sensitivePrefixes = append([]sensitivePath{
		{Path: filepath.Join(tmp, "secret.json"), IsDir: false},
	}, orig...)

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	got := listEntries(t.Context(), entries, tmp)
	names := map[string]bool{}
	for _, f := range got {
		names[f.Name] = true
	}
	if !names["visible.txt"] || !names[".hidden"] {
		t.Errorf("listEntries dropped a visible entry: %v", names)
	}
	if names["secret.json"] {
		t.Errorf("listEntries leaked the sensitive entry: %v", names)
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
	dest := locAt(h, filepath.Join(dir, "missing-dir", "x.txt")) // parent does not exist
	n, err := writeOneUpload(t.Context(), dest, fhs[0])
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
	// Two files in the subdirectory, not one: a directory walk that enumerated
	// only its first child would archive b.txt and silently drop c.txt.
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("bravo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("charlie"), 0o644); err != nil {
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
	// The second child: a walk that enumerated the directory one entry at a
	// time would archive only the first and drop this one.
	if want := filepath.Join("sub", "c.txt"); got[want] != "charlie" {
		t.Errorf("zip entry %q = %q, want %q (every child of a directory must be archived)",
			want, got[want], "charlie")
	}
}

// handleDownloadZip rejects a non-POST method and an empty path list
// before streaming anything.
func TestHandleDownloadZip_Rejects(t *testing.T) {
	h, _, _ := testDir(t)

	rec := getReq(t, h, "/api/files/download")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
	// RFC 9110 §15.5.6: a 405 must name the resource's permitted methods.
	if got := rec.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q, want %q", got, "POST")
	}
	if rec := postReq(t, h, "/api/files/download", `{"paths":[]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty paths status = %d, want 400", rec.Code)
	}
}

// handleFile serves one resource under two methods, so its 405 must list
// both. Routes are registered as plain paths (no ServeMux method patterns),
// which is why an unsupported method reaches the handler at all. The path
// must resolve inside the granted mount: the resolve prelude runs before the
// method switch, so an unresolvable path yields 403 and never reaches 405.
func TestHandleFile_RejectionListsEveryPermittedMethod(t *testing.T) {
	h, _, prefix := testDir(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodDelete, "/api/file?path="+prefix+"/f.txt", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, PUT" {
		t.Errorf("Allow = %q, want %q", got, "GET, PUT")
	}
}

// Security regression: the container's HOME tree (/config/home/) holds the
// real credential stores — the AWS SSO token + OAuth client secret
// (~/.aws/sso/cache), git SSH keys (~/.ssh), the forge PAT
// (~/.config/gh/hosts.yml), and ~/.gitconfig — and /config/mcp.json plus
// /config/mcp-secrets.json hold MCP env/header/oauth secrets and the OAuth
// refresh tokens and PKCE verifiers in cleartext. A prior audit found these
// browsable/readable/downloadable because sensitivePrefixes omitted them.
// Pins that every access path (IsSensitive predicate, resolvePath, HTTP
// read, HTTP download) now refuses them. The handler mounts "/config"
// at the POLICY level (temp-dir backed), so the rejection exercises
// the sensitive layer — not the mount match — and fires on the lexical
// enforce pass; no such file needs to exist on the test host.
func TestSensitivePaths_CredentialStoresRefused(t *testing.T) {
	credPaths := []string{
		"config/home/.aws/sso/cache/kiro-auth-token.json",    // live SSO bearer + refresh token
		"config/home/.aws/sso/cache/botocore-client-id.json", // OAuth client id + secret
		"config/home/.ssh/id_ed25519",                        // git SSH private key
		"config/home/.config/gh/hosts.yml",                   // forge PAT / OAuth token
		"config/home/.gitconfig",                             // git identity / credential config
		"config/mcp.json",                                    // MCP secrets, cleartext
		"config/mcp-secrets.json",                            // MCP OAuth refresh tokens + PKCE verifiers
	}
	h := testHandlerAt(t, "/config")
	for _, p := range credPaths {
		t.Run(p, func(t *testing.T) {
			abs := filepath.Clean("/" + p)
			// IsSensitive is the predicate enforce relies on.
			if !IsSensitive(abs) {
				t.Errorf("IsSensitive(%q) = false, want true (credential store must be protected)", abs)
			}
			// resolvePath gates every read/write/download/action.
			if _, err := h.resolvePath(p); err == nil {
				t.Errorf("resolvePath(%q) = nil error, want rejection", p)
			}
			// GET read must 403.
			if rec := getReq(t, h, "/api/file?path="+p); rec.Code != http.StatusForbidden {
				t.Errorf("GET /api/file?path=%s: status = %d, want 403; body=%s",
					p, rec.Code, rec.Body.String())
			}
			// GET download must 403 (this is the escaped-confinement path).
			if rec := getReq(t, h, "/api/file/download?path="+p); rec.Code != http.StatusForbidden {
				t.Errorf("GET /api/file/download?path=%s: status = %d, want 403; body=%s",
					p, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestWriteFile_StaleWriteGuard pins the guard that stops the editor silently
// discarding an agent's work.
//
// The scenario is routine rather than exotic in this app: the file a user has
// open in the editor is in the same tree the agent writes to, so "changed since
// you loaded it" happens whenever the agent touches that file mid-edit. Before
// this, the PUT overwrote unconditionally and the agent's version was gone with
// no trace.
//
// A DIGEST rather than an mtime, because this repo measured that Linux stamps
// inode timestamps from a coarse clock: two writes inside one tick are
// byte-identical in mtime, which is exactly the rapid agent write the guard
// exists to catch.
func TestWriteFile_StaleWriteGuard(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	target := filepath.Join(dir, "note.md")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Read it the way the editor does, to learn the hash.
	rec := getReq(t, h, "/api/file?path="+target)
	var read struct {
		Content     string `json:"content"`
		ContentHash string `json:"content_hash"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if read.ContentHash == "" {
		t.Fatal("read returned no content_hash, so a client has nothing to send back")
	}

	t.Run("matching hash writes", func(t *testing.T) {
		body := `{"content":"mine\n","expected_hash":"` + read.ContentHash + `"}`
		rec := putReq(t, h, "/api/file?path="+target, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		got, _ := os.ReadFile(target)
		if string(got) != "mine\n" {
			t.Errorf("file = %q, want the written content", got)
		}
	})

	t.Run("stale hash is refused and returns the current content", func(t *testing.T) {
		// The file now says "mine\n"; save against the ORIGINAL hash, as an editor
		// that loaded before the change would.
		body := `{"content":"clobber\n","expected_hash":"` + read.ContentHash + `"}`
		rec := putReq(t, h, "/api/file?path="+target, body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rec.Code)
		}
		var out struct {
			Error       string `json:"error"`
			Content     string `json:"content"`
			ContentHash string `json:"content_hash"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode conflict: %v", err)
		}
		// The current content rides the refusal so the client can show what
		// changed instead of asking the user to reload and compare by eye.
		if out.Content != "mine\n" {
			t.Errorf("conflict content = %q, want the on-disk bytes", out.Content)
		}
		if out.ContentHash == "" || out.ContentHash == read.ContentHash {
			t.Error("conflict must carry the NEW hash so the next save can succeed")
		}
		if got, _ := os.ReadFile(target); string(got) != "mine\n" {
			t.Errorf("file = %q, the refused write must not have landed", got)
		}
	})

	t.Run("omitted hash still writes", func(t *testing.T) {
		// Optional by design: every non-editor writer and any older client keeps
		// working rather than being blocked by a guard it does not know about.
		rec := putReq(t, h, "/api/file?path="+target, `{"content":"unguarded\n"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}

// touch is a no-op on an entry that already exists: O_EXCL refuses the create
// with EEXIST, which the action reports as success without disturbing the bytes
// or the mtime. The doc comment on actionTouch rests on exactly this.
func TestAction_Touch_ExistingFileSucceedsAndKeepsContent(t *testing.T) {
	h, dir, prefix := testDir(t)
	const want = "keep me"
	existing := filepath.Join(dir, "have.txt")
	if err := os.WriteFile(existing, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := postReq(t, h, "/api/files/action", `{"action":"touch","path":"`+prefix+`/have.txt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("touch on an existing file: status = %d, want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("touch on an existing file: content = %q, want %q", got, want)
	}
}

// The synthetic root listing reports each mount's REAL mode and modification
// time, statted through the mount's own root handle. The entry is seeded with a
// bare directory placeholder, so a listing that skipped the stat would still
// look plausible while carrying a zero timestamp.
func TestListFiles_Root_MountEntriesCarryStattedMetadata(t *testing.T) {
	dirA := t.TempDir()
	h, err := New(dirA)
	if err != nil {
		t.Fatal(err)
	}
	rec := getReq(t, h, "/api/files?path=.")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp struct {
		Files []fileEntry
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("files = %+v, want exactly the 1 mount", resp.Files)
	}
	if got := resp.Files[0].ModTime; got == 0 {
		t.Errorf("files[0].ModTime = %d for mount %q, want the statted timestamp", got, dirA)
	}
	if got := resp.Files[0].Mode; got == os.ModeDir.String() {
		t.Errorf("files[0].Mode = %q for mount %q, want the statted mode rather than the bare directory placeholder",
			got, dirA)
	}
}

// captureFilebrowseLogs redirects the slog default into a buffer for the
// duration of one test. The default logger is process-global, so a test using
// this must not run in parallel.
func captureFilebrowseLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// A writability probe that works says nothing. Every directory listing runs one,
// so a probe that logged on the success path would put a line per listing into
// the operator's log — and its cleanup warning, the one that means a probe file
// leaked, would be buried among them.
func TestIsWritable_SucceedsWithoutLogging(t *testing.T) {
	h, dir, _ := testDir(t)
	buf := captureFilebrowseLogs(t)

	if got := isWritable(locAt(h, dir)); !got {
		t.Fatalf("isWritable(%q) = false, want true (a fresh temp dir is writable)", dir)
	}
	if got := buf.String(); got != "" {
		t.Errorf("isWritable(%q) logged %q, want nothing on the success path", dir, got)
	}
}

// A touch that creates a file is recorded: the log line is the operator's only
// trace that the file appeared, and it must survive the close.
func TestAction_Touch_CreationIsLogged(t *testing.T) {
	h, dir, _ := testDir(t)
	buf := captureFilebrowseLogs(t)

	l := locAt(h, filepath.Join(dir, "fresh.txt"))
	if err := actionTouch(t.Context(), httptest.NewRecorder(), fileAction{}, l, h); err != nil {
		t.Fatalf("actionTouch(%q) = %v, want nil", l.abs, err)
	}
	if got := buf.String(); !strings.Contains(got, "filebrowse: touch") {
		t.Errorf("actionTouch(%q) logged %q, want a line naming the touch", l.abs, got)
	}
}

// The zip stream stops ON its budgets, not one entry past them: the file that
// takes the running totals to either ceiling is the last one written. Driven at
// the counters because the real boundary is 500 MB and 10 000 files.
func TestZipStream_WriteFileStopsOnEachBudget(t *testing.T) {
	src := filepath.Join(t.TempDir(), "five.txt")
	if err := os.WriteFile(src, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		bytesBefore  int64
		filesBefore  int
		wantContinue bool
	}{
		{name: "both_budgets_untouched", wantContinue: true},
		{name: "one_byte_short_of_the_byte_budget", bytesBefore: maxZipBytes - 6, wantContinue: true},
		{name: "this_file_reaches_the_byte_budget", bytesBefore: maxZipBytes - 5, wantContinue: false},
		{name: "two_files_short_of_the_file_budget", filesBefore: maxZipFiles - 2, wantContinue: true},
		{name: "this_file_reaches_the_file_budget", filesBefore: maxZipFiles - 1, wantContinue: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.Open(src)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()

			zw := zip.NewWriter(&bytes.Buffer{})
			z := &zipStream{
				zw:         zw,
				flusher:    httptest.NewRecorder(),
				ctx:        t.Context(),
				totalBytes: tc.bytesBefore,
				fileCount:  tc.filesBefore,
			}
			got := z.writeFile(f, "five.txt")
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if got != tc.wantContinue {
				t.Errorf("writeFile(five.txt) after %d bytes and %d files = %v, want %v",
					tc.bytesBefore, tc.filesBefore, got, tc.wantContinue)
			}
		})
	}
}

// nonFlushingWriter is an http.ResponseWriter that deliberately does NOT
// implement http.Flusher — the shape any middleware that wraps the writer
// without forwarding Flush produces.
type nonFlushingWriter struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (w *nonFlushingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *nonFlushingWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *nonFlushingWriter) WriteHeader(code int)        { w.code = code }

// The zip download streams over a ResponseWriter that cannot flush. The
// handler's flusher is an optional type assertion precisely so a wrapped writer
// does not have to carry Flush, and every entry must still reach the client.
func TestHandleDownloadZip_WriterWithoutFlushSupport(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/files/download",
		strings.NewReader(`{"paths":["`+prefix+`/a.txt"]}`))
	req.Header.Set("Content-Type", "application/json")
	w := &nonFlushingWriter{}
	mux.ServeHTTP(w, req)

	zr, err := zip.NewReader(bytes.NewReader(w.body.Bytes()), int64(w.body.Len()))
	if err != nil {
		t.Fatalf("open zip written to a non-flushing writer: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "a.txt" {
		t.Fatalf("zip entries = %+v, want just a.txt", zr.File)
	}
}
