package filehandler

// Tests in this file target surviving gremlins mutation-testing mutants
// in the internal/filehandler package (unit vibekit-u19). Each test is
// written so that applying the specific mutation at the named line
// changes an asserted observable outcome. Helpers/types are prefixed
// gk_vibekit_u19_ to avoid colliding with sibling units that share the
// package.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// gk_vibekit_u19_handler returns a Handler rooted at a fresh temp dir.
func gk_vibekit_u19_handler(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	h, err := New(dir)
	if err != nil {
		t.Fatalf("New(%q) error: %v", dir, err)
	}
	return h, dir
}

// gk_vibekit_u19_statusRecorder captures the HTTP status code without
// buffering the response body, so the maxCopySize download-boundary
// test never allocates the 100 MB served payload.
type gk_vibekit_u19_statusRecorder struct {
	hdr     http.Header
	code    int
	written bool
}

func (s *gk_vibekit_u19_statusRecorder) Header() http.Header {
	if s.hdr == nil {
		s.hdr = http.Header{}
	}
	return s.hdr
}

func (s *gk_vibekit_u19_statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.code = code
		s.written = true
	}
}

func (s *gk_vibekit_u19_statusRecorder) Write(p []byte) (int, error) {
	if !s.written {
		s.code = http.StatusOK
		s.written = true
	}
	return len(p), nil
}

// --- filehandler_actions.go ---

// 107: actionMkdir `strings.Count(clean, "/") < 2` (CONDITIONALS_BOUNDARY).
// At exactly two slashes the original `< 2` is false (mkdir proceeds);
// the `<= 2` mutant would forbid it.
func TestGk_vibekit_u19_MkdirBoundaryAtTwoSegments(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
	resolved := "/a/b" // count("/a/b") == 2
	err := actionMkdir(context.Background(), httptest.NewRecorder(), fileAction{}, resolved, h)
	if err != nil {
		t.Fatalf("actionMkdir(%q) = %v, want nil (a 2-segment path must be allowed to mkdir)", resolved, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "a", "b")); statErr != nil {
		t.Errorf("actionMkdir(%q): expected dir created at <root>/a/b, stat err: %v", resolved, statErr)
	}
}

