package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// TestArchive_MovesActiveToArchive verifies Archive relocates the chat
// file (and its plan draft) into the archive subdirectory, records the
// tombstone, and fires onArchive.
func TestArchive_MovesActiveToArchive(t *testing.T) {
	var rec purgeRecorder
	svc, store, archiveDir := newArchiveTestService(t, WithOnArchive(rec.record))
	storeDir := store.Dir()

	activePath := filepath.Join(storeDir, "chatA.json")
	if err := os.WriteFile(activePath, []byte(`{"id":"chatA"}`), 0o600); err != nil {
		t.Fatalf("write active chat: %v", err)
	}
	if err := svc.Archive(context.Background(), "chatA"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	if exists(t, activePath) {
		t.Errorf("active chat file still present after archive: %s", activePath)
	}
	if !exists(t, filepath.Join(archiveDir, "chatA.json")) {
		t.Errorf("chat not moved into archive dir")
	}
	if got := store.markedDeleted(); !slices.Equal(got, []string{"chatA"}) {
		t.Errorf("MarkDeleted called with %v, want [chatA]", got)
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"chatA"}) {
		t.Errorf("onArchive fired for %v, want [chatA]", got)
	}
}

// TestArchive_RejectsInvalidChatID verifies a traversal-shaped id is
// refused before any filesystem mutation or tombstone.
func TestArchive_RejectsInvalidChatID(t *testing.T) {
	svc, store, _ := newArchiveTestService(t)
	if err := svc.Archive(context.Background(), "../escape"); err == nil {
		t.Fatal("Archive(invalid id) = nil, want error")
	}
	if got := store.markedDeleted(); len(got) != 0 {
		t.Errorf("MarkDeleted called %v for invalid id, want none", got)
	}
}

// TestRestoreArchived_MovesArchiveToActive verifies Restore relocates the
// archived chat back to the active directory and clears the tombstone.
func TestRestoreArchived_MovesArchiveToActive(t *testing.T) {
	svc, store, archiveDir := newArchiveTestService(t)
	storeDir := store.Dir()

	writeArchivedChat(t, archiveDir, "chatR", 0)

	if err := svc.RestoreArchived(context.Background(), "chatR"); err != nil {
		t.Fatalf("RestoreArchived: %v", err)
	}

	if !exists(t, filepath.Join(storeDir, "chatR.json")) {
		t.Errorf("chat not restored to active dir")
	}
	if exists(t, filepath.Join(archiveDir, "chatR.json")) {
		t.Errorf("archived chat still present after restore")
	}
	if got := store.clearedTombstones(); !slices.Equal(got, []string{"chatR"}) {
		t.Errorf("ClearTombstone called with %v, want [chatR]", got)
	}
}

// TestRestoreArchived_CollisionGuard verifies Restore refuses to clobber
// an active chat that already occupies the target id, leaving the
// archived copy intact.
func TestRestoreArchived_CollisionGuard(t *testing.T) {
	svc, store, archiveDir := newArchiveTestService(t)
	storeDir := store.Dir()

	writeArchivedChat(t, archiveDir, "dup", 0)
	if err := os.WriteFile(filepath.Join(storeDir, "dup.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write active chat: %v", err)
	}

	err := svc.RestoreArchived(context.Background(), "dup")
	var inUse *IDInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("RestoreArchived collision = %v, want *IDInUseError", err)
	}
	if !exists(t, filepath.Join(archiveDir, "dup.json")) {
		t.Errorf("archived chat removed despite collision")
	}
}

// TestRestoreArchived_RejectsInvalidChatID verifies a traversal-shaped id
// is refused.
func TestRestoreArchived_RejectsInvalidChatID(t *testing.T) {
	svc, _, _ := newArchiveTestService(t)
	if err := svc.RestoreArchived(context.Background(), "a/b"); err == nil {
		t.Fatal("RestoreArchived(invalid id) = nil, want error")
	}
}

// TestDeleteArchived_RemovesFile verifies DeleteArchived permanently
// removes the archived chat and fires onPurge.
func TestDeleteArchived_RemovesFile(t *testing.T) {
	var rec purgeRecorder
	svc, _, archiveDir := newArchiveTestService(t, WithOnPurge(rec.record))

	chatPath := writeArchivedChat(t, archiveDir, "chatD", 0)

	if err := svc.DeleteArchived(context.Background(), "chatD"); err != nil {
		t.Fatalf("DeleteArchived: %v", err)
	}
	if exists(t, chatPath) {
		t.Errorf("archived chat not removed: %s", chatPath)
	}
	if got := rec.sorted(); !slices.Equal(got, []string{"chatD"}) {
		t.Errorf("onPurge fired for %v, want [chatD]", got)
	}
}

