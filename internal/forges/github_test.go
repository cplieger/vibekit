package forges

// GitHub provider tests. The argv assertions run against the stub-CLI
// harness in clistub_test.go, so they exercise the real cliexec plumbing
// (LookPath, sanitized env, output capture) with no network and no gh.

import (
	"fmt"
	"strings"
	"testing"
)

// newGitHubWithStub points a github provider at a stub gh that prints
// stdout and records its argv into recPath.
func newGitHubWithStub(t *testing.T, stdout string) (*githubProvider, string) {
	t.Helper()
	dir := stubPath(t)
	recPath := dir + "/rec"
	stubCLI(t, dir, "gh", recordingScript(recPath)+"\n"+stdout)
	return newGitHub("github.com"), recPath
}

func TestClassifyGHCheck(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		conclusion string
		state      string
		want       string
	}{
		// CheckRun shape: status drives it, conclusion decides a
		// completed run.
		{"queued run", "QUEUED", "", "", checkPending},
		{"in progress run", "IN_PROGRESS", "", "", checkPending},
		{"waiting run", "WAITING", "", "", checkPending},
		{"requested run", "REQUESTED", "", "", checkPending},
		{"completed success", "COMPLETED", "SUCCESS", "", checkPassing},
		{"completed failure", "COMPLETED", "FAILURE", "", checkFailing},
		{"completed timeout", "COMPLETED", "TIMED_OUT", "", checkFailing},
		{"completed cancelled", "COMPLETED", "CANCELLED", "", checkFailing},
		{"completed startup failure", "COMPLETED", "STARTUP_FAILURE", "", checkFailing},
		{"completed action required", "COMPLETED", "ACTION_REQUIRED", "", checkFailing},
		// SKIPPED and NEUTRAL carry no verdict: a PR whose every check
		// was skipped must not read as passing.
		{"completed skipped", "COMPLETED", "SKIPPED", "", ""},
		{"completed neutral", "COMPLETED", "NEUTRAL", "", ""},
		// StatusContext shape: one state word, no status.
		{"status success", "", "", "SUCCESS", checkPassing},
		{"status pending", "", "", "PENDING", checkPending},
		{"status expected", "", "", "EXPECTED", checkPending},
		{"status failure", "", "", "FAILURE", checkFailing},
		{"status error", "", "", "ERROR", checkFailing},
		// Unrecognised input classifies as no verdict rather than a pass.
		{"empty", "", "", "", ""},
		{"garbage", "WAT", "WAT", "WAT", ""},
		{"lowercase run", "completed", "success", "", checkPassing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyGHCheck(tc.status, tc.conclusion, tc.state)
			if got != tc.want {
				t.Errorf("classifyGHCheck(%q,%q,%q) = %q, want %q",
					tc.status, tc.conclusion, tc.state, got, tc.want)
			}
		})
	}
}

