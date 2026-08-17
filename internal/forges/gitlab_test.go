package forges

// GitLab provider tests.
//
// glab is not installed in this build environment, so every argv here is
// asserted against the stub-CLI harness rather than a real binary, and the
// flag spellings come from GitLab's own `glab mr merge` / `glab mr reopen`
// / `glab api` documentation. These tests are what pins them: a future
// flag rename shows up here as a failing expectation rather than as a
// silent runtime error.

import (
	"strings"
	"testing"
)

func newGitLabWithStub(t *testing.T, stdout string) (*gitlabProvider, string) {
	t.Helper()
	dir := stubPath(t)
	recPath := dir + "/rec"
	stubCLI(t, dir, "glab", recordingScript(recPath)+"\n"+stdout)
	return newGitLab("gitlab.com"), recPath
}

func TestMapGLabCheckStatus(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ci_still_running", checkPending},
		{"ci_must_pass", checkFailing},
		// `mergeable` is NOT a CI statement: a project that does not
		// require pipelines to pass is mergeable with a red pipeline, so
		// reading it as passing would paint green over a failure.
		{"mergeable", ""},
		{"not_approved", ""},
		{"conflict", ""},
		{"checking", ""},
		{"", ""},
		{"wat", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := mapGLabCheckStatus(tc.in); got != tc.want {
				t.Errorf("mapGLabCheckStatus(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMapGLabMergeBlock(t *testing.T) {
	cases := []struct {
		name         string
		detailed     string
		mergeStatus  string
		draft        bool
		hasConflicts bool
		want         string
	}{
		{"mergeable", "mergeable", "can_be_merged", false, false, ""},
		{"draft", "draft_status", "can_be_merged", true, false, blockDraft},
		{"conflict", "conflict", "cannot_be_merged", false, true, blockConflicts},
		{"ci must pass", "ci_must_pass", "can_be_merged", false, false, blockChecksFailing},
		{"ci still running", "ci_still_running", "can_be_merged", false, false, blockChecksRunning},
		{"needs rebase", "need_rebase", "can_be_merged", false, false, blockBehind},
		{"not computed yet", "checking", "unchecked", false, false, blockUnknown},
		{"approvals missing is policy", "not_approved", "can_be_merged", false, false, blockProtected},
		{"discussions open is policy", "discussions_not_resolved", "can_be_merged", false, false, blockProtected},
		{"status checks are policy", "status_checks_must_pass", "can_be_merged", false, false, blockProtected},
		// detailed_merge_status absent (an instance old enough not to
		// send it): the deprecated field plus the conflict flag answer.
		{"legacy mergeable", "", "can_be_merged", false, false, ""},
		{"legacy draft", "", "can_be_merged", true, false, blockDraft},
		{"legacy conflicts", "", "cannot_be_merged", false, true, blockConflicts},
		{"legacy unmergeable without a cause", "", "cannot_be_merged", false, false, blockUnknown},
		{"legacy recheck", "", "cannot_be_merged_recheck", false, false, blockUnknown},
		{"legacy silent", "", "", false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapGLabMergeBlock(tc.detailed, tc.mergeStatus, tc.draft, tc.hasConflicts)
			if got != tc.want {
				t.Errorf("mapGLabMergeBlock(%q,%q,%t,%t) = %q, want %q",
					tc.detailed, tc.mergeStatus, tc.draft, tc.hasConflicts, got, tc.want)
			}
		})
	}
}

// TestGitLabListPRs_FoldsListPayload is D99's GitLab half: the pipeline
// state, the merge cause, the head SHA and the auto-merge flag all come
// off the ONE `glab mr list` payload. head_pipeline is deliberately not
// read (GitLab documents it on the single-MR response only), which is why
// the CI verdict comes from detailed_merge_status instead.
func TestGitLabListPRs_FoldsListPayload(t *testing.T) {
	const out = `printf '%s' '[
	  {"iid":3,"title":"Running","state":"opened","sha":"aaaaaaa1111",
	   "merge_status":"can_be_merged","detailed_merge_status":"ci_still_running",
	   "merge_when_pipeline_succeeds":true},
	  {"iid":2,"title":"Failing","state":"opened","sha":"bbbbbbb2222",
	   "merge_status":"can_be_merged","detailed_merge_status":"ci_must_pass"},
	  {"iid":1,"title":"Clean","state":"opened","sha":"ccccccc3333",
	   "merge_status":"can_be_merged","detailed_merge_status":"mergeable"}
	]'`
	p, recPath := newGitLabWithStub(t, out)

	prs, err := p.ListPRs(t.Context(), "grp/proj", StateOpen)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 3 {
		t.Fatalf("ListPRs returned %d MRs, want 3", len(prs))
	}
	want := []struct {
		sha    string
		check  string
		block  string
		armed  bool
		number int
	}{
		{"aaaaaaa1111", checkPending, blockChecksRunning, true, 3},
		{"bbbbbbb2222", checkFailing, blockChecksFailing, false, 2},
		{"ccccccc3333", "", "", false, 1},
	}
	for i, w := range want {
		got := prs[i]
		if got.Number != w.number {
			t.Errorf("[%d] Number = %d, want %d", i, got.Number, w.number)
		}
		if got.HeadSHA != w.sha {
			t.Errorf("[%d] HeadSHA = %q, want %q", i, got.HeadSHA, w.sha)
		}
		if got.CheckStatus != w.check {
			t.Errorf("[%d] CheckStatus = %q, want %q", i, got.CheckStatus, w.check)
		}
		if got.MergeBlocked != w.block {
			t.Errorf("[%d] MergeBlocked = %q, want %q", i, got.MergeBlocked, w.block)
		}
		if got.AutoMergeArmed != w.armed {
			t.Errorf("[%d] AutoMergeArmed = %t, want %t", i, got.AutoMergeArmed, w.armed)
		}
	}
	if argv := recordLines(readRecord(t, recPath), "argv:"); len(argv) != 1 {
		t.Fatalf("glab invoked %d times, want exactly 1: %v", len(argv), argv)
	}
}

func TestGitLabMergePR_Argv(t *testing.T) {
	cases := []struct {
		name string
		opts MergeOptions
		want []string
		deny []string
	}{
		{
			// glab documents --auto-merge as defaulting to true, so a
			// plain merge must keep sending exactly what it always has.
			name: "plain merge is byte-for-byte what it was",
			opts: MergeOptions{},
			want: []string{"mr merge 4", "--repo grp/proj", "--yes"},
			deny: []string{"--sha", "--auto-merge"},
		},
		{
			name: "head pin uses --sha",
			opts: MergeOptions{HeadSHA: "aaaaaaa1111"},
			want: []string{"--sha aaaaaaa1111"},
			deny: []string{"--auto-merge"},
		},
		{
			name: "arming is explicit",
			opts: MergeOptions{HeadSHA: "aaaaaaa1111", Auto: true},
			want: []string{"--sha aaaaaaa1111", "--auto-merge=true"},
		},
		{
			name: "squash keeps its strategy",
			opts: MergeOptions{Method: MergeSquash, HeadSHA: "aaaaaaa1111"},
			want: []string{"--squash", "--sha aaaaaaa1111"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, recPath := newGitLabWithStub(t, "")
			if err := p.MergePR(t.Context(), "grp/proj", 4, tc.opts); err != nil {
				t.Fatalf("MergePR: %v", err)
			}
			argv := recordLines(readRecord(t, recPath), "argv:")
			if len(argv) != 1 {
				t.Fatalf("glab invoked %d times, want 1: %v", len(argv), argv)
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

func TestGitLabReopenPR_Argv(t *testing.T) {
	p, recPath := newGitLabWithStub(t, "")
	if err := p.ReopenPR(t.Context(), "grp/proj", 9); err != nil {
		t.Fatalf("ReopenPR: %v", err)
	}
	argv := recordLines(readRecord(t, recPath), "argv:")
	if len(argv) != 1 || !strings.Contains(argv[0], "mr reopen 9 --repo grp/proj") {
		t.Fatalf("argv = %v, want a single `mr reopen 9 --repo grp/proj`", argv)
	}
}

// glMRHead is the head commit every rerun fixture's MR reports.
const glMRHead = "aaaa1111bbbb2222cccc3333dddd4444eeee5555"

// stubGLabRerun stands up a glab stub answering the single-MR read.
func stubGLabRerun(t *testing.T, mrJSON string) (*gitlabProvider, string) {
	t.Helper()
	dir := stubPath(t)
	recPath := dir + "/rec"
	stubCLI(t, dir, "glab", recordingScript(recPath)+`
case "$*" in
  *"merge_requests/4"*) printf '%s' '`+mrJSON+`' ;;
esac`)
	return newGitLab("gitlab.com"), recPath
}

// TestGitLabRerunFailedChecks_RetriesHeadPipeline: GitLab has no
// "rerun failed" verb — retrying failed jobs IS a pipeline endpoint, so
// this reads head_pipeline off the single-MR response and POSTs the
// retry through glab api, the same route CommitStatus already takes.
//
// head_pipeline is a commit linkage rather than a branch one (it is the pipeline
// GitLab reports FOR the MR's head), so this path never had GitHub's
// wrong-commit defect; what the pin adds is the stale-row refusal below.
func TestGitLabRerunFailedChecks_RetriesHeadPipeline(t *testing.T) {
	p, recPath := stubGLabRerun(t,
		`{"iid":4,"sha":"`+glMRHead+`","head_pipeline":{"id":8821,"status":"failed"}}`)

	if err := p.RerunFailedChecks(t.Context(), "grp/proj", 4, glMRHead); err != nil {
		t.Fatalf("RerunFailedChecks: %v", err)
	}
	argv := recordLines(readRecord(t, recPath), "argv:")
	if len(argv) != 2 {
		t.Fatalf("glab invoked %d times, want 2 (mr read, pipeline retry): %v", len(argv), argv)
	}
	if !strings.Contains(argv[0], "api projects/grp%2Fproj/merge_requests/4") {
		t.Errorf("first call should read the MR: %s", argv[0])
	}
	if !strings.Contains(argv[1], "--method POST") ||
		!strings.Contains(argv[1], "projects/grp%2Fproj/pipelines/8821/retry") {
		t.Errorf("second call should POST the pipeline retry: %s", argv[1])
	}
}

// The stale-row case: the caller's row was rendered from a commit the MR no
// longer points at, so nothing is retried.
func TestGitLabRerunFailedChecks_RefusesAStaleHead(t *testing.T) {
	const staleHead = "9999888877776666555544443333222211110000"
	p, recPath := stubGLabRerun(t,
		`{"iid":4,"sha":"`+glMRHead+`","head_pipeline":{"id":8821,"status":"failed"}}`)

	err := p.RerunFailedChecks(t.Context(), "grp/proj", 4, staleHead)
	if err == nil {
		t.Fatal("expected a refusal when the MR head moved since the row was read, got nil")
	}
	if !strings.Contains(err.Error(), glMRHead) {
		t.Errorf("the refusal should name the commit the MR is on now, got %q", err)
	}
	if argv := recordLines(readRecord(t, recPath), "argv:"); len(argv) != 1 {
		t.Errorf("glab invoked %d times, want 1 (nothing may be retried): %v", len(argv), argv)
	}
}

// A merged-results pipeline runs against a synthesized merge commit, so the
// pipeline's own sha does NOT equal the MR's head. head_pipeline is GitLab's
// own answer to "which pipeline is this MR's", so the retry proceeds: comparing
// the pipeline's sha would refuse every re-run on a project using merged
// results or a merge train.
func TestGitLabRerunFailedChecks_AcceptsAMergedResultsPipeline(t *testing.T) {
	p, recPath := stubGLabRerun(t,
		`{"iid":4,"sha":"`+glMRHead+`","head_pipeline":{"id":8821,"status":"failed",`+
			`"sha":"0000111122223333444455556666777788889999"}}`)

	if err := p.RerunFailedChecks(t.Context(), "grp/proj", 4, glMRHead); err != nil {
		t.Fatalf("RerunFailedChecks: %v", err)
	}
	argv := recordLines(readRecord(t, recPath), "argv:")
	if len(argv) != 2 || !strings.Contains(argv[1], "pipelines/8821/retry") {
		t.Fatalf("argv = %v, want the head pipeline retried", argv)
	}
}

func TestGitLabRerunFailedChecks_NoPipelineSaysSo(t *testing.T) {
	p, recPath := newGitLabWithStub(t, `printf '%s' '{"iid":4}'`)

	err := p.RerunFailedChecks(t.Context(), "grp/proj", 4, "")
	if err == nil {
		t.Fatal("expected an error when the MR has no pipeline, got nil")
	}
	if !strings.Contains(err.Error(), "!4") {
		t.Errorf("error should name the MR, got %q", err)
	}
	if argv := recordLines(readRecord(t, recPath), "argv:"); len(argv) != 1 {
		t.Errorf("glab invoked %d times, want 1 (no retry should be attempted): %v", len(argv), argv)
	}
}

func TestGLabProjectPath(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "grp/proj", want: "grp%2Fproj"},
		{in: "grp/sub", want: "grp%2Fsub"},
		{in: "noslash", wantErr: true},
		{in: "/proj", wantErr: true},
		{in: "grp/", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := gLabProjectPath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("gLabProjectPath(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("gLabProjectPath(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("gLabProjectPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
