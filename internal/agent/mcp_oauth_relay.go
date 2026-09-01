package agent

// The MCP OAuth loopback relay.
//
// KAS owns the whole MCP OAuth flow (discovery, DCR, PKCE, token exchange,
// refresh) and binds its own redirect listener on
// `http://localhost:<ephemeral>/oauth/callback` inside the container.
// vibekit's browser is elsewhere — a phone, a laptop, anything reaching the
// container over the network — so the provider's 302 sends that browser to
// its OWN localhost, nothing answers, and the flow dies with no recovery
// path. For a remotely-reached container that is the normal case, not an
// edge one.
//
// The user copies the address bar off the dead page and pastes it here;
// vibekit validates it and replays the GET to the loopback listener from
// INSIDE the container, where `127.0.0.1` means what KAS meant by it. KAS's
// own listener then completes the exchange with the redirect_uri it
// originally sent, stores the tokens through `_kiro/secret/*`, and
// connects. Adopted from KiroCrew's `POST /api/mcp/oauth/relay` (see
// #kiro-crew-research), including its central rule: request data must
// never choose a remote host.
//
// Two shapes deliberately not built: rewriting `redirect_uri` to vibekit's
// own origin (breaks at the token endpoint — RFC 6749 §4.1.3 requires the
// value used at authorize and at token exchange to match, and KAS still
// sends its own loopback value there); and terminating the exchange in
// vibekit (inverts "kiro-cli owns what kiro-cli owns", and secretstore's
// blobs are opaque by decision).
//
// THE TRUST MODEL. This endpoint takes an authorization code on an HTTP
// surface with no auth of its own. The pasted address is UNTRUSTED input;
// the stored authorization URL is the trust anchor, because KAS wrote it
// and vibekit only ever kept it verbatim. Everything the relay dials or
// forwards is checked against that URL rather than taken on the request's
// word: the dial target comes from the URL's `redirect_uri`, and `state`
// must match the URL's `state`. Without that binding this route would be
// an authorization-code injection lever into KAS's token exchange.

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/ssrf/v4"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp/v2"
)

const (
	// relayURLCap bounds the pasted address; an authorization code is a
	// few hundred bytes at the outside.
	relayURLCap = 4096

	// relayMinPort refuses a privileged port. KAS binds an EPHEMERAL port
	// for its redirect listener, so nothing legitimate lands below 1024.
	relayMinPort = 1024

	// relayDialTimeout and relayTotalTimeout bound the replay: the
	// listener is in this container and either answers immediately or is
	// gone.
	relayDialTimeout  = 3 * time.Second
	relayTotalTimeout = 8 * time.Second

	// relayBodyCap bounds the response we read and discard.
	relayBodyCap = 64 << 10
)

// relayQueryKeys is the allowlist of query parameters forwarded to the
// loopback listener: `code`/`state` are the flow, `scope` is RFC 6749
// §4.1.2, `iss` is RFC 9207 issuer identification, `session_state` is OIDC
// session management. Anything else is refused, since this request is
// replayed verbatim into KAS's own handler and an unrecognised parameter is
// one neither side vouched for.
//
// An error redirect (`error`, `error_description`) is deliberately absent:
// it carries no code, so there is nothing to relay.
var relayQueryKeys = map[string]struct{}{
	"code":          {},
	"state":         {},
	"scope":         {},
	"iss":           {},
	"session_state": {},
}