// TestDeleteArchived_RejectsInvalidChatID verifies a traversal-shaped id
// is refused.
func TestDeleteArchived_RejectsInvalidChatID(t *testing.T) {
	svc, _, _ := newArchiveTestService(t)
	if err := svc.DeleteArchived(context.Background(), ".."); err == nil {
		t.Fatal("DeleteArchived(invalid id) = nil, want error")
	}
}

// TestRestoreArchived_BroadcastsChatCreated verifies a successful restore
// (the reloaded chat is readable) broadcasts a chat_created event so every
// connected client re-adds the entry to the sidebar without a manual
// refresh. The broadcast is gated on the post-restore reload succeeding;
// if that guard is inverted, a healthy restore would go un-broadcast.
func TestRestoreArchived_BroadcastsChatCreated(t *testing.T) {
	svc, store, archiveDir := newArchiveTestService(t)
	rec := &recordingBroadcaster{}
	store.bc = rec
	store.loadResult = &api.Chat{ID: "chatB"} // reload after restore succeeds

	writeArchivedChat(t, archiveDir, "chatB", 0)

	if err := svc.RestoreArchived(context.Background(), "chatB"); err != nil {
		t.Fatalf("RestoreArchived: %v", err)
	}

	if !rec.hasType(api.EventChatCreated) {
		t.Errorf("restore did not broadcast %q (events seen: %v); a successful reload must announce the restored chat",
			api.EventChatCreated, rec.recordedTypes())
	}
}

// TestRestoreArchived_NoBroadcastWhenReloadFails verifies the complement:
// when the post-restore reload fails, no chat_created is broadcast (the
// store has nothing valid to announce). Pins both sides of the reload
// guard so it can't be loosened to broadcast on a failed reload.
func TestRestoreArchived_NoBroadcastWhenReloadFails(t *testing.T) {
	svc, store, archiveDir := newArchiveTestService(t)
	rec := &recordingBroadcaster{}
	store.bc = rec
	// loadResult left nil: Load returns an error after the rename.

	writeArchivedChat(t, archiveDir, "chatC", 0)

	if err := svc.RestoreArchived(context.Background(), "chatC"); err != nil {
		t.Fatalf("RestoreArchived: %v", err)
	}

	if rec.hasType(api.EventChatCreated) {
		t.Errorf("restore broadcast %q despite a failed reload (events seen: %v)",
			api.EventChatCreated, rec.recordedTypes())
	}
}

// TestArchive_PreArchiveRunsBeforeFileMoves verifies the pre-archive hook
// fires while the chat file is still in the active dir (not yet in the
// archive dir) — the ordering that lets the hub tear down a chat's live
// bridge / in-flight turn / .partial before the record moves. (fakeStore.Load
// errors, so the ArchivedAt stamp is a no-op here; the rename still proceeds.)
func TestArchive_PreArchiveRunsBeforeFileMoves(t *testing.T) {
	dir := t.TempDir()
	archiveDir := filepath.Join(dir, Subdir)
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		t.Fatalf("mkdir archive dir: %v", err)
	}
	store := newFakeStore(dir)
	activePath := filepath.Join(dir, "chatP.json")
	if err := os.WriteFile(activePath, []byte(`{"id":"chatP"}`), 0o600); err != nil {
		t.Fatalf("write active chat: %v", err)
	}

	var (
		hookRan          bool
		srcPresentAtHook bool
		dstAbsentAtHook  bool
	)
	svc := New(store, WithPreArchive(func(id api.ChatID) {
		hookRan = true
		srcPresentAtHook = exists(t, activePath)
		dstAbsentAtHook = !exists(t, filepath.Join(archiveDir, string(id)+".json"))
	}))

	if err := svc.Archive(context.Background(), "chatP"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	if !hookRan {
		t.Fatal("preArchive hook never ran")
	}
	if !srcPresentAtHook {
		t.Error("preArchive ran AFTER the chat file left the active dir")
	}
	if !dstAbsentAtHook {
		t.Error("preArchive ran AFTER the chat file reached the archive dir")
	}
	if exists(t, activePath) {
		t.Error("chat file not moved out of the active dir")
	}
	if !exists(t, filepath.Join(archiveDir, "chatP.json")) {
		t.Error("chat file not moved into the archive dir")
	}
}