func TestSummarizeGHRollup(t *testing.T) {
	run := func(status, conclusion string) ghRollupEntry {
		return ghRollupEntry{TypeName: "CheckRun", Status: status, Conclusion: conclusion}
	}
	ctxEntry := func(state string) ghRollupEntry {
		return ghRollupEntry{TypeName: "StatusContext", State: state}
	}
	cases := []struct {
		name        string
		entries     []ghRollupEntry
		wantStatus  string
		wantTotal   int
		wantFailing int
	}{
		{"nil rollup is no verdict", nil, "", 0, 0},
		{"empty rollup is no verdict", []ghRollupEntry{}, "", 0, 0},
		{
			"all passing",
			[]ghRollupEntry{run("COMPLETED", "SUCCESS"), run("COMPLETED", "SUCCESS")},
			checkPassing, 2, 0,
		},
		{
			"a failure outranks passes",
			[]ghRollupEntry{run("COMPLETED", "SUCCESS"), run("COMPLETED", "FAILURE")},
			checkFailing, 2, 1,
		},
		{
			"a pending outranks passes",
			[]ghRollupEntry{run("COMPLETED", "SUCCESS"), run("IN_PROGRESS", "")},
			checkPending, 2, 0,
		},
		{
			"a failure outranks a pending",
			[]ghRollupEntry{run("IN_PROGRESS", ""), run("COMPLETED", "FAILURE")},
			checkFailing, 2, 1,
		},
		{
			"two failures are counted",
			[]ghRollupEntry{run("COMPLETED", "FAILURE"), run("COMPLETED", "TIMED_OUT"), run("COMPLETED", "SUCCESS")},
			checkFailing, 3, 2,
		},
		{
			"all skipped is no verdict, not a pass",
			[]ghRollupEntry{run("COMPLETED", "SKIPPED"), run("COMPLETED", "SKIPPED")},
			"", 0, 0,
		},
		{
			"skipped entries do not inflate the total",
			[]ghRollupEntry{run("COMPLETED", "SKIPPED"), run("COMPLETED", "SUCCESS")},
			checkPassing, 1, 0,
		},
		{
			"status contexts fold with check runs",
			[]ghRollupEntry{run("COMPLETED", "SUCCESS"), ctxEntry("FAILURE")},
			checkFailing, 2, 1,
		},
		{"status context alone", []ghRollupEntry{ctxEntry("SUCCESS")}, checkPassing, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, total, failing := summarizeGHRollup(tc.entries)
			if status != tc.wantStatus || total != tc.wantTotal || failing != tc.wantFailing {
				t.Errorf("summarizeGHRollup() = (%q,%d,%d), want (%q,%d,%d)",
					status, total, failing, tc.wantStatus, tc.wantTotal, tc.wantFailing)
			}
		})
	}
}

func TestMapGHMergeState(t *testing.T) {
	cases := []struct {
		mergeState  string
		checkStatus string
		want        string
	}{
		{"CLEAN", checkPassing, ""},
		// GitHub permits an UNSTABLE merge, so the button must not be
		// disabled on it.
		{"UNSTABLE", checkFailing, ""},
		{"HAS_HOOKS", checkPassing, ""},
		{"DIRTY", "", blockConflicts},
		{"DRAFT", "", blockDraft},
		{"BEHIND", "", blockBehind},
		// BLOCKED defers to the rollup, which is more specific than
		// "branch protection" when a required check is the cause.
		{"BLOCKED", checkFailing, blockChecksFailing},
		{"BLOCKED", checkPending, blockChecksRunning},
		{"BLOCKED", checkPassing, blockProtected},
		{"BLOCKED", "", blockProtected},
		{"UNKNOWN", "", blockUnknown},
		{"", "", ""},
		{"clean", checkPassing, ""},
		{"wat", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.mergeState+"/"+tc.checkStatus, func(t *testing.T) {
			if got := mapGHMergeState(tc.mergeState, tc.checkStatus); got != tc.want {
				t.Errorf("mapGHMergeState(%q,%q) = %q, want %q",
					tc.mergeState, tc.checkStatus, got, tc.want)
			}
		})
	}
}

