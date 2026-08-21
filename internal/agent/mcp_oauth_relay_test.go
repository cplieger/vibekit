package agent

// Tests for mcp_oauth_relay.go: the paste-the-return-address relay that
// rescues an MCP OAuth callback the browser could not deliver.
//
// The listener in these tests is a real httptest server on 127.0.0.1, so the
// dial half is exercised end to end rather than mocked: httptest binds an
// ephemeral loopback port, which is exactly the shape KAS's own redirect
// listener has, and it is what relayClientFor's loopback address policy must
// accept. A stubbed transport would have proved nothing about that policy.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/ssrf/v4"
)

// authURLFor builds the authorization URL KAS would have stored for a flow
// whose loopback listener is at listenerURL. This is the relay's trust anchor,
// so the fixture builds it the way KAS does — redirect_uri plus state as query
// parameters on the provider's authorize endpoint — rather than letting a test
// hand-pick the fields it wants to pass.
func authURLFor(t *testing.T, listenerURL, state string) string {
	t.Helper()
	lu, err := url.Parse(listenerURL)
	if err != nil {
		t.Fatalf("parse listener URL %q: %v", listenerURL, err)
	}
	redirect := "http://127.0.0.1:" + lu.Port() + "/oauth/callback"
	q := url.Values{
		"client_id":     {"vibekit-test"},
		"response_type": {"code"},
		"redirect_uri":  {redirect},
		"state":         {state},
	}
	return "https://provider.example/authorize?" + q.Encode()
}

// pastedFor builds what the user copies out of the dead browser tab.
func pastedFor(t *testing.T, listenerURL, code, state string) string {
	t.Helper()
	lu, err := url.Parse(listenerURL)
	if err != nil {
		t.Fatalf("parse listener URL %q: %v", listenerURL, err)
	}
	q := url.Values{"code": {code}, "state": {state}}
	return "http://127.0.0.1:" + lu.Port() + "/oauth/callback?" + q.Encode()
}

// postRelay drives the handler and returns the recorder.
func postRelay(t *testing.T, h *Runtime, server, pasted string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"server":` + strconv.Quote(server) + `,"redirect_url":` + strconv.Quote(pasted) + `}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/mcp/oauth-relay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleOAuthRelay(rec, req)
	return rec
}

// callbackListener stands in for KAS's redirect listener. It records the query
// it received so a test can prove the code arrived VERBATIM — the relay's whole
// job is to deliver the provider's parameters unaltered, and a relay that
// re-encoded them would break the token exchange it exists to unblock.
type callbackListener struct {
	srv     *httptest.Server
	status  int
	gotCode string
	gotHits int
}

func newCallbackListener(t *testing.T, status int) *callbackListener {
	t.Helper()
	l := &callbackListener{status: status}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.gotHits++
		l.gotCode = r.URL.Query().Get("code")
		w.WriteHeader(l.status)
		_, _ = w.Write([]byte("you can close this window"))
	}))
	t.Cleanup(l.srv.Close)
	return l
}

// stageFlow puts a server into the waiting-for-authorization state with the
// authorization URL KAS would have advertised.
func stageFlow(t *testing.T, h *Runtime, server, listenerURL, state string) {
	t.Helper()
	h.mcpRegistry.RecordOAuth(t.Context(), server, authURLFor(t, listenerURL, state))
}

// relayState reads the server's latch off the registry SNAPSHOT, which is the
// same projection /api/mcp/status serves. Reading the wire surface rather than a
// private accessor is deliberate: "may this be pasted again" is a question the
// client answers from that field, so the test asserts what the client would see.
func relayState(t *testing.T, h *Runtime, server string) (relayed, pending bool) {
	t.Helper()
	for _, s := range h.mcpRegistry.Snapshot() {
		if s.Name == server {
			return s.Relayed, s.State == mcpStateOAuth
		}
	}
	t.Fatalf("server %q is not in the registry snapshot", server)
	return false, false
}

func TestOAuthRelay_DeliversAStrandedCallback(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")

	rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "the-code", "st-abc"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if l.gotHits != 1 {
		t.Errorf("listener hits = %d, want exactly 1", l.gotHits)
	}
	// Verbatim: the code KAS's listener sees must be the code the provider
	// issued, or the token exchange this relay exists to unblock fails.
	if l.gotCode != "the-code" {
		t.Errorf("listener saw code %q, want %q", l.gotCode, "the-code")
	}
}

