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

func mkRepo(t *testing.T, workDir, name string) string {
	t.Helper()
	dir := filepath.Join(workDir, name)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("Setup: mkdir %s: %v", name, err)
	}
	initFixtureRepo(t, dir)
	return dir
}

// snapshotRepos sorts, so an assertion does not depend on scan order.
func snapshotRepos(h *Handler, key string) []string {
	snap, _ := h.statusCache.read(key)
	names := make([]string, 0, len(snap.rows()))
	for _, r := range snap.rows() {
		names = append(names, r.Repo)
	}
	slices.Sort(names)
	return names
}

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
func TestHandleStatusAll_ScopedReadRescansOnlyTheOwningRepo(t *testing.T) {
	skipNoGit(t)
	workDir := t.TempDir()
	scanned := mkRepo(t, workDir, "scanned")
	skipped := mkRepo(t, workDir, "skipped")
	h := NewHandler(workDir)
	// The seeded row exists in no workspace, so a full rescan could not reproduce it.
	seedSnapshot(&h.statusCache, statusKeyPoll, []allRepoStatus{
		repoRow("scanned"), repoRow("skipped"), repoRow("seeded"),
	}, time.Now())
	// Both are dirtied so the scope is falsifiable: were `skipped` clean, the
	// assertion would pass under a full rescan too.
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
	snap, _ := h.statusCache.read(statusKeyPoll)
	for _, r := range snap.rows() {
		if r.Repo == "skipped" && len(r.Files) != 0 {
			t.Errorf("skipped reported %d files; it was rescanned when the scope named "+
				"only `scanned`, so the refresh cost the whole tree", len(r.Files))
		}
	}
}

