package filebrowse

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uploadsHandler grants one mount CLAIMING the parent of defaultUploadDir,
// backed by a throwaway directory, so the default upload target resolves
// without the test machine needing a real /workspace. Returns the backing
// directory so a test can assert where the bytes actually landed.
func uploadsHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	backingDir := t.TempDir()
	backing, err := os.OpenRoot(backingDir)
	if err != nil {
		t.Fatal(err)
	}
	claim := filepath.Dir(filepath.Clean("/" + defaultUploadDir))
	return &Handler{mounts: []mount{{
		root: backing,
		dir:  claim,
		name: strings.TrimPrefix(claim, "/"),
	}}}, backingDir
}

// uploadOrdered is multipartUpload with a guaranteed part ORDER, which the
// map-keyed helper cannot give. Partial-batch behaviour is only observable
// when the test controls which file fails second.
func uploadOrdered(t *testing.T, targetDir string, names []string, contents [][]byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("dir", targetDir); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		fw, err := w.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(contents[i]); err != nil {
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

func serveUpload(t *testing.T, h *Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// uploadBody decodes an upload response body's error + uploaded keys.
func uploadBody(t *testing.T, rec *httptest.ResponseRecorder) (errMsg string, uploaded []string) {
	t.Helper()
	var body struct {
		Error    string   `json:"error"`
		Uploaded []string `json:"uploaded"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body.Error, body.Uploaded
}

// --- the default target directory (D3a) ---

// An upload with no "dir" lands in the uploads folder, and that folder does
// not have to exist first: handleUpload's MkdirAll creates it inside the
// mount's own os.Root, which is why nothing else in the app pre-creates it.
func TestHandleUpload_DefaultDirIsUploadsCreatedOnDemand(t *testing.T) {
	h, backing := uploadsHandler(t)
	rel := strings.TrimPrefix(filepath.Clean("/"+defaultUploadDir), filepath.Dir(filepath.Clean("/"+defaultUploadDir)))
	uploadsDir := filepath.Join(backing, filepath.Clean(rel))

	if _, err := os.Stat(uploadsDir); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s should not exist yet, stat err = %v", uploadsDir, err)
	}

	rec := serveUpload(t, h, uploadOrdered(t, "", []string{"note.txt"}, [][]byte{[]byte("hi")}))
	if rec.Code != http.StatusOK {
		// A 403 here most likely means the claimed mount could not be
		// resolved on this machine (a symlinked /workspace), not that the
		// default changed. Say so rather than leaving a bare status mismatch.
		t.Fatalf("status = %d, want 200; body %q (a 403 points at path resolution for %q, not at the default)",
			rec.Code, rec.Body.String(), defaultUploadDir)
	}
	got, err := os.ReadFile(filepath.Join(uploadsDir, "note.txt"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(got) != "hi" {
		t.Errorf("content = %q, want %q", got, "hi")
	}
}

// An explicit dir still wins: the file browser and the upload picker upload
// where the user is looking, and only the composer takes the default.
func TestHandleUpload_ExplicitDirStillWins(t *testing.T) {
	h, dir, prefix := testDir(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := serveUpload(t, h,
		uploadOrdered(t, prefix+"/sub", []string{"x.txt"}, [][]byte{[]byte("x")}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "x.txt")); err != nil {
		t.Errorf("explicit target not honored: %v", err)
	}
}

// A target of "." is refused, and must stay refused.
//
// This is a regression guard on a live bug the retarget fixed: the chat view
// uploaded with dir="." for both drop and paste, the server cleaned that to
// "/", and "/" is inside no granted mount (filepath.Rel from any grant to "/"
// escapes), so every chat-view drop and every pasted screenshot answered 403.
// No test covered the "." shape, which is how it shipped.
//
// The fix is that no caller sends "." any more. Do NOT "fix" it by teaching
// resolvePath to read "." as the workspace: the mount list is an allow-list of
// several roots, so a relative path would become ambiguous about which one it
// means.
func TestHandleUpload_DotDirIsRefused(t *testing.T) {
	for _, dir := range []string{".", "/", "./", "/."} {
		t.Run(dir, func(t *testing.T) {
			h, _, _ := testDir(t)
			rec := serveUpload(t, h,
				uploadOrdered(t, dir, []string{"x.txt"}, [][]byte{[]byte("x")}))
			if rec.Code != http.StatusForbidden {
				t.Errorf("dir %q: status = %d, want 403", dir, rec.Code)
			}
		})
	}
}

// --- the partial batch (D98) ---

// A batch that fails partway is NOT rolled back, and the response says so: the
// names that landed ride the error body so the client can report "3 of 5
// uploaded, then X failed" and still attach the three.
func TestHandleUpload_PartialBatchReportsWhatLanded(t *testing.T) {
	h, dir, prefix := testDir(t)
	// "first.txt" is written, then ".." trips the invalid-filename guard.
	rec := serveUpload(t, h, uploadOrdered(t, prefix,
		[]string{"first.txt", "..", "never.txt"},
		[][]byte{[]byte("one"), []byte("two"), []byte("three")}))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %q", rec.Code, rec.Body.String())
	}
	errMsg, uploaded := uploadBody(t, rec)
	if errMsg == "" {
		t.Error("error message is empty; the client renders it verbatim")
	}
	if len(uploaded) != 1 || uploaded[0] != "first.txt" {
		t.Errorf("uploaded = %v, want [first.txt]", uploaded)
	}
	// The file that landed stays on disk. Rollback would delete it, and it
	// cannot be done correctly anyway: an upload may overwrite, so undoing one
	// needs a backup of every destination.
	if _, err := os.Stat(filepath.Join(dir, "first.txt")); err != nil {
		t.Errorf("first.txt should remain on disk: %v", err)
	}
	// Nothing after the failure is attempted.
	if _, err := os.Stat(filepath.Join(dir, "never.txt")); !os.IsNotExist(err) {
		t.Errorf("never.txt should not exist, stat err = %v", err)
	}
}

// The uploaded key is present on a failure that wrote nothing, encoded as an
// empty array rather than null, so the client needs no null branch.
func TestHandleUpload_ErrorBodyCarriesEmptyUploadedArray(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := serveUpload(t, h,
		uploadOrdered(t, prefix, []string{".."}, [][]byte{[]byte("x")}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"uploaded":[]`)) {
		t.Errorf("body = %q, want an empty uploaded array", rec.Body.String())
	}
	_, uploaded := uploadBody(t, rec)
	if uploaded == nil || len(uploaded) != 0 {
		t.Errorf("uploaded = %v, want an empty non-nil slice", uploaded)
	}
}

// A whole-batch success keeps its existing shape: the client's fallback to its
// own filenames only fires when the array is missing.
func TestHandleUpload_SuccessBodyListsEveryName(t *testing.T) {
	h, _, prefix := testDir(t)
	rec := serveUpload(t, h, uploadOrdered(t, prefix,
		[]string{"a.txt", "b.txt"}, [][]byte{[]byte("a"), []byte("b")}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", rec.Code, rec.Body.String())
	}
	_, uploaded := uploadBody(t, rec)
	if len(uploaded) != 2 || uploaded[0] != "a.txt" || uploaded[1] != "b.txt" {
		t.Errorf("uploaded = %v, want [a.txt b.txt]", uploaded)
	}
}