// The relay is SINGLE USE per authorization attempt. A code is spent by the
// first delivery, so a second paste would make KAS's own single-use rule answer
// with an error the user cannot interpret; refusing here says what happened.
func TestOAuthRelay_IsSingleUsePerAttempt(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")
	pasted := pastedFor(t, l.srv.URL, "the-code", "st-abc")

	if rec := postRelay(t, h, "linear", pasted); rec.Code != http.StatusOK {
		t.Fatalf("first relay status = %d, want 200", rec.Code)
	}
	rec := postRelay(t, h, "linear", pasted)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second relay status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already delivered") {
		t.Errorf("second relay body = %s, want it to name the already-delivered case", rec.Body.String())
	}
	if l.gotHits != 1 {
		t.Errorf("listener hits = %d, want 1: the second paste must not reach it", l.gotHits)
	}
}

// blockingListener is KAS's redirect listener held OPEN. The first request
// parks inside the handler until the test releases it, so the test can act while
// a relay is genuinely in flight — the only window the two races below live in.
// Every LATER request answers immediately and is counted, which is what makes a
// regression report itself: a second callback that should never have been
// replayed shows up as a second hit instead of deadlocking the test.
type blockingListener struct {
	srv     *httptest.Server
	codes   chan string
	release chan struct{}
	once    sync.Once
	hits    atomic.Int64
}

func newBlockingListener(t *testing.T, status int) *blockingListener {
	t.Helper()
	l := &blockingListener{
		codes:   make(chan string, 8),
		release: make(chan struct{}),
	}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := l.hits.Add(1)
		l.codes <- r.URL.Query().Get("code")
		if n == 1 {
			<-l.release
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("you can close this window"))
	}))
	t.Cleanup(func() {
		l.releaseAll()
		l.srv.Close()
	})
	return l
}

// waitForCallback blocks until the next callback reaches the listener and
// returns its code. This is the synchronization the concurrency tests hang off:
// a sleep here could land before the relay arrived, and every assertion after it
// would then pass without the race window ever opening.
func (l *blockingListener) waitForCallback(t *testing.T) string {
	t.Helper()
	select {
	case code := <-l.codes:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("no callback reached the loopback listener")
		return ""
	}
}

// releaseAll lets every parked request answer. Idempotent so the cleanup can
// call it after a test already did.
func (l *blockingListener) releaseAll() {
	l.once.Do(func() { close(l.release) })
}

// The single-use rule has to be ATOMIC, and the serial test above cannot show
// that: it starts its second paste only after the first has finished latching,
// which a check-then-act implementation passes. Here the listener holds the
// first relay open until a second paste has attempted entry, which is the real
// double-click / two-device window. Both callers reading "not relayed yet" would
// spend the same authorization code twice, and KAS's own single-use rule would
// then answer one of them with an error the user cannot act on.
func TestOAuthRelay_ConcurrentPastesDeliverOnce(t *testing.T) {
	l := newBlockingListener(t, http.StatusOK)
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")
	pasted := pastedFor(t, l.srv.URL, "the-code", "st-abc")

	firstStatus := make(chan int, 1)
	go func() { firstStatus <- postRelay(t, h, "linear", pasted).Code }()
	if code := l.waitForCallback(t); code != "the-code" {
		t.Fatalf("listener saw code %q, want %q", code, "the-code")
	}

	// In flight now. This is the paste that must be refused.
	second := postRelay(t, h, "linear", pasted)
	l.releaseAll()

	if code := <-firstStatus; code != http.StatusOK {
		t.Fatalf("first relay status = %d, want 200", code)
	}
	if second.Code != http.StatusConflict {
		t.Fatalf("concurrent second relay status = %d, want 409: the reservation must be taken before the network call, not after it; body %s",
			second.Code, second.Body.String())
	}
	if got := l.hits.Load(); got != 1 {
		t.Errorf("listener hits = %d, want exactly 1: the same authorization code was replayed twice", got)
	}
}

