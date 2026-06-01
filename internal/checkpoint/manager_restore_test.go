package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRestorePreservesUnrelatedFiles(t *testing.T) {
	// This is THE motivating change for the rewrite. When the user
	// has a second file open (editor buffer, other chat's edit,
	// whatever) that this chat's agent never touched, a Restore
	// must not revert it.
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	agentFile := filepath.Join(work, "agent.go")
	userFile := filepath.Join(work, "user_notes.md")

	// Agent edit 1.
	if err := os.WriteFile(agentFile, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// Snapshot captures v1 as the BEFORE state, then the agent
	// writes v2.
	tag1, err := m.Snapshot(ctx, "agent.go", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(agentFile, []byte("v2"), 0o600)

	// User edits an unrelated file manually.
	if err := os.WriteFile(userFile, []byte("important notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Agent edit 2 on the same file.
	if err := m.AdvanceTurn(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "agent.go", nil, 4); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(agentFile, []byte("v3"), 0o600)

	// Restore to tag1 (pre-first-edit). Agent file should revert
	// to v1, user notes must survive unchanged.
	mc, err := m.Restore(ctx, tag1)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if mc != 2 {
		t.Errorf("watermark = %d, want 2", mc)
	}
	got, _ := os.ReadFile(agentFile)
	if string(got) != "v1" {
		t.Errorf("agent file after restore = %q, want v1", got)
	}
	gotUser, _ := os.ReadFile(userFile)
	if string(gotUser) != "important notes" {
		t.Errorf("user file clobbered by restore: got %q", gotUser)
	}
}

func TestRestoreDeletesAgentCreatedFiles(t *testing.T) {
	// A file created fresh by the agent after `tag` has no content
	// at `tag` — Restore must delete it, not leave an orphan.
	ctx := context.Background()
	m, work := newTestManager(t, "c")

	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	tag1, err := m.Snapshot(ctx, "new.go", nil, 2) // file doesn't exist yet
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(work, "new.go"), []byte("agent created this"), 0o600)

	// Before restoring we need a later tag to roll back TO the
	// earlier one; Restore targets `tag` so we just point at tag1
	// and it should delete the file.
	if _, err := m.Restore(ctx, tag1); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "new.go")); !os.IsNotExist(err) {
		t.Errorf("file should be deleted, stat err = %v", err)
	}
}

func TestRestoreRecreatesDeletedFile(t *testing.T) {
	// Symmetric case: agent deletes a file after snapshotting its
	// content. Restore must recreate it from the blob.
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	p := filepath.Join(work, "doomed.txt")
	_ = os.WriteFile(p, []byte("keep me"), 0o600)

	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	tag1, err := m.Snapshot(ctx, "doomed.txt", nil, 2) // BEFORE deletion
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	// We also need a later observation so Restore knows this file
	// was touched in the relevant range.
	if err := m.AdvanceTurn(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "doomed.txt", nil, 4); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Restore(ctx, tag1); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("file should be recreated: %v", err)
	}
	if string(got) != "keep me" {
		t.Errorf("recreated content = %q, want %q", got, "keep me")
	}
}

func TestRestoreMissingTag(t *testing.T) {
	m, _ := newTestManager(t, "c")
	_, err := m.Restore(context.Background(), Tag("42"))
	if !errors.Is(err, ErrTagNotFound) {
		t.Errorf("got %v, want ErrTagNotFound", err)
	}
}