// Refusal reasons. Each is its own value so the handler can map it to a status
// and the tests can assert the specific rejection rather than "an error".
var (
	errRelayNoFlow      = errors.New("no sign-in is waiting for this server")
	errRelayAlreadyDone = errors.New("this sign-in's callback was already delivered")
	errRelayTooLong     = errors.New("that address is too long to be a sign-in callback")
	errRelayBadBytes    = errors.New("that address contains characters a URL cannot carry")
	errRelayUnparsable  = errors.New("that does not look like a URL")
	errRelayNotHTTP     = errors.New("a sign-in callback is an http address")
	errRelayHasCreds    = errors.New("that address carries a username or password")
	errRelayHasFragment = errors.New("that address carries a #fragment")
	errRelayNotLoopback = errors.New("that address is not a local callback address")
	errRelayBadPort     = errors.New("that address has no usable port number")
	errRelayNoCode      = errors.New("that address carries no authorization code, so the sign-in did not complete")
	errRelayBadQuery    = errors.New("that address carries a query parameter this callback does not use")
	errRelayNoRedirect  = errors.New("this sign-in did not advertise a callback address, so there is nothing to relay to")
	errRelayNoState     = errors.New("this sign-in carries no state value, so a pasted callback cannot be verified against it")
	errRelayTargetDrift = errors.New("that address is not the callback address this sign-in asked for")
	errRelayStateDrift  = errors.New("that address belongs to a different sign-in")
)

// relayClientFor returns the client for ONE validated callback target,
// pinned to that target's port. Built per attempt rather than once at
// init: the destination is not known until a callback is pasted and
// checked.
//
// ssrf.SafeTransport re-validates the ACTUALLY-CONNECTED address in a
// dialer Control hook, which a hand-rolled loopback check cannot do —
// closing DNS rebinding, live here because `localhost` is an accepted host
// and is a DNS name.
func relayClientFor(target *url.URL) (*http.Client, error) {
	// parseLoopbackCallback already accepted this port; a mismatch here
	// means the two disagree, so refuse rather than guess.
	port, err := strconv.ParseUint(target.Port(), 10, 16)
	if err != nil || port < relayMinPort {
		return nil, errRelayBadPort
	}
	tr := ssrf.SafeTransport(
		ssrf.WithAddressPolicy(func(a netip.Addr) bool { return a.IsLoopback() }),
		ssrf.WithAllowedPorts(uint16(port)),
		ssrf.WithDialer(&net.Dialer{Timeout: relayDialTimeout}),
	)
	tr.DisableKeepAlives = true
	return &http.Client{
		Timeout:   relayTotalTimeout,
		Transport: tr,
		// Do not follow a redirect: return it as the response instead, so
		// KAS's handler cannot steer the relay onward.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

type mcpOAuthRelayReq struct {
	Server      string `json:"server"`
	RedirectURL string `json:"redirect_url"`
}

type mcpOAuthRelayResp struct {
	// Status is the loopback listener's HTTP status. Surfaced so the UI can say
	// what answered rather than only that something did.
	Status int `json:"status"`
}

// handleOAuthRelay: POST /api/mcp/oauth-relay {server, redirect_url} →
// replay a stranded loopback callback into KAS's waiting listener.
//
// Registered by RegisterRoutes. NOT wrapped in webhttp.LoopbackOnly: the
// whole point is that a REMOTE browser reaches it. The loopback constraint
// belongs on the outbound half instead (relayClientFor's address policy).
func (reg *mcpRegistry) handleOAuthRelay(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpOAuthRelayReq
	if !httpreply.DecodeJSON(w, req, &body) {
		return
	}

	// RESERVE FIRST, and derive everything downstream from the reservation:
	// the authorization URL travels on the attempt rather than being
	// re-read later, since recordOAuth can replace the record at any point
	// while this request is out, and a code validated against attempt A
	// must never be replayed under attempt B's reservation.
	//
	// A refusal here also covers an unknown server name, since a server
	// with no authorization in flight has nothing to relay either way.
	attempt, err := reg.beginOAuthRelay(body.Server)
	if err != nil {
		httpreply.Conflict(w, err.Error())
		return
	}

	target, err := validateRelayAddress(body.RedirectURL, attempt.authURL)
	if err != nil {
		reg.releaseOAuthRelay(attempt)
		httpreply.BadRequest(w, err.Error())
		return
	}

	status, err := replayCallback(req.Context(), target)
	if err != nil {
		reg.releaseOAuthRelay(attempt)
		slog.Warn("mcp oauth relay: could not reach the loopback callback listener",
			"server", body.Server, "port", target.Port(), "error", dialErrWithoutURL(err))
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON("the local sign-in listener did not answer, so the code was not delivered; the sign-in may have timed out"))
		return
	}
	if status >= http.StatusBadRequest {
		// Delivered, and refused. The reservation goes back so a
		// corrected paste can still be tried.
		reg.releaseOAuthRelay(attempt)
		slog.Warn("mcp oauth relay: the loopback listener refused the callback",
			"server", body.Server, "port", target.Port(), "status", status)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON("the local sign-in listener rejected that callback (HTTP "+strconv.Itoa(status)+"); start the sign-in again"))
		return
	}

	slog.Info("mcp oauth relay: delivered a stranded callback to the loopback listener",
		"server", body.Server, "port", target.Port(), "status", status)
	// KAS still owns reporting the connected state over _kiro/mcp/status;
	// the client refetches status and waits for that frame.
	webhttp.WriteJSON(w, mcpOAuthRelayResp{Status: status})
}

