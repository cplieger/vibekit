package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// newTestManager builds a Manager over a temp config + workspace.
// Returns the manager and the workspace root (for writing files
// directly to simulate agent edits or user edits).
func newTestManager(t *testing.T, id string) (*Manager, string) {
	t.Helper()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	log := newEventLog(cfg, id)
	deps := &managerDeps{blobs: blobs, index: newCrossChatIndex()}
	return newManager(id, work, log, deps), work
}

func TestAdvanceTurnSnapshotTagSequencing(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")

	// Before any AdvanceTurn, the initial tag is 0.0 (not "0" —
	// that slot is reserved for an explicit turn-start event if
	// one ever comes).
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}
	tag, err := m.Snapshot(ctx, "a.txt", nil, 1)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if tag != "0.0" {
		t.Errorf("first snapshot tag = %q, want 0.0", tag)
	}

	// Advance to turn 1. Next snapshot should be "1".
	if err := m.AdvanceTurn(ctx, 2); err != nil {
		t.Fatal(err)
	}
	tag2, err := m.Snapshot(ctx, "a.txt", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if tag2 != "1" {
		t.Errorf("turn 1 first snapshot = %q, want 1", tag2)
	}

	// Second snapshot in the same turn is "1.1".
	tag3, err := m.Snapshot(ctx, "a.txt", nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	if tag3 != "1.1" {
		t.Errorf("turn 1 second snapshot = %q, want 1.1", tag3)
	}

	// Another turn, then one more snapshot: "2".
	if err := m.AdvanceTurn(ctx, 5); err != nil {
		t.Fatal(err)
	}
	tag4, _ := m.Snapshot(ctx, "a.txt", nil, 6)
	if tag4 != "2" {
		t.Errorf("turn 2 first snapshot = %q, want 2", tag4)
	}
}

func TestOldestTagAndList(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	_ = os.WriteFile(filepath.Join(work, "f"), []byte("a"), 0o600)
	if got := m.OldestTag(context.Background()); got != "" {
		t.Errorf("empty chat OldestTag = %q, want empty", got)
	}
	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "f", nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "f", nil, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "f", nil, 3); err != nil {
		t.Fatal(err)
	}
	tags := m.tags()
	want := []string{"1", "2", "2.1"}
	if len(tags) != len(want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
	for i := range tags {
		if tags[i] != want[i] {
			t.Errorf("tags[%d] = %q, want %q", i, tags[i], want[i])
		}
	}
	if got := m.OldestTag(context.Background()); got != "1" {
		t.Errorf("OldestTag = %q, want 1", got)
	}
}

func TestDiff(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	p := filepath.Join(work, "hello.txt")
	_ = os.WriteFile(p, []byte("line 1\nline 2\n"), 0o600)
	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "hello.txt", nil, 1); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p, []byte("line 1\nline 2\nline 3\n"), 0o600)
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "hello.txt", nil, 2); err != nil {
		t.Fatal(err)
	}

	diffs, err := m.Diff(ctx, Tag("1"), Tag("2"))
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v, want 1 entry", diffs)
	}
	d := diffs[0]
	if d.Path != "hello.txt" || d.Status != FileModified || d.LinesAdded != 1 {
		t.Errorf("diff[0] = %+v, want hello.txt M +1", d)
	}
}

func TestResumeAcrossManagers(t *testing.T) {
	// Simulate container restart: a fresh Manager pointed at the
	// same on-disk event log must resume with the right tag
	// sequence. No in-memory state survives the restart, so every
	// method must replay the log on demand.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	log := newEventLog(cfg, "chat")
	idx := newCrossChatIndex()
	m1 := newManager("chat", work, log, &managerDeps{blobs: blobs, index: idx})
	_ = os.WriteFile(filepath.Join(work, "f"), []byte("a"), 0o600)
	if err := m1.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if tag, _ := m1.Snapshot(ctx, "f", nil, 1); tag != "1" {
		t.Fatalf("m1 first tag = %q, want 1", tag)
	}
	if err := m1.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if tag, _ := m1.Snapshot(ctx, "f", nil, 2); tag != "2" {
		t.Fatalf("m1 second tag = %q, want 2", tag)
	}

	// Second Manager instance on same disk.
	log2 := newEventLog(cfg, "chat")
	m2 := newManager("chat", work, log2, &managerDeps{blobs: blobs, index: newCrossChatIndex()})
	if err := m2.AdvanceTurn(ctx, 3); err != nil {
		t.Fatal(err)
	}
	tag, _ := m2.Snapshot(ctx, "f", nil, 4)
	if tag != "3" {
		t.Errorf("resumed tag = %q, want 3", tag)
	}
	tags := m2.tags()
	if len(tags) != 3 || tags[0] != "1" || tags[2] != "3" {
		t.Errorf("Tags after resume = %v, want [1, 2, 3]", tags)
	}
}

func TestCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	log := newEventLog(cfg, "chat")
	m := newManager("chat", work, log, &managerDeps{blobs: blobs, index: newCrossChatIndex()})
	_ = os.WriteFile(filepath.Join(work, "f"), []byte("a"), 0o600)
	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "f", nil, 1); err != nil {
		t.Fatal(err)
	}
	m.Cleanup(ctx)
	chatDir := chatsRoot(cfg)
	chatDir = filepath.Join(chatDir, "chat")
	if _, err := os.Stat(chatDir); !os.IsNotExist(err) {
		t.Errorf("event log dir should be gone, err = %v", err)
	}
	// Blobs should survive Cleanup — they may be referenced by
	// other chats or by historical tags we'd want to restore after
	// a Cleanup-then-redeploy scenario.
	blobsDir := blobsRoot(cfg)
	if _, err := os.Stat(blobsDir); err != nil {
		t.Errorf("blobs dir should survive Cleanup: %v", err)
	}
}

func TestTwoChatsShareBlobs(t *testing.T) {
	// The headline win of the rewrite: two chats, same file, same
	// content → one blob on disk. The old shadow-git-per-chat
	// design had no cross-chat dedup because each chat had its own
	// git repo. Passing non-nil newContent exercises BOTH the
	// beforeSHA path (each chat reads identical disk content) and
	// the afterSHA path (each chat records identical post-write
	// content). When before and after bytes match, we expect one
	// blob total.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	p := filepath.Join(work, "shared.txt")
	if err := os.WriteFile(p, []byte("identical"), 0o600); err != nil {
		t.Fatal(err)
	}

	blobs := newBlobStore(cfg)
	logA := newEventLog(cfg, "A")
	logB := newEventLog(cfg, "B")
	idx := newCrossChatIndex()
	mA := newManager("A", work, logA, &managerDeps{blobs: blobs, index: idx})
	mB := newManager("B", work, logB, &managerDeps{blobs: blobs, index: idx})
	if err := mA.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mA.Snapshot(ctx, "shared.txt", []byte("identical"), 1); err != nil {
		t.Fatal(err)
	}
	if err := mB.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mB.Snapshot(ctx, "shared.txt", []byte("identical"), 1); err != nil {
		t.Fatal(err)
	}

	// beforeSHA(identical) == afterSHA(identical), so exactly 1 blob.
	blobsRoot := blobsRoot(cfg)
	count := 0
	entries, _ := os.ReadDir(blobsRoot)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		inner, _ := os.ReadDir(filepath.Join(blobsRoot, e.Name()))
		count += len(inner)
	}
	if count != 1 {
		t.Errorf("blob count = %d, want 1 (shared between chats)", count)
	}
}

func TestTwoChatsShareBeforeBlobWhenNewContentDiffers(t *testing.T) {
	// Complement to TestTwoChatsShareBlobs: locks the dedup axis.
	// Both chats read identical disk content (1 beforeSHA shared),
	// each writes distinct content (2 distinct afterSHAs). Total
	// blobs = 3. A regression in the beforeSHA path would show 4
	// (no sharing); a regression in afterSHA accounting would show
	// 2 or miss one of the post-write blobs.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	p := filepath.Join(work, "shared.txt")
	if err := os.WriteFile(p, []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()
	mA := newManager("A", work, newEventLog(cfg, "A"), &managerDeps{blobs: blobs, index: idx})
	mB := newManager("B", work, newEventLog(cfg, "B"), &managerDeps{blobs: blobs, index: idx})
	if err := mA.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mA.Snapshot(ctx, "shared.txt", []byte("A-new"), 1); err != nil {
		t.Fatal(err)
	}
	if err := mB.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mB.Snapshot(ctx, "shared.txt", []byte("B-new"), 1); err != nil {
		t.Fatal(err)
	}
	blobsRoot := blobsRoot(cfg)
	count := 0
	entries, _ := os.ReadDir(blobsRoot)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		inner, _ := os.ReadDir(filepath.Join(blobsRoot, e.Name()))
		count += len(inner)
	}
	if count != 3 {
		t.Errorf("blob count = %d, want 3 (1 shared before + 2 distinct after)", count)
	}
}