// TestGitHubListPRs_RequestsRollupOnTheListCall is D99's contract: the
// check status, the merge cause and the head SHA all arrive in the one
// `gh pr list` call, so no per-row fetch may appear.
func TestGitHubListPRs_RequestsRollupOnTheListCall(t *testing.T) {
	const out = `printf '%s' '[{
	  "number": 7, "title": "T", "state": "OPEN",
	  "author": {"login":"alice"},
	  "headRefName": "feat", "baseRefName": "main",
	  "url": "https://github.com/o/r/pull/7",
	  "headRefOid": "deadbeefcafe1234",
	  "mergeable": "MERGEABLE", "mergeStateStatus": "BLOCKED", "isDraft": false,
	  "autoMergeRequest": {"enabledAt":"2026-01-01T00:00:00Z"},
	  "statusCheckRollup": [
	    {"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},
	    {"__typename":"CheckRun","status":"COMPLETED","conclusion":"FAILURE"},
	    {"__typename":"StatusContext","state":"SUCCESS"}
	  ]
	}]'`
	p, recPath := newGitHubWithStub(t, out)

	prs, err := p.ListPRs(t.Context(), "o/r", StateOpen)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("ListPRs returned %d PRs, want 1", len(prs))
	}
	got := prs[0]
	if got.HeadSHA != "deadbeefcafe1234" {
		t.Errorf("HeadSHA = %q, want the headRefOid", got.HeadSHA)
	}
	if got.CheckStatus != checkFailing {
		t.Errorf("CheckStatus = %q, want %q", got.CheckStatus, checkFailing)
	}
	if got.ChecksTotal != 3 || got.ChecksFailing != 1 {
		t.Errorf("checks = %d total / %d failing, want 3/1", got.ChecksTotal, got.ChecksFailing)
	}
	if got.MergeBlocked != blockChecksFailing {
		t.Errorf("MergeBlocked = %q, want %q", got.MergeBlocked, blockChecksFailing)
	}
	if !got.AutoMergeArmed {
		t.Error("AutoMergeArmed = false, want true (autoMergeRequest was present)")
	}

	argv := recordLines(readRecord(t, recPath), "argv:")
	if len(argv) != 1 {
		t.Fatalf("gh was invoked %d times, want exactly 1 (a per-row fetch is the shape D99 rejects): %v", len(argv), argv)
	}
	for _, field := range []string{"statusCheckRollup", "mergeStateStatus", "headRefOid", "autoMergeRequest"} {
		if !strings.Contains(argv[0], field) {
			t.Errorf("gh pr list argv is missing --json field %q: %s", field, argv[0])
		}
	}
}

// TestGitHubListPRs_EmptyRollupIsNoChip pins the other half of the fold:
// a PR with no checks reports no verdict, so the row shows no chip rather
// than a green one.
func TestGitHubListPRs_EmptyRollupIsNoChip(t *testing.T) {
	const out = `printf '%s' '[{"number":1,"title":"T","state":"OPEN","headRefOid":"abcdef1234567",
	  "mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","statusCheckRollup":[]}]'`
	p, _ := newGitHubWithStub(t, out)

	prs, err := p.ListPRs(t.Context(), "o/r", StateOpen)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	got := prs[0]
	if got.CheckStatus != "" {
		t.Errorf("CheckStatus = %q, want \"\" for an empty rollup", got.CheckStatus)
	}
	if got.ChecksTotal != 0 {
		t.Errorf("ChecksTotal = %d, want 0", got.ChecksTotal)
	}
	if got.MergeBlocked != "" {
		t.Errorf("MergeBlocked = %q, want \"\" on a CLEAN PR", got.MergeBlocked)
	}
	if got.AutoMergeArmed {
		t.Error("AutoMergeArmed = true, want false when autoMergeRequest is absent")
	}
}

