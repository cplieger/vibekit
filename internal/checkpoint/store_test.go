package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/slogx/capture"
)

// newTestStore wires a Store over a temp configDir + workDir
// without starting the background GC goroutine — unit tests
// exercise method semantics, not the ticker.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	cfg := t.TempDir()
	work := t.TempDir()
	return NewStore(cfg, work, nil), work
}

func TestStore_GetIsLazyAndReused(t *testing.T) {
	s, _ := newTestStore(t)
	m1 := s.get("c1")
	m2 := s.get("c1")
	if m1 != m2 {
		t.Errorf("Store.get returned two distinct Managers for the same chat")
	}
	if s.get("c2") == m1 {
		t.Errorf("Store.get returned shared Manager for distinct chats")
	}
}

func TestStore_AdvanceTurnThenSnapshot(t *testing.T) {
	ctx := context.Background()
	s, work := newTestStore(t)
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}

	s.AdvanceTurn(ctx, "c", 0)
	tag, err := s.Snapshot(ctx, "c", "f", nil, 1)
	if err != nil {
		t.Fatalf("Store.Snapshot err = %v, want nil", err)
	}
	if tag != "1" {
		t.Errorf("Store.Snapshot tag = %q, want 1 (first snapshot of turn 1)", tag)
	}
}