func TestCrossChatRestoreIsolated(t *testing.T) {
	// A Restore on chat A must never touch files that only chat B
	// has ever snapshotted. This is the property that motivated
	// the rewrite in the user's words: "if I have 2 chats with 2
	// agents writing to the same file checkpoints will be a mess."
	// We test the complement — a file ONLY chat B touched is
	// invisible to chat A's Restore.
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()
	mA := newManager("A", work, newEventLog(cfg, "A"), &managerDeps{blobs: blobs, index: idx})
	mB := newManager("B", work, newEventLog(cfg, "B"), &managerDeps{blobs: blobs, index: idx})

	// Chat A touches only a.txt.
	aFile := filepath.Join(work, "a.txt")
	_ = os.WriteFile(aFile, []byte("a0"), 0o600)
	if err := mA.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	tagA, err := mA.Snapshot(ctx, "a.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(aFile, []byte("a1"), 0o600)
	if err := mA.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := mA.Snapshot(ctx, "a.txt", nil, 2); err != nil {
		t.Fatal(err)
	}

	// Chat B touches only b.txt after that.
	bFile := filepath.Join(work, "b.txt")
	_ = os.WriteFile(bFile, []byte("b0"), 0o600)
	if err := mB.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mB.Snapshot(ctx, "b.txt", nil, 1); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(bFile, []byte("b1"), 0o600)

	// Restore chat A to tagA. a.txt should go back to a0; b.txt
	// should stay at b1 because chat A has no record of it.
	if _, err := mA.Restore(ctx, tagA); err != nil {
		t.Fatalf("Restore A: %v", err)
	}
	gotA, _ := os.ReadFile(aFile)
	if string(gotA) != "a0" {
		t.Errorf("a.txt after A restore = %q, want a0", gotA)
	}
	gotB, _ := os.ReadFile(bFile)
	if string(gotB) != "b1" {
		t.Errorf("b.txt clobbered by A's restore: got %q, want b1", gotB)
	}
}

func TestIsHexHash(t *testing.T) {
	if isHexHash("") {
		t.Error(`isHexHash("") should be false (empty is not a valid hash)`)
	}
	if !isHexHash("a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90") {
		t.Error("valid 64-char hex should pass")
	}
	if isHexHash("not-hex") {
		t.Error("short string should fail")
	}
	if isHexHash(string(make([]byte, 64))) {
		t.Error("64 NUL bytes should fail")
	}
}

func TestConflicts_EmptyForFreshChat(t *testing.T) {
	m, _ := newTestManager(t, "c")
	got, err := m.Conflicts(context.Background())
	if err != nil {
		t.Fatalf("Conflicts() err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Conflicts() = %v, want empty slice", got)
	}
}

func TestConflicts_ReturnsRecordedDrift(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()
	path := filepath.Join(work, "shared.go")
	_ = os.WriteFile(path, []byte("baseline"), 0o600)

	mA := newManager("A", work, newEventLog(cfg, "A"), &managerDeps{blobs: blobs, index: idx})
	if err := mA.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	aNew := []byte("A wrote this")
	if _, err := mA.Snapshot(ctx, "shared.go", aNew, 1); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, aNew, 0o600)
	_ = os.WriteFile(path, []byte("external edit"), 0o600)

	mB := newManager("B", work, newEventLog(cfg, "B"), &managerDeps{blobs: blobs, index: idx})
	if err := mB.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mB.Snapshot(ctx, "shared.go", nil, 1); err != nil {
		t.Fatal(err)
	}

	got, err := mB.Conflicts(context.Background())
	if err != nil {
		t.Fatalf("Conflicts() err = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("Conflicts() = %d entries, want 1", len(got))
	}
	c := got[0]
	if c.Path != "shared.go" {
		t.Errorf("Conflicts()[0].Path = %q, want shared.go", c.Path)
	}
	if c.OtherChat != "A" {
		t.Errorf("Conflicts()[0].OtherChat = %q, want A", c.OtherChat)
	}
	if c.ExpectedSHA == "" {
		t.Error("Conflicts()[0].ExpectedSHA is empty, want non-empty")
	}
	if c.TS == 0 {
		t.Error("Conflicts()[0].TS = 0, want non-zero timestamp")
	}
}

func TestConflicts_ReturnedSliceIsCopy(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()
	path := filepath.Join(work, "f.go")
	_ = os.WriteFile(path, []byte("v0"), 0o600)

	mA := newManager("A", work, newEventLog(cfg, "A"), &managerDeps{blobs: blobs, index: idx})
	if err := mA.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mA.Snapshot(ctx, "f.go", []byte("a"), 1); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, []byte("a"), 0o600)
	_ = os.WriteFile(path, []byte("external"), 0o600)

	mB := newManager("B", work, newEventLog(cfg, "B"), &managerDeps{blobs: blobs, index: idx})
	if err := mB.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mB.Snapshot(ctx, "f.go", nil, 1); err != nil {
		t.Fatal(err)
	}

	first, _ := mB.Conflicts(context.Background())
	if len(first) != 1 {
		t.Fatalf("setup: got %d conflicts, want 1", len(first))
	}
	first[0].Path = "mutated"

	second, _ := mB.Conflicts(context.Background())
	if second[0].Path != "f.go" {
		t.Errorf("Conflicts() returned internal slice: path mutated to %q", second[0].Path)
	}
}

func TestReadBlob_RejectsNonHex(t *testing.T) {
	m, _ := newTestManager(t, "c")
	if _, err := m.ReadBlob(context.Background(), "not-hex"); err == nil {
		t.Error(`ReadBlob("not-hex") err = nil, want error`)
	}
	short := "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	if _, err := m.ReadBlob(context.Background(), short); err == nil {
		t.Errorf("ReadBlob(63-char) err = nil, want error")
	}
}

