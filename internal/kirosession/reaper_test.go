package kirosession

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// An EMPTY keep-list against a populated tree is refused, not obeyed.
//
// This is the incident test. A dev build ran with KIRO_CONFIG_DIR pointed at a
// scratch directory while KIRO_HOME was left unset, so the chat store was empty
// while the session store still resolved to the real `$HOME/.kiro` shared with
// another application. The startup sweep received a keep-list of zero refs,
// treated it as authoritative, and deleted ~450 sessions belonging to that other
// app. The caller's completeness flag could not catch it: an empty store IS
// complete, having successfully read all zero of its files.
//
// Every session here is old enough to be past the guard, so without the refusal
// this test would reap all four.
func TestSweepRefusesEmptyKeepListAgainstPopulatedTree(t *testing.T) {
	root := t.TempDir()
	old := 30 * time.Minute
	for _, id := range []string{"sess_a", "sess_b", "sess_c"} {
		makeSession(t, root, "otherapp", id, old)
	}
	makeSession(t, root, "second_hash", "sess_d", old)

	for _, keep := range []map[string]struct{}{nil, {}} {
		if n := New(root).Sweep(keep); n != 0 {
			t.Errorf("Sweep(%v) reaped %d, want 0 — an empty keep-list must be refused", keep, n)
		}
	}

	for _, p := range []string{
		filepath.Join(root, "otherapp", "sess_a"),
		filepath.Join(root, "otherapp", "sess_b"),
		filepath.Join(root, "otherapp", "sess_c"),
		filepath.Join(root, "second_hash", "sess_d"),
	} {
		if !exists(t, p) {
			t.Errorf("%s was deleted by a zero-reference sweep", p)
		}
	}
}

// The refusal is scoped to "there is something to lose". A fresh volume has an
// empty keep-list AND an empty tree, which is an ordinary no-op — refusing there
// would be indistinguishable from working, but returning early on a populated
// tree only is what keeps the guard honest rather than a blanket disable.
func TestSweepEmptyKeepListOnEmptyTreeIsOrdinaryNoop(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if n := New(root).Sweep(map[string]struct{}{}); n != 0 {
		t.Errorf("reaped %d on an empty tree, want 0", n)
	}
}

