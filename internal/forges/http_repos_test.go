package forges

// Tests for the per-PR action route: the merge options carried as query
// parameters, the head-SHA boundary check, and the two new arms.
//
// handlePRAction takes its ForgeOps directly, so these drive the real
// handler with a recorder and a stub provider — no Manager, no CLI.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubOps records what the route asked of the provider. The embedded
// ForgeOps is nil: any method these tests do not exercise panics rather
// than silently returning a zero value, which is the behaviour we want
// from a stub.
type stubOps struct {
	mergeOpts MergeOptions
	ForgeOps
	rerunHead  string
	merged     int
	reopened   int
	rerun      int
	rerunErr   error
	closed     int
	mergeCalls int
	rerunCalls int
}

func (s *stubOps) MergePR(_ context.Context, _ string, number int, opts MergeOptions) error {
	s.mergeCalls++
	s.merged = number
	s.mergeOpts = opts
	return nil
}

func (s *stubOps) ClosePR(_ context.Context, _ string, number int) error {
	s.closed = number
	return nil
}

func (s *stubOps) ReopenPR(_ context.Context, _ string, number int) error {
	s.reopened = number
	return nil
}

func (s *stubOps) RerunFailedChecks(_ context.Context, _ string, number int, headSHA string) error {
	s.rerunCalls++
	s.rerun = number
	s.rerunHead = headSHA
	return s.rerunErr
}

// doPRAction drives one request through handlePRAction. target is
// "<n>/<op>[?query]"; the tail the router passes down is the PATH part
// only, which is exactly how handleRepos splits it in production.
func doPRAction(t *testing.T, ops ForgeOps, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	pathTail, _, _ := strings.Cut(target, "?")
	h := &HTTPHandler{}
	r := httptest.NewRequestWithContext(t.Context(), method,
		"/api/forges/github:github.com/repos/o/r/prs/"+target, nil)
	w := httptest.NewRecorder()
	h.handlePRAction(w, r, ops, "o/r", pathTail)
	return w
}

func TestHandlePRAction_MergeCarriesOptionsFromQuery(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want MergeOptions
	}{
		{
			name: "bare merge",
			tail: "7/merge",
			want: MergeOptions{},
		},
		{
			name: "head pin",
			tail: "7/merge?head_sha=aaaaaaa1111",
			want: MergeOptions{HeadSHA: "aaaaaaa1111"},
		},
		{
			name: "auto=1 arms",
			tail: "7/merge?auto=1",
			want: MergeOptions{Auto: true},
		},
		{
			name: "auto=true arms too",
			tail: "7/merge?auto=true",
			want: MergeOptions{Auto: true},
		},
		{
			name: "auto=0 does not arm",
			tail: "7/merge?auto=0",
			want: MergeOptions{},
		},
		{
			name: "method still travels",
			tail: "7/merge?method=squash&head_sha=aaaaaaa1111&auto=1",
			want: MergeOptions{Method: MergeSquash, HeadSHA: "aaaaaaa1111", Auto: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops := &stubOps{}
			w := doPRAction(t, ops, http.MethodPost, tc.tail)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
			}
			if ops.merged != 7 {
				t.Errorf("merged PR = %d, want 7", ops.merged)
			}
			if ops.mergeOpts != tc.want {
				t.Errorf("MergeOptions = %+v, want %+v", ops.mergeOpts, tc.want)
			}
		})
	}
}

// TestHandlePRAction_RejectsMalformedHeadSHA: the pin reaches a
// subprocess argv and a JSON body, so a non-SHA is refused at the
// boundary and the provider is never called.
//
// Both pinned actions are covered, because both spend the value the same way: a
// re-run resolves it against the PR's live head and can trigger a deployment,
// so it earns the same boundary check as the merge rather than a weaker one.
func TestHandlePRAction_RejectsMalformedHeadSHA(t *testing.T) {
	malformed := []string{
		"not-a-sha",
		"abc",                   // too short to be an object id
		"--match-head-commit",   // flag-shaped
		"aaaaaaa1111%20--admin", // whitespace-bearing
		"a123456789012345678901234567890123456789012345678901234567890abcd1", // 65 chars
	}
	for _, op := range []string{"merge", "rerun"} {
		for _, sha := range malformed {
			t.Run(op+"/"+sha, func(t *testing.T) {
				ops := &stubOps{}
				w := doPRAction(t, ops, http.MethodPost, "7/"+op+"?head_sha="+sha)
				if w.Code != http.StatusBadRequest {
					t.Errorf("status = %d, want 400", w.Code)
				}
				if ops.mergeCalls != 0 || ops.rerunCalls != 0 {
					t.Errorf("provider was called (%d merge, %d rerun), want 0",
						ops.mergeCalls, ops.rerunCalls)
				}
			})
		}
	}
}

