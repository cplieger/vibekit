package git

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// repoRow builds one dashboard row: a repository that exists and is clean, which
// is all these tests need of a row's contents. The promoted fields are set
// directly in the literal, which is Go 1.27's spelling and what the modernize
// `embedlit` analyzer asks for.
func repoRow(name string) allRepoStatus {
	return allRepoStatus{Repo: name, IsRepo: true, Files: []gitFile{}}
}

// seedSnapshot plants a completed scan with a chosen age. Written directly rather
// than through publish, which stamps `now` and so cannot express an old snapshot.
func seedSnapshot(c *statusCache, key string, rows []allRepoStatus, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slots == nil {
		c.slots = make(map[string]*statusSlot, 2)
	}
	slot := c.slots[key]
	if slot == nil {
		slot = &statusSlot{}
		c.slots[key] = slot
	}
	slot.snap = &statusSnapshot{at: at, repos: rows}
}

// getStatusAll drives the handler and decodes its answer.
func getStatusAll(t *testing.T, h *Handler, query string) statusAllResp {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/git/status-all"+query, nil)
	rec := httptest.NewRecorder()
	h.handleStatusAll(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp statusAllResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

// The poll answers from the snapshot and does not run the scan.
//
// This is the whole point of the holder, and the seeded row is what makes it
// falsifiable: "seeded" exists in no workspace, so an answer carrying it can only
// have come from the snapshot, and an answer carrying the real repository can only
// have come from a scan the request waited for.
func TestHandleStatusAll_PollAnswersFromTheSnapshot(t *testing.T) {
	workDir := t.TempDir()
	initFixtureRepo(t, workDir)
	h := NewHandler(workDir)
	seeded := []allRepoStatus{repoRow("seeded")}
	seedSnapshot(&h.statusCache, statusKeyPoll, seeded, time.Now())

	got := getStatusAll(t, h, "")

	if len(got.Repos) != 1 || got.Repos[0].Repo != "seeded" {
		t.Errorf("repos = %+v, want the seeded snapshot: the poll ran a scan instead of "+
			"answering from the holder", got.Repos)
	}
}

// The answer carries the snapshot's age, which is what lets a client show data it
// knows is seconds old instead of waiting for certainty.
func TestHandleStatusAll_ReportsTheSnapshotAge(t *testing.T) {
	h := NewHandler(t.TempDir())
	seedSnapshot(&h.statusCache, statusKeyPoll, []allRepoStatus{}, time.Now().Add(-3*time.Second))

	got := getStatusAll(t, h, "")

	if got.AgeMS < 3000 {
		t.Errorf("age_ms = %d, want at least 3000 for a snapshot stamped 3s ago", got.AgeMS)
	}
}

// A stale snapshot is answered immediately AND refreshed behind the answer, so the
// next poll finds fresh data without any request having waited.
func TestHandleStatusAll_StaleSnapshotRefreshesBehindTheAnswer(t *testing.T) {
	workDir := t.TempDir()
	repoDir := filepath.Join(workDir, "myrepo")
	if err := os.Mkdir(repoDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initFixtureRepo(t, repoDir)
	h := NewHandler(workDir)
	stale := []allRepoStatus{repoRow("seeded")}
	seedSnapshot(&h.statusCache, statusKeyPoll, stale, time.Now().Add(-statusMaxAge-time.Second))

	got := getStatusAll(t, h, "")
	if len(got.Repos) != 1 || got.Repos[0].Repo != "seeded" {
		t.Fatalf("repos = %+v, want the stale snapshot answered immediately", got.Repos)
	}

	// The refresh is detached, so wait for it to publish rather than sleeping past
	// it: the assertion is that the snapshot MOVED, which is the half a bare sleep
	// would let pass for the wrong reason.
	waitForFreshSnapshot(t, h, statusKeyPoll, "myrepo")
	if next := getStatusAll(t, h, ""); len(next.Repos) != 1 || next.Repos[0].Repo != "myrepo" {
		t.Errorf("repos after the refresh = %+v, want the real workspace", next.Repos)
	}
}

// waitForFreshSnapshot polls the holder until it carries a scan naming repo.
func waitForFreshSnapshot(t *testing.T, h *Handler, key, repo string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, _ := h.statusCache.read(key)
		if snap != nil && len(snap.repos) == 1 && snap.repos[0].Repo == repo {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no snapshot naming %q after 10s; the background refresh never published", repo)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A forced refresh WAITS, because it is a gesture: "Refresh all" that answered
// from a snapshot would do nothing the user can see.
//
// The seeded snapshot is fresh, so nothing but the fetch rule can make this
// request scan at all.
func TestHandleStatusAll_ForcedRefreshWaitsForFreshData(t *testing.T) {
	skipNoGit(t)
	workDir := t.TempDir()
	repoDir := filepath.Join(workDir, "myrepo")
	if err := os.Mkdir(repoDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initFixtureRepo(t, repoDir)
	h := NewHandler(workDir)
	seeded := []allRepoStatus{repoRow("seeded")}
	seedSnapshot(&h.statusCache, statusKeyFetch, seeded, time.Now())

	got := getStatusAll(t, h, "?fetch=1")

	if len(got.Repos) != 1 || got.Repos[0].Repo != "myrepo" {
		t.Errorf("repos = %+v, want the real workspace: a forced refresh must not "+
			"answer from a snapshot", got.Repos)
	}
}

// The two variants hold separate snapshots, so a cheap poll never piggybacks a
// fetch-less answer onto a forced refresh or the reverse. They report different
// facts: `?fetch=1` measures ahead/behind against a freshly fetched ref.
func TestHandleStatusAll_TheFetchVariantHasItsOwnSnapshot(t *testing.T) {
	h := NewHandler(t.TempDir())
	seeded := []allRepoStatus{repoRow("poll-only")}
	seedSnapshot(&h.statusCache, statusKeyPoll, seeded, time.Now())

	if got := getStatusAll(t, h, "?fetch=1"); len(got.Repos) != 0 {
		t.Errorf("fetch variant answered with %+v, want the empty workspace: it read "+
			"the poll variant's snapshot", got.Repos)
	}
	if got := getStatusAll(t, h, ""); len(got.Repos) != 1 {
		t.Errorf("poll variant = %+v, want its own seeded snapshot back", got.Repos)
	}
}

// While a scan is in flight the answer says so, which is what lets a client show a
// spinner over data it is already rendering.
//
// The slot is claimed by the TEST, so the handler joins a refresh that never
// completes and the flag cannot flicker: a fixture that started a real scan would
// be racing it.
func TestHandleStatusAll_ReportsAScanInFlight(t *testing.T) {
	h := NewHandler(t.TempDir())
	seedSnapshot(&h.statusCache, statusKeyPoll, []allRepoStatus{}, time.Now().Add(-statusMaxAge-time.Second))
	done, started := h.statusCache.claim(statusKeyPoll, nil)
	if !started {
		t.Fatal("claim on a fresh cache did not start")
	}
	t.Cleanup(func() { h.statusCache.finish(statusKeyPoll, nil) })

	if got := getStatusAll(t, h, ""); !got.Scanning {
		t.Error("scanning = false while a refresh is in flight")
	}

	// Waking the joiners is the other half: a caller that waited on this channel
	// must be released, or a cold read holds its whole budget for a scan that
	// already finished.
	if _, running := h.statusCache.read(statusKeyPoll); running == nil {
		t.Fatal("the slot reported no refresh in flight while the test held it")
	}
	select {
	case <-done:
		t.Fatal("the refresh channel closed before anything published")
	default:
	}
}

// One refresh at a time per variant: N concurrent pollers cost one scan, which is
// what the singleflight used to provide and the reason a poll is cheap at all.
func TestStatusCache_AdmitsOneRefreshAtATime(t *testing.T) {
	var c statusCache

	first, started := c.claim(statusKeyPoll, nil)
	if !started {
		t.Fatal("the first claim did not start a refresh")
	}
	second, started := c.claim(statusKeyPoll, nil)
	if started {
		t.Error("a second claim started a refresh while one was in flight")
	}
	if second != first {
		t.Error("the second caller got a different channel; it would wait for a refresh nobody runs")
	}
	// A different variant is not blocked by it.
	if _, startedFetch := c.claim(statusKeyFetch, nil); !startedFetch {
		t.Error("the fetch variant could not claim while the poll variant was in flight")
	}

	rows := []allRepoStatus{{Repo: "one"}}
	c.finish(statusKeyPoll, rows)
	select {
	case <-first:
	default:
		t.Error("publish did not close the refresh channel")
	}
	snap, running := c.read(statusKeyPoll)
	if snap == nil || len(snap.repos) != 1 {
		t.Fatalf("snapshot after publish = %+v, want the published rows", snap)
	}
	if running != nil {
		t.Error("the slot is still marked in flight after publish")
	}
	// And the slot is claimable again, or the variant would never refresh once more.
	if _, again := c.claim(statusKeyPoll, nil); !again {
		t.Error("the slot stayed claimed after publish")
	}
}

// An empty scan still releases the slot. A publish skipped on a result nobody
// wanted would leave the variant permanently claimed: every later read would join
// a refresh that already ended, and the snapshot would never move again.
func TestStatusCache_AnEmptyScanStillReleasesTheSlot(t *testing.T) {
	var c statusCache
	if _, started := c.claim(statusKeyPoll, nil); !started {
		t.Fatal("claim did not start")
	}
	c.finish(statusKeyPoll, nil)

	snap, running := c.read(statusKeyPoll)
	if running != nil {
		t.Error("the slot is still in flight after an empty publish")
	}
	if snap == nil {
		t.Fatal("an empty scan published no snapshot; a later read cannot tell cold from empty")
	}
	if rows := snap.rows(); rows == nil || len(rows) != 0 {
		t.Errorf("rows = %v, want a non-nil empty array: null breaks the client's iteration", rows)
	}
}

// A read of a variant nothing has scanned reports cold rather than an empty
// answer, which is what makes the first request wait instead of rendering "no
// repositories" over a workspace full of them.
func TestStatusCache_ColdReadIsDistinguishableFromAnEmptyScan(t *testing.T) {
	var c statusCache
	if snap, running := c.read(statusKeyPoll); snap != nil || running != nil {
		t.Errorf("cold read = (%+v, %v), want (nil, nil)", snap, running)
	}
	if !(*statusSnapshot)(nil).stale(statusMaxAge) {
		t.Error("a missing snapshot is not stale, so nothing would ever refresh it")
	}
}
