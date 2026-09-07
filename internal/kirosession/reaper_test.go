package kirosession

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// testWorkspace is the workspace root every fixture session claims unless a case
// says otherwise. Every reap and every sweep is confined to it, so a fixture
// without a matching session.json is retained rather than reaped — which is what
// the mismatch and doubt cases below drive.
const testWorkspace = "/ws"

// makeSession creates a fake KAS session on disk belonging to testWorkspace.
func makeSession(t *testing.T, root, hash, id string, age time.Duration) {
	t.Helper()
	makeSessionIn(t, root, hash, id, age, `{"workspacePaths":["`+testWorkspace+`"]}`)
}

// makeSessionIn creates a fake KAS session on disk: a sessions/<hash>/<id>/ dir
// (with an inner file plus the session.json the workspace guard reads) and a
// sessions/cli/<id>.history sidecar, then back-dates both to age. record is
// written verbatim so a case can supply a foreign root, an empty list or
// undecodable bytes; an empty record writes no session.json at all.
func makeSessionIn(t *testing.T, root, hash, id string, age time.Duration, record string) {
	t.Helper()
	dir := filepath.Join(root, hash, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if record != "" {
		if err := os.WriteFile(filepath.Join(dir, sessionRecordName), []byte(record), 0o644); err != nil {
			t.Fatal(err)
		}
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

	New(root, testWorkspace).Reap("sess_gone")

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
	r := New(root, testWorkspace)
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

	reaped := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_ref": {}})

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
	if n := New(filepath.Join(t.TempDir(), "nonexistent"), testWorkspace).Sweep(nil); n != 0 {
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
// A config dir pointed elsewhere while KIRO_HOME still resolves to a shared
// `$HOME/.kiro` yields zero refs, and the caller's completeness flag cannot
// catch it — an empty store IS complete. Every session here is past the guard,
// so without the refusal this reaps all four.
func TestSweepRefusesEmptyKeepListAgainstPopulatedTree(t *testing.T) {
	root := t.TempDir()
	old := 30 * time.Minute
	for _, id := range []string{"sess_a", "sess_b", "sess_c"} {
		makeSession(t, root, "otherapp", id, old)
	}
	makeSession(t, root, "second_hash", "sess_d", old)

	for _, keep := range []map[string]struct{}{nil, {}} {
		if n := New(root, testWorkspace).Sweep(keep); n != 0 {
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
	if n := New(root, testWorkspace).Sweep(map[string]struct{}{}); n != 0 {
		t.Errorf("reaped %d on an empty tree, want 0", n)
	}
}

// countSessions is what the guard keys on, so it must see sessions across every
// workspace-hash directory, and it counts SESSIONS rather than files: one session
// leaves a dir plus one or more cli/ sidecars, and its number is what the guard's
// log states as how much is at stake.
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

	if got := New(root, testWorkspace).countSessions(); got != 3 {
		t.Errorf("countSessions() = %d, want 3", got)
	}
}

// TestSweep_ReapingASessionTakesItsSidecarsWhateverTheirAge pins the two things
// a reaped session dir owes: it counts, and its sidecars go with it.
//
// The guard spares a young ORPHAN because it may not be referenced yet; a young
// SIDECAR of an already-reapable session is not spared, since nothing will ever
// reference it again.
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

	reaped := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_live": {}})

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

	reaped := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_live": {}})

	if reaped != 1 {
		t.Errorf("Sweep() = %d, want 1: a stranded v3 sidecar is a session being reclaimed", reaped)
	}
	if exists(t, stranded) {
		t.Error("stranded sidecar survived the sweep")
	}
}

// TestReaperLogsOnlyWhatHappened pins every line this package emits, and its
// silence. Each reports an outcome the filesystem no longer shows, so the log is
// the whole record — and a line that fires when nothing went wrong is how a
// reader learns to skip all of them.
func TestReaperLogsOnlyWhatHappened(t *testing.T) {
	t.Run("a successful reap says nothing", func(t *testing.T) {
		root := t.TempDir()
		makeSession(t, root, "h", "sess_gone", 0)
		logs := captureLogs(t)

		New(root, testWorkspace).Reap("sess_gone")

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

		if n := New(root, testWorkspace).Sweep(map[string]struct{}{}); n != 0 {
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

		if n := New(root, testWorkspace).Sweep(map[string]struct{}{}); n != 0 {
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

		if n := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_live": {}}); n != 0 {
			t.Fatalf("Sweep() = %d, want 0: the only session is referenced", n)
		}
		if out := logs.String(); strings.Contains(out, "orphan sweep reaped sessions") {
			t.Errorf("logs = %q, must not claim a reap when nothing was reaped", out)
		}

		makeSession(t, root, "h", "sess_orphan", 30*time.Minute)
		logs2 := captureLogs(t)
		if n := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_live": {}}); n != 1 {
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

// TestReap_SkipsASessionInAnotherWorkspacesBucket covers the DELETE path, which
// is the one an unguarded hash makes reachable without any collision: Reap
// locates a session by globbing sessions/*/sess_<id> and never reads the hash, so
// a bucket belonging to another workspace root under the same Kiro home is
// matched exactly like this workspace's own.
func TestReap_SkipsASessionInAnotherWorkspacesBucket(t *testing.T) {
	root := t.TempDir()
	makeSessionIn(t, root, "ourhash", "sess_dup", 0, `{"workspacePaths":["`+testWorkspace+`"]}`)
	makeSessionIn(t, root, "otherhash", "sess_dup", 0, `{"workspacePaths":["/other/ws"]}`)

	New(root, testWorkspace).Reap("sess_dup")

	if exists(t, filepath.Join(root, "ourhash", "sess_dup")) {
		t.Error("this workspace's own session survived the reap it asked for")
	}
	if !exists(t, filepath.Join(root, "otherhash", "sess_dup")) {
		t.Error("a session whose record names another workspace was reaped: the glob crossed buckets")
	}
}

// TestSweep_SkipsSessionsThatDoNotNameThisWorkspace protects every session
// another client in this Kiro home created: unreferenced by construction, since
// the keep-list is built from vibekit's own chats.
//
// The trailing case is what keeps this from being a hash comparison in disguise:
// the record decides, not the path.
func TestSweep_SkipsSessionsThatDoNotNameThisWorkspace(t *testing.T) {
	root := t.TempDir()
	old := 30 * time.Minute
	makeSessionIn(t, root, "h", "sess_ours", old, `{"workspacePaths":["`+testWorkspace+`"]}`)
	makeSessionIn(t, root, "h", "sess_theirs", old, `{"workspacePaths":["/other/ws"]}`)
	makeSessionIn(t, root, "elsewhere", "sess_ours_elsewhere", old,
		`{"workspacePaths":["/other/ws","`+testWorkspace+`"]}`)

	reaped := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_live": {}})

	if reaped != 2 {
		t.Errorf("Sweep() = %d, want 2: both sessions naming this workspace, and only those", reaped)
	}
	if exists(t, filepath.Join(root, "h", "sess_ours")) {
		t.Error("an orphan naming this workspace survived")
	}
	if exists(t, filepath.Join(root, "elsewhere", "sess_ours_elsewhere")) {
		t.Error("an orphan naming this workspace among several survived because its bucket looked foreign")
	}
	if !exists(t, filepath.Join(root, "h", "sess_theirs")) {
		t.Error("an orphan naming another workspace was deleted: that is another workspace's history")
	}
	// The sidecar is the narrower route to the same loss: it carries no
	// workspacePaths of its own, so a guard on the dirs alone spares the session
	// and deletes its history file anyway.
	if !exists(t, filepath.Join(root, "cli", "sess_theirs.history")) {
		t.Error("another workspace's cli sidecar was deleted while its session dir was spared")
	}
}

// TestSweep_DoubtRetains pins the three doubt cases, the ones a future KAS
// version can produce by accident. Nothing distinguishes "KAS stopped writing
// workspacePaths" from "not ours" at the unlink, and reaping costs unrecoverable
// history, so retaining is the answer for all four.
func TestSweep_DoubtRetains(t *testing.T) {
	old := 30 * time.Minute
	for _, tc := range []struct {
		desc   string
		record string
	}{
		{desc: "no session.json at all", record: ""},
		{desc: "undecodable session.json", record: `{`},
		{desc: "absent workspacePaths", record: `{"id":"sess_x"}`},
		{desc: "empty workspacePaths", record: `{"workspacePaths":[]}`},
		{desc: "an empty entry in workspacePaths", record: `{"workspacePaths":[""]}`},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			root := t.TempDir()
			makeSessionIn(t, root, "h", "sess_doubt", old, tc.record)

			if n := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_live": {}}); n != 0 {
				t.Errorf("Sweep() = %d, want 0: doubt retains", n)
			}
			if !exists(t, filepath.Join(root, "h", "sess_doubt")) {
				t.Errorf("a session whose ownership could not be established (%s) was deleted", tc.desc)
			}
		})
	}
}

// TestReap_NonCanonicalWorkspaceRootsStillMatch pins the one normalization the
// guard does, and its limit. A trailing slash or an interior "." on either side
// is the same root and must still match, because a caller's KIRO_WORK_DIR and
// KAS's own record are written by different programs. A path that only resolves
// to the same directory through a symlink does NOT match, and that is the doubt
// direction again: nothing here calls EvalSymlinks.
func TestReap_NonCanonicalWorkspaceRootsStillMatch(t *testing.T) {
	root := t.TempDir()
	makeSessionIn(t, root, "h", "sess_slash", 0, `{"workspacePaths":["/ws/"]}`)
	makeSessionIn(t, root, "h", "sess_dot", 0, `{"workspacePaths":["/ws/./"]}`)

	r := New(root, "/ws/")
	r.Reap("sess_slash")
	r.Reap("sess_dot")

	for _, id := range []string{"sess_slash", "sess_dot"} {
		if exists(t, filepath.Join(root, "h", id)) {
			t.Errorf("%s survived: a trailing slash made the same root read as a different one", id)
		}
	}
}

// stepRecord is a workflow STEP session's own session.json, in the shape measured
// on the live volume: workspacePaths beside a `_meta.kiro.workflow` block whose
// workflowId names the run that created the step.
func stepRecord(workflowID string) string {
	return `{"workspacePaths":["` + testWorkspace + `"],` +
		`"_meta":{"kiro":{"workflow":{"workflowId":"` + workflowID + `",` +
		`"nodeId":"implement","nodePath":["` + workflowID + `","loop","iter-0","implement"],"type":"step"}}}}`
}

// makeRunDir creates the run-state directory KAS keeps a workflow run in, inside
// one workspace-hash bucket.
func makeRunDir(t *testing.T, root, hash, workflowID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, hash, workflowsDirName, workflowID), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestSweep_SparesAStepSessionWhoseRunStillExists is the prerequisite for reading
// a step's transcript back at all.
//
// A step session is referenced by no chat and is no bridge's own, so it is an
// orphan from creation and the sweep was reaping steps MID-RUN. The rule: spared
// while its run is on disk, reaped once the run is gone, unchanged for a
// non-step.
func TestSweep_SparesAStepSessionWhoseRunStillExists(t *testing.T) {
	old := 30 * time.Minute
	for _, tc := range []struct {
		desc    string
		record  string
		makeRun bool
		want    bool // want reaped
	}{
		{
			desc:    "a step session whose run directory exists is SPARED",
			record:  stepRecord("wf_live"),
			makeRun: true,
			want:    false,
		},
		{
			desc:   "the same step session with no run directory is reaped",
			record: stepRecord("wf_gone"),
			want:   true,
		},
		{
			desc:   "a session with no _meta block is reaped as before",
			record: `{"workspacePaths":["` + testWorkspace + `"]}`,
			want:   true,
		},
		{
			// The `_meta` block is present but the workflow id is empty, which is
			// what a non-step session carrying other kiro metadata looks like. An
			// unconditional run-dir stat would spare it, because
			// `<bucket>/workflows/` itself exists as soon as any run has ever run.
			desc:    "an empty workflowId is not a step, so the run tree does not spare it",
			record:  `{"workspacePaths":["` + testWorkspace + `"],"_meta":{"kiro":{"workflow":{"workflowId":""}}}}`,
			makeRun: true,
			want:    true,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			root := t.TempDir()
			makeSessionIn(t, root, "h", "sess_step", old, tc.record)
			if tc.makeRun {
				makeRunDir(t, root, "h", "wf_live")
			}

			n := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_live": {}})

			gone := !exists(t, filepath.Join(root, "h", "sess_step"))
			if gone != tc.want {
				t.Errorf("reaped = %v, want %v (Sweep reported %d)", gone, tc.want, n)
			}
			// The sidecar goes with the dir and stays with it, or a spared step
			// session loses the history half of its state anyway.
			hist := exists(t, filepath.Join(root, "cli", "sess_step.history"))
			if hist == tc.want {
				t.Errorf("sidecar present = %v, want %v", hist, !tc.want)
			}
		})
	}
}

// TestSweep_AStepSessionIsNotSparedByARunInAnotherBucket pins the containment
// half. A workspace-hash bucket is one workspace's tree, so a run directory in
// somebody else's bucket says nothing about whether this step's run still exists —
// and looking across buckets would let a foreign volume's run pin our disk
// indefinitely.
func TestSweep_AStepSessionIsNotSparedByARunInAnotherBucket(t *testing.T) {
	root := t.TempDir()
	makeSessionIn(t, root, "h", "sess_step", 30*time.Minute, stepRecord("wf_live"))
	makeRunDir(t, root, "elsewhere", "wf_live")

	if n := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_live": {}}); n != 1 {
		t.Errorf("Sweep() = %d, want 1", n)
	}
	if exists(t, filepath.Join(root, "h", "sess_step")) {
		t.Error("a step session was spared by a same-named run in another workspace bucket")
	}
}

// TestSweep_AStepSessionInAForeignWorkspaceIsStillNotOurs pins the ORDER of the
// two questions orphanReapable asks off one read: the workspace check comes first,
// so the spare can never widen ownership. A foreign step session is skipped
// either way, and it must be skipped for the workspace reason.
func TestSweep_AStepSessionInAForeignWorkspaceIsStillNotOurs(t *testing.T) {
	root := t.TempDir()
	makeSessionIn(t, root, "h", "sess_theirs", 30*time.Minute,
		`{"workspacePaths":["/other"],"_meta":{"kiro":{"workflow":{"workflowId":"wf_x"}}}}`)

	if n := New(root, testWorkspace).Sweep(map[string]struct{}{"sess_live": {}}); n != 0 {
		t.Errorf("Sweep() = %d, want 0", n)
	}
	if !exists(t, filepath.Join(root, "h", "sess_theirs")) {
		t.Error("another workspace's step session was deleted")
	}
}

// TestReap_StillTakesAStepSession pins that the spare is the SWEEP's alone.
// Deleting a chat must still reap that chat's sessions, and the direct path is
// also what a run delete would use — so a spare there would make a step session
// unreclaimable by any gesture.
func TestReap_StillTakesAStepSession(t *testing.T) {
	root := t.TempDir()
	makeSessionIn(t, root, "h", "sess_step", 0, stepRecord("wf_live"))
	makeRunDir(t, root, "h", "wf_live")

	New(root, testWorkspace).Reap("sess_step")

	if exists(t, filepath.Join(root, "h", "sess_step")) {
		t.Error("a direct Reap spared a step session: the spare belongs to the sweep only")
	}
}

// TestReadSessionRecord pins the decode itself, including the two shapes the
// sweep's two answers come apart on.
func TestReadSessionRecord(t *testing.T) {
	for _, tc := range []struct {
		desc      string
		record    string
		wantPaths []string
		wantWfID  string
		wantOK    bool
	}{
		{desc: "no file", record: "", wantOK: false},
		{desc: "undecodable", record: `{`, wantOK: false},
		{
			desc:      "an ordinary session",
			record:    `{"workspacePaths":["/ws"]}`,
			wantPaths: []string{"/ws"},
			wantOK:    true,
		},
		{
			desc:      "a step session",
			record:    stepRecord("wf_7"),
			wantPaths: []string{testWorkspace},
			wantWfID:  "wf_7",
			wantOK:    true,
		},
		{
			// Decodable and present, with no paths at all: ok is true and the
			// CALLER decides, which is what keeps doubt-retains a policy rather
			// than a decode outcome.
			desc:   "readable but empty",
			record: `{}`,
			wantOK: true,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			if tc.record != "" {
				if err := os.WriteFile(filepath.Join(dir, sessionRecordName), []byte(tc.record), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			paths, wfID, ok := readSessionRecord(dir)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !slices.Equal(paths, tc.wantPaths) {
				t.Errorf("workspacePaths = %v, want %v", paths, tc.wantPaths)
			}
			if wfID != tc.wantWfID {
				t.Errorf("workflowID = %q, want %q", wfID, tc.wantWfID)
			}
		})
	}
}
