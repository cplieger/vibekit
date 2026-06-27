package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
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
	activeDraft := filepath.Join(storeDir, "chatA"+planDraftSuffix)
	if err := os.WriteFile(activeDraft, []byte("# plan"), 0o600); err != nil {
		t.Fatalf("write active draft: %v", err)
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
	if !exists(t, filepath.Join(archiveDir, "chatA"+planDraftSuffix)) {
		t.Errorf("plan draft not moved into archive dir")
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
// archived chat (and plan draft) back to the active directory and clears
// the tombstone.
func TestRestoreArchived_MovesArchiveToActive(t *testing.T) {
	svc, store, archiveDir := newArchiveTestService(t)
	storeDir := store.Dir()

	writeArchivedChat(t, archiveDir, "chatR", 0)
	if err := os.WriteFile(filepath.Join(archiveDir, "chatR"+planDraftSuffix), []byte("# plan"), 0o600); err != nil {
		t.Fatalf("write archived draft: %v", err)
	}

	if err := svc.RestoreArchived(context.Background(), "chatR"); err != nil {
		t.Fatalf("RestoreArchived: %v", err)
	}

	if !exists(t, filepath.Join(storeDir, "chatR.json")) {
		t.Errorf("chat not restored to active dir")
	}
	if exists(t, filepath.Join(archiveDir, "chatR.json")) {
		t.Errorf("archived chat still present after restore")
	}
	if !exists(t, filepath.Join(storeDir, "chatR"+planDraftSuffix)) {
		t.Errorf("plan draft not restored to active dir")
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

// TestDeleteArchived_RemovesFileAndDraft verifies DeleteArchived
// permanently removes the archived chat and its plan draft and fires
// onPurge.
func TestDeleteArchived_RemovesFileAndDraft(t *testing.T) {
	var rec purgeRecorder
	svc, _, archiveDir := newArchiveTestService(t, WithOnPurge(rec.record))

	chatPath := writeArchivedChat(t, archiveDir, "chatD", 0)
	draftPath := filepath.Join(archiveDir, "chatD"+planDraftSuffix)
	if err := os.WriteFile(draftPath, []byte("# plan"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}

	if err := svc.DeleteArchived(context.Background(), "chatD"); err != nil {
		t.Fatalf("DeleteArchived: %v", err)
	}
	if exists(t, chatPath) {
		t.Errorf("archived chat not removed: %s", chatPath)
	}
	if exists(t, draftPath) {
		t.Errorf("plan draft not removed: %s", draftPath)
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
