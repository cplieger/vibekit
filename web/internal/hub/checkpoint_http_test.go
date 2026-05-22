// HTTP surface tests for /api/checkpoints/* endpoints.
//
// The checkpoint package has its own deep-dive unit tests; these
// are focused on the Hub wiring: route dispatch, status codes,
// response shape, and the chat-scoped isolation guarantee on the
// blob endpoint.

package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"vibekit/internal/checkpoint"
)

// newCheckpointHub wires up a Hub with a real checkpoint.Store
// backed by temp dirs. Unlike newTestHub (which skips checkpoints),
// this helper exists because the HTTP handlers short-circuit with
// 404 when h.checkpoints is nil.
func newCheckpointHub(t *testing.T) (*Hub, *checkpoint.Store, string) {
	t.Helper()
	h, _, _ := newTestHub()
	cfgDir := t.TempDir()
	workDir := t.TempDir()
	s := checkpoint.NewStore(cfgDir, workDir, nil)
	h.checkpoints = &checkpointAdapter{store: s}
	t.Cleanup(func() { s.Stop() })
	return h, s, workDir
}

// writeWork creates a file at workDir/relPath with the given
// content. The checkpoint package reads disk to compute beforeSHA,
// so tests that want non-empty before-states must write to disk
// BEFORE calling Snapshot.
func writeWork(t *testing.T, workDir, relPath string, content []byte) {
	t.Helper()
	abs := filepath.Join(workDir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCheckpointHTTP_DiffRoute exercises the happy path for diff.
func TestCheckpointHTTP_DiffRoute(t *testing.T) {
	h, s, workDir := newCheckpointHub(t)
	// Seed two snapshots so we have tags to diff between. The
	// first snapshot captures a brand-new file (no pre-content),
	// the second captures v1-on-disk about to be replaced by v2.
	s.AdvanceTurn(context.Background(), "c1", 0)
	if _, err := s.Snapshot(context.Background(), "c1", "f.go", []byte("v1"), 1); err != nil {
		t.Fatal(err)
	}
	writeWork(t, workDir, "f.go", []byte("v1"))
	s.AdvanceTurn(context.Background(), "c1", 1)
	if _, err := s.Snapshot(context.Background(), "c1", "f.go", []byte("v2"), 2); err != nil {
		t.Fatal(err)
	}
	// Hit the endpoint.
	req := httptest.NewRequest(http.MethodGet, "/api/checkpoints/c1/diff?from=1&to=2", nil)
	rec := httptest.NewRecorder()
	h.handleCheckpoint(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var got struct {
		Files []checkpoint.FileChange `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "f.go" {
		t.Errorf("diff result = %+v, want one f.go entry", got.Files)
	}
}

func TestCheckpointHTTP_DiffRequiresFromAndTo(t *testing.T) {
	h, _, _ := newCheckpointHub(t)
	cases := []struct {
		name     string
		method   string
		path     string
		wantCode int
	}{
		{"DiffRequiresFromAndTo", http.MethodGet, "/api/checkpoints/c1/diff?from=1", http.StatusBadRequest},
		{"BlobRouteRejectsBadSHA", http.MethodGet, "/api/checkpoints/c1/blob/not-a-sha", http.StatusNotFound},
		{"BlobRouteRequiresSha", http.MethodGet, "/api/checkpoints/c1/blob/", http.StatusBadRequest},
		{"UnknownSubResource", http.MethodGet, "/api/checkpoints/c1/bogus", http.StatusNotFound},
		{"RejectsInvalidChatID", http.MethodGet, "/api/checkpoints/../evil/diff?from=1&to=2", http.StatusBadRequest},
		{"MethodNotAllowed", http.MethodPost, "/api/checkpoints/c1/diff", http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			h.handleCheckpoint(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d; body=%q", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

// TestCheckpointHTTP_RestorePreviewRoute confirms preview returns
// the list of files a restore would touch.
func TestCheckpointHTTP_RestorePreviewRoute(t *testing.T) {
	h, s, _ := newCheckpointHub(t)
	s.AdvanceTurn(context.Background(), "c1", 0)
	s.Snapshot(context.Background(), "c1", "a.go", []byte("v"), 1)
	s.Snapshot(context.Background(), "c1", "b.go", []byte("v"), 1)
	// Tag "1" corresponds to the first snapshot at turn 1.
	req := httptest.NewRequest(http.MethodGet, "/api/checkpoints/c1/restore-preview?tag=1", nil)
	rec := httptest.NewRecorder()
	h.handleCheckpoint(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var got struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Both files were touched at or after tag 1.
	if len(got.Files) != 2 {
		t.Errorf("files = %v, want 2 (a.go + b.go)", got.Files)
	}
}

// TestCheckpointHTTP_ConflictsRouteEmpty confirms a chat with no
// drift returns a well-formed empty list (not null).
func TestCheckpointHTTP_ConflictsRouteEmpty(t *testing.T) {
	h, _, _ := newCheckpointHub(t)
	req := httptest.NewRequest(http.MethodGet, "/api/checkpoints/c1/conflicts", nil)
	rec := httptest.NewRecorder()
	h.handleCheckpoint(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Conflicts []any `json:"conflicts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Conflicts == nil {
		t.Error("conflicts field is null; client iteration needs []")
	}
}

// TestCheckpointHTTP_BlobRouteScoping checks the key invariant:
// blob reads are chat-scoped. Chat A owns blob X; chat B asks for
// the same SHA and gets 404 even though the blob physically exists
// on disk. This prevents lateral probing.
func TestCheckpointHTTP_BlobRouteScoping(t *testing.T) {
	h, s, _ := newCheckpointHub(t)
	ctx := context.Background()
	s.AdvanceTurn(context.Background(), "A", 0)
	tag, err := s.Snapshot(ctx, "A", "secret.txt", []byte("A's secret"), 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = tag

	// Discover A's afterSHA by reading the conflicts replay (it
	// carries the same SHAs as snapshots do). Simpler: just look
	// at the event log via the Store-internal API through the
	// conflict endpoint; that's empty here. Workaround: request
	// the blob via A's chat to observe the 200 path, then request
	// the same URL under chat B and assert 404.
	//
	// To get the SHA without poking package internals, hash the
	// content ourselves (the store uses SHA-256 of the raw bytes).
	sha := shaOfContent([]byte("A's secret"))

	// A should be allowed to read its own blob.
	reqA := httptest.NewRequest(http.MethodGet, "/api/checkpoints/A/blob/"+sha, nil)
	recA := httptest.NewRecorder()
	h.handleCheckpoint(recA, reqA)
	if recA.Code != http.StatusOK {
		t.Fatalf("chat A own-blob read = %d, want 200; body=%q", recA.Code, recA.Body.String())
	}
	if recA.Body.String() != "A's secret" {
		t.Errorf("chat A body = %q, want %q", recA.Body.String(), "A's secret")
	}

	// B (no snapshots yet) must NOT be able to read A's blob.
	reqB := httptest.NewRequest(http.MethodGet, "/api/checkpoints/B/blob/"+sha, nil)
	recB := httptest.NewRecorder()
	h.handleCheckpoint(recB, reqB)
	if recB.Code != http.StatusNotFound {
		t.Errorf("chat B cross-chat probe = %d, want 404", recB.Code)
	}
}

// shaOfContent mirrors the checkpoint.blobStore's hashOf helper.
// Kept as a test-local copy so the test doesn't reach into the
// package's unexported surface.
func shaOfContent(b []byte) string {
	return hashOfBytesSHA256(b)
}

// hashOfBytesSHA256 is the hex SHA-256 of b; mirrors the blob
// store's addressing. Kept test-local to avoid widening the
// checkpoint package's public surface for a test concern.
func hashOfBytesSHA256(b []byte) string {
	sum := sha256Sum(b)
	return hexEncode(sum[:])
}

// Thin wrappers so the test doesn't pull crypto/sha256 and
// encoding/hex into every test file that includes it via shared
// helpers.
func sha256Sum(b []byte) [32]byte {
	return sha256.Sum256(b)
}

func hexEncode(b []byte) string {
	return hex.EncodeToString(b)
}
