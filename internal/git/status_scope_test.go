package git

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// mkRepo creates a git repository at name under workDir and returns its path.
func mkRepo(t *testing.T, workDir, name string) string {
	t.Helper()
	dir := filepath.Join(workDir, name)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("Setup: mkdir %s: %v", name, err)
	}
	initFixtureRepo(t, dir)
	return dir
}

// snapshotRepos is the repository names the holder currently answers with, sorted
// so an assertion does not depend on scan order.
func snapshotRepos(h *Handler, key string) []string {
	snap, _ := h.statusCache.read(key)
	names := make([]string, 0, len(snap.rows()))
	for _, r := range snap.rows() {
		names = append(names, r.Repo)
	}
	slices.Sort(names)
	return names
}

// waitForRepoRow polls the holder until the row for repo carries the marker a
// scoped scan would have produced: a dirty working tree.
func waitForRepoRow(t *testing.T, h *Handler, key, repo string, wantDirty bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, _ := h.statusCache.read(key)
		for _, r := range snap.rows() {
			if r.Repo == repo && len(r.Files) > 0 == wantDirty {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("row for %q never reported dirty=%v after 10s; snapshot = %+v",
				repo, wantDirty, snapshotRepos(h, key))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A scoped read rescans ONLY the repositories owning the named paths and MERGES the
// result, so every other row survives.
//
// This is the whole point of `?paths=`: a single-file edit costs one repository's two
// git subprocesses rather than the whole tree's, which is what makes a refresh per
// edit affordable at all. The fixture makes both halves falsifiable — one repo is
// dirtied and the other is not, and the untouched row is a SEEDED name that exists
// in no workspace, so a full rescan would delete it.
func TestHandleStatusAll_ScopedReadRescansOnlyTheOwningRepo(t *testing.T) {
	skipNoGit(t)
	workDir := t.TempDir()
	scanned := mkRepo(t, workDir, "scanned")
	skipped := mkRepo(t, workDir, "skipped")
	h := NewHandler(workDir)
	// A snapshot in which both real repos are clean, plus a row for a repository
	// that does not exist. A full rescan cannot reproduce the third row.
	seedSnapshot(&h.statusCache, statusKeyPoll, []allRepoStatus{
		repoRow("scanned"), repoRow("skipped"), repoRow("seeded"),
	}, time.Now())
	// BOTH repos are dirtied on disk. That is what makes the scope falsifiable: the
	// snapshot says both are clean, so a scan that looked at `skipped` would report
	// it dirty, and only a scan that did not look leaves its seeded row alone. With
	// `skipped` genuinely clean the assertion would pass under a full rescan too.
	for _, dir := range []string{scanned, skipped} {
		if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("Setup: write in %s: %v", dir, err)
		}
	}

	got := getStatusAll(t, h, "?paths=scanned/new.txt")
	if len(got.Repos) != 3 {
		t.Fatalf("repos = %+v, want all three rows: a scoped read answers the whole "+
			"list, never the scanned subset", got.Repos)
	}

	waitForRepoRow(t, h, statusKeyPoll, "scanned", true)
	if names := snapshotRepos(h, statusKeyPoll); !slices.Equal(names, []string{"scanned", "seeded", "skipped"}) {
		t.Errorf("snapshot repos = %v, want all three kept; the scoped result REPLACED "+
			"the snapshot instead of merging into it", names)
	}
	// The row the scan did not look at keeps the snapshot's answer, dirty tree and
	// all — and "seeded" is the row no scan could recreate.
	snap, _ := h.statusCache.read(statusKeyPoll)
	for _, r := range snap.rows() {
		if r.Repo == "skipped" && len(r.Files) != 0 {
			t.Errorf("skipped reported %d files; it was rescanned when the scope named "+
				"only `scanned`, so the refresh cost the whole tree", len(r.Files))
		}
	}
}

// The snapshot's timestamp does NOT move on a scoped merge.
//
// `at` answers "when was the WHOLE tree last known", which is the question `age_ms`
// and stale() ask. A scoped refresh answers it for one repository out of fifty-five,
// so moving the stamp would claim the other fifty-four were rescanned AND would
// suppress the periodic full refresh for as long as edits kept arriving — an
// unscoped read would find a snapshot that never looks stale.
func TestHandleStatusAll_AScopedMergeDoesNotFreshenTheSnapshot(t *testing.T) {
	skipNoGit(t)
	workDir := t.TempDir()
	repo := mkRepo(t, workDir, "one")
	h := NewHandler(workDir)
	old := time.Now().Add(-time.Minute)
	// Seeded CLEAN and then dirtied, so the merged row differs from the seeded one.
	// Without that difference there is nothing to wait for and the assertion below
	// would read the pre-merge snapshot — passing whatever the merge does to the
	// stamp.
	seedSnapshot(&h.statusCache, statusKeyPoll, []allRepoStatus{repoRow("one")}, old)
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("Setup: write: %v", err)
	}

	got := getStatusAll(t, h, "?paths=one/f.txt")
	if got.AgeMS < 60_000 {
		t.Errorf("age_ms = %d, want at least 60000: the answer claimed a freshness the "+
			"scoped scan did not establish", got.AgeMS)
	}

	// The dirty row is the merge landing; only then is the stamp worth reading.
	waitForRepoRow(t, h, statusKeyPoll, "one", true)
	snap, _ := h.statusCache.read(statusKeyPoll)
	if snap.at.After(old) {
		t.Errorf("snapshot at = %v, want the original %v; a scoped merge freshened it and "+
			"an unscoped read will never see it as stale", snap.at, old)
	}
}

// A path no repository owns is DROPPED, and a request whose every path is
// unowned scans nothing at all.
//
// Both halves matter. Erroring would break the caller that sends a turn's changed
// files, which legitimately include paths outside every worktree; falling through to
// a full scan would make an unresolvable path the most expensive kind of request.
func TestHandleStatusAll_AnUnownedPathScansNothing(t *testing.T) {
	workDir := t.TempDir()
	mkRepo(t, workDir, "one")
	h := NewHandler(workDir)
	seedSnapshot(&h.statusCache, statusKeyPoll, []allRepoStatus{repoRow("seeded")}, time.Now())

	got := getStatusAll(t, h, "?paths=nowhere/f.txt,../outside.txt")

	if len(got.Repos) != 1 || got.Repos[0].Repo != "seeded" {
		t.Errorf("repos = %+v, want the snapshot untouched", got.Repos)
	}
	if got.Scanning {
		t.Error("scanning = true; an unresolvable scope started a scan, which is the " +
			"full-tree cost it exists to avoid")
	}
}

// A COLD read carrying paths falls through to the FULL scan, because there is no
// snapshot to merge a partial result into.
//
// Publishing a two-repo result as though it were a whole scan is the harm: every
// later read answers from it, `age_ms` says it is fresh, and no field can say it
// covered two repositories out of fifty-five. So the first read of a process pays
// for the tree once and every scoped read after it is cheap.
func TestHandleStatusAll_AColdScopedReadScansTheWholeTree(t *testing.T) {
	skipNoGit(t)
	workDir := t.TempDir()
	mkRepo(t, workDir, "one")
	mkRepo(t, workDir, "two")
	h := NewHandler(workDir)

	got := getStatusAll(t, h, "?paths=one/f.txt")

	if names := len(got.Repos); names != 2 {
		t.Errorf("repos = %+v, want both repositories: a cold scoped read published a "+
			"partial scan as a complete one", got.Repos)
	}
}

// A scoped read that finds a scan already running records its repositories as
// PENDING, and the running refresh drains them.
//
// Without this the one-at-a-time rule loses work: the joining read returns having
// scanned nothing, and if the scan it joined was scoped to a different repository,
// its own repository stays stale until the next full scan — which is the defect
// scoping was added to fix, arriving through the slot instead of through the timer.
func TestStatusCache_AScopedReadJoiningAScanIsNotDropped(t *testing.T) {
	var c statusCache
	seedSnapshot(&c, statusKeyPoll, []allRepoStatus{repoRow("a"), repoRow("b")}, time.Now())

	first, started := c.claimScoped(statusKeyPoll, map[string]struct{}{"a": {}})
	if !started {
		t.Fatal("Setup: the first scoped claim did not start")
	}
	second, started := c.claimScoped(statusKeyPoll, map[string]struct{}{"b": {}})
	if started {
		t.Error("a second scoped claim started a concurrent scan; the subprocess bound is gone")
	}
	if second != first {
		t.Error("the joining read got a different channel; it would wait on a refresh nobody runs")
	}

	// The first pass publishes "a" and is handed "b" to go round again.
	next := c.mergeScoped(statusKeyPoll, []allRepoStatus{{Repo: "a", IsRepo: true, Files: []gitFile{{Path: "x"}}}})
	if _, want := next["b"]; !want || len(next) != 1 {
		t.Fatalf("mergeScoped returned %v, want exactly {b}: the joining read's repository "+
			"was dropped and its rows stay stale", next)
	}
	// Waiters are woken either way, because the snapshot they were waiting on moved.
	select {
	case <-first:
	default:
		t.Error("mergeScoped left the waiters blocked on a channel it replaced")
	}
	// And the slot is STILL claimed, so a third read joins this chain rather than
	// starting a second concurrent scan.
	if _, again := c.claimScoped(statusKeyPoll, nil); again {
		t.Error("the slot opened between passes; a third read would start a concurrent scan")
	}

	// The second pass finds nothing accumulated, so it releases.
	if last := c.mergeScoped(statusKeyPoll, []allRepoStatus{repoRow("b")}); len(last) != 0 {
		t.Errorf("mergeScoped returned %v after an idle pass, want nothing: the chain would never end", last)
	}
	if _, running := c.read(statusKeyPoll); running != nil {
		t.Error("the slot is still in flight after the chain ended; the variant would never refresh again")
	}
}

// A FULL scan covers every repository, so anything a scoped read asked for while it
// ran is already answered and the pending set is cleared.
//
// Left uncleared, the full scan's publish would release the slot with work still
// recorded and nobody looping — so the next scoped read would inherit a stale
// pending set and rescan repositories for no reason.
func TestStatusCache_AFullScanSatisfiesEveryPendingScope(t *testing.T) {
	var c statusCache
	if _, started := c.claim(statusKeyPoll); !started {
		t.Fatal("Setup: claim did not start")
	}
	if _, started := c.claimScoped(statusKeyPoll, map[string]struct{}{"a": {}}); started {
		t.Fatal("Setup: the scoped read did not join the running full scan")
	}

	c.publish(statusKeyPoll, []allRepoStatus{repoRow("a"), repoRow("b")})

	c.mu.Lock()
	pending := c.slots[statusKeyPoll].pending
	c.mu.Unlock()
	if len(pending) != 0 {
		t.Errorf("pending = %v after a full publish, want empty: a full scan looked at "+
			"every repository", pending)
	}
}

// mergeScoped appends a row the snapshot does not hold, which is how a repository
// cloned since the last full scan reaches the answer at all.
func TestStatusCache_AScopedMergeAppendsAnUnknownRepo(t *testing.T) {
	var c statusCache
	seedSnapshot(&c, statusKeyPoll, []allRepoStatus{repoRow("old")}, time.Now())
	if _, started := c.claimScoped(statusKeyPoll, map[string]struct{}{"fresh": {}}); !started {
		t.Fatal("Setup: claim did not start")
	}

	c.mergeScoped(statusKeyPoll, []allRepoStatus{repoRow("fresh")})

	snap, _ := c.read(statusKeyPoll)
	names := make([]string, 0, 2)
	for _, r := range snap.rows() {
		names = append(names, r.Repo)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"fresh", "old"}) {
		t.Errorf("repos = %v, want both: a repo cloned since the last full scan never "+
			"reaches the dashboard", names)
	}
}