// An old callback's completion must not attribute itself to whatever attempt is
// current when it lands. recordOAuth replaces the record on every new sign-in,
// so a relay that was still out when the user restarted would otherwise mark the
// NEW attempt as delivered: its own callback was never replayed, the paste box is
// withheld, and the user is stranded until they start yet another sign-in.
func TestOAuthRelay_AnOldRelayCannotLatchANewAttempt(t *testing.T) {
	l := newBlockingListener(t, http.StatusOK)
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-one")
	pastedA := pastedFor(t, l.srv.URL, "c1", "st-one")

	firstStatus := make(chan int, 1)
	go func() { firstStatus <- postRelay(t, h, "linear", pastedA).Code }()
	if code := l.waitForCallback(t); code != "c1" {
		t.Fatalf("listener saw code %q, want the first attempt's %q", code, "c1")
	}

	// The user restarted the sign-in while attempt A's callback is still out:
	// KAS advertises a new authorization URL and recordOAuth replaces the record.
	stageFlow(t, h, "linear", l.srv.URL, "st-two")
	l.releaseAll()
	if code := <-firstStatus; code != http.StatusOK {
		t.Fatalf("the in-flight relay ended %d, want 200", code)
	}

	if relayed, pending := relayState(t, h, "linear"); relayed || !pending {
		t.Fatalf("relayed = %v, pending = %v; the old relay's completion latched the new attempt, whose own callback was never delivered",
			relayed, pending)
	}
	// And the new attempt is still relayable, which is the user-visible half.
	rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "c2", "st-two"))
	if rec.Code != http.StatusOK {
		t.Fatalf("relaying the new attempt = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if code := l.waitForCallback(t); code != "c2" {
		t.Errorf("listener saw code %q, want the second attempt's %q", code, "c2")
	}
}

// A fresh authorization attempt clears the single-use latch. recordOAuth
// replaces the whole record, so this asserts the reset survives that rewrite —
// without it a user who restarted a sign-in could never relay again.
func TestOAuthRelay_ANewAttemptClearsTheLatch(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-one")
	if rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "c1", "st-one")); rec.Code != http.StatusOK {
		t.Fatalf("first relay status = %d, want 200", rec.Code)
	}

	stageFlow(t, h, "linear", l.srv.URL, "st-two")
	rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "c2", "st-two"))

	if rec.Code != http.StatusOK {
		t.Fatalf("relay after a new attempt status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if l.gotCode != "c2" {
		t.Errorf("listener saw code %q, want the second attempt's %q", l.gotCode, "c2")
	}
}

// No flow in flight means no relay. This is what keeps the route from being a
// standing lever on an HTTP surface that carries no auth of its own: with
// nothing waiting there is no stored state to check a paste against.
func TestOAuthRelay_RefusesWithNoFlowInFlight(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	for name, server := range map[string]string{
		"a server with no authorization pending": "linear",
		"a server that does not exist at all":    "never-heard-of-it",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(nil)
			if server == "linear" {
				// Connected, not awaiting authorization.
				h.mcpRegistry.RecordConnected(t.Context(), server, nil, nil, nil)
			}
			rec := postRelay(t, h, server, pastedFor(t, l.srv.URL, "c", "st"))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body.String())
			}
			if l.gotHits != 0 {
				t.Errorf("listener hits = %d, want 0", l.gotHits)
			}
		})
	}
}

