package forges

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// leakTestToken is a recognizable placeholder PAT. If it ever appears in
// an error string, the token-leak regression has returned.
const leakTestToken = "gitea-pat-SUPERSECRET-do-not-leak-abc123"

// newGiteaWithToken points a gitea provider at host and stubs the tea
// CLI so `tea login helper get` mints leakTestToken for it — the same
// credential-helper path production uses (no config file is read).
func newGiteaWithToken(t *testing.T, host string) *giteaProvider {
	t.Helper()
	dir := stubPath(t)
	stubCLI(t, dir, "tea", `printf 'username=alice\npassword=`+leakTestToken+`\n'`)
	t.Cleanup(func() { teaTokenCache.Delete(host) })
	return newGitea(KindGitea, host)
}

// TestGiteaAPI_TokenNotInErrorOnHTTPError is the regression test for the
// PAT-leak fix. When the Gitea API returns a non-2xx status, the error
// surfaced to callers (and thence to writeOpsError → the HTTP response
// body and slog) must NOT contain the auth token. The token travels as
// an Authorization header, never a process argument, so it cannot be
// joined into a CmdError string as it was under the old curl path.
func TestGiteaAPI_TokenNotInErrorOnHTTPError(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	defer srv.Close()

	p := newGiteaWithToken(t, "leak.example")
	ctx := t.Context()

	assertNoToken := func(t *testing.T, op string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected an error on HTTP 500, got nil", op)
		}
		if strings.Contains(err.Error(), leakTestToken) {
			t.Fatalf("%s: token leaked into error: %q", op, err.Error())
		}
	}

	_, err := p.apiGet(ctx, srv.URL+"/api/v1/repos/o/r/pulls/1")
	assertNoToken(t, "apiGet", err)

	err = p.apiPostJSON(ctx, srv.URL+"/api/v1/repos/o/r/pulls/1/merge", []byte(`{"Do":"merge"}`))
	assertNoToken(t, "apiPostJSON", err)

	err = p.apiPatchJSON(ctx, srv.URL+"/api/v1/repos/o/r/issues/1", []byte(`{"state":"closed"}`))
	assertNoToken(t, "apiPatchJSON", err)

	// Positive control: the token WAS transmitted, and as a header —
	// proving auth still works and the value is structurally header-only.
	if want := "token " + leakTestToken; gotAuth != want {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, want)
	}
}

// TestGiteaAPI_TokenNotInErrorOnTransportError covers the connection-error
// branch of doAPI (server unreachable): the transport error must not
// carry the token either.
func TestGiteaAPI_TokenNotInErrorOnTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srvURL := srv.URL
	srv.Close() // force connection-refused on the next request

	p := newGiteaWithToken(t, "leak.example")
	_, err := p.apiGet(t.Context(), srvURL+"/api/v1/repos/o/r/pulls/1")
	if err == nil {
		t.Fatal("expected a transport error against a closed server, got nil")
	}
	if strings.Contains(err.Error(), leakTestToken) {
		t.Fatalf("token leaked into transport error: %q", err.Error())
	}
}