func TestReadBlob_RejectsEmpty(t *testing.T) {
	m, _ := newTestManager(t, "c")
	if _, err := m.ReadBlob(context.Background(), ""); err == nil {
		t.Error(`ReadBlob("") err = nil, want error`)
	}
}

func TestReadBlob_ChatScopedIsolation(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()
	path := filepath.Join(work, "secret.txt")
	if err := os.WriteFile(path, []byte("A's private content"), 0o600); err != nil {
		t.Fatal(err)
	}

	mA := newManager("A", work, newEventLog(cfg, "A"), &managerDeps{blobs: blobs, index: idx})
	if err := mA.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mA.Snapshot(ctx, "secret.txt", nil, 1); err != nil {
		t.Fatal(err)
	}

	sha := hashOf([]byte("A's private content"))
	gotA, err := mA.ReadBlob(context.Background(), sha)
	if err != nil {
		t.Fatalf("A.ReadBlob own sha err = %v, want nil", err)
	}
	if string(gotA) != "A's private content" {
		t.Errorf("A.ReadBlob = %q, want A's private content", gotA)
	}

	mB := newManager("B", work, newEventLog(cfg, "B"), &managerDeps{blobs: blobs, index: idx})
	if _, err := mB.ReadBlob(context.Background(), sha); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("B.ReadBlob(A's sha) err = %v, want ErrBlobNotFound", err)
	}
}

func TestDiff_AddedFile(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	// Tag 1: take a snapshot just to establish tag 1's existence.
	existing := filepath.Join(work, "existing.go")
	_ = os.WriteFile(existing, []byte("x"), 0o600)
	if _, err := m.Snapshot(ctx, "existing.go", nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// Agent creates new.go between turn 1 and turn 2 (disk write
	// first), then we snapshot — so beforeSHA captures the real
	// content. fromExisted=false (no snapshot at tag 1); toExisted
	// =true (beforeSHA non-empty at tag 2) → status "A".
	newPath := filepath.Join(work, "new.go")
	if err := os.WriteFile(newPath, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "new.go", nil, 2); err != nil {
		t.Fatal(err)
	}

	diffs, err := m.Diff(ctx, Tag("1"), Tag("2"))
	if err != nil {
		t.Fatalf("Diff(1,2) err = %v, want nil", err)
	}
	var added *FileChange
	for i := range diffs {
		if diffs[i].Path == "new.go" {
			added = &diffs[i]
			break
		}
	}
	if added == nil {
		t.Fatalf("Diff(1,2) = %+v, want entry for new.go", diffs)
	}
	if added.Status != FileAdded {
		t.Errorf("Diff(new.go).Status = %q, want A", added.Status)
	}
	if added.LinesAdded != 1 {
		t.Errorf("Diff(new.go).LinesAdded = %d, want 1", added.LinesAdded)
	}
	if added.LinesRemoved != 0 {
		t.Errorf("Diff(new.go).LinesRemoved = %d, want 0", added.LinesRemoved)
	}
}

func TestDiff_DeletedFile(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	p := filepath.Join(work, "doomed.txt")
	if err := os.WriteFile(p, []byte("line1\nline2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "doomed.txt", []byte("line1\nline2\n"), 1); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "doomed.txt", nil, 2); err != nil {
		t.Fatal(err)
	}

	diffs, err := m.Diff(ctx, Tag("1"), Tag("2"))
	if err != nil {
		t.Fatalf("Diff(1,2) err = %v, want nil", err)
	}
	var d *FileChange
	for i := range diffs {
		if diffs[i].Path == "doomed.txt" {
			d = &diffs[i]
			break
		}
	}
	if d == nil {
		t.Fatalf("Diff(1,2) = %+v, want entry for doomed.txt", diffs)
	}
	if d.Status != FileDeleted {
		t.Errorf("Diff(doomed.txt).Status = %q, want D", d.Status)
	}
	if d.LinesRemoved != 2 {
		t.Errorf("Diff(doomed.txt).LinesRemoved = %d, want 2", d.LinesRemoved)
	}
}

func TestDiff_SwapsReversedTags(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	p := filepath.Join(work, "f.txt")
	_ = os.WriteFile(p, []byte("a\n"), 0o600)
	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "f.txt", nil, 1); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p, []byte("a\nb\n"), 0o600)
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "f.txt", nil, 2); err != nil {
		t.Fatal(err)
	}

	forward, err := m.Diff(ctx, Tag("1"), Tag("2"))
	if err != nil {
		t.Fatalf("Diff(1,2) err = %v", err)
	}
	reversed, err := m.Diff(ctx, Tag("2"), Tag("1"))
	if err != nil {
		t.Fatalf("Diff(2,1) err = %v", err)
	}
	if len(forward) != len(reversed) {
		t.Fatalf("len(Diff(1,2))=%d != len(Diff(2,1))=%d", len(forward), len(reversed))
	}
	if forward[0].Path != reversed[0].Path {
		t.Errorf("Diff swap changed path: %q vs %q", forward[0].Path, reversed[0].Path)
	}
	if forward[0].LinesAdded != reversed[0].LinesAdded ||
		forward[0].LinesRemoved != reversed[0].LinesRemoved {
		t.Errorf("Diff swap changed stats: forward=%+v reversed=%+v", forward[0], reversed[0])
	}
}