// A listener that REFUSES the callback leaves the attempt retryable. KAS runs
// its own state check and its own single-use rule, so a 4xx here is most often
// that check talking, and latching the attempt would strand the user on a
// "already delivered" refusal for a code that was never accepted.
func TestOAuthRelay_ARefusedCallbackStaysRetryable(t *testing.T) {
	l := newCallbackListener(t, http.StatusBadRequest)
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")

	rec := postRelay(t, h, "linear", pastedFor(t, l.srv.URL, "the-code", "st-abc"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
	if relayed, pending := relayState(t, h, "linear"); relayed || !pending {
		t.Errorf("relayed = %v, pending = %v; want a still-unrelayed pending flow", relayed, pending)
	}
}

// A listener that is GONE (the flow timed out and KAS closed it) reports a
// gateway failure rather than a success the user would wait on forever.
func TestOAuthRelay_ADeadListenerIsAGatewayFailure(t *testing.T) {
	l := newCallbackListener(t, http.StatusOK)
	dead := l.srv.URL
	l.srv.Close() // frees the port; nothing is listening now
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", dead, "st-abc")

	rec := postRelay(t, h, "linear", pastedFor(t, dead, "the-code", "st-abc"))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
}

func TestOAuthRelay_RejectsNonPOST(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/mcp/oauth-relay", nil)
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleOAuthRelay(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// The authorization code must never reach the log. It is a bearer credential
// for the duration of the exchange, and the logs ship to Loki.
func TestOAuthRelay_NeverLogsTheCode(t *testing.T) {
	const secret = "super-secret-authorization-code"
	logs := captureLogs(t)
	l := newCallbackListener(t, http.StatusOK)
	h := newHubWithMCPConfig(nil)
	stageFlow(t, h, "linear", l.srv.URL, "st-abc")

	// Three paths, and the third is the one that actually leaked. net/http wraps
	// every transport failure in a *url.Error whose message opens with the full
	// request URL, so logging the error verbatim put the code in Loki on exactly
	// the path most likely to be read: the one where the relay failed.
	postRelay(t, h, "linear", pastedFor(t, l.srv.URL, secret, "st-abc"))

	stageFlow(t, h, "linear", l.srv.URL, "st-abc")
	l.status = http.StatusInternalServerError
	postRelay(t, h, "linear", pastedFor(t, l.srv.URL, secret, "st-abc"))

	dead := l.srv.URL
	l.srv.Close()
	stageFlow(t, h, "linear", dead, "st-abc")
	postRelay(t, h, "linear", pastedFor(t, dead, secret, "st-abc"))

	if got := logs.String(); strings.Contains(got, secret) {
		t.Errorf("the authorization code reached the log:\n%s", got)
	}
}

func TestValidateRelayAddress(t *testing.T) {
	const (
		host  = "127.0.0.1:41234"
		state = "st-abc"
	)
	authURL := "https://provider.example/authorize?" + url.Values{
		"redirect_uri": {"http://" + host + "/oauth/callback"},
		"state":        {state},
	}.Encode()

	cases := map[string]struct {
		pasted string
		auth   string
		want   error
	}{
		"the ordinary stranded callback": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state,
			want:   nil,
		},
		"clipboard whitespace is trimmed, not rejected": {
			pasted: "  http://" + host + "/oauth/callback?code=abc&state=" + state + "\n",
			want:   nil,
		},
		// The user's address bar may say `localhost` where KAS advertised the
		// literal, or the reverse. Both are loopback and neither decides the
		// dial target, so the spelling is not a refusal in either direction.
		"a localhost paste against a 127.0.0.1 advertisement": {
			pasted: "http://localhost:41234/oauth/callback?code=abc&state=" + state,
			want:   nil,
		},
		"a 127.0.0.1 paste against a localhost advertisement": {
			pasted: "http://127.0.0.1:41234/oauth/callback?code=abc&state=" + state,
			auth: "https://provider.example/authorize?" + url.Values{
				"redirect_uri": {"http://localhost:41234/oauth/callback"},
				"state":        {state},
			}.Encode(),
			want: nil,
		},
		"the RFC 9207 issuer parameter rides along": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state + "&iss=https%3A%2F%2Fp.example",
			want:   nil,
		},
		// --- the address is not a loopback callback ---
		"a remote host is refused": {
			pasted: "http://evil.example:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayNotLoopback,
		},
		"a host that merely starts with the literal is refused": {
			pasted: "http://127.0.0.1.evil.example:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayNotLoopback,
		},
		"a link-local address is not loopback": {
			pasted: "http://169.254.169.254:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayNotLoopback,
		},
		// The ORDER is the property, which is why the expected error is the byte
		// gate rather than the loopback gate. isLoopbackHost lowercases its input
		// before matching an ALLOW-LIST, and a widening fold on an allow-list
		// fails OPEN — the class that shipped a live Host-header widening in
		// webhttp. Measured on go1.27.0 (Unicode 17.0.0): strings.ToLower maps
		// exactly two already-assigned runes into pure ASCII, U+0130 -> "i" and
		// U+212A -> "k", and none of this gate's three literals ("127.0.0.1",
		// "::1", "localhost") contains an i or a k, so 0 of the ~1.11M non-ASCII
		// one-rune substitutions across them are accepted. That makes the site
		// provably unlaunderable on any Unicode version, not merely on this one.
		// isPrintableASCII running FIRST is a second, independent proof (it
		// admits 0 non-ASCII bytes at all, and url.Parse admits 0 non-ASCII runes
		// into a scheme), and this case is what keeps the two in that order:
		// moving the byte gate after the host check would change this error
		// identity while leaving every other case green.
		"a non-ASCII host never reaches the loopback allow-list": {
			pasted: "http://\u212Aocalhost:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayBadBytes,
		},
		"https is a different address, not a nicer one": {
			pasted: "https://" + host + "/oauth/callback?code=abc&state=" + state,
			want:   errRelayNotHTTP,
		},
		"a non-http scheme is refused": {
			pasted: "file:///etc/passwd?code=abc&state=" + state,
			want:   errRelayNotHTTP,
		},
		"userinfo is refused": {
			pasted: "http://user:pw@127.0.0.1:41234/oauth/callback?code=abc&state=" + state,
			want:   errRelayHasCreds,
		},
		"a fragment means this is not the address the browser was sent to": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state + "#frag",
			want:   errRelayHasFragment,
		},
		"a privileged port is refused": {
			pasted: "http://127.0.0.1:80/oauth/callback?code=abc&state=" + state,
			want:   errRelayBadPort,
		},
		"no port at all is refused": {
			pasted: "http://127.0.0.1/oauth/callback?code=abc&state=" + state,
			want:   errRelayBadPort,
		},
		// --- the payload is not a callback ---
		"no code means the sign-in did not complete": {
			pasted: "http://" + host + "/oauth/callback?state=" + state,
			want:   errRelayNoCode,
		},
		"an empty code is no code": {
			pasted: "http://" + host + "/oauth/callback?code=&state=" + state,
			want:   errRelayNoCode,
		},
		"an error redirect carries nothing to relay": {
			pasted: "http://" + host + "/oauth/callback?error=access_denied&state=" + state,
			want:   errRelayBadQuery,
		},
		"an unexpected query parameter is refused, not forwarded": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state + "&surprise=1",
			want:   errRelayBadQuery,
		},
		"a CRLF injection attempt cannot reach the request line": {
			pasted: "http://" + host + "/oauth/callback?code=a\r\nX-Evil: 1&state=" + state,
			want:   errRelayBadBytes,
		},
		"a raw space is refused": {
			pasted: "http://" + host + "/oauth/callback?code=a b&state=" + state,
			want:   errRelayBadBytes,
		},
		"a NUL byte is refused": {
			pasted: "http://" + host + "/oauth/callback?code=a\x00b&state=" + state,
			want:   errRelayBadBytes,
		},
		"non-ASCII is refused": {
			pasted: "http://" + host + "/oauth/callback?code=café&state=" + state,
			want:   errRelayBadBytes,
		},
		"an oversize paste is refused before it is parsed": {
			pasted: "http://" + host + "/oauth/callback?code=" + strings.Repeat("a", relayURLCap) + "&state=" + state,
			want:   errRelayTooLong,
		},
		"a non-URL is refused": {
			pasted: "http://[not-an-address/oauth/callback?code=abc",
			want:   errRelayUnparsable,
		},
		// --- disagreement with what KAS advertised ---
		"a loopback port KAS never advertised is refused": {
			pasted: "http://127.0.0.1:9999/oauth/callback?code=abc&state=" + state,
			want:   errRelayTargetDrift,
		},
		"a path KAS never advertised is refused": {
			pasted: "http://" + host + "/somewhere/else?code=abc&state=" + state,
			want:   errRelayTargetDrift,
		},
		"a mismatched state belongs to a different sign-in": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=st-other",
			want:   errRelayStateDrift,
		},
		"a missing state cannot be verified": {
			pasted: "http://" + host + "/oauth/callback?code=abc",
			want:   errRelayStateDrift,
		},
		"a state that is a prefix of the real one is refused": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=st-ab",
			want:   errRelayStateDrift,
		},
		// --- the stored authorization URL cannot anchor a relay ---
		"an authorization URL with no redirect_uri has nothing to relay to": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state,
			auth:   "https://provider.example/authorize?state=" + state,
			want:   errRelayNoRedirect,
		},
		"an authorization URL with no state cannot verify a paste": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state,
			auth:   "https://provider.example/authorize?redirect_uri=http%3A%2F%2F" + host + "%2Foauth%2Fcallback",
			want:   errRelayNoState,
		},
		// The advertisement is what gets dialed, so a stored value that is not
		// an unprivileged loopback http address is refused rather than followed.
		"an advertised redirect off-box is refused": {
			pasted: "http://" + host + "/oauth/callback?code=abc&state=" + state,
			auth: "https://provider.example/authorize?" + url.Values{
				"redirect_uri": {"http://169.254.169.254:41234/oauth/callback"},
				"state":        {state},
			}.Encode(),
			want: errRelayNoRedirect,
		},
		"an advertised redirect on a privileged port is refused": {
			pasted: "http://127.0.0.1:22/oauth/callback?code=abc&state=" + state,
			auth: "https://provider.example/authorize?" + url.Values{
				"redirect_uri": {"http://127.0.0.1:22/oauth/callback"},
				"state":        {state},
			}.Encode(),
			want: errRelayBadPort,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			auth := tc.auth
			if auth == "" {
				auth = authURL
			}
			got, err := validateRelayAddress(tc.pasted, auth)
			if err != tc.want {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.want == nil && got == nil {
				t.Fatal("accepted the address but returned no URL to replay")
			}
			if tc.want != nil && got != nil {
				t.Error("refused the address but still returned a URL to replay")
			}
		})
	}
}