// 112: actionMkdir `err != nil` after MkdirAll (CONDITIONALS_NEGATION).
// On the MkdirAll-error path the original returns the error; the `== nil`
// mutant would swallow it and report success.
func TestGk_vibekit_u19_MkdirPropagatesError(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
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

// 131: actionTouch `strings.Count(clean, "/") < 2` (CONDITIONALS_BOUNDARY).
// Same boundary as mkdir: at two slashes the original proceeds and creates
// the file; the `<= 2` mutant forbids.
func TestGk_vibekit_u19_TouchBoundaryAtTwoSegments(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
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

// 137: actionTouch `err != nil` after OpenFile (CONDITIONALS_NEGATION).
// On the OpenFile-error path the original returns the error; the `== nil`
// mutant falls through to f.Close() on a nil file (nil deref / swallow).
func TestGk_vibekit_u19_TouchPropagatesOpenError(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
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

// 163: actionDelete `segments < 2` (CONDITIONALS_BOUNDARY). At exactly two
// segments the original allows the delete; the `<= 2` mutant forbids it.
func TestGk_vibekit_u19_DeleteBoundaryAtTwoSegments(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
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

// 177: actionDelete `err != nil` after RemoveAll (CONDITIONALS_NEGATION).
// On the RemoveAll-error path the original returns the error; the `== nil`
// mutant swallows it. An escaping symlink component makes os.Root refuse
// the operation.
func TestGk_vibekit_u19_DeletePropagatesRemoveError(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
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

// 245: actionRename `err != nil` after Rename (CONDITIONALS_NEGATION).
// Renaming a missing source surfaces an error past every guard; the
// `== nil` mutant swallows it.
func TestGk_vibekit_u19_RenamePropagatesError(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
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

// 376: actionMove `err != nil` after Rename (CONDITIONALS_NEGATION). Same
// shape as rename: a missing source must produce a propagated error.
func TestGk_vibekit_u19_MovePropagatesError(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
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

// 301 + 326 + 332: streamCopy size/limit logic at exactly sizeCap.
//   - 301 `info.Size() > sizeCap`  (CONDITIONALS_BOUNDARY): size == cap must
//     NOT be oversize. The `>=` mutant returns errOversize.
//   - 326 `io.LimitReader(in, sizeCap+1)` (ARITHMETIC_BASE): the `-1` mutant
//     reads only sizeCap-1 bytes, truncating the copy.
//   - 332 `n > sizeCap` (CONDITIONALS_BOUNDARY): n == cap must succeed. The
//     `>=` mutant returns errOversize.
func TestGk_vibekit_u19_StreamCopyAtExactCap(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
	src := []byte("0123456789") // exactly sizeCap bytes
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

// 345: streamCopy `renameErr != nil` after the temp->dest rename
// (CONDITIONALS_NEGATION). Renaming the temp over an existing non-empty
// directory fails; the original returns the error, the `== nil` mutant
// reports success.
func TestGk_vibekit_u19_StreamCopyPropagatesRenameError(t *testing.T) {
	h, dir := gk_vibekit_u19_handler(t)
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

// --- filehandler_list.go ---

// 77: listEntries `name[0] == '.'` (CONDITIONALS_NEGATION). At root,
// dotfiles are filtered and visible files are kept. The `!=` mutant
// inverts both.
func TestGk_vibekit_u19_ListEntriesFiltersDotfilesAtRoot(t *testing.T) {
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

// --- filehandler_read.go ---

// 51 + 59 + 66: readFile size/limit logic at exactly maxFileSize.
//   - 51 `info.Size() > maxFileSize` (CONDITIONALS_BOUNDARY): size == max
//     must be readable (200). The `>=` mutant returns 413.
//   - 59 `io.LimitReader(f, maxFileSize+1)` (ARITHMETIC_BASE): the `-1`
//     mutant reads only maxFileSize-1 bytes, truncating the content.
//   - 66 `int64(len(data)) > maxFileSize` (CONDITIONALS_BOUNDARY): len == max
//     must be returned. The `>=` mutant returns 413.
func TestGk_vibekit_u19_ReadFileAtExactMaxSize(t *testing.T) {
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

// 123: handleDownload `info.Size() > maxCopySize` (CONDITIONALS_BOUNDARY).
// A file exactly at the cap must download (200); the `>=` mutant returns 413.
// A sparse file + discarding ResponseWriter keep this cheap.
func TestGk_vibekit_u19_DownloadAtExactMaxCopySize(t *testing.T) {
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
	sw := &gk_vibekit_u19_statusRecorder{}
	h.handleDownload(sw, req)
	if sw.code != http.StatusOK {
		t.Fatalf("download (size == maxCopySize) status = %d, want 200 (a file exactly at the cap is downloadable)", sw.code)
	}
}

// --- filehandler_upload.go ---

// 171: writeOneUpload `werr != nil` after atomicfile.WriteReader
// (CONDITIONALS_NEGATION). A dest whose parent dir is missing makes
// WriteReader fail; the original returns the error, the `== nil` mutant
// swallows it and reports success.
func TestGk_vibekit_u19_WriteOneUploadPropagatesError(t *testing.T) {
	req := multipartUpload(t, "ignored", map[string][]byte{"a.txt": []byte("hi")})
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	fhs := req.MultipartForm.File["files"]
	if len(fhs) == 0 {
		t.Fatal("no multipart file parsed")
	}
	dest := filepath.Join(t.TempDir(), "missing-dir", "x.txt") // parent does not exist
	n, err := writeOneUpload(context.Background(), dest, fhs[0])
	if err == nil {
		t.Fatalf("writeOneUpload(dest with missing parent) = (n=%d, nil), want non-nil error", n)
	}
}