func TestStore_RestoreAndRestorePreview(t *testing.T) {
	ctx := context.Background()
	s, work := newTestStore(t)
	p := filepath.Join(work, "f.txt")
	if err := os.WriteFile(p, []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}

	s.AdvanceTurn(ctx, "c", 0)
	tag, err := s.Snapshot(ctx, "c", "f.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p, []byte("v1"), 0o600)

	files, err := s.RestorePreview(context.Background(), "c", tag)
	if err != nil {
		t.Fatalf("RestorePreview err = %v, want nil", err)
	}
	if len(files) != 1 || files[0] != "f.txt" {
		t.Errorf("RestorePreview = %v, want [f.txt]", files)
	}

	mc, err := s.Restore(ctx, "c", tag)
	if err != nil {
		t.Fatalf("Restore err = %v, want nil", err)
	}
	if mc != 1 {
		t.Errorf("Restore watermark = %d, want 1", mc)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "v0" {
		t.Errorf("file after Store.Restore = %q, want v0", got)
	}
}

func TestStore_OldestTag(t *testing.T) {
	ctx := context.Background()
	s, work := newTestStore(t)
	p := filepath.Join(work, "f.txt")
	if err := os.WriteFile(p, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := s.OldestTag(context.Background(), "c"); got != "" {
		t.Errorf("OldestTag(empty chat) = %q, want empty", got)
	}

	s.AdvanceTurn(ctx, "c", 0)
	tag, err := s.Snapshot(ctx, "c", "f.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p, []byte("b"), 0o600)

	if got := s.OldestTag(context.Background(), "c"); got != tag {
		t.Errorf("OldestTag = %q, want %q", got, tag)
	}
}

func TestStore_DiffDelegates(t *testing.T) {
	ctx := context.Background()
	s, work := newTestStore(t)
	p := filepath.Join(work, "f.txt")
	if err := os.WriteFile(p, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.AdvanceTurn(ctx, "c", 0)
	if _, err := s.Snapshot(ctx, "c", "f.txt", nil, 1); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p, []byte("a\nb\n"), 0o600)
	s.AdvanceTurn(ctx, "c", 1)
	if _, err := s.Snapshot(ctx, "c", "f.txt", nil, 2); err != nil {
		t.Fatal(err)
	}

	diffs, err := s.Diff(ctx, "c", "1", "2")
	if err != nil {
		t.Fatalf("Store.Diff err = %v, want nil", err)
	}
	if len(diffs) != 1 || diffs[0].Path != "f.txt" {
		t.Errorf("Store.Diff = %+v, want single entry for f.txt", diffs)
	}
}

func TestStore_ConflictsAndReadBlob(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	s := NewStore(cfg, work, nil)
	p := filepath.Join(work, "shared.go")
	if err := os.WriteFile(p, []byte("baseline"), 0o600); err != nil {
		t.Fatal(err)
	}

	aNew := []byte("A wrote this")
	s.AdvanceTurn(ctx, "A", 0)
	if _, err := s.Snapshot(ctx, "A", "shared.go", aNew, 1); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p, aNew, 0o600)
	_ = os.WriteFile(p, []byte("external edit"), 0o600)

	s.AdvanceTurn(ctx, "B", 0)
	if _, err := s.Snapshot(ctx, "B", "shared.go", nil, 1); err != nil {
		t.Fatal(err)
	}

	conflicts, err := s.Conflicts(context.Background(), "B")
	if err != nil {
		t.Fatalf("Store.Conflicts err = %v, want nil", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("Store.Conflicts = %d entries, want 1", len(conflicts))
	}

	// B's beforeSHA on the conflict entry is what B actually read
	// from disk ("external edit"); B owns that blob in its log.
	data, err := s.ReadBlob(context.Background(), "B", conflicts[0].ActualSHA)
	if err != nil {
		t.Fatalf("Store.ReadBlob own sha err = %v, want nil", err)
	}
	if string(data) != "external edit" {
		t.Errorf("Store.ReadBlob = %q, want 'external edit'", data)
	}

	if _, err := s.ReadBlob(context.Background(), "C", conflicts[0].ActualSHA); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("Store.ReadBlob(C,other-sha) err = %v, want ErrBlobNotFound", err)
	}
}

func TestStore_CleanupEvictsManagerFromCache(t *testing.T) {
	ctx := context.Background()
	s, work := newTestStore(t)
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("v"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.AdvanceTurn(ctx, "c", 0)
	if _, err := s.Snapshot(ctx, "c", "f", nil, 1); err != nil {
		t.Fatal(err)
	}

	s.Cleanup(ctx, "c")

	m := s.get("c")
	if tags := m.tags(); len(tags) != 0 {
		t.Errorf("Tags after Cleanup = %v, want empty (fresh Manager)", tags)
	}
}

func TestStore_CleanupUncachedChat(t *testing.T) {
	// Cleanup on a chat never touched must still wipe the on-disk
	// event log and forget index entries. Exercises the
	// "m, ok := managers[chatID]; !ok" fallthrough to wipe().
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()

	log := newEventLog(cfg, "ghost")
	if err := log.Append(context.Background(), &event{Kind: kindSnapshot, Tag: "1", Path: "f"}); err != nil {
		t.Fatal(err)
	}

	s := NewStore(cfg, work, nil)
	s.Cleanup(ctx, "ghost")

	chatDir := filepath.Join(chatsRoot(cfg), "ghost")
	if _, err := os.Stat(chatDir); !os.IsNotExist(err) {
		t.Errorf("ghost chat dir still exists after Cleanup, err = %v", err)
	}
}

func TestStore_StartBackgroundTasksIsIdempotent(t *testing.T) {
	// Two Start calls in a row must not fork a second gcLoop or
	// add a second wait to gcDone. Stop should return promptly
	// once we wait for both.
	cfg := t.TempDir()
	work := t.TempDir()
	s := NewStore(cfg, work, nil)
	s.StartBackgroundTasks(context.Background())
	s.StartBackgroundTasks(context.Background()) // second call should be a no-op
	s.Stop()
}

// TestStoreAdvanceTurn_NoWarnOnSuccess pins that a successful delegated
// AdvanceTurn is silent: the Store-level failure warn must fire only
// when the underlying Manager call errors.
func TestStoreAdvanceTurn_NoWarnOnSuccess(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	s := NewStore(cfg, work, nil)

	has := capture.Default(t)
	s.AdvanceTurn(ctx, "chat-a", 1)
	if has.Contains("AdvanceTurn failed") {
		t.Errorf("Store.AdvanceTurn logged 'AdvanceTurn failed' on success; that warn must fire only when the delegated call errors")
	}
}

// TestWipe_NoFailureWarnOnSuccess pins that wiping an existing log is
// silent: the wipe-failure warn must fire only when Wipe errors.
func TestWipe_NoFailureWarnOnSuccess(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	log := newEventLog(cfg, "wp")
	deps := &managerDeps{blobs: blobs, index: newCrossChatIndex()}
	m := newManager("wp", work, log, deps)
	if err := m.AdvanceTurn(ctx, 1); err != nil { // create the log on disk
		t.Fatal(err)
	}

	has := capture.Default(t)
	wipe(cfg, "wp")
	if has.Contains("checkpoint: wipe failed") {
		t.Errorf("wipe() logged 'wipe failed' on a successful wipe; that warn must fire only on wipe error")
	}
}