func TestDiff_MissingTag(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	if _, err := m.Diff(context.Background(), Tag("nope"), Tag("2")); !errors.Is(err, ErrTagNotFound) {
		t.Errorf("Diff(nope,2) err = %v, want ErrTagNotFound", err)
	}
	_ = os.WriteFile(filepath.Join(work, "f"), []byte("x"), 0o600)
	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(context.Background(), "f", nil, 1); err != nil {
		t.Fatal(err)
	}
	tags := m.tags()
	if _, err := m.Diff(context.Background(), Tag(tags[0]), Tag("nope")); !errors.Is(err, ErrTagNotFound) {
		t.Errorf("Diff(%q,nope) err = %v, want ErrTagNotFound", tags[0], err)
	}
}

func TestCountLineDelta_EmptyInputs(t *testing.T) {
	cases := []struct {
		name                string
		from, to            string
		wantAdd, wantRemove int
	}{
		{"both empty", "", "", 0, 0},
		{"from empty", "", "a\nb\n", 2, 0},
		{"to empty", "a\nb\n", "", 0, 2},
		{"identical", "a\nb\n", "a\nb\n", 0, 0},
		{"append one line", "a\n", "a\nb\n", 1, 0},
		{"remove one line", "a\nb\n", "a\n", 0, 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotAdd, gotRemove := countLineDelta(context.Background(), []byte(tt.from), []byte(tt.to))
			if gotAdd != tt.wantAdd || gotRemove != tt.wantRemove {
				t.Errorf("countLineDelta(%q,%q) = (%d,%d), want (%d,%d)",
					tt.from, tt.to, gotAdd, gotRemove, tt.wantAdd, tt.wantRemove)
			}
		})
	}
}

func TestCountLineDelta_ReorderIsNotUnchanged(t *testing.T) {
	// The load-bearing property: swapping two lines counts as one
	// add and one remove (LCS=1), not zero. A naive multiset/bag
	// approach would return (0,0) here.
	from := []byte("a\nb\n")
	to := []byte("b\na\n")
	add, remove := countLineDelta(context.Background(), from, to)
	if add != 1 || remove != 1 {
		t.Errorf("countLineDelta(reorder) = (%d,%d), want (1,1)", add, remove)
	}
}