// The cold wait matches the SCAN's budget, not the per-repo one.
//
// A read that gives up before the scan it is waiting on answers `{repos: [],
// scanning: true}`, and a client reading `repos` renders "no repositories" — a claim
// about the tree rather than about the read. The old value was the 10s per-repo
// budget against a 30s scan, which on a ~55-repo tree is not obviously enough.
func TestStatusAll_ColdWaitCoversTheWholeScan(t *testing.T) {
	if statusColdWait < statusScanBudget {
		t.Errorf("statusColdWait = %v, want at least the scan budget %v: a cold read can "+
			"time out while the scan still has time left, and it then answers an empty "+
			"list that renders as \"no repositories\"", statusColdWait, statusScanBudget)
	}
	h := NewHandler(t.TempDir())
	if got := h.coldWait(nil, false); got != statusColdWait {
		t.Errorf("coldWait(cold, poll) = %v, want %v", got, statusColdWait)
	}
	// And a read that HAS a snapshot never waits: that is what makes the poll cheap.
	if got := h.coldWait(&statusSnapshot{at: time.Now()}, false); got != 0 {
		t.Errorf("coldWait(warm, poll) = %v, want 0", got)
	}
}

// statusScope resolves the query into repository names, and this is the table for
// what it accepts. Driven through the handler's own resolver rather than the HTTP
// surface so each rule fails on its own.
func TestStatusScope(t *testing.T) {
	workDir := t.TempDir()
	// The WORKSPACE ROOT is a repository too, which is what makes the traversal case
	// below mean anything: "." owns every path no subdirectory repo claims, so
	// without the escape check `../etc/passwd` resolves to it and a file outside the
	// tree scopes the refresh to the root repo.
	for _, name := range []string{".", "alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(workDir, name, ".git"), 0o750); err != nil {
			t.Fatalf("Setup: mkdir %s: %v", name, err)
		}
	}
	h := NewHandler(workDir)
	warm := &statusSnapshot{at: time.Now()}

	cases := []struct {
		name       string
		query      string
		snap       *statusSnapshot
		wantScoped bool
		wantRepos  []string
	}{
		{
			name:       "no paths is not a scoped read",
			query:      "",
			snap:       warm,
			wantScoped: false,
		},
		{
			name:       "one path resolves to its owning repository",
			query:      "?paths=alpha/main.go",
			snap:       warm,
			wantScoped: true,
			wantRepos:  []string{"alpha"},
		},
		{
			name:       "two paths in one repository are one repository",
			query:      "?paths=alpha/main.go,alpha/other.go",
			snap:       warm,
			wantScoped: true,
			wantRepos:  []string{"alpha"},
		},
		{
			name:       "paths across repositories name both",
			query:      "?paths=alpha/main.go,beta/app.ts",
			snap:       warm,
			wantScoped: true,
			wantRepos:  []string{"alpha", "beta"},
		},
		{
			// A path that escapes the workspace is owned by nothing HERE. Checked
			// before ownerOf because the workspace-root repo (".") owns every path no
			// subdirectory repo claims, so this would otherwise scope the refresh to
			// the root repo for a file outside the tree.
			name:       "a traversal is owned by nothing",
			query:      "?paths=../etc/passwd",
			snap:       warm,
			wantScoped: true,
			wantRepos:  nil,
		},
		{
			name:       "empty entries are skipped, not resolved",
			query:      "?paths=,,alpha/main.go,",
			snap:       warm,
			wantScoped: true,
			wantRepos:  []string{"alpha"},
		},
		{
			// The precondition for a merge. A cold read has nothing to merge into.
			name:       "a cold read carrying paths is not scoped",
			query:      "?paths=alpha/main.go",
			snap:       nil,
			wantScoped: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/git/status-all"+c.query, nil)
			scoped, only := h.statusScope(req, c.snap)
			if scoped != c.wantScoped {
				t.Fatalf("statusScope(%q) scoped = %v, want %v", c.query, scoped, c.wantScoped)
			}
			got := make([]string, 0, len(only))
			for name := range only {
				got = append(got, name)
			}
			slices.Sort(got)
			if !slices.Equal(got, c.wantRepos) {
				t.Errorf("statusScope(%q) repos = %v, want %v", c.query, got, c.wantRepos)
			}
		})
	}
}

// The path count is capped, so one request cannot make the server resolve an
// unbounded list. A scoped refresh exists to scan FEWER repositories than a full
// one, so a caller naming more paths than the workspace has repos is asking for the
// full scan by a longer route.
func TestStatusScope_CapsThePathCount(t *testing.T) {
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "last", ".git"), 0o750); err != nil {
		t.Fatalf("Setup: mkdir: %v", err)
	}
	h := NewHandler(workDir)

	// statusPathsMax unowned paths, then a real one: the cap must cut before it.
	query := "?paths="
	for range statusPathsMax {
		query += "nowhere/f.txt,"
	}
	query += "last/main.go"
	req := httptest.NewRequest(http.MethodGet, "/api/git/status-all"+query, nil)

	scoped, only := h.statusScope(req, &statusSnapshot{at: time.Now()})
	if !scoped {
		t.Fatal("statusScope reported unscoped for a request carrying paths")
	}
	if _, reached := only["last"]; reached {
		t.Errorf("the path past the %d-path cap was resolved anyway", statusPathsMax)
	}
}