func TestGitHubMergePR_Argv(t *testing.T) {
	cases := []struct {
		name string
		opts MergeOptions
		want []string
		deny []string
	}{
		{
			name: "plain merge is unchanged",
			opts: MergeOptions{},
			want: []string{"pr merge 7", "--repo o/r", "--merge"},
			deny: []string{"--match-head-commit", "--auto"},
		},
		{
			name: "head pin",
			opts: MergeOptions{HeadSHA: "abc1234def"},
			want: []string{"--merge", "--match-head-commit abc1234def"},
			deny: []string{"--auto"},
		},
		{
			name: "auto merge arms and still pins",
			opts: MergeOptions{HeadSHA: "abc1234def", Auto: true},
			want: []string{"--match-head-commit abc1234def", "--auto"},
		},
		{
			name: "squash keeps its strategy alongside the pin",
			opts: MergeOptions{Method: MergeSquash, HeadSHA: "abc1234def"},
			want: []string{"--squash", "--match-head-commit abc1234def"},
			deny: []string{"--merge"},
		},
		{
			name: "rebase keeps its strategy",
			opts: MergeOptions{Method: MergeRebase},
			want: []string{"--rebase"},
			deny: []string{"--merge"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, recPath := newGitHubWithStub(t, "")
			if err := p.MergePR(t.Context(), "o/r", 7, tc.opts); err != nil {
				t.Fatalf("MergePR: %v", err)
			}
			argv := recordLines(readRecord(t, recPath), "argv:")
			if len(argv) != 1 {
				t.Fatalf("gh invoked %d times, want 1: %v", len(argv), argv)
			}
			for _, want := range tc.want {
				if !strings.Contains(argv[0], want) {
					t.Errorf("argv missing %q: %s", want, argv[0])
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(argv[0], deny) {
					t.Errorf("argv unexpectedly contains %q: %s", deny, argv[0])
				}
			}
		})
	}
}

func TestGitHubReopenPR_Argv(t *testing.T) {
	p, recPath := newGitHubWithStub(t, "")
	if err := p.ReopenPR(t.Context(), "o/r", 12); err != nil {
		t.Fatalf("ReopenPR: %v", err)
	}
	argv := recordLines(readRecord(t, recPath), "argv:")
	if len(argv) != 1 || !strings.Contains(argv[0], "pr reopen 12 --repo o/r") {
		t.Fatalf("argv = %v, want a single `pr reopen 12 --repo o/r`", argv)
	}
}

// currentHead is the head commit every rerun fixture's PR reports.
const currentHead = "aaaa1111bbbb2222cccc3333dddd4444eeee5555"

// stubGHRerun stands up a gh stub that answers `pr view` with a head commit and
// a rollup, and records every invocation.
func stubGHRerun(t *testing.T, head, rollup string) (*githubProvider, string) {
	t.Helper()
	dir := stubPath(t)
	recPath := dir + "/rec"
	stubCLI(t, dir, "gh", recordingScript(recPath)+`
case "$*" in
  *"pr view"*) printf '%s' '{"headRefOid":"`+head+`","statusCheckRollup":`+rollup+`}' ;;
esac`)
	return newGitHub("github.com"), recPath
}

// actionsCheck renders one failing Actions check run pointing at runID.
func actionsCheck(name string, runID int64, repo string) string {
	return fmt.Sprintf(`{"__typename":"CheckRun","name":%q,"status":"COMPLETED","conclusion":"FAILURE",
	  "detailsUrl":"https://github.com/%s/actions/runs/%d/job/900%d"}`, name, repo, runID, runID)
}

// TestGitHubRerunFailedChecks_RerunsARunOfTheCurrentHead pins the resolution
// that replaced the branch search: the run id comes from the PR's own
// statusCheckRollup, which is the head commit's check set, so no run belonging
// to another commit can be selected however the branch has moved.
func TestGitHubRerunFailedChecks_RerunsARunOfTheCurrentHead(t *testing.T) {
	rollup := "[" + actionsCheck("build", 42, "o/r") + "," +
		`{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"SUCCESS",
		  "detailsUrl":"https://github.com/o/r/actions/runs/91/job/9091"}` + "]"
	p, recPath := stubGHRerun(t, currentHead, rollup)

	if err := p.RerunFailedChecks(t.Context(), "o/r", 5, currentHead); err != nil {
		t.Fatalf("RerunFailedChecks: %v", err)
	}
	argv := recordLines(readRecord(t, recPath), "argv:")
	if len(argv) != 2 {
		t.Fatalf("gh invoked %d times, want 2 (pr view, run rerun): %v", len(argv), argv)
	}
	// The rollup carries the linkage, so there is no `run list` at all — a
	// branch-keyed listing is the shape that could name another commit's run.
	if strings.Contains(argv[0], "run list") || strings.Contains(argv[1], "run list") {
		t.Errorf("a branch-keyed run list is back: %v", argv)
	}
	if !strings.Contains(argv[0], "statusCheckRollup") || !strings.Contains(argv[0], "headRefOid") {
		t.Errorf("pr view did not request the head commit and its rollup: %s", argv[0])
	}
	// 42 is the FAILING check's run; 91 is a higher id but its check passed.
	if !strings.Contains(argv[1], "run rerun 42") || !strings.Contains(argv[1], "--failed") {
		t.Errorf("argv = %q, want `run rerun 42 … --failed`", argv[1])
	}
}

// The stale-row case, and the one the branch search got wrong: the caller's row
// was rendered from a commit the PR no longer points at. A re-run can trigger a
// deployment, so this refuses rather than acting on whatever replaced it.
func TestGitHubRerunFailedChecks_RefusesAStaleHead(t *testing.T) {
	const staleHead = "9999888877776666555544443333222211110000"
	rollup := "[" + actionsCheck("build", 42, "o/r") + "]"
	p, recPath := stubGHRerun(t, currentHead, rollup)

	err := p.RerunFailedChecks(t.Context(), "o/r", 5, staleHead)
	if err == nil {
		t.Fatal("expected a refusal when the PR head moved since the row was read, got nil")
	}
	if !strings.Contains(err.Error(), currentHead) {
		t.Errorf("the refusal should name the commit the PR is on now, got %q", err)
	}
	if argv := recordLines(readRecord(t, recPath), "argv:"); len(argv) != 1 {
		t.Errorf("gh invoked %d times, want 1 (nothing may be re-run): %v", len(argv), argv)
	}
}

// An unpinned caller (a forge that reported no head SHA) still works: the
// re-run proceeds against whatever the head is now, which is the only commit
// this path can act on.
func TestGitHubRerunFailedChecks_UnpinnedCallerUsesTheLiveHead(t *testing.T) {
	rollup := "[" + actionsCheck("build", 42, "o/r") + "]"
	p, recPath := stubGHRerun(t, currentHead, rollup)

	if err := p.RerunFailedChecks(t.Context(), "o/r", 5, ""); err != nil {
		t.Fatalf("RerunFailedChecks: %v", err)
	}
	argv := recordLines(readRecord(t, recPath), "argv:")
	if len(argv) != 2 || !strings.Contains(argv[1], "run rerun 42") {
		t.Fatalf("argv = %v, want a rerun of the live head's failed run", argv)
	}
}

// TestGitHubRerunFailedChecks_NoFailedRunSaysSo is the case that motivated the
// rewrite: the current head's only failure is a third-party status, so there is
// no Actions run to re-run. The old code fell back to the newest failed run on
// the BRANCH, which is how an older commit's CI got re-run.
func TestGitHubRerunFailedChecks_NoFailedRunSaysSo(t *testing.T) {
	rollup := `[{"__typename":"StatusContext","context":"buildkite/deploy","state":"FAILURE",
	  "targetUrl":"https://buildkite.com/o/r/builds/77"}]`
	p, recPath := stubGHRerun(t, currentHead, rollup)

	err := p.RerunFailedChecks(t.Context(), "o/r", 5, currentHead)
	if err == nil {
		t.Fatal("expected an error when no failed Actions run exists, got nil")
	}
	if !strings.Contains(err.Error(), currentHead) {
		t.Errorf("error should name the commit it searched, got %q", err)
	}
	if !strings.Contains(err.Error(), "GitHub Actions only") {
		t.Errorf("error should say why there is nothing to re-run, got %q", err)
	}
	if argv := recordLines(readRecord(t, recPath), "argv:"); len(argv) != 1 {
		t.Errorf("gh invoked %d times, want 1 (no rerun should be attempted): %v", len(argv), argv)
	}
}

// Several failing workflows on one commit is an ordinary shape, and the choice
// between them must be deterministic. Run ids are assigned in creation order,
// so the highest is the newest attempt.
func TestGitHubRerunFailedChecks_PicksTheNewestFailedRunOnThatCommit(t *testing.T) {
	rollup := "[" + actionsCheck("old-build", 7, "o/r") + "," +
		actionsCheck("new-build", 88, "o/r") + "," +
		actionsCheck("mid-build", 50, "o/r") + "]"
	p, recPath := stubGHRerun(t, currentHead, rollup)

	if err := p.RerunFailedChecks(t.Context(), "o/r", 5, currentHead); err != nil {
		t.Fatalf("RerunFailedChecks: %v", err)
	}
	argv := recordLines(readRecord(t, recPath), "argv:")
	if !strings.Contains(argv[1], "run rerun 88") {
		t.Errorf("argv = %q, want the highest run id (88)", argv[1])
	}
}

func TestGHActionsRunID(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		repo    string
		want    int64
		wantOK  bool
		wantWhy string
	}{
		{
			name: "a job link", repo: "o/r",
			url:  "https://github.com/o/r/actions/runs/31893412903/job/95032969029",
			want: 31893412903, wantOK: true,
		},
		{
			name: "a bare run link", repo: "o/r",
			url:  "https://github.com/o/r/actions/runs/42",
			want: 42, wantOK: true,
		},
		{
			name: "an enterprise host", repo: "o/r",
			url:  "https://github.example.com/o/r/actions/runs/42/job/9",
			want: 42, wantOK: true,
		},
		{
			// gh echoes GitHub's canonical casing while the caller's repo
			// string came from the row.
			name: "casing differs", repo: "O/R",
			url:  "https://github.com/o/r/actions/runs/42",
			want: 42, wantOK: true,
		},
		{
			name: "another repository's run", repo: "o/r",
			url:     "https://github.com/someone/else/actions/runs/42",
			wantWhy: "re-running another repository's workflow is not this call's to make",
		},
		{
			name: "a third-party dashboard", repo: "o/r",
			url:     "https://buildkite.com/o/r/builds/77",
			wantWhy: "gh run cannot re-run a third-party status",
		},
		{
			name: "a check-suite link rather than a run", repo: "o/r",
			url:     "https://github.com/o/r/actions/checks/42",
			wantWhy: "not a run id",
		},
		{
			name: "a non-numeric id", repo: "o/r",
			url:     "https://github.com/o/r/actions/runs/latest",
			wantWhy: "not a run id",
		},
		{
			// Run ids start at 1. A zero (or negative) id is not a run
			// this call could re-run, and accepting it would report a
			// found run whose id names nothing.
			name: "a zero id", repo: "o/r",
			url:     "https://github.com/o/r/actions/runs/0",
			wantWhy: "run ids start at 1",
		},
		{
			name: "a negative id", repo: "o/r",
			url:     "https://github.com/o/r/actions/runs/-3",
			wantWhy: "run ids start at 1",
		},
		{
			name: "empty", repo: "o/r", url: "", wantWhy: "a check run may report no details URL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ghActionsRunID(tc.url, tc.repo)
			if ok != tc.wantOK {
				t.Fatalf("ghActionsRunID(%q,%q) ok = %t, want %t (%s)", tc.url, tc.repo, ok, tc.wantOK, tc.wantWhy)
			}
			if ok && got != tc.want {
				t.Errorf("ghActionsRunID(%q,%q) = %d, want %d", tc.url, tc.repo, got, tc.want)
			}
		})
	}
}

