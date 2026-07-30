package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// The tests in this file pin the CONFINEMENT contract of the two archive
// operations that mutate the tree (RestoreArchived's rename,
// DeleteArchived's remove): a symlinked intermediate directory that
// leaves the chat store is refused, while the operator layouts vibekit
// invariant 6 supports keep working.
//
// The lexical pathinside.Inside gates those operations also run cannot
// see any of this — every path here is lexically inside the store — so
// each escape test fails against the pre-os.Root code by having the
// syscall follow the planted symlink and reach the file outside.

// plantedTree is the on-disk fixture an escape test builds: a chat store
// whose "archive" entry is a symlink, plus the outside directory that
// symlink points at.
type plantedTree struct {
	storeDir    string // the store dir the Service is told about
	outsideDir  string // the directory the planted symlink redirects to
	victimPath  string // the file outside the store an escape would reach
	restoredPos string // where an escaped restore would land the victim
}

// plantEscapingArchiveDir builds a store dir whose archive subdirectory
// is a symlink to a sibling directory OUTSIDE the store, holding one
// archived-looking chat file. This is the plantable shape: /config is
// operator- and agent-writable, so anything that can create
// <configDir>/chats/archive as a link redirects both archive syscalls
// out of the tree.
func plantEscapingArchiveDir(t *testing.T, chatID, target string) plantedTree {
	t.Helper()
	base := t.TempDir()
	storeDir := filepath.Join(base, "chats")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}
	outside := target
	if outside == "" {
		outside = filepath.Join(base, "outside")
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	victim := filepath.Join(outside, chatID+chatFileSuffix)
	if err := os.WriteFile(victim, []byte(`{"id":"`+chatID+`"}`), 0o600); err != nil {
		t.Fatalf("write victim file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(storeDir, Subdir)); err != nil {
		t.Fatalf("plant archive symlink: %v", err)
	}
	return plantedTree{
		storeDir:    storeDir,
		outsideDir:  outside,
		victimPath:  victim,
		restoredPos: filepath.Join(storeDir, chatID+chatFileSuffix),
	}
}

// pathPresent reports whether a name exists, without following a final
// symlink (os.Stat would report a link to a deleted target as absent).
func pathPresent(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	t.Fatalf("lstat %s: %v", path, err)
	return false
}

// assertLockReleased verifies the per-chat mutex was handed back on the
// refusal path — the confinement checks sit inside the locked region, so
// an early return that forgets its Unlock would wedge every later
// operation on that chat.
func assertLockReleased(t *testing.T, store *fakeStore, chatID api.ChatID) {
	t.Helper()
	m := store.Lock(chatID)
	if !m.TryLock() {
		t.Errorf("per-chat mutex still held after the refusal")
		return
	}
	m.Unlock()
}

// TestRestoreArchived_RefusesEscapingSymlinkedArchiveDir is the core
// red-green case for the restore side: with <storeDir>/archive symlinked
// outside the store, the confined rename must refuse rather than drag the
// outside file into the store. Pre-fix (ambient os.Rename) this passed
// the lexical gate and moved the victim.
func TestRestoreArchived_RefusesEscapingSymlinkedArchiveDir(t *testing.T) {
	const chatID = "chatEsc"
	tree := plantEscapingArchiveDir(t, chatID, "")
	store := newFakeStore(tree.storeDir)
	svc := New(store)

	err := svc.RestoreArchived(context.Background(), chatID)
	if err == nil {
		t.Fatal("RestoreArchived through a symlinked archive dir = nil, want refusal")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("refusal reported as a missing file (%v); the escape must be refused, not read as absent", err)
	}
	if !pathPresent(t, tree.victimPath) {
		t.Errorf("file outside the store was moved out of %s: the rename followed the planted symlink", tree.outsideDir)
	}
	if pathPresent(t, tree.restoredPos) {
		t.Errorf("outside file landed inside the store at %s", tree.restoredPos)
	}
	assertLockReleased(t, store, chatID)
}

// TestDeleteArchived_RefusesEscapingSymlinkedArchiveDir is the same case
// for the delete side. Pre-fix (ambient os.Remove) the victim outside the
// store was deleted and the call returned nil.
func TestDeleteArchived_RefusesEscapingSymlinkedArchiveDir(t *testing.T) {
	const chatID = "chatEscD"
	tree := plantEscapingArchiveDir(t, chatID, "")
	store := newFakeStore(tree.storeDir)
	var rec purgeRecorder
	svc := New(store, WithOnPurge(rec.record))

	err := svc.DeleteArchived(context.Background(), chatID)
	if err == nil {
		t.Fatal("DeleteArchived through a symlinked archive dir = nil, want refusal")
	}
	if !pathPresent(t, tree.victimPath) {
		t.Errorf("file outside the store was deleted: the remove followed the planted symlink")
	}
	if got := rec.sorted(); len(got) != 0 {
		t.Errorf("onPurge fired %v for a refused delete, want none", got)
	}
	assertLockReleased(t, store, chatID)
}

// TestRestoreArchived_RefusesAbsoluteInTreeSymlinkedArchiveDir pins a Go
// os.Root rule that surprises: a symlink whose target is written as an
// ABSOLUTE path is refused even when it points back inside the root,
// because os.Root resolves an absolute link target against the root
// itself. An operator who wants to relocate the archive dir within the
// store must therefore use a RELATIVE link (see the test below).
func TestRestoreArchived_RefusesAbsoluteInTreeSymlinkedArchiveDir(t *testing.T) {
	const chatID = "chatAbs"
	base := t.TempDir()
	storeDir := filepath.Join(base, "chats")
	realArchive := filepath.Join(storeDir, "real-archive")
	if err := os.MkdirAll(realArchive, 0o700); err != nil {
		t.Fatalf("mkdir real archive: %v", err)
	}
	if err := os.Symlink(realArchive, filepath.Join(storeDir, Subdir)); err != nil {
		t.Fatalf("plant absolute in-tree symlink: %v", err)
	}
	chatPath := filepath.Join(realArchive, chatID+chatFileSuffix)
	if err := os.WriteFile(chatPath, []byte(`{"id":"`+chatID+`"}`), 0o600); err != nil {
		t.Fatalf("write archived chat: %v", err)
	}
	svc := New(newFakeStore(storeDir))

	if err := svc.RestoreArchived(context.Background(), chatID); err == nil {
		t.Fatal("RestoreArchived through an absolute in-tree symlink = nil, want refusal")
	}
	if !pathPresent(t, chatPath) {
		t.Errorf("archived chat moved despite the refusal")
	}
}

// TestRestoreArchived_FollowsRelativeInTreeSymlinkedArchiveDir verifies
// the confinement only refuses links that LEAVE the store: a relative
// symlink from archive to a sibling directory inside the store is still
// followed, so an operator reshaping the store internally keeps working.
func TestRestoreArchived_FollowsRelativeInTreeSymlinkedArchiveDir(t *testing.T) {
	const chatID = "chatRel"
	base := t.TempDir()
	storeDir := filepath.Join(base, "chats")
	realArchive := filepath.Join(storeDir, "real-archive")
	if err := os.MkdirAll(realArchive, 0o700); err != nil {
		t.Fatalf("mkdir real archive: %v", err)
	}
	if err := os.Symlink("real-archive", filepath.Join(storeDir, Subdir)); err != nil {
		t.Fatalf("plant relative in-tree symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realArchive, chatID+chatFileSuffix),
		[]byte(`{"id":"`+chatID+`"}`), 0o600); err != nil {
		t.Fatalf("write archived chat: %v", err)
	}
	svc := New(newFakeStore(storeDir))

	if err := svc.RestoreArchived(context.Background(), chatID); err != nil {
		t.Fatalf("RestoreArchived through a relative in-tree symlink: %v, want success", err)
	}
	if !pathPresent(t, filepath.Join(storeDir, chatID+chatFileSuffix)) {
		t.Errorf("chat not restored to the active dir")
	}
}

// TestArchiveOps_SymlinkedStoreDirIsSupported is the vibekit invariant-6
// regression guard: the operator may symlink <configDir>/chats itself
// onto another filesystem (or symlink configDir above it). os.OpenRoot
// resolves the root path with ordinary path resolution, so the whole
// archive lifecycle must keep working through such a link — the
// confinement applies to the RESOLVED tree, it does not refuse the link.
func TestArchiveOps_SymlinkedStoreDirIsSupported(t *testing.T) {
	const chatID = "chatVol"
	base := t.TempDir()
	otherVolume := t.TempDir() // stands in for a second filesystem
	realStore := filepath.Join(otherVolume, "chat-store")
	if err := os.MkdirAll(filepath.Join(realStore, Subdir), 0o700); err != nil {
		t.Fatalf("mkdir relocated store: %v", err)
	}
	storeDir := filepath.Join(base, "chats") // the symlink the app is told about
	if err := os.Symlink(realStore, storeDir); err != nil {
		t.Fatalf("symlink store dir onto the other volume: %v", err)
	}
	archived := filepath.Join(realStore, Subdir, chatID+chatFileSuffix)
	if err := os.WriteFile(archived, []byte(`{"id":"`+chatID+`"}`), 0o600); err != nil {
		t.Fatalf("write archived chat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realStore, Subdir, chatID+planDraftSuffix),
		[]byte("# plan"), 0o600); err != nil {
		t.Fatalf("write archived draft: %v", err)
	}
	svc := New(newFakeStore(storeDir))

	if err := svc.RestoreArchived(context.Background(), chatID); err != nil {
		t.Fatalf("RestoreArchived on a symlinked store dir: %v, want success (invariant 6)", err)
	}
	restored := filepath.Join(realStore, chatID+chatFileSuffix)
	if !pathPresent(t, restored) {
		t.Fatalf("chat not restored inside the relocated store")
	}
	if !pathPresent(t, filepath.Join(realStore, chatID+planDraftSuffix)) {
		t.Errorf("plan draft not restored inside the relocated store")
	}

	// Re-archive it and delete it again, so the delete side is proven on
	// the same relocated layout.
	if err := svc.Archive(context.Background(), chatID); err != nil {
		t.Fatalf("Archive on a symlinked store dir: %v", err)
	}
	if err := svc.DeleteArchived(context.Background(), chatID); err != nil {
		t.Fatalf("DeleteArchived on a symlinked store dir: %v, want success (invariant 6)", err)
	}
	if pathPresent(t, archived) {
		t.Errorf("archived chat still present after delete")
	}
}
