package hub

// Tests for CaptureEditorSave routing: workspace boundary, owner
// resolution, message-count watermark, and the nil-checkpoints and
// snapshot-failure no-crash paths. The checkpoint mechanics themselves
// (pre-write blob capture, index update, undo) are covered in
// internal/checkpoint's ownerof_test.go.

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	checkpoint "github.com/cplieger/vibekit/internal/checkpoint/types"
)

// captureSpy fakes OwnerOf/Snapshot. The embedded interface covers the
// methods this path never touches (nil — a call would panic the test).
type captureSpy struct {
	api.CheckpointService
	mu        sync.Mutex
	owner     api.ChatID
	hasOwner  bool
	snapErr   error
	snapshots []capturedSnap
}

type capturedSnap struct {
	chatID       api.ChatID
	relPath      string
	content      string
	messageCount int
}

func (s *captureSpy) OwnerOf(_ context.Context, _ string) (api.ChatID, bool) {
	return s.owner, s.hasOwner
}

func (s *captureSpy) Snapshot(_ context.Context, chatID api.ChatID, relPath string, newContent []byte, messageCount int) (checkpoint.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapErr != nil {
		return "", s.snapErr
	}
	s.snapshots = append(s.snapshots, capturedSnap{chatID, relPath, string(newContent), messageCount})
	return checkpoint.Tag("1"), nil
}

func (s *captureSpy) taken() []capturedSnap {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedSnap(nil), s.snapshots...)
}

// newCaptureHub builds a hub with a captureSpy checkpoint service and
// returns both plus the hub's workDir.
func newCaptureHub(t *testing.T, spy *captureSpy) (*Hub, *fakeChatStore, string) {
	t.Helper()
	work := t.TempDir()
	cs := newFakeChatStore()
	h := New(work, func() api.ACPBridge { return newFakeBridge() }, cs)
	cs.Bus = h
	h.checkpoints = spy
	return h, cs, work
}

func TestCaptureEditorSave_RecordsIntoOwnerChat(t *testing.T) {
	spy := &captureSpy{owner: "c1", hasOwner: true}
	h, cs, work := newCaptureHub(t, spy)
	ctx := context.Background()

	// Three persisted messages → watermark 3.
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Messages = []api.Message{{ID: "m1", Role: api.RoleUser}, {ID: "m2", Role: api.RoleAssistant}, {ID: "m3", Role: api.RoleUser}}
		return true
	})

	h.CaptureEditorSave(ctx, filepath.Join(work, "sub", "f.go"), []byte("manual"))

	got := spy.taken()
	if len(got) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(got))
	}
	want := capturedSnap{chatID: "c1", relPath: "sub/f.go", content: "manual", messageCount: 3}
	if got[0] != want {
		t.Errorf("snapshot = %+v, want %+v", got[0], want)
	}
}

func TestCaptureEditorSave_SkipsUnownedAndOutsidePaths(t *testing.T) {
	cases := []struct {
		name     string
		hasOwner bool
		abs      func(work string) string
	}{
		{"outside work tree", true, func(string) string { return "/config/home/settings.json" }},
		{"work tree root itself", true, func(work string) string { return work }},
		{"no owning chat", false, func(work string) string { return filepath.Join(work, "f.go") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &captureSpy{owner: "c1", hasOwner: tc.hasOwner}
			h, _, work := newCaptureHub(t, spy)
			h.CaptureEditorSave(context.Background(), tc.abs(work), []byte("x"))
			if n := len(spy.taken()); n != 0 {
				t.Errorf("snapshots = %d, want 0", n)
			}
		})
	}
}

// The boundary guard rejects the work-tree root ("."), parent escapes
// ("..", "../x"), and RelPath errors before any owner lookup — the
// spy's owner answer is irrelevant for those shapes by construction.

func TestCaptureEditorSave_NilCheckpointsAndSnapshotFailure(t *testing.T) {
	// Nil checkpoints: must be a silent no-op.
	h, _, work := newCaptureHub(t, &captureSpy{})
	h.checkpoints = nil
	h.CaptureEditorSave(context.Background(), filepath.Join(work, "f.go"), []byte("x")) // must not panic

	// Snapshot failure: logged, never surfaced, no snapshot recorded.
	spy := &captureSpy{owner: "c1", hasOwner: true, snapErr: errors.New("disk full")}
	h2, _, work2 := newCaptureHub(t, spy)
	h2.CaptureEditorSave(context.Background(), filepath.Join(work2, "f.go"), []byte("x"))
	if n := len(spy.taken()); n != 0 {
		t.Errorf("snapshots = %d, want 0 after error", n)
	}
}