// The snapshot's timestamp does NOT move on a scoped merge: `at` answers "when was
// the WHOLE tree last known", and moving it suppresses the periodic full refresh.
func TestHandleStatusAll_AScopedMergeDoesNotFreshenTheSnapshot(t *testing.T) {
	skipNoGit(t)
	workDir := t.TempDir()
	repo := mkRepo(t, workDir, "one")
	h := NewHandler(workDir)
	old := time.Now().Add(-time.Minute)
	// Seeded CLEAN then dirtied: with no difference, the wait below reads the
	// pre-merge snapshot and passes whatever the merge does to the stamp.
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

// A path no repository owns is DROPPED, and a request whose every path is unowned
// scans nothing at all: erroring would break the caller that sends a turn's changed
// files, and a full scan would make an unresolvable path the costliest request.
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

// A COLD read carrying paths falls through to the FULL scan: there is no snapshot to
// merge a partial result into, and no field could say the answer covered two repos.
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
// PENDING, and the running refresh drains them; otherwise the one-at-a-time rule
// loses the joining read's work until the next full scan.
func TestStatusCache_AScopedReadJoiningAScanIsNotDropped(t *testing.T) {
	var c statusCache
	seedSnapshot(&c, statusKeyPoll, []allRepoStatus{repoRow("a"), repoRow("b")}, time.Now())

	first, started := c.claim(statusKeyPoll, map[string]struct{}{"a": {}})
	if !started {
		t.Fatal("Setup: the first scoped claim did not start")
	}
	second, started := c.claim(statusKeyPoll, map[string]struct{}{"b": {}})
	if started {
		t.Error("a second scoped claim started a concurrent scan; the subprocess bound is gone")
	}
	if second != first {
		t.Error("the joining read got a different channel; it would wait on a refresh nobody runs")
	}

	next, run := c.finish(statusKeyPoll, []allRepoStatus{{Repo: "a", IsRepo: true, Files: []gitFile{{Path: "x"}}}})
	if !run {
		t.Fatal("finish ended the chain with a scope still recorded; the joining read's " +
			"repository was dropped and its rows stay stale")
	}
	if _, want := next["b"]; !want || len(next) != 1 {
		t.Fatalf("finish returned %v, want exactly {b}: the joining read's repository "+
			"was dropped and its rows stay stale", next)
	}
	// Waiters are woken either way, because the snapshot they were waiting on moved.
	select {
	case <-first:
	default:
		t.Error("finish left the waiters blocked on a channel it replaced")
	}
	// The slot is STILL claimed, so a third read joins this chain.
	if _, again := c.claim(statusKeyPoll, map[string]struct{}{"a": {}}); again {
		t.Error("the slot opened between passes; a third read would start a concurrent scan")
	}
	third, run := c.finish(statusKeyPoll, []allRepoStatus{repoRow("b")})
	if !run || len(third) != 1 {
		t.Fatalf("finish returned (%v, %v) after a read joined between passes, want its "+
			"scope drained", third, run)
	}

	if last, run := c.finish(statusKeyPoll, []allRepoStatus{repoRow("a")}); run {
		t.Errorf("finish returned %v after an idle pass, want the chain to end", last)
	}
	if _, running := c.read(statusKeyPoll); running != nil {
		t.Error("the slot is still in flight after the chain ended; the variant would never refresh again")
	}
}

// An unscoped read that joins a SCOPED scan is not dropped either: `run` with a nil
// scope says "scan everything", and only a full pass moves `at`.
func TestStatusCache_AFullReadJoiningAScopedScanIsNotDropped(t *testing.T) {
	var c statusCache
	old := time.Now().Add(-time.Minute)
	seedSnapshot(&c, statusKeyPoll, []allRepoStatus{repoRow("a")}, old)

	if _, started := c.claim(statusKeyPoll, map[string]struct{}{"a": {}}); !started {
		t.Fatal("Setup: the scoped claim did not start")
	}
	if _, started := c.claim(statusKeyPoll, nil); started {
		t.Fatal("Setup: the unscoped read did not join the running scoped scan")
	}

	next, run := c.finish(statusKeyPoll, []allRepoStatus{repoRow("a")})
	if !run {
		t.Fatal("finish released the slot with a full scan recorded: the whole-tree scan " +
			"never runs, and the snapshot it would have freshened stays stale forever")
	}
	if next != nil {
		t.Errorf("finish returned scope %v, want nil: a drained full intent must scan "+
			"EVERY repository, not the scope of the pass that preceded it", next)
	}
	// The scoped pass left `at` alone, which is why the full pass was still needed.
	snap, _ := c.read(statusKeyPoll)
	if snap.at.After(old) {
		t.Errorf("snapshot at = %v, want the original %v after a SCOPED pass", snap.at, old)
	}

	if _, run := c.finish(statusKeyPoll, []allRepoStatus{repoRow("a"), repoRow("b")}); run {
		t.Error("the chain did not end after the full pass")
	}
	snap, running := c.read(statusKeyPoll)
	if !snap.at.After(old) {
		t.Errorf("snapshot at = %v, want freshened: the full pass merged instead of "+
			"publishing, so every later read still sees a stale snapshot", snap.at)
	}
	if len(snap.rows()) != 2 {
		t.Errorf("repos = %+v, want both: the full pass did not replace the snapshot", snap.rows())
	}
	if running != nil {
		t.Error("the slot is still in flight after the chain ended")
	}
}

// A write landing BEHIND a running full scan's cursor is rescanned, because a full
// scan's coverage is not its timing: a repository is read at an unspecified point in
// the window, so rows published after that point can predate the triggering write.
func TestStatusCache_AWriteBehindAFullScansCursorIsStillRescanned(t *testing.T) {
	var c statusCache
	if _, started := c.claim(statusKeyPoll, nil); !started {
		t.Fatal("Setup: claim did not start")
	}
	if _, started := c.claim(statusKeyPoll, map[string]struct{}{"a": {}}); started {
		t.Fatal("Setup: the scoped read did not join the running full scan")
	}

	next, run := c.finish(statusKeyPoll, []allRepoStatus{repoRow("a"), repoRow("b")})
	if !run {
		t.Fatal("finish ended the chain after a full scan swallowed a scoped read: the " +
			"repository that moved keeps its pre-write rows until the next unscoped gesture")
	}
	if _, want := next["a"]; !want || len(next) != 1 {
		t.Fatalf("finish returned %v, want exactly {a}: the joining read's repository "+
			"was dropped by the full scan it arrived behind", next)
	}

	// The follow-up pass is SCOPED, so the full scan's newer `at` must survive.
	published, _ := c.read(statusKeyPoll)
	if _, run := c.finish(statusKeyPoll, []allRepoStatus{repoRow("a")}); run {
		t.Error("the chain did not end after the scoped follow-up pass")
	}
	snap, _ := c.read(statusKeyPoll)
	if !snap.at.Equal(published.at) {
		t.Errorf("snapshot at = %v, want the full scan's %v: the follow-up pass published "+
			"one repository's rows as a whole-tree scan", snap.at, published.at)
	}
	if len(snap.rows()) != 2 {
		t.Errorf("repos = %+v, want both: the scoped pass replaced the full scan's rows "+
			"instead of merging into them", snap.rows())
	}
}

// A whole-tree read joining a whole-tree scan records NOTHING on purpose: recording
// it would chain a second whole-tree sweep for a read the running scan already covers.
func TestStatusCache_AFullReadJoiningAFullScanChainsNothing(t *testing.T) {
	var c statusCache
	if _, started := c.claim(statusKeyPoll, nil); !started {
		t.Fatal("Setup: claim did not start")
	}
	if _, started := c.claim(statusKeyPoll, nil); started {
		t.Fatal("Setup: the second unscoped read did not join the running full scan")
	}

	c.mu.Lock()
	slot := c.slots[statusKeyPoll]
	pending, pendingFull := slot.pending, slot.pendingFull
	c.mu.Unlock()
	if len(pending) != 0 || pendingFull {
		t.Errorf("recorded pending = %v, pendingFull = %v for a whole-tree read joining a "+
			"whole-tree scan, want nothing: the sweep would run twice", pending, pendingFull)
	}

	if next, run := c.finish(statusKeyPoll, []allRepoStatus{repoRow("a"), repoRow("b")}); run {
		t.Errorf("finish chained a %v pass after a full scan, want the chain to end", next)
	}
}

// A scoped merge appends a row the snapshot does not hold, which is how a repository
// cloned since the last full scan reaches the answer at all.
func TestStatusCache_AScopedMergeAppendsAnUnknownRepo(t *testing.T) {
	var c statusCache
	seedSnapshot(&c, statusKeyPoll, []allRepoStatus{repoRow("old")}, time.Now())
	if _, started := c.claim(statusKeyPoll, map[string]struct{}{"fresh": {}}); !started {
		t.Fatal("Setup: claim did not start")
	}

	c.finish(statusKeyPoll, []allRepoStatus{repoRow("fresh")})

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

// The cold wait matches the SCAN's budget, not the per-repo one: a read that gives up
// first answers an empty list, which a client renders as "no repositories" — a claim
// about the tree rather than about the read.
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
	// A read that HAS a snapshot never waits: that is what makes the poll cheap.
	if got := h.coldWait(&statusSnapshot{at: time.Now()}, false); got != 0 {
		t.Errorf("coldWait(warm, poll) = %v, want 0", got)
	}
}

// statusScope resolves the query into repository names; this is the table for what it
// accepts, driven through the resolver rather than HTTP so each rule fails on its own.
func TestStatusScope(t *testing.T) {
	workDir := t.TempDir()
	// The WORKSPACE ROOT is a repository too, which is what makes the traversal case
	// below mean anything: "." owns every path no subdirectory repo claims.
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
			// Checked before ownerOf, or "." would own a path outside the tree.
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
// unbounded list.
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