// TestGiteaAPI_SuccessReturnsBody verifies the 2xx path returns the raw
// body unchanged — the parsing callers (viewPR/viewIssue/CommitStatus)
// depend on it.
func TestGiteaAPI_SuccessReturnsBody(t *testing.T) {
	const wantBody = `{"login":"alice","full_name":"Alice"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(wantBody))
	}))
	defer srv.Close()

	p := newGiteaWithToken(t, "leak.example")
	out, err := p.apiGet(t.Context(), srv.URL+"/api/v1/users/alice")
	if err != nil {
		t.Fatalf("apiGet: %v", err)
	}
	if string(out) != wantBody {
		t.Fatalf("apiGet body = %q, want %q", out, wantBody)
	}
}

// TestGiteaParsePRs_NoCheckChip is the documented gitea degradation: the
// PR payload carries no CI state, so every gitea row reports NO check
// verdict. The two alternatives were both worse — one statuses request
// per PR (the N-call fan-out this work rejects) or a chip inferred from
// `mergeable`, which would present a guess as a fact.
func TestGiteaParsePRs_NoCheckChip(t *testing.T) {
	const payload = `[
	  {"number":4,"title":"Ready","state":"open","mergeable":true,
	   "head":{"ref":"feat","sha":"aaaaaaa1111"},"base":{"ref":"main"}},
	  {"number":3,"title":"Draft","state":"open","mergeable":true,"draft":true,
	   "head":{"ref":"wip","sha":"bbbbbbb2222"},"base":{"ref":"main"}},
	  {"number":2,"title":"Stuck","state":"open","mergeable":false,
	   "head":{"ref":"old","sha":"ccccccc3333"},"base":{"ref":"main"}}
	]`
	prs, err := parsePRs([]byte(payload))
	if err != nil {
		t.Fatalf("parsePRs: %v", err)
	}
	if len(prs) != 3 {
		t.Fatalf("parsePRs returned %d PRs, want 3", len(prs))
	}
	want := []struct {
		sha   string
		block string
	}{
		{"aaaaaaa1111", ""},
		{"bbbbbbb2222", blockDraft},
		// One bit with no cause behind it: blockUnknown says the forge
		// refuses without inventing conflicts or checks as the reason.
		{"ccccccc3333", blockUnknown},
	}
	for i, w := range want {
		got := prs[i]
		if got.CheckStatus != "" {
			t.Errorf("[%d] CheckStatus = %q, want \"\" (gitea reports no CI state)", i, got.CheckStatus)
		}
		if got.ChecksTotal != 0 || got.ChecksFailing != 0 {
			t.Errorf("[%d] check counts = %d/%d, want 0/0", i, got.ChecksTotal, got.ChecksFailing)
		}
		// The head SHA still arrives, so the merge pin works on gitea
		// even though the chip does not.
		if got.HeadSHA != w.sha {
			t.Errorf("[%d] HeadSHA = %q, want %q", i, got.HeadSHA, w.sha)
		}
		if got.MergeBlocked != w.block {
			t.Errorf("[%d] MergeBlocked = %q, want %q", i, got.MergeBlocked, w.block)
		}
	}
}

// TestGiteaRerunFailedChecks_NotSupported: tea has no CI verb and Gitea
// Actions' re-run endpoints are outside the stable API, so this reports
// the sentinel rather than failing obscurely. writeOpsError turns it into
// a 501 and the client hides the control.
func TestGiteaRerunFailedChecks_NotSupported(t *testing.T) {
	p := newGitea(KindGitea, "gitea.example")
	err := p.RerunFailedChecks(t.Context(), "o/r", 1, "aaaaaaa1111")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("RerunFailedChecks error = %v, want it to wrap ErrNotSupported", err)
	}
	if !strings.Contains(err.Error(), "gitea") {
		t.Errorf("error should name the forge it does not support, got %q", err)
	}
}

// TestGiteaMergeRequestBody covers the pin and the arm as BODY fields:
// tea's `pulls merge` exposes neither, and the merge already went through
// the API, so both ride the request Gitea documents.
func TestGiteaMergeRequestBody(t *testing.T) {
	cases := []struct {
		name string
		opts MergeOptions
		want string
	}{
		{
			// An instance predating merge_when_checks_succeed must see
			// exactly the body it has always seen.
			name: "plain merge omits both new fields",
			opts: MergeOptions{},
			want: `{"Do":"merge"}`,
		},
		{
			name: "head pin",
			opts: MergeOptions{HeadSHA: "aaaaaaa1111"},
			want: `{"Do":"merge","head_commit_id":"aaaaaaa1111"}`,
		},
		{
			name: "arming",
			opts: MergeOptions{Auto: true},
			want: `{"Do":"merge","merge_when_checks_succeed":true}`,
		},
		{
			name: "squash with both",
			opts: MergeOptions{Method: MergeSquash, HeadSHA: "aaaaaaa1111", Auto: true},
			want: `{"Do":"squash","head_commit_id":"aaaaaaa1111","merge_when_checks_succeed":true}`,
		},
		{
			name: "rebase",
			opts: MergeOptions{Method: MergeRebase},
			want: `{"Do":"rebase"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := giteaMergeRequestBody(tc.opts)
			if err != nil {
				t.Fatalf("giteaMergeRequestBody: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("body = %s, want %s", got, tc.want)
			}
		})
	}
}