// FuzzValidateRelayAddress asserts the ACCEPTANCE INVARIANT rather than merely
// that nothing panics: whatever the paste, an accepted address is one that is
// safe to dial and forward. Every clause is a property the handler relies on
// without re-checking it, so a fuzz input that satisfies the validator and
// violates one of these is a live defect, not a curiosity.
func FuzzValidateRelayAddress(f *testing.F) {
	const authURL = "https://p.example/authorize?" +
		"redirect_uri=http%3A%2F%2F127.0.0.1%3A41234%2Foauth%2Fcallback&state=st-abc"

	f.Add("http://127.0.0.1:41234/oauth/callback?code=abc&state=st-abc", authURL)
	f.Add("http://localhost:41234/oauth/callback?code=abc&state=st-abc", authURL)
	f.Add("http://127.0.0.1:41234/oauth/callback?code=abc&state=wrong", authURL)
	f.Add("http://evil.example:41234/oauth/callback?code=abc&state=st-abc", authURL)
	f.Add("http://127.0.0.1:41234/oauth/callback?code=a\r\nX: 1&state=st-abc", authURL)
	f.Add("http://user:pw@127.0.0.1:41234/x?code=a&state=st-abc", authURL)
	f.Add("://", "")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, pasted, auth string) {
		got, err := validateRelayAddress(pasted, auth)
		if err != nil {
			if got != nil {
				t.Fatalf("refused (%v) but returned a URL to replay: %q", err, got.String())
			}
			return
		}
		if got == nil {
			t.Fatal("accepted but returned no URL to replay")
		}

		// 1. Only ever plain http to a loopback name, on an unprivileged port.
		//    This is what makes the outbound dial reachable-by-design instead of
		//    an arbitrary request the caller composed.
		if !strings.EqualFold(got.Scheme, "http") {
			t.Errorf("accepted scheme %q, want http", got.Scheme)
		}
		if !isLoopbackHost(got.Hostname()) {
			t.Errorf("accepted non-loopback host %q", got.Hostname())
		}
		port, perr := strconv.Atoi(got.Port())
		if perr != nil || port < relayMinPort || port > 65535 {
			t.Errorf("accepted port %q, want %d..65535", got.Port(), relayMinPort)
		}

		// 2. No credentials, no fragment: both would mean the paste is not the
		//    address the browser was actually sent to.
		if got.User != nil {
			t.Error("accepted a URL carrying userinfo")
		}
		if got.Fragment != "" {
			t.Errorf("accepted a URL carrying fragment %q", got.Fragment)
		}

		// 3. Nothing but printable ASCII survives, so the replayed request line
		//    cannot be split or truncated by the paste.
		if !isPrintableASCII(got.String()) {
			t.Errorf("accepted a URL that is not printable ASCII: %q", got.String())
		}

		// 4. There is a code, and every query key is one the callback uses.
		q := got.Query()
		if q.Get("code") == "" {
			t.Error("accepted a URL with no authorization code")
		}
		for k := range q {
			if _, ok := relayQueryKeys[k]; !ok {
				t.Errorf("accepted a URL carrying unexpected query key %q", k)
			}
		}

		// 5. THE DIAL TARGET IS KAS'S, THE QUERY IS THE PASTE'S, and nothing
		//    crosses over. This is the injection binding: if a paste can move
		//    the target by a single byte the route becomes a request generator,
		//    and if the query is rewritten the code stops being the provider's.
		aq, aerr := url.Parse(auth)
		if aerr != nil {
			t.Fatalf("accepted a paste against an unparsable authorization URL %q", auth)
		}
		want, werr := url.Parse(aq.Query().Get("redirect_uri"))
		if werr != nil {
			t.Fatalf("accepted a paste against an unparsable redirect_uri")
		}
		if got.Scheme != want.Scheme || got.Host != want.Host ||
			got.EscapedPath() != want.EscapedPath() {
			t.Errorf("dial target %q is not the advertised callback %q verbatim",
				got.Scheme+"://"+got.Host+got.EscapedPath(),
				want.Scheme+"://"+want.Host+want.EscapedPath())
		}
		if got.User != nil || got.Fragment != "" {
			t.Error("the advertised callback contributed userinfo or a fragment")
		}
		// The paste's query, unaltered. Re-parsed from the input rather than
		// compared against got.Query(), so a rewrite would show up.
		pu, puerr := url.Parse(strings.TrimSpace(pasted))
		if puerr != nil {
			t.Fatalf("accepted a paste that does not parse: %q", pasted)
		}
		if got.RawQuery != pu.RawQuery {
			t.Errorf("query was rewritten: %q, want the pasted %q", got.RawQuery, pu.RawQuery)
		}
		if s := aq.Query().Get("state"); s == "" || s != q.Get("state") {
			t.Errorf("accepted state %q against advertised state %q", q.Get("state"), s)
		}
	})
}