// replayCallback performs the one GET and returns the listener's status.
func replayCallback(ctx context.Context, target *url.URL) (int, error) {
	client, err := relayClientFor(target)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), http.NoBody)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain a bounded prefix so a hostile listener cannot stream forever; the
	// body itself is not read for meaning. Connection reuse is NOT a reason
	// here — keep-alives are off — so the cap is the whole point.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, relayBodyCap))
	return resp.StatusCode, nil
}

// dialErrWithoutURL strips the request URL out of an http.Client error
// before it is logged. REQUIRED: net/http wraps a transport failure in a
// *url.Error whose Error() begins `Get "<the full URL>"`, and that URL's
// query is the authorization code.
func dialErrWithoutURL(err error) error {
	// errors.AsType (Go 1.26); `go fix -errorsastype` and gocritic's
	// modernize both skip this site because it is a clause of a boolean
	// expression, one of their documented blind spots.
	if ue, ok := errors.AsType[*url.Error](err); ok && ue.Err != nil {
		return ue.Err
	}
	return err
}

// validateRelayAddress checks a pasted callback address against the
// authorization URL KAS advertised, and returns the URL to replay.
//
// Pure, so it is unit-testable and fuzzable without a listener. Every
// check is a refusal rather than a repair — this function never rewrites
// the pasted address.
//
// The replay URL is assembled from the ADVERTISED callback (KAS's own
// scheme, host, path) carrying the PASTED query, so the host is never read
// off the paste at all. It also fixes a spelling problem: `localhost` and
// `127.0.0.1` are both loopback but different dial targets, and KAS's
// listener is bound to whichever it named.
func validateRelayAddress(pasted, authURL string) (*url.URL, error) {
	u, err := parseLoopbackCallback(strings.TrimSpace(pasted))
	if err != nil {
		return nil, err
	}
	q, err := validateCallbackQuery(u.RawQuery)
	if err != nil {
		return nil, err
	}

	advertised, err := matchAdvertisedCallback(u, q.Get("state"), authURL)
	if err != nil {
		return nil, err
	}
	advertised.RawQuery = u.RawQuery
	return advertised, nil
}

// parseLoopbackCallback checks a raw address is a plain-http,
// credential-free, fragment-free loopback URL on an unprivileged port.
// Shared by the paste and by the advertised redirect_uri, so both are held
// to the same shape.
func parseLoopbackCallback(raw string) (*url.URL, error) {
	if len(raw) > relayURLCap {
		return nil, errRelayTooLong
	}
	// Checked before parsing, not after: url.Parse accepts control bytes
	// and percent-decoding can reintroduce them, and this address is
	// replayed into another program's HTTP handler.
	if !isPrintableASCII(raw) {
		return nil, errRelayBadBytes
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errRelayUnparsable
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return nil, errRelayNotHTTP
	}
	if u.User != nil {
		return nil, errRelayHasCreds
	}
	if u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#") {
		return nil, errRelayHasFragment
	}
	if !isLoopbackHost(u.Hostname()) {
		return nil, errRelayNotLoopback
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < relayMinPort || port > 65535 {
		return nil, errRelayBadPort
	}
	return u, nil
}

