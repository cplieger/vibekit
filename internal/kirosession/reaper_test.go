package kirosession

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeSession creates a fake KAS session on disk: a
// sessions/<hash>/<id>/ dir (with an inner file) plus a
// sessions/cli/<id>.history sidecar, then back-dates both to age.
func makeSession(t *testing.T, root, hash, id string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(root, hash, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cliDir := filepath.Join(root, "cli")
	if err := os.MkdirAll(cliDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hist := filepath.Join(cliDir, id+".history")
	if err := os.WriteFile(hist, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	// Chtimes the dir AFTER writing its contents (writing the inner file
	// bumps the dir mtime back to now).
	for _, p := range []string{dir, hist} {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestReap(t *testing.T) {
	root := t.TempDir()
	makeSession(t, root, "hash1", "sess_keep", 0)
	makeSession(t, root, "hash1", "sess_gone", 0)

	New(root).Reap("sess_gone")

	if exists(t, filepath.Join(root, "hash1", "sess_gone")) {
		t.Error("reaped session dir still present")
	}
	if exists(t, filepath.Join(root, "cli", "sess_gone.history")) {
		t.Error("reaped session history sidecar still present")
	}
	if !exists(t, filepath.Join(root, "hash1", "sess_keep")) {
		t.Error("unrelated session dir was removed")
	}
	if !exists(t, filepath.Join(root, "cli", "sess_keep.history")) {
		t.Error("unrelated session sidecar was removed")
	}
}

func TestReapRejectsMalformedID(t *testing.T) {
	root := t.TempDir()
	makeSession(t, root, "hash1", "sess_keep", 0)
	r := New(root)
	// None of these should touch the filesystem (no sess_ prefix, empty
	// remainder, or glob/path metacharacters).
	for _, bad := range []string{"", "sess_", "no-prefix", "sess_../keep", "sess_*"} {
		r.Reap(bad)
	}
	if !exists(t, filepath.Join(root, "hash1", "sess_keep")) {
		t.Error("a malformed-id Reap removed a real session")
	}
}

func TestSweep(t *testing.T) {
	root := t.TempDir()
	old := 30 * time.Minute                       // older than defaultGuard (10m)
	makeSession(t, root, "h", "sess_ref", old)    // referenced → keep
	makeSession(t, root, "h", "sess_orphan", old) // unreferenced + old → reap
	makeSession(t, root, "h", "sess_young", 0)    // unreferenced + young → keep (guard)

	// A dead v2-engine file (bare uuid, no sess_ prefix), aged.
	cliDir := filepath.Join(root, "cli")
	v2 := filepath.Join(cliDir, "0c2257a1-7898-869e-0000-000000000000.json")
	if err := os.WriteFile(v2, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-old)
	if err := os.Chtimes(v2, when, when); err != nil {
		t.Fatal(err)
	}

	reaped := New(root).Sweep(map[string]struct{}{"sess_ref": {}})

	if reaped != 1 {
		t.Errorf("reaped count = %d, want 1 (only sess_orphan)", reaped)
	}
	if !exists(t, filepath.Join(root, "h", "sess_ref")) {
		t.Error("referenced session was reaped")
	}
	if !exists(t, filepath.Join(root, "h", "sess_young")) {
		t.Error("young orphan was reaped despite the guard window")
	}
	if exists(t, filepath.Join(root, "h", "sess_orphan")) {
		t.Error("old orphan session dir survived the sweep")
	}
	if exists(t, filepath.Join(cliDir, "sess_orphan.history")) {
		t.Error("old orphan session sidecar survived the sweep")
	}
	if exists(t, v2) {
		t.Error("dead v2-engine file survived the sweep")
	}
}

func TestSweepEmptyDirIsNoop(t *testing.T) {
	// Missing sessions dir must not panic or error.
	if n := New(filepath.Join(t.TempDir(), "nonexistent")).Sweep(nil); n != 0 {
		t.Errorf("sweep of missing dir reaped %d, want 0", n)
	}
}

func TestNilReaperSafe(t *testing.T) {
	var r *Reaper
	r.Reap("sess_x") // must not panic
	if n := r.Sweep(nil); n != 0 {
		t.Errorf("nil reaper Sweep = %d, want 0", n)
	}
}