// The relay's client is pinned to the ONE port it was built for. This is the
// invariant that replaced a transport with the port check switched off: a
// standing client could only ever express a range, so nothing stopped a
// validated callback on port A from being replayed to port B. Building per
// attempt makes the allowlist exactly the port about to be dialed, and this
// test is what would fail if that were widened back to a range.
func TestRelayClientForPinsTheOnePort(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	target, err := url.Parse(srv.URL + "/callback?code=abc&state=xyz")
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", srv.URL, err)
	}
	client, err := relayClientFor(target)
	if err != nil {
		t.Fatalf("relayClientFor(%q) error = %v, want a client", target, err)
	}

	// The port it was built for works.
	resp, err := client.Get(target.String())
	if err != nil {
		t.Fatalf("GET the pinned port error = %v, want it allowed", err)
	}
	_ = resp.Body.Close()

	// A DIFFERENT loopback port does not, even though it is loopback and
	// unprivileged. No listener is needed: the refusal precedes the dial.
	port, err := strconv.ParseUint(target.Port(), 10, 16)
	if err != nil {
		t.Fatalf("ParseUint(%q) error = %v", target.Port(), err)
	}
	other := max(uint16(port)+1, relayMinPort)
	//nolint:bodyclose // the request is expected to fail before a response exists
	_, err = client.Get("http://127.0.0.1:" + strconv.Itoa(int(other)) + "/callback")
	if err == nil {
		t.Fatalf("GET port %d with a client pinned to %d succeeded, want it refused", other, port)
	}
	// Assert the REASON, not merely that it failed. Nothing listens on the other
	// port, so a widened allowlist still produces a connection-refused error and
	// an `err != nil` check would pass while the pin was gone.
	var se *ssrf.Error
	if !errors.As(err, &se) || se.Kind != ssrf.KindBadPort {
		t.Errorf("GET port %d error = %v, want an ssrf KindBadPort refusal (the pin, not a dial failure)", other, err)
	}
}

// A port parseLoopbackCallback would have rejected is rejected here too, so the
// two layers cannot disagree about what is dialable.
func TestRelayClientForRefusesBadPorts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"privileged", "http://127.0.0.1:80/callback"},
		{"port zero", "http://127.0.0.1:0/callback"},
		{"no port", "http://127.0.0.1/callback"},
		// NOT in this table: an unparseable port like ":notaport". url.Parse
		// always rejects it, so the case never reached relayClientFor and
		// asserted nothing — it passed with the port validation deleted. The
		// refusal is real but it belongs to url.Parse, and TestValidateRelayAddress
		// is where the parse layer is exercised.
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v; every case in this table must reach relayClientFor", tc.raw, err)
			}
			if _, err := relayClientFor(u); err == nil {
				t.Errorf("relayClientFor(%q) = nil error, want the port refused", tc.raw)
			}
		})
	}
}