func TestRestorePreview_ReturnsFilesTouchedAtOrAfterTag(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	a := filepath.Join(work, "a.txt")
	b := filepath.Join(work, "b.txt")
	_ = os.WriteFile(a, []byte("a0"), 0o600)
	_ = os.WriteFile(b, []byte("b0"), 0o600)

	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	tag1, err := m.Snapshot(ctx, "a.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(a, []byte("a1"), 0o600)
	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot(ctx, "b.txt", nil, 2); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(b, []byte("b1"), 0o600)

	files, err := m.RestorePreview(context.Background(), tag1)
	if err != nil {
		t.Fatalf("RestorePreview(%q) err = %v, want nil", tag1, err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f] = true
	}
	if !got["a.txt"] {
		t.Errorf("RestorePreview(%q) = %v, want includes a.txt", tag1, files)
	}
	if !got["b.txt"] {
		t.Errorf("RestorePreview(%q) = %v, want includes b.txt (touched after tag)", tag1, files)
	}
}

func TestRestorePreview_MissingTag(t *testing.T) {
	m, _ := newTestManager(t, "c")
	_, err := m.RestorePreview(context.Background(), Tag("does-not-exist"))
	if !errors.Is(err, ErrTagNotFound) {
		t.Errorf("RestorePreview(%q) err = %v, want ErrTagNotFound", "does-not-exist", err)
	}
}

func TestRestore_RefusesToFollowSymlinkAtStagingPath(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	target := filepath.Join(work, "victim.txt")
	if err := os.WriteFile(target, []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	tag, err := m.Snapshot(ctx, "victim.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Plant a symlink at the pre-rewrite deterministic stage path.
	escapeTarget := filepath.Join(t.TempDir(), "escape")
	oldStagePath := target + ".vibekit-restore"
	if err := os.Symlink(escapeTarget, oldStagePath); err != nil {
		t.Skipf("symlink creation not supported on this platform: %v", err)
	}

	if _, err := m.Restore(ctx, tag); err != nil {
		t.Fatalf("Restore err = %v, want nil", err)
	}
	if _, err := os.Stat(escapeTarget); !os.IsNotExist(err) {
		t.Errorf("restore wrote through the planted symlink; escape file exists (err=%v)", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "v0" {
		t.Errorf("victim.txt after restore = %q, want v0", got)
	}
}

func TestCheckoutSingleFile(t *testing.T) {
	// Per-file Undo: only the named file reverts; other files from
	// the same tag stay at current content.
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	a := filepath.Join(work, "a.txt")
	b := filepath.Join(work, "b.txt")
	_ = os.WriteFile(a, []byte("a0"), 0o600)
	_ = os.WriteFile(b, []byte("b0"), 0o600)

	if err := m.AdvanceTurn(ctx, 1); err != nil {
		t.Fatal(err)
	}
	tagA, err := m.Snapshot(ctx, "a.txt", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(a, []byte("a1"), 0o600)
	if _, err := m.Snapshot(ctx, "b.txt", nil, 3); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(b, []byte("b1"), 0o600)

	if err := m.CheckoutFile(ctx, tagA, "a.txt"); err != nil {
		t.Fatalf("CheckoutFile: %v", err)
	}
	gotA, _ := os.ReadFile(a)
	if string(gotA) != "a0" {
		t.Errorf("a after checkout = %q, want a0", gotA)
	}
	// b must stay at its post-snapshot value — CheckoutFile only
	// touches the named path.
	gotB, _ := os.ReadFile(b)
	if string(gotB) != "b1" {
		t.Errorf("b clobbered by per-file checkout: got %q, want b1", gotB)
	}
}

func TestCheckoutFile_RejectsBadPaths(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	_ = os.WriteFile(filepath.Join(work, "f.txt"), []byte("v"), 0o600)
	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	tag, err := m.Snapshot(ctx, "f.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		tag  Tag
	}{
		{"empty_path", "", Tag("1")},
		{"parent_dir_traversal", "../escape", Tag("1")},
		{"deep_traversal", "../../etc/passwd", tag},
		{"absolute_path", "/etc/passwd", Tag("1")},
		{"null_byte", "foo\x00bar", Tag("1")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := m.CheckoutFile(ctx, tc.tag, tc.path); err == nil {
				t.Errorf("CheckoutFile(%q) err = nil, want rejection error", tc.path)
			}
		})
	}
}

func TestCheckoutMissingTag(t *testing.T) {
	m, _ := newTestManager(t, "c")
	err := m.CheckoutFile(context.Background(), Tag("42"), "foo.txt")
	if !errors.Is(err, ErrTagNotFound) {
		t.Errorf("got %v, want ErrTagNotFound", err)
	}
}

func TestCheckoutFile_DeletesFileThatDidNotExistAtTag(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	p := filepath.Join(work, "new.txt")

	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	tag1, err := m.Snapshot(ctx, "new.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p, []byte("agent created this"), 0o600)

	if err := m.CheckoutFile(ctx, tag1, "new.txt"); err != nil {
		t.Fatalf("CheckoutFile err = %v, want nil", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("CheckoutFile should delete file recreated after tag; stat err = %v", err)
	}
}

func TestCheckoutFile_RefusesToFollowSymlinkAtStagingPath(t *testing.T) {
	ctx := context.Background()
	m, work := newTestManager(t, "c")
	target := filepath.Join(work, "victim.txt")
	if err := os.WriteFile(target, []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.AdvanceTurn(ctx, 0); err != nil {
		t.Fatal(err)
	}
	tag, err := m.Snapshot(ctx, "victim.txt", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	escapeTarget := filepath.Join(t.TempDir(), "escape")
	oldStagePath := target + ".vibekit-restore"
	if err := os.Symlink(escapeTarget, oldStagePath); err != nil {
		t.Skipf("symlink creation not supported on this platform: %v", err)
	}

	if err := m.CheckoutFile(ctx, tag, "victim.txt"); err != nil {
		t.Fatalf("CheckoutFile err = %v, want nil", err)
	}
	if _, err := os.Stat(escapeTarget); !os.IsNotExist(err) {
		t.Errorf("checkout wrote through the planted symlink; escape file exists (err=%v)", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "v0" {
		t.Errorf("victim.txt after checkout = %q, want v0", got)
	}
}

func TestCleanupStages_RemovesStagedFilesOnFailure(t *testing.T) {
	m, work := newTestManager(t, "cleanup-stages")

	// Create staged temp files that simulate a mid-restore failure.
	tmp1 := filepath.Join(work, "file1.vibekit-restore-abc")
	tmp2 := filepath.Join(work, "file2.vibekit-restore-def")
	if err := os.WriteFile(tmp1, []byte("staged1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp2, []byte("staged2"), 0o644); err != nil {
		t.Fatal(err)
	}

	stages := []restoreStage{
		{path: "file1.txt", abs: filepath.Join(work, "file1.txt"), tmp: tmp1, existed: true},
		{path: "file2.txt", abs: filepath.Join(work, "file2.txt"), tmp: tmp2, existed: true},
		// Entry with empty tmp (delete-only stage) should be skipped.
		{path: "file3.txt", abs: filepath.Join(work, "file3.txt"), tmp: "", existed: false},
	}

	m.cleanupStages(stages)

	if _, err := os.Stat(tmp1); !os.IsNotExist(err) {
		t.Errorf("staged file %s still exists after cleanup", tmp1)
	}
	if _, err := os.Stat(tmp2); !os.IsNotExist(err) {
		t.Errorf("staged file %s still exists after cleanup", tmp2)
	}
}

// --- tarch-b9-c5-p1: BenchmarkRestore ---

// BenchmarkRestore measures checkpoint Restore latency with varying
// snapshot counts across multiple files.
func BenchmarkRestore(b *testing.B) {
	for _, snapshots := range []int{5, 20, 50} {
		for _, files := range []int{3, 10} {
			b.Run(fmt.Sprintf("snaps=%d/files=%d", snapshots, files), func(b *testing.B) {
				ctx := context.Background()
				cfg := b.TempDir()
				work := b.TempDir()
				blobs := newBlobStore(cfg)
				log := newEventLog(cfg, "bench")
				deps := &managerDeps{blobs: blobs, index: newCrossChatIndex()}
				m := newManager("bench", work, log, deps)

				// Create files and snapshots.
				fileNames := make([]string, files)
				for f := range files {
					name := fmt.Sprintf("file%d.go", f)
					fileNames[f] = name
					if err := os.WriteFile(filepath.Join(work, name), []byte("v0"), 0o600); err != nil {
						b.Fatal(err)
					}
				}

				var firstTag Tag
				for s := range snapshots {
					if err := m.AdvanceTurn(ctx, s*2); err != nil {
						b.Fatal(err)
					}
					name := fileNames[s%files]
					tag, err := m.Snapshot(ctx, name, nil, s*2+1)
					if err != nil {
						b.Fatal(err)
					}
					if s == 0 {
						firstTag = tag
					}
					// Simulate agent edit.
					content := fmt.Sprintf("v%d", s+1)
					if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o600); err != nil {
						b.Fatal(err)
					}
				}

				b.ResetTimer()
				b.ReportAllocs()
				for range b.N {
					// Restore to first tag, then re-advance to allow next restore.
					_, err := m.Restore(ctx, firstTag)
					if err != nil {
						b.Fatal(err)
					}
					// Re-write files so next iteration can restore again.
					for _, name := range fileNames {
						_ = os.WriteFile(filepath.Join(work, name), []byte("latest"), 0o600)
					}
				}
			})
		}
	}
}
