package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mustWrite writes content to work/rel, creating parents.
func mustWrite(t *testing.T, work, rel, content string) {
	t.Helper()
	p := filepath.Join(work, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOwnerOf_TracksLastWriter: ownership follows the most recent
// agent write across chats. The 2ms sleeps keep event timestamps
// (millisecond-granular) strictly ordered — cross-chat same-ms ties
// keep the incumbent by design.
func TestOwnerOf_TracksLastWriter(t *testing.T) {
	s, work := newTestStore(t)
	ctx := context.Background()

	mustWrite(t, work, "f.go", "v0")
	s.AdvanceTurn(ctx, "A", 0)
	if _, err := s.Snapshot(ctx, "A", "f.go", []byte("va"), 1); err != nil {
		t.Fatalf("snapshot A: %v", err)
	}
	mustWrite(t, work, "f.go", "va")

	if owner, ok := s.OwnerOf(ctx, "f.go"); !ok || owner != "A" {
		t.Fatalf("OwnerOf after A's write = (%q, %v), want (A, true)", owner, ok)
	}

	time.Sleep(2 * time.Millisecond)
	s.AdvanceTurn(ctx, "B", 0)
	if _, err := s.Snapshot(ctx, "B", "f.go", []byte("vb"), 1); err != nil {
		t.Fatalf("snapshot B: %v", err)
	}
	mustWrite(t, work, "f.go", "vb")

	if owner, ok := s.OwnerOf(ctx, "f.go"); !ok || owner != "B" {
		t.Errorf("OwnerOf after B's write = (%q, %v), want (B, true)", owner, ok)
	}
}

// TestOwnerOf_UntrackedPath: a path no chat ever snapshotted has no
// owner — the capture path must treat it as none of checkpointing's
// business. Also exercises warmIndex against a configDir with no
// chats directory at all (fresh volume).
func TestOwnerOf_UntrackedPath(t *testing.T) {
	s, _ := newTestStore(t)
	if owner, ok := s.OwnerOf(context.Background(), "never-written.go"); ok {
		t.Errorf("OwnerOf(untracked) = (%q, true), want ok=false", owner)
	}
}

// TestOwnerOf_WarmsFromDisk: a brand-new Store over an existing
// configDir (process restart) answers ownership without any prior
// manager access — the first OwnerOf call replays every chat log on
// disk into the shared index.
func TestOwnerOf_WarmsFromDisk(t *testing.T) {
	cfg := t.TempDir()
	work := t.TempDir()
	ctx := context.Background()

	{
		s := NewStore(cfg, work, nil)
		mustWrite(t, work, "sub/tracked.go", "v0")
		s.AdvanceTurn(ctx, "A", 0)
		if _, err := s.Snapshot(ctx, "A", "sub/tracked.go", []byte("v1"), 1); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		mustWrite(t, work, "sub/tracked.go", "v1")
	}

	fresh := NewStore(cfg, work, nil)
	if owner, ok := fresh.OwnerOf(ctx, "sub/tracked.go"); !ok || owner != "A" {
		t.Errorf("cold OwnerOf = (%q, %v), want (A, true)", owner, ok)
	}
	if owner, ok := fresh.OwnerOf(ctx, "other.go"); ok {
		t.Errorf("cold OwnerOf(other) = (%q, true), want ok=false", owner)
	}
}

// TestEditorSaveCapture_NextAgentWriteSeesNoConflict: the captured
// save updates the cross-chat index, so ANOTHER chat's next write to
// the same file sees the manual content as the expected disk state —
// no spurious drift conflict for a save the user made on purpose.
func TestEditorSaveCapture_NextAgentWriteSeesNoConflict(t *testing.T) {
	cfg := t.TempDir()
	work := t.TempDir()
	ctx := context.Background()

	var conflicts int
	s := NewStore(cfg, work, func(string, *ConflictPayload) { conflicts++ })

	// Chat A owns the file.
	s.AdvanceTurn(ctx, "A", 0)
	if _, err := s.Snapshot(ctx, "A", "f.go", []byte("agent v1"), 1); err != nil {
		t.Fatalf("agent snapshot: %v", err)
	}
	mustWrite(t, work, "f.go", "agent v1")

	// Manual save captured into A's timeline.
	time.Sleep(2 * time.Millisecond)
	if _, err := s.Snapshot(ctx, "A", "f.go", []byte("manual v2"), 1); err != nil {
		t.Fatalf("capture snapshot: %v", err)
	}
	mustWrite(t, work, "f.go", "manual v2")

	// Chat B writes next: disk ("manual v2") matches the index's
	// expected SHA (the capture's afterSHA) → no conflict.
	time.Sleep(2 * time.Millisecond)
	s.AdvanceTurn(ctx, "B", 0)
	if _, err := s.Snapshot(ctx, "B", "f.go", []byte("agent v3"), 1); err != nil {
		t.Fatalf("B snapshot: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("conflicts = %d, want 0 (capture should have updated the index)", conflicts)
	}

	// Control: WITHOUT a capture the same manual edit does conflict —
	// proving the assertion above is load-bearing, not vacuous.
	mustWrite(t, work, "f.go", "uncaptured manual edit")
	time.Sleep(2 * time.Millisecond)
	s.AdvanceTurn(ctx, "A", 0)
	if _, err := s.Snapshot(ctx, "A", "f.go", []byte("agent v4"), 2); err != nil {
		t.Fatalf("A snapshot: %v", err)
	}
	if conflicts != 1 {
		t.Errorf("control conflicts = %d, want 1 (uncaptured drift must still be detected)", conflicts)
	}
}