// countSessions is what the guard keys on, so it must see sessions across every
// workspace-hash directory — the bug deleted across ten of them.
//
// It counts SESSIONS, not files. A session leaves a dir under its workspace hash
// and one or more sidecars under cli/, so counting the sidecars instead reports a
// number the guard's log line then states as how much is at stake — and an
// operator deciding whether to repair a volume reads that number.
func TestCountSessionsSpansWorkspaceHashes(t *testing.T) {
	root := t.TempDir()
	makeSession(t, root, "h1", "sess_1", 0)
	makeSession(t, root, "h1", "sess_2", 0)
	makeSession(t, root, "h2", "sess_3", 0)
	// A bare-uuid v2 file is not a session and must not inflate the count.
	if err := os.WriteFile(filepath.Join(root, "cli", "1111-2222.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nor does a second sidecar for a session already counted once.
	if err := os.WriteFile(filepath.Join(root, "cli", "sess_1.state"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := New(root).countSessions(); got != 3 {
		t.Errorf("countSessions() = %d, want 3", got)
	}
}

// TestSweep_ReapingASessionTakesItsSidecarsWhateverTheirAge pins the two things
// a reaped session dir owes: it counts, and its sidecars go with it.
//
// The guard window spares a YOUNG orphan because a session being created right
// now has no reference yet. It does not follow that a young SIDECAR of a session
// already judged reapable is spared: the session it belongs to is gone, so
// nothing will ever reference it again and the age that would normally protect it
// is meaningless. And the returned count is what the caller logs, so a dir this
// sweep really removed has to be in it.
func TestSweep_ReapingASessionTakesItsSidecarsWhateverTheirAge(t *testing.T) {
	root := t.TempDir()
	makeSession(t, root, "h", "sess_orphan", 30*time.Minute)

	// Freshen only the sidecar, so the dir is past the guard and the sidecar is
	// not. sweepCLI would spare it; the dir's own reap must not.
	sidecar := filepath.Join(root, "cli", "sess_orphan.history")
	now := time.Now()
	if err := os.Chtimes(sidecar, now, now); err != nil {
		t.Fatal(err)
	}

	reaped := New(root).Sweep(map[string]struct{}{"sess_live": {}})

	if reaped != 1 {
		t.Errorf("Sweep() = %d, want 1: the session dir it removed must be counted", reaped)
	}
	if exists(t, filepath.Join(root, "h", "sess_orphan")) {
		t.Error("old orphan session dir survived the sweep")
	}
	if exists(t, sidecar) {
		t.Error("a reaped session's sidecar survived because it was young; nothing will ever reference it again")
	}
}

// TestSweep_CountsAStrandedSidecarAsAReapedSession pins the other half of the
// count: a v3 sidecar whose session dir is already gone.
//
// This is the shape a crash between the two removals leaves behind, and it is a
// SESSION being reclaimed rather than incidental cleanup — unlike a dead v2 file,
// which is deliberately not counted. The count is what the caller logs, so
// reclaiming one and reporting nothing makes the sweep look idle while it works.
func TestSweep_CountsAStrandedSidecarAsAReapedSession(t *testing.T) {
	root := t.TempDir()
	cliDir := filepath.Join(root, "cli")
	if err := os.MkdirAll(cliDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stranded := filepath.Join(cliDir, "sess_stranded.history")
	if err := os.WriteFile(stranded, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(stranded, when, when); err != nil {
		t.Fatal(err)
	}

	reaped := New(root).Sweep(map[string]struct{}{"sess_live": {}})

	if reaped != 1 {
		t.Errorf("Sweep() = %d, want 1: a stranded v3 sidecar is a session being reclaimed", reaped)
	}
	if exists(t, stranded) {
		t.Error("stranded sidecar survived the sweep")
	}
}

// TestReaperLogsOnlyWhatHappened pins every line this package emits, and its
// silence.
//
// Each of them reports an outcome the filesystem no longer shows: a removal that
// failed leaves the entry behind, but the same call succeeding leaves nothing to
// look at either way, so the log is the whole record. Two of the lines are load
// bearing — the refusal is what stopped ~450 sessions of another application
// being deleted, and the reaped count is what says a sweep did anything — and a
// line that fires when nothing went wrong is how a reader learns to skip all of
// them.
func TestReaperLogsOnlyWhatHappened(t *testing.T) {
	t.Run("a successful reap says nothing", func(t *testing.T) {
		root := t.TempDir()
		makeSession(t, root, "h", "sess_gone", 0)
		logs := captureLogs(t)

		New(root).Reap("sess_gone")

		out := logs.String()
		if exists(t, filepath.Join(root, "h", "sess_gone")) {
			t.Fatal("the session dir survived, so this case is not testing the success path")
		}
		if strings.Contains(out, "kirosession: remove session dir") {
			t.Errorf("logs = %q, must not report a removal failure for a dir that was removed", out)
		}
		if strings.Contains(out, "kirosession: remove cli sidecar") {
			t.Errorf("logs = %q, must not report a sidecar failure for a sidecar that was removed", out)
		}
	})

	t.Run("a refused sweep names what is at stake", func(t *testing.T) {
		root := t.TempDir()
		makeSession(t, root, "otherapp", "sess_a", 30*time.Minute)
		makeSession(t, root, "otherapp", "sess_b", 30*time.Minute)
		logs := captureLogs(t)

		if n := New(root).Sweep(map[string]struct{}{}); n != 0 {
			t.Fatalf("Sweep(empty keep-list) = %d, want 0", n)
		}
		out := logs.String()
		if !strings.Contains(out, "REFUSING orphan sweep") {
			t.Errorf("logs = %q, want the refusal recorded: it is the only trace of a misconfigured volume", out)
		}
		if !strings.Contains(out, "sessions_on_disk=2") {
			t.Errorf("logs = %q, want sessions_on_disk=2: the count is what an operator acts on", out)
		}
	})

	t.Run("an empty tree with no references is silent", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "cli"), 0o755); err != nil {
			t.Fatal(err)
		}
		logs := captureLogs(t)

		if n := New(root).Sweep(map[string]struct{}{}); n != 0 {
			t.Fatalf("Sweep() = %d, want 0", n)
		}
		if out := logs.String(); strings.Contains(out, "REFUSING orphan sweep") {
			t.Errorf("logs = %q, must not refuse on a fresh volume: there is nothing to lose", out)
		}
	})

	t.Run("a sweep that reaps reports its count, and one that does not stays quiet", func(t *testing.T) {
		root := t.TempDir()
		makeSession(t, root, "h", "sess_live", 30*time.Minute)
		logs := captureLogs(t)

		if n := New(root).Sweep(map[string]struct{}{"sess_live": {}}); n != 0 {
			t.Fatalf("Sweep() = %d, want 0: the only session is referenced", n)
		}
		if out := logs.String(); strings.Contains(out, "orphan sweep reaped sessions") {
			t.Errorf("logs = %q, must not claim a reap when nothing was reaped", out)
		}

		makeSession(t, root, "h", "sess_orphan", 30*time.Minute)
		logs2 := captureLogs(t)
		if n := New(root).Sweep(map[string]struct{}{"sess_live": {}}); n != 1 {
			t.Fatalf("Sweep() = %d, want 1", n)
		}
		out := logs2.String()
		if !strings.Contains(out, "orphan sweep reaped sessions") {
			t.Errorf("logs = %q, want the reap recorded", out)
		}
		if !strings.Contains(out, "count=1") {
			t.Errorf("logs = %q, want count=1", out)
		}
	})
}

// captureLogs swaps the slog default to a buffer-backed debug handler for the
// duration of the test and restores it on cleanup. The handler is global, so this
// package's tests never run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}