// Mergeable is GitHub's own verdict, and only the one word means yes.
// CONFLICTING and UNKNOWN are both "not mergeable" — UNKNOWN because GitHub is
// still computing it, which is not a green light.
func TestGitHubListPRs_MergeableIsTheExactVerdict(t *testing.T) {
	const out = `printf '%s' '[
	  {"number":1,"title":"clean","state":"OPEN","headRefOid":"aaa","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","statusCheckRollup":[]},
	  {"number":2,"title":"conflicted","state":"OPEN","headRefOid":"bbb","mergeable":"CONFLICTING","mergeStateStatus":"DIRTY","statusCheckRollup":[]},
	  {"number":3,"title":"pending","state":"OPEN","headRefOid":"ccc","mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN","statusCheckRollup":[]}
	]'`
	p, _ := newGitHubWithStub(t, out)

	prs, err := p.ListPRs(t.Context(), "o/r", StateOpen)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 3 {
		t.Fatalf("ListPRs returned %d PRs, want 3", len(prs))
	}
	want := map[int]bool{1: true, 2: false, 3: false}
	for _, pr := range prs {
		if pr.Mergeable != want[pr.Number] {
			t.Errorf("PR %d (%s) Mergeable = %t, want %t", pr.Number, pr.Title, pr.Mergeable, want[pr.Number])
		}
	}
}