func TestBytesToLines(t *testing.T) {
	cases := []struct {
		name string
		want []string
		in   []byte
	}{
		{"empty", nil, []byte("")},
		{"single without newline", []string{"a"}, []byte("a")},
		{"trailing newline dropped", []string{"a", "b"}, []byte("a\nb\n")},
		{"no trailing newline", []string{"a", "b"}, []byte("a\nb")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := bytesToLines(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("bytesToLines(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("bytesToLines(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestSnapshot_ConflictPayloadTagMatchesSnapshotTag pins the fix
// for the CYCLE 2 Q1 drift between persisted and broadcast
// conflict payloads. Before the fix the persisted kindConflict
// event had Tag="" (allocateTag hadn't run yet) while the live
// onConf broadcast carried state.latestTag (the *previous*
// snapshot's tag, or ""). Both are wrong: the conflict belongs
// to the snapshot that observed the drift, not the one before
// it. After the fix allocateTag runs before the conflict branch
// so the persisted event, the broadcast, and the subsequent
// snapshot all agree.
func TestSnapshot_ConflictPayloadTagMatchesSnapshotTag(t *testing.T) {
	ctx := context.Background()
	cfg := t.TempDir()
	work := t.TempDir()
	blobs := newBlobStore(cfg)
	idx := newCrossChatIndex()

	shared := filepath.Join(work, "shared.txt")
	if err := os.WriteFile(shared, []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Chat A snapshots v0 and its agent writes v1 to disk.
	mA := newManager("A", work, newEventLog(cfg, "A"), &managerDeps{blobs: blobs, index: idx})
	if err := mA.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mA.Snapshot(ctx, "shared.txt", []byte("v1"), 1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Drift: somebody else (not tracked by the index) mutates
	// the file to v2 before chat B snapshots it.
	if err := os.WriteFile(shared, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}

	var broadcast *ConflictPayload
	mB := newManager("B", work, newEventLog(cfg, "B"), &managerDeps{
		blobs: blobs, index: idx,
		onConf: func(_ string, p *ConflictPayload) { broadcast = p },
	})
	if err := mB.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	// Chat B's snapshot observes the drift (disk is v2 but chat A
	// last left v1) and triggers the conflict path.
	bTag, err := mB.Snapshot(ctx, "shared.txt", []byte("v3"), 1)
	if err != nil {
		t.Fatal(err)
	}

	// Persisted conflict event replays with the same tag that the
	// snapshot allocated.
	got, err := mB.Conflicts(context.Background())
	if err != nil {
		t.Fatalf("Conflicts err = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Conflicts() = %d entries, want 1", len(got))
	}
	if got[0].Tag != string(bTag) {
		t.Errorf("persisted Conflicts()[0].Tag = %q, want %q (snapshot tag)", got[0].Tag, bTag)
	}

	// Live broadcast carries the same tag.
	if broadcast == nil {
		t.Fatal("expected onConf broadcast, got nil")
	}
	if broadcast.Tag != string(bTag) {
		t.Errorf("broadcast.Tag = %q, want %q (snapshot tag)", broadcast.Tag, bTag)
	}

	// And the two values agree with each other — the invariant
	// the fix restored.
	if got[0].Tag != broadcast.Tag {
		t.Errorf("persisted Tag %q != broadcast Tag %q", got[0].Tag, broadcast.Tag)
	}
}

// TestCountLineDelta_DegradedFallbackForExtremelyLargeInputs pins
// the CYCLE 2 Q9 cap. LCS is O(N*M) in memory and time; without
// the cap a pathological multi-hundred-thousand-line diff would
// allocate many GB synchronously inside Manager.Diff while holding
// m.mu, starving every other chat operation. The cap trades
// precision for bounded allocation: above the product cell cap
// each side's line count is reported verbatim as added+removed
// (degraded but truthful — everything counts as changed).
func TestCountLineDelta_DegradedFallbackForExtremelyLargeInputs(t *testing.T) {
	// Pick dimensions that exceed lcsCellCap when multiplied but
	// stay cheap to allocate for the test: 5000 × 5000 = 25 M
	// cells, well over the 16 M cap. The single-dimension (big,
	// nil) cases short-circuit via the n==0/m==0 guards before
	// the product check, so they still exercise the "empty side"
	// branch for free.
	const n = 5_000
	big := bytes.Repeat([]byte("a\n"), n)
	add, remove := countLineDelta(context.Background(), big, nil)
	if add != 0 || remove != n {
		t.Errorf("countLineDelta(big, nil) = (%d, %d), want (0, %d)", add, remove, n)
	}
	add, remove = countLineDelta(context.Background(), nil, big)
	if add != n || remove != 0 {
		t.Errorf("countLineDelta(nil, big) = (%d, %d), want (%d, 0)", add, remove, n)
	}

	// Both sides over the product cap (n*m = 25 M > 16 M):
	// fallback reports everything as changed in both directions.
	a := bytes.Repeat([]byte("a\n"), n)
	b := bytes.Repeat([]byte("b\n"), n)
	add, remove = countLineDelta(context.Background(), a, b)
	if add != n || remove != n {
		t.Errorf("countLineDelta(oversized, oversized) = (%d, %d), want (%d, %d)", add, remove, n, n)
	}
}

// BenchmarkCountLineDelta measures the LCS-based diff at typical file
// sizes and a pathological case near lcsCellCap to verify the fallback
// fires correctly. countLineDelta is called from Manager.Snapshot on
// every file snapshot (5-20 times/sec during active agent turns).
func BenchmarkCountLineDelta(b *testing.B) {
	// generate builds two slices with ~50% shared lines to exercise
	// the LCS inner loop realistically.
	generate := func(n int) ([]byte, []byte) {
		var from, to bytes.Buffer
		for i := range n {
			from.WriteString("line ")
			from.WriteRune(rune('A' + i%26))
			from.WriteByte('\n')
			if i%2 == 0 {
				to.WriteString("line ")
				to.WriteRune(rune('A' + i%26))
			} else {
				to.WriteString("new ")
				to.WriteRune(rune('a' + i%26))
			}
			to.WriteByte('\n')
		}
		return from.Bytes(), to.Bytes()
	}

	for _, size := range []int{100, 500, 2000} {
		from, to := generate(size)
		b.Run(fmt.Sprintf("%d_lines", size), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				countLineDelta(context.Background(), from, to)
			}
		})
	}

	// Pathological case: both sides just over sqrt(lcsCellCap) so
	// the product exceeds the cap and the fallback fires.
	capSize := 4097 // 4097*4097 = ~16.8M > 16M cap
	from, to := generate(capSize)
	b.Run("near_cap_fallback", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			countLineDelta(context.Background(), from, to)
		}
	})
}

func TestSnapshot_OverCapFileRecordsEmptyBeforeSHA(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "overcap")

	// Create a file larger than contentCap (16 MiB).
	bigFile := filepath.Join(work, "big.bin")
	// Write contentCap + 1 byte to exceed the cap.
	data := make([]byte, contentCap+1)
	for i := range data {
		data[i] = 'x'
	}
	if err := os.WriteFile(bigFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Advance turn so we have a valid tag.
	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}

	// Snapshot should succeed — the over-cap file just gets an
	// empty beforeSHA (no rollback target), but the snapshot
	// itself still records the new content via afterSHA.
	newContent := []byte("replaced")
	tag, err := m.Snapshot(ctx, "big.bin", newContent, 1)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	if tag == "" {
		t.Fatal("expected non-empty tag")
	}

	// contentAtOrBeforeTag at the exact tag returns beforeSHA,
	// which is empty for the over-cap case. Verify that the
	// snapshot was recorded by checking that the tag exists in
	// the ordered list and that a subsequent lookup (simulating
	// a later tag) resolves the afterSHA.
	m.mu.Lock()
	_, exactExists := m.state.contentAtOrBeforeTag("big.bin", string(tag))
	m.mu.Unlock()
	// The exact-tag lookup returns beforeSHA which is "" for
	// over-cap files, so (_, false) is the expected result.
	if exactExists {
		t.Error("expected contentAtOrBeforeTag to return false for over-cap beforeSHA")
	}

	// Verify the tag was recorded by checking it appears in the
	// tag list.
	tags := m.tags()
	if !slices.Contains(tags, string(tag)) {
		t.Errorf("tag %q not found in tags list %v", tag, tags)
	}
}

// BenchmarkSnapshot measures the per-file-write hot path (Manager.Snapshot).
// Sub-benchmarks cover cold start (first file, exercises ensureLoaded),
// warm steady-state (Nth file), and multi-file turns.
func BenchmarkSnapshot(b *testing.B) {
	b.Run("cold_first_file", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			cfg := b.TempDir()
			work := b.TempDir()
			blobs := newBlobStore(cfg)
			log := newEventLog(cfg, "bench")
			m := newManager("bench", work, log, &managerDeps{blobs: blobs, index: newCrossChatIndex()})
			_ = os.WriteFile(filepath.Join(work, "f.go"), []byte("package main\n"), 0o600)
			_ = m.AdvanceTurn(context.Background(), 0)
			_, _ = m.Snapshot(context.Background(), "f.go", []byte("package main\nfunc main(){}\n"), 1)
		}
	})

	b.Run("warm_Nth_file", func(b *testing.B) {
		cfg := b.TempDir()
		work := b.TempDir()
		blobs := newBlobStore(cfg)
		log := newEventLog(cfg, "bench")
		m := newManager("bench", work, log, &managerDeps{blobs: blobs, index: newCrossChatIndex()})
		_ = os.WriteFile(filepath.Join(work, "f.go"), []byte("v0"), 0o600)
		_ = m.AdvanceTurn(context.Background(), 0)
		_, _ = m.Snapshot(context.Background(), "f.go", []byte("v1"), 1)
		b.ResetTimer()
		b.ReportAllocs()
		for i := range b.N {
			content := fmt.Appendf(nil, "v%d", i+2)
			_, _ = m.Snapshot(context.Background(), "f.go", content, i+2)
		}
	})

	b.Run("10_files_per_turn", func(b *testing.B) {
		cfg := b.TempDir()
		work := b.TempDir()
		blobs := newBlobStore(cfg)
		log := newEventLog(cfg, "bench")
		m := newManager("bench", work, log, &managerDeps{blobs: blobs, index: newCrossChatIndex()})
		for i := range 10 {
			name := fmt.Sprintf("file%d.go", i)
			_ = os.WriteFile(filepath.Join(work, name), []byte("package p\n"), 0o600)
		}
		_ = m.AdvanceTurn(context.Background(), 0)
		b.ResetTimer()
		b.ReportAllocs()
		for n := range b.N {
			for i := range 10 {
				name := fmt.Sprintf("file%d.go", i)
				content := fmt.Appendf(nil, "package p // iter %d\n", n)
				_, _ = m.Snapshot(context.Background(), name, content, n*10+i+1)
			}
		}
	})

	b.Run("large_file_near_cap", func(b *testing.B) {
		cfg := b.TempDir()
		work := b.TempDir()
		blobs := newBlobStore(cfg)
		log := newEventLog(cfg, "bench")
		m := newManager("bench", work, log, &managerDeps{blobs: blobs, index: newCrossChatIndex()})
		// Use a 1 MiB file (large but under contentCap).
		bigContent := bytes.Repeat([]byte("x"), 1<<20)
		_ = os.WriteFile(filepath.Join(work, "big.bin"), bigContent, 0o600)
		_ = m.AdvanceTurn(context.Background(), 0)
		_, _ = m.Snapshot(context.Background(), "big.bin", bigContent, 1)
		b.ResetTimer()
		b.ReportAllocs()
		for i := range b.N {
			newContent := append(bigContent[:len(bigContent)-1:len(bigContent)-1], byte('0'+i%10))
			_, _ = m.Snapshot(context.Background(), "big.bin", newContent, i+2)
		}
	})
}

// FuzzAbsPath exercises the security-critical path-escape validator.
// Invariants: (1) no returned path resolves outside workDir,
// (2) ErrPathEscape for any input that would escape, (3) empty
// string always returns ErrPathEscape, (4) no panic on arbitrary bytes.
func FuzzAbsPath(f *testing.F) {
	f.Add("")
	f.Add(".")
	f.Add("..")
	f.Add("../etc/passwd")
	f.Add("a/b/c")
	f.Add("a/../../../escape")
	f.Add("valid/path.go")
	f.Add("\x00null")
	f.Add("a/b/../c")
	f.Add("/absolute")

	f.Fuzz(func(t *testing.T, relPath string) {
		work := t.TempDir()
		m := newManager("fuzz", work, nil, &managerDeps{blobs: nil, index: nil})
		abs, err := m.absPath(relPath)
		if err != nil {
			// Must be ErrPathEscape.
			if !errors.Is(err, ErrPathEscape) {
				t.Fatalf("absPath(%q) returned non-ErrPathEscape error: %v", relPath, err)
			}
			return
		}
		// If no error, the path must be inside workDir.
		rel, relErr := filepath.Rel(work, abs)
		if relErr != nil {
			t.Fatalf("absPath(%q) = %q, but Rel(workDir, abs) failed: %v", relPath, abs, relErr)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("absPath(%q) = %q escapes workDir %q (rel=%q)", relPath, abs, work, rel)
		}
	})
}

func TestCountLineDelta_RapidInvariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		from := rapid.SliceOfN(rapid.StringMatching(`[^\n]{0,40}`), 0, 50).Draw(t, "from")
		to := rapid.SliceOfN(rapid.StringMatching(`[^\n]{0,40}`), 0, 50).Draw(t, "to")

		fromBytes := []byte(strings.Join(from, "\n"))
		toBytes := []byte(strings.Join(to, "\n"))

		added, removed := countLineDelta(context.Background(), fromBytes, toBytes)

		// Invariant 1: non-negativity.
		if added < 0 || removed < 0 {
			t.Fatalf("negative delta: added=%d removed=%d", added, removed)
		}

		// Invariant 2: bounds — added <= len(toLines), removed <= len(fromLines).
		fromLines := bytesToLines(fromBytes)
		toLines := bytesToLines(toBytes)
		if added > len(toLines) {
			t.Fatalf("added=%d > len(toLines)=%d", added, len(toLines))
		}
		if removed > len(fromLines) {
			t.Fatalf("removed=%d > len(fromLines)=%d", removed, len(fromLines))
		}

		// Invariant 3: symmetry — swap(from,to) swaps (added,removed).
		added2, removed2 := countLineDelta(context.Background(), toBytes, fromBytes)
		if added2 != removed || removed2 != added {
			t.Fatalf("symmetry: delta(%q,%q)=(%d,%d) but delta(%q,%q)=(%d,%d)",
				fromBytes, toBytes, added, removed,
				toBytes, fromBytes, added2, removed2)
		}

		// Invariant 4: identity — countLineDelta(x,x) == (0,0).
		selfAdd, selfRem := countLineDelta(context.Background(), fromBytes, fromBytes)
		if selfAdd != 0 || selfRem != 0 {
			t.Fatalf("identity: delta(x,x)=(%d,%d), want (0,0)", selfAdd, selfRem)
		}
	})
}