// validateCallbackQuery checks the query is a callback's: every key
// allowlisted, and a non-empty code present.
func validateCallbackQuery(rawQuery string) (url.Values, error) {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, errRelayUnparsable
	}
	for k := range q {
		if _, allowed := relayQueryKeys[k]; !allowed {
			return nil, errRelayBadQuery
		}
	}
	if q.Get("code") == "" {
		return nil, errRelayNoCode
	}
	return q, nil
}

// matchAdvertisedCallback binds a pasted callback to the authorization URL
// KAS advertised and returns the advertised callback to dial. This is the
// whole security argument for the route.
//
// The advertised `redirect_uri` is itself validated as an unprivileged
// loopback http address, so a corrupt or hostile stored URL cannot aim the
// dial elsewhere.
//
// PORT and PATH must agree with the paste (which listener, which handler);
// HOST is not compared, since it is taken from the advertisement rather
// than the paste — `localhost` and `127.0.0.1` are both legitimate
// spellings.
//
// The `state` match is the CSRF/injection binding. A missing state in the
// stored URL is a REFUSAL, not a waiver: relaying without it would accept
// any code anyone posted.
func matchAdvertisedCallback(pasted *url.URL, pastedState, authURL string) (*url.URL, error) {
	auth, err := url.Parse(authURL)
	if err != nil {
		return nil, errRelayNoRedirect
	}
	aq := auth.Query()

	redirect := aq.Get("redirect_uri")
	if redirect == "" {
		return nil, errRelayNoRedirect
	}
	// Held to the SAME shape as the paste, because the dial goes here. Any
	// refusal collapses to errRelayNoRedirect rather than the specific
	// one: the user did not write this value and cannot fix it.
	want, err := parseLoopbackCallback(redirect)
	if err != nil {
		return nil, errRelayNoRedirect
	}
	if want.Port() != pasted.Port() || want.EscapedPath() != pasted.EscapedPath() {
		return nil, errRelayTargetDrift
	}

	wantState := aq.Get("state")
	if wantState == "" {
		return nil, errRelayNoState
	}
	// Constant-time: the comparison is against a secret-equivalent value,
	// and a byte-at-a-time early exit would leak it a byte at a time to a
	// caller that can retry.
	if len(wantState) != len(pastedState) ||
		subtle.ConstantTimeCompare([]byte(wantState), []byte(pastedState)) != 1 {
		return nil, errRelayStateDrift
	}
	// A deep copy: the caller overwrites RawQuery, and the parsed
	// advertisement must not become a shared mutable value. url.Clone
	// (Go 1.27) rather than `dial := *want`, which shares the User
	// pointer — safe here only because parseLoopbackCallback refuses
	// userinfo, but Clone makes the claim true by construction.
	return want.Clone(), nil
}

// isLoopbackHost reports whether host is one of the three spellings a
// loopback redirect uses. A fixed set rather than resolve-and-check: the
// resolved address is re-validated at socket time by relayClientFor's
// policy, so this gate exists to reject the whole class of remote hosts
// before any lookup happens.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// isPrintableASCII reports whether s is entirely printable ASCII with no space.
// Excludes C0 controls, DEL, the high bit, and 0x20 — every byte that could
// split or truncate the request line this address becomes.
func isPrintableASCII(s string) bool {
	for i := range len(s) {
		if s[i] <= 0x20 || s[i] >= 0x7f {
			return false
		}
	}
	return true
}