// An unset state means open, and a state the caller DID supply must reach gh
// unchanged. Substituting open for an explicit "closed" quietly answers a
// different question than the one asked.
func TestGitHubList_StateDefaultsToOpenAndOtherwisePassesThrough(t *testing.T) {
	cases := []struct {
		name      string
		state     ListState
		wantState string
	}{
		{name: "unset means open", state: "", wantState: "--state open"},
		{name: "closed is passed through", state: StateClosed, wantState: "--state closed"},
		{name: "all is passed through", state: StateAll, wantState: "--state all"},
	}
	for _, tc := range cases {
		t.Run("prs_"+tc.name, func(t *testing.T) {
			p, recPath := newGitHubWithStub(t, `printf '%s' '[]'`)
			if _, err := p.ListPRs(t.Context(), "o/r", tc.state); err != nil {
				t.Fatalf("ListPRs(%q): %v", tc.state, err)
			}
			argv := recordLines(readRecord(t, recPath), "argv:")
			if len(argv) != 1 || !strings.Contains(argv[0], tc.wantState) {
				t.Errorf("gh pr list argv = %v, want it to carry %q", argv, tc.wantState)
			}
		})
		t.Run("issues_"+tc.name, func(t *testing.T) {
			p, recPath := newGitHubWithStub(t, `printf '%s' '[]'`)
			if _, err := p.ListIssues(t.Context(), "o/r", tc.state); err != nil {
				t.Fatalf("ListIssues(%q): %v", tc.state, err)
			}
			argv := recordLines(readRecord(t, recPath), "argv:")
			if len(argv) != 1 || !strings.Contains(argv[0], tc.wantState) {
				t.Errorf("gh issue list argv = %v, want it to carry %q", argv, tc.wantState)
			}
		})
	}
}

// gh targets a host through GH_HOST, so the host a provider was built with has
// to reach the subprocess environment. An explicit host silently replaced by
// github.com sends an enterprise user's request to the public site.
func TestNewGitHub_TargetsTheHostItWasGiven(t *testing.T) {
	cases := []struct {
		name     string
		host     string
		wantHost string
	}{
		{name: "empty means the public host", host: "", wantHost: "github.com"},
		{name: "an enterprise host is kept", host: "github.example.com", wantHost: "github.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := stubPath(t)
			recPath := dir + "/rec"
			stubCLI(t, dir, "gh", `printf 'host:%s\n' "$GH_HOST" >> `+recPath+`
printf '%s' '[]'`)
			p := newGitHub(tc.host)

			if _, err := p.ListPRs(t.Context(), "o/r", StateOpen); err != nil {
				t.Fatalf("ListPRs: %v", err)
			}

			hosts := recordLines(readRecord(t, recPath), "host:")
			if len(hosts) != 1 || hosts[0] != tc.wantHost {
				t.Errorf("gh saw GH_HOST %v, want [%s]", hosts, tc.wantHost)
			}
		})
	}
}