// The rerun pin has to REACH the provider, not merely pass validation: the whole
// point is that the provider can refuse a run belonging to another commit.
func TestHandlePRAction_RerunCarriesTheHeadPin(t *testing.T) {
	const head = "aaaa1111bbbb2222cccc3333dddd4444eeee5555"
	ops := &stubOps{}
	if w := doPRAction(t, ops, http.MethodPost, "5/rerun?head_sha="+head); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
	}
	if ops.rerunHead != head {
		t.Errorf("head pin reaching the provider = %q, want %q", ops.rerunHead, head)
	}
	// Unpinned stays legal: a forge that reported no head SHA leaves the
	// re-run to resolve the live head itself.
	ops = &stubOps{}
	if w := doPRAction(t, ops, http.MethodPost, "5/rerun"); w.Code != http.StatusOK {
		t.Fatalf("unpinned status = %d, want 200 (body %s)", w.Code, w.Body)
	}
	if ops.rerunHead != "" {
		t.Errorf("unpinned rerun sent %q, want the empty pin", ops.rerunHead)
	}
}

func TestHandlePRAction_RoutesCloseReopenAndRerun(t *testing.T) {
	ops := &stubOps{}
	for _, tail := range []string{"3/close", "4/reopen", "5/rerun"} {
		if w := doPRAction(t, ops, http.MethodPost, tail); w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (body %s)", tail, w.Code, w.Body)
		}
	}
	if ops.closed != 3 {
		t.Errorf("closed = %d, want 3", ops.closed)
	}
	if ops.reopened != 4 {
		t.Errorf("reopened = %d, want 4", ops.reopened)
	}
	if ops.rerun != 5 {
		t.Errorf("rerun = %d, want 5", ops.rerun)
	}
}

// TestHandlePRAction_NotSupportedIs501 is what lets the client tell an
// absent capability from a failure: a forge with no re-run mechanism
// answers 501 with a machine-readable code, so the control can be hidden
// rather than offered and broken.
func TestHandlePRAction_NotSupportedIs501(t *testing.T) {
	ops := &stubOps{rerunErr: fmt.Errorf("%w: no CI verb here", ErrNotSupported)}
	w := doPRAction(t, ops, http.MethodPost, "5/rerun")
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %s: %v", w.Body, err)
	}
	if body.Code != string(ForgeErrNotSupported) {
		t.Errorf("code = %q, want %q", body.Code, ForgeErrNotSupported)
	}
}

func TestHandlePRAction_RejectsNonPOSTAndUnknownOps(t *testing.T) {
	ops := &stubOps{}
	if w := doPRAction(t, ops, http.MethodGet, "7/merge"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET merge: status = %d, want 405", w.Code)
	}
	if w := doPRAction(t, ops, http.MethodPost, "7/detonate"); w.Code != http.StatusNotFound {
		t.Errorf("unknown op: status = %d, want 404", w.Code)
	}
	if w := doPRAction(t, ops, http.MethodPost, "seven/merge"); w.Code != http.StatusBadRequest {
		t.Errorf("bad number: status = %d, want 400", w.Code)
	}
	if ops.mergeCalls != 0 {
		t.Errorf("provider was called %d times, want 0", ops.mergeCalls)
	}
}

func TestQueryTrue(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"", false},
		{"0", false},
		{"false", false},
		{"TRUE", false},
		{"yes", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := queryTrue(tc.in); got != tc.want {
				t.Errorf("queryTrue(%q) = %t, want %t", tc.in, got, tc.want)
			}
		})
	}
}