// apiRequest is one intercepted Gitea API call.
type apiRequest struct {
	method string
	path   string
	body   string
}

// interceptGiteaAPI stands a TLS server up, points the package's shared API
// client at it, and returns a provider whose host IS that server plus the
// recorded request log.
//
// The TLS server plus the client swap is what makes the REAL methods testable:
// every Gitea call builds its own `https://<host>/api/v1/…` URL from p.host, so
// a test that wants to observe what ClosePR produces has to let ClosePR build
// that URL. Reconstructing the expected body and calling apiPatchJSON directly
// is the shape this replaced, and it asserted the test's own arithmetic — it
// stayed green for a ClosePR sending the wrong state, the wrong number, or no
// request at all.
//
// giteaAPIClient is package-global, so this must not run under t.Parallel.
func interceptGiteaAPI(t *testing.T) (*giteaProvider, *[]apiRequest) {
	t.Helper()
	var log []apiRequest
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		log = append(log, apiRequest{method: r.Method, path: r.URL.Path, body: string(b)})
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	previous := giteaAPIClient
	giteaAPIClient = srv.Client()
	t.Cleanup(func() { giteaAPIClient = previous })

	return newGiteaWithToken(t, strings.TrimPrefix(srv.URL, "https://")), &log
}

// TestGiteaClosePRAndReopenPR_AreTheSamePatchWithOppositeStates pins that reopen
// is the exact mirror of close: one PATCH, one field, one endpoint, one token
// path. Driven through the real methods, so the recorded method, URL and body
// are the ones the provider actually produces.
func TestGiteaClosePRAndReopenPR_AreTheSamePatchWithOppositeStates(t *testing.T) {
	p, log := interceptGiteaAPI(t)
	ctx := t.Context()

	if err := p.ClosePR(ctx, "o/r", 7); err != nil {
		t.Fatalf("ClosePR: %v", err)
	}
	if err := p.ReopenPR(ctx, "o/r", 7); err != nil {
		t.Fatalf("ReopenPR: %v", err)
	}

	want := []apiRequest{
		{method: http.MethodPatch, path: "/api/v1/repos/o/r/pulls/7", body: `{"state":"closed"}`},
		{method: http.MethodPatch, path: "/api/v1/repos/o/r/pulls/7", body: `{"state":"open"}`},
	}
	if len(*log) != len(want) {
		t.Fatalf("recorded %d requests, want %d: %+v", len(*log), len(want), *log)
	}
	for i, w := range want {
		got := (*log)[i]
		if got != w {
			t.Errorf("[%d] request = %+v, want %+v", i, got, w)
		}
	}
}

// The PR number and the repo travel from the arguments into the URL, so a
// provider addressing the wrong PR fails here rather than silently closing
// somebody else's.
func TestGiteaClosePR_AddressesTheRepoAndNumberItWasGiven(t *testing.T) {
	p, log := interceptGiteaAPI(t)
	if err := p.ClosePR(t.Context(), "other-owner/other-repo", 4242); err != nil {
		t.Fatalf("ClosePR: %v", err)
	}
	if len(*log) != 1 {
		t.Fatalf("recorded %d requests, want 1: %+v", len(*log), *log)
	}
	if got := (*log)[0].path; got != "/api/v1/repos/other-owner/other-repo/pulls/4242" {
		t.Errorf("path = %q, want the repo and number from the call", got)
	}
}

func TestGiteaMergeBlock(t *testing.T) {
	cases := []struct {
		name      string
		mergeable bool
		draft     bool
		want      string
	}{
		{"mergeable", true, false, ""},
		{"draft outranks mergeable", true, true, blockDraft},
		{"unmergeable has no stated cause", false, false, blockUnknown},
		{"draft outranks unmergeable", false, true, blockDraft},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := giteaMergeBlock(tc.mergeable, tc.draft); got != tc.want {
				t.Errorf("giteaMergeBlock(%t,%t) = %q, want %q", tc.mergeable, tc.draft, got, tc.want)
			}
		})
	}
}
