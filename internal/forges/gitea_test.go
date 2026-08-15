package forges

import (
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