func FuzzCountLineDelta(f *testing.F) {
	f.Add([]byte("a\nb\n"), []byte("b\nc\n"))
	f.Add([]byte(""), []byte("x\n"))
	f.Add([]byte("x\n"), []byte(""))
	f.Add([]byte("same\n"), []byte("same\n"))
	f.Fuzz(func(t *testing.T, from, to []byte) {
		added, removed := countLineDelta(context.Background(), from, to)
		if added < 0 || removed < 0 {
			t.Fatalf("negative delta: added=%d removed=%d", added, removed)
		}
		fromLines := bytesToLines(from)
		toLines := bytesToLines(to)
		if added > len(toLines) {
			t.Fatalf("added=%d > len(toLines)=%d", added, len(toLines))
		}
		if removed > len(fromLines) {
			t.Fatalf("removed=%d > len(fromLines)=%d", removed, len(fromLines))
		}
	})
}

func BenchmarkSnapshotContention(b *testing.B) {
	// Create 4 managers simulating 4 concurrent chats.

	setup := func(id string) (*Manager, string) {
		cfg := b.TempDir()
		work := b.TempDir()
		blobs := newBlobStore(cfg)
		log := newEventLog(cfg, id)
		deps := &managerDeps{blobs: blobs, index: newCrossChatIndex()}
		m := newManager(id, work, log, deps)
		// Write a file to snapshot.
		fpath := filepath.Join(work, "file.go")
		os.WriteFile(fpath, []byte("package main\n"), 0o600)
		// Advance turn so snapshots produce valid tags.
		m.AdvanceTurn(context.Background(), 1)
		return m, work
	}

	b.Run("no_gc_baseline", func(b *testing.B) {
		managers := make([]*Manager, 4)
		for i := range 4 {
			m, _ := setup(fmt.Sprintf("chat-%d", i))
			managers[i] = m
		}
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				m := managers[i%4]
				m.Snapshot(context.Background(), "file.go", nil, 1)
				i++
			}
		})
	})
}
