package git

// The Pull-all pre-flight, tested against real repositories rather than fakes:
// every predicate here answers a question about git's own on-disk state, so a
// fake would only assert that this file and the fixture agree about a format
// neither of them owns.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pullAllRepos drives the handler over workDir and returns its rows keyed by
// repo, which is how every assertion below reads them.
func pullAllRepos(t *testing.T, workDir string) map[string]pullResult {
	t.Helper()
	h := NewHandler(workDir)
	rec := httptest.NewRecorder()
	h.handlePullAll(rec, httptest.NewRequest(http.MethodPost, "/api/git/pull-all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Repos []pullResult `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	out := make(map[string]pullResult, len(body.Repos))
	for _, r := range body.Repos {
		out[r.Repo] = r
	}
	return out
}

// behindClone builds a workspace holding one repo named `work` that is one
// commit behind its origin, and returns the workspace root and that repo's dir.
//
// The remote is served over git's dumb HTTP rather than being a local path,
// because the pull runs through the production exec path and that sets
// GIT_PROTOCOL_FROM_USER=0 — which is exactly what makes git refuse the `file`
// transport. serveFixtureRepo is the existing answer to that; what this adds is
// a workspace root the handler can discover, holding only the clone.
//
// The seed and the served bare repo live OUTSIDE that root, so discovery finds
// one repo and the assertions below need no filtering.
func behindClone(t *testing.T) (workDir, repoDir string) {
	t.Helper()
	skipNoGit(t)
	base := t.TempDir()
	remote := serveFixtureRepo(t, base)
	seed := filepath.Join(base, "src")
	bare := filepath.Join(base, "srv.git")

	workDir = filepath.Join(base, "ws")
	if err := os.Mkdir(workDir, 0o750); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	runGit(t, workDir, "clone", "-q", remote, "work")
	repoDir = filepath.Join(workDir, "work")

	// Advance the served remote, then refresh the info files dumb HTTP reads for
	// ref discovery. The clone is left un-fetched: measuring `behind` is the
	// pass's own job, so a fixture that fetched for it would hide a missing fetch.
	writeCommit(t, seed, "upstream.txt", "from upstream\n", "add upstream file")
	runGit(t, seed, "push", "-q", bare, "HEAD:main")
	runGit(t, bare, "update-server-info")
	return workDir, repoDir
}

func TestPullAll_FastForwardsARepoThatIsOnlyBehind(t *testing.T) {
	workDir, repoDir := behindClone(t)

	rows := pullAllRepos(t, workDir)
	got, ok := rows["work"]
	if !ok {
		t.Fatalf("no row for the clone; rows = %+v", rows)
	}
	if got.Verdict != verdictPulled {
		t.Fatalf("verdict = %q (reason %q, detail %q), want pulled", got.Verdict, got.Reason, got.Detail)
	}
	// The pull is what the verdict claims, so assert on the tree rather than on
	// the word: the upstream commit's file has to be on disk.
	if _, err := os.Stat(filepath.Join(repoDir, "upstream.txt")); err != nil {
		t.Errorf("upstream file absent after a pulled verdict: %v", err)
	}
}

// The pass is idempotent: running it twice pulls once and then reports nothing
// to do. Asserted by running it rather than by hand-merging the fixture, so what
// is checked is the transition the pass itself produces.
func TestPullAll_SkipsARepoItHasAlreadyPulled(t *testing.T) {
	workDir, _ := behindClone(t)
	if first := pullAllRepos(t, workDir)["work"]; first.Verdict != verdictPulled {
		t.Fatalf("first pass verdict = %q (reason %q, detail %q), want pulled",
			first.Verdict, first.Reason, first.Detail)
	}

	got := pullAllRepos(t, workDir)["work"]
	if got.Verdict != verdictSkipped || got.Reason != reasonUpToDate {
		t.Errorf("second pass verdict/reason = %q/%q, want skipped/up_to_date", got.Verdict, got.Reason)
	}
}

// A repo with local commits of its own has no fast-forward, which is the hazard
// a reader most needs named: `--ff-only` refuses, and the pass must say why
// rather than leaving the repo silently un-pulled.
func TestPullAll_BlocksADivergedRepoAndNamesTheCommitCount(t *testing.T) {
	workDir, repoDir := behindClone(t)
	writeCommit(t, repoDir, "local.txt", "mine\n", "local work")

	got := pullAllRepos(t, workDir)["work"]
	if got.Verdict != verdictBlocked || got.Reason != reasonDiverged {
		t.Fatalf("verdict/reason = %q/%q, want blocked/diverged", got.Verdict, got.Reason)
	}
	if !strings.Contains(got.Detail, "1 local commit") {
		t.Errorf("detail = %q, want it to name the one local commit", got.Detail)
	}
	// Blocked means untouched: the incoming file must NOT be on disk.
	if _, err := os.Stat(filepath.Join(repoDir, "upstream.txt")); err == nil {
		t.Error("a blocked repo was pulled anyway")
	}
}

// The overlap check: a local edit to a file the incoming commits also rewrite.
// git would refuse this, and the reason has to name the file — that is the whole
// point of computing the intersection rather than letting the pull fail.
func TestPullAll_BlocksWhenALocalEditWouldBeOverwritten(t *testing.T) {
	workDir, repoDir := behindClone(t)
	if err := os.WriteFile(filepath.Join(repoDir, "upstream.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := pullAllRepos(t, workDir)["work"]
	if got.Verdict != verdictBlocked || got.Reason != reasonLocalChanges {
		t.Fatalf("verdict/reason = %q/%q, want blocked/local_changes", got.Verdict, got.Reason)
	}
	if !strings.Contains(got.Detail, "upstream.txt") {
		t.Errorf("detail = %q, want it to name upstream.txt", got.Detail)
	}
}

// A local edit to a file the incoming commits do NOT touch is not a hazard, and
// this is the case the whole intersection exists to keep pullable: git
// fast-forwards happily with unrelated work in the tree.
func TestPullAll_PullsWithALocalEditTheIncomingCommitsDoNotTouch(t *testing.T) {
	workDir, repoDir := behindClone(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("edited\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := pullAllRepos(t, workDir)["work"]
	if got.Verdict != verdictPulled {
		t.Fatalf("verdict = %q (reason %q, detail %q), want pulled", got.Verdict, got.Reason, got.Detail)
	}
}

// An operation stopped part-way is the state where pulling is most obviously
// wrong, and where git's own refusal names the symptom rather than the cause.
func TestPullAll_BlocksWhileAnOperationIsInProgress(t *testing.T) {
	workDir, repoDir := behindClone(t)
	gitDir, err := gitCmd(t.Context(), repoDir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	head, err := gitCmd(t.Context(), repoDir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if werr := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(head+"\n"), 0o600); werr != nil {
		t.Fatalf("write MERGE_HEAD: %v", werr)
	}

	got := pullAllRepos(t, workDir)["work"]
	if got.Verdict != verdictBlocked || got.Reason != reasonInProgress {
		t.Errorf("verdict/reason = %q/%q, want blocked/in_progress", got.Verdict, got.Reason)
	}
}

// A repo tracking nothing has nothing to pull, and it must not read as an error:
// aheadBehind answers (0, 0) for both this and being in sync, which is exactly
// why upstreamDivergence reports ok separately.
func TestPullAll_SkipsARepoWithNoUpstream(t *testing.T) {
	skipNoGit(t)
	workDir := t.TempDir()
	repoDir := filepath.Join(workDir, "solo")
	if err := os.Mkdir(repoDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initFixtureRepo(t, repoDir)

	got := pullAllRepos(t, workDir)["solo"]
	if got.Verdict != verdictSkipped || got.Reason != reasonNoUpstream {
		t.Errorf("verdict/reason = %q/%q, want skipped/no_upstream", got.Verdict, got.Reason)
	}
}

// A row with no verdict would reach the client as an unclassifiable repo, so the
// zero value has to be a real answer rather than the absence of one.
//
// Driven with an already-cancelled context, because that is the ONLY way the
// early return is reachable: the whole-pass budget is seconds, so a fixture
// going through the handler always reassigns the verdict on its way out and
// would report green with the zero value removed. (It was written that way
// first, and the red check caught it.)
func TestPullOne_CarriesAVerdictWhenTheBudgetIsAlreadySpent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	h := NewHandler(t.TempDir())
	got := h.pullOne(ctx, repoEntry{Name: "work", Dir: t.TempDir()}, time.Now())
	if got.Verdict != verdictSkipped || got.Reason != reasonOutOfTime {
		t.Errorf("verdict/reason = %q/%q, want skipped/out_of_time", got.Verdict, got.Reason)
	}
}

// Every discovered repository gets a verdict on the ordinary path too, which is
// the same invariant one layer out.
func TestPullAll_EveryRowCarriesAVerdict(t *testing.T) {
	workDir, _ := behindClone(t)
	// A plain directory beside the clone: discovery skips a non-repo, so the only
	// thing this asserts is that nothing in the response is unclassified.
	if err := os.Mkdir(filepath.Join(workDir, "notrepo"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	rows := pullAllRepos(t, workDir)
	if len(rows) == 0 {
		t.Fatal("no rows at all")
	}
	for name, r := range rows {
		if r.Verdict == "" {
			t.Errorf("%s: empty verdict", name)
		}
	}
}

func TestPullAll_RefusesANonPOST(t *testing.T) {
	h := NewHandler(t.TempDir())
	rec := httptest.NewRecorder()
	h.handlePullAll(rec, httptest.NewRequest(http.MethodGet, "/api/git/pull-all", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// The seven pairs are git's own; the point of the table is that the two
// both-changed cases carry no U at all, which is what a per-side parse loses.
func TestIsUnmerged_CoversGitsSevenConflictPairs(t *testing.T) {
	for _, pair := range []string{"DD", "AU", "UD", "UA", "DU", "AA", "UU"} {
		if !isUnmerged(pair[0], pair[1]) {
			t.Errorf("%s: not reported as unmerged", pair)
		}
	}
	for _, pair := range []string{"M ", " M", "MM", "A ", " D", "??", "R ", "AD", "DA"} {
		if isUnmerged(pair[0], pair[1]) {
			t.Errorf("%s: reported as unmerged", pair)
		}
	}
}

func TestNameSome_CountsThePathsItDoesNotName(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{"one", []string{"a"}, "a"},
		{"at the cap", []string{"a", "b", "c"}, "a, b, c"},
		{"over the cap", []string{"a", "b", "c", "d", "e"}, "a, b, c and 2 more"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nameSome(tc.paths); got != tc.want {
				t.Errorf("nameSome(%v) = %q, want %q", tc.paths, got, tc.want)
			}
		})
	}
}

func TestPlural_AgreesWithTheCount(t *testing.T) {
	if got := plural(1, "local commit", "local commits"); got != "1 local commit" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(3, "local commit", "local commits"); got != "3 local commits" {
		t.Errorf("plural(3) = %q", got)
	}
}

// A rename's ORIGIN path counts as dirty too: a fast-forward touching either end
// is refused, and the origin arrives as its own NUL field rather than in the
// record the status letters are on.
func TestWorktreeState_TakesBothEndsOfARenameAsDirty(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	writeCommit(t, dir, "old.txt", "content\n", "add old")
	runGit(t, dir, "mv", "old.txt", "new.txt")

	dirty, conflicted, ok := worktreeState(t.Context(), dir)
	if !ok {
		t.Fatal("worktreeState reported the tree unreadable")
	}
	if conflicted {
		t.Error("a rename was reported as a conflict")
	}
	for _, want := range []string{"old.txt", "new.txt"} {
		if _, hit := dirty[want]; !hit {
			t.Errorf("%s missing from the dirty set: %v", want, dirty)
		}
	}
}

// The parse this exists for: a real conflict, read from a real repo, so the
// assertion is against git's output rather than against a hand-written record.
func TestWorktreeState_ReportsARealMergeConflict(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	writeCommit(t, dir, "shared.txt", "base\n", "base")
	runGit(t, dir, "checkout", "-b", "other")
	writeCommit(t, dir, "shared.txt", "theirs\n", "theirs")
	runGit(t, dir, "checkout", "main")
	writeCommit(t, dir, "shared.txt", "ours\n", "ours")
	// Expected to fail: the point is the conflicted index it leaves behind.
	_, _ = gitCmd(t.Context(), dir, "merge", "other")

	_, conflicted, ok := worktreeState(t.Context(), dir)
	if !ok {
		t.Fatal("worktreeState reported the tree unreadable")
	}
	if !conflicted {
		t.Error("a conflicted index was not reported as conflicted")
	}
}
