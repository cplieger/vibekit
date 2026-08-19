package hub

// The MCP OAuth loopback relay.
//
// THE DEAD END IT FIXES. KAS owns the whole MCP OAuth flow — discovery, DCR,
// PKCE, token exchange, refresh — and it binds its OWN redirect listener on
// `http://localhost:<ephemeral>/oauth/callback` inside the container. vibekit's
// browser is somewhere else: a phone, a laptop, anything reaching the container
// over the network. So the provider's 302 sends that browser to ITS OWN
// localhost, nothing is listening, and the flow dies on a connection-refused
// page with no recovery path — clicking "Finish sign-in" again just repeats it.
// For a container reached from another machine that is the NORMAL case, not an
// edge one.
//
// THE SHAPE. The user copies the address bar off the dead page and pastes it
// here; vibekit validates it and replays the GET to the loopback listener from
// INSIDE the container, where `127.0.0.1` means what KAS meant by it. KAS's own
// listener then completes the exchange with the redirect_uri it originally sent
// (which still matches, because nothing was rewritten), stores the tokens
// through `_kiro/secret/*`, and connects. One manual step, provider-agnostic,
// and not one line of KAS's flow is reimplemented.
//
// Adopted from KiroCrew's `POST /api/mcp/oauth/relay` (see #kiro-crew-research),
// including its central rule: request data must never choose a remote host.
//
// TWO SHAPES DELIBERATELY NOT BUILT.
//
//   - Rewriting `redirect_uri` to vibekit's own origin so the callback arrives
//     with no paste at all. It breaks at the token endpoint: KAS would still
//     send its own loopback redirect_uri there, and RFC 6749 §4.1.3 requires
//     that value to match the one used at authorize. Only viable if KAS can be
//     told a public redirect base, which nothing in this tree shows it can.
//   - Terminating the exchange in vibekit. That is `kiro-cli owns what kiro-cli
//     owns` inverted, and internal/secretstore's blobs are opaque by decision.
//
// THE TRUST MODEL, stated because this endpoint takes an authorization code on
// an HTTP surface that carries no auth of its own. The pasted address is
// UNTRUSTED input; the stored authorization URL is the trust anchor, because
// KAS wrote it and vibekit only ever kept it verbatim. Everything the relay
// dials or forwards is checked against that URL rather than taken on the
// request's word: the dial target comes from the URL's `redirect_uri`, and the
// `state` must match the URL's `state`. Without that binding this route would be
// an authorization-code injection lever into KAS's token exchange, which is
// worse than the dead end it fixes.

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
	"github.com/cplieger/vibekit/internal/httpwire"
)

const (
	// relayURLCap bounds the pasted address. An authorization code is a few
	// hundred bytes at the outside; 4 KiB leaves room for a long code plus
	// `state`, `scope` and `iss` without accepting a payload.
	relayURLCap = 4096

	// relayMinPort refuses a privileged port. KAS binds an EPHEMERAL port for
	// its redirect listener, so nothing legitimate lands below 1024, and the
	// floor keeps a pasted address from aiming the dial at a low-numbered
	// service that happens to share the loopback interface.
	relayMinPort = 1024

	// relayDialTimeout and relayTotalTimeout bound the replay. The listener is
	// in this container and either answers immediately or is gone, so both are
	// short: a hung dial must not hold the request open.
	relayDialTimeout  = 3 * time.Second
	relayTotalTimeout = 8 * time.Second

	// relayBodyCap bounds the response we read and discard. KAS answers with a
	// small "you can close this window" page; only the status line is used.
	relayBodyCap = 64 << 10
)

// relayQueryKeys is the allowlist of query parameters forwarded to the loopback
// listener. `code` and `state` are the flow; `scope` is RFC 6749 §4.1.2, `iss`
// is RFC 9207 issuer identification, `session_state` is OIDC session
// management. Anything else is refused rather than forwarded, because this
// request is replayed verbatim into KAS's own handler and an unrecognised
// parameter is a parameter neither side vouched for.
//
// An error redirect (`error`, `error_description`) is deliberately absent: it
// carries no code, so there is nothing to relay, and the refusal below names
// the missing code rather than pretending the relay could help.
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

// relayClientFor returns the client for ONE validated callback target, pinned
// to that target's port. Built per attempt rather than once at init: the
// destination is not known until a callback is pasted and checked, and an
// allowlist of exactly the port about to be dialed is a stronger check than any
// standing policy a package-level client could carry. ssrf offers no way to
// lift its port restriction, which is the library being right — the earlier
// shape here switched the port check off entirely and leaned on
// parseLoopbackCallback as the only guard.
//
// The cost this pays is a fresh dialer per paste, which is the right trade: a
// paste is a rare human action, and keep-alives are disabled because this is
// one GET to a listener that stops as soon as it answers, so there is no pooled
// connection worth preserving between attempts.
//
// READ THE OPTIONS BEFORE ASSUMING THE DEFAULTS. `ssrf.SafeTransport`'s default
// posture is the opposite of what this needs — it allows only PUBLIC addresses
// and only port 443 — and both are replaced here. It is used anyway, rather
// than hand-rolling a loopback check, for the one guarantee a hand-roll gets
// wrong: the library re-validates the ACTUALLY-CONNECTED address in a dialer
// Control hook, which it owns and a caller cannot supply. That closes DNS
// rebinding, and rebinding is live here because `localhost` is an accepted host
// and is a DNS name — a resolver answering `localhost` with a routable address
// would otherwise turn this route into an outbound request generator.
func relayClientFor(target *url.URL) (*http.Client, error) {
	// parseLoopbackCallback already accepted this port, so a failure here means
	// the two disagree; refuse rather than guess.
	port, err := strconv.ParseUint(target.Port(), 10, 16)
	if err != nil || port < relayMinPort {
		return nil, errRelayBadPort
	}
	tr := ssrf.SafeTransport(
		ssrf.WithAddressPolicy(func(a netip.Addr) bool { return a.IsLoopback() }),
		ssrf.WithAllowedPorts(uint16(port)),
		ssrf.WithDialer(&net.Dialer{Timeout: relayDialTimeout}),
	)
	// One request, one connection, no pool: the listener is about to exit.
	tr.DisableKeepAlives = true
	return &http.Client{
		Timeout:   relayTotalTimeout,
		Transport: tr,
		// Do not follow a redirect: return it as the response instead. Following
		// would let KAS's handler steer the relay onward, and the status is all
		// this needs. ErrUseLastResponse rather than an error so a 3xx stays a
		// delivered callback rather than becoming a transport failure.
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

// handleOAuthRelay: POST /api/mcp/oauth-relay {server, redirect_url} → replay a
// stranded loopback callback into KAS's waiting listener.
//
// Registered by RegisterRoutes. It is NOT wrapped in webhttp.LoopbackOnly and
// must not be: the whole point is that a REMOTE browser reaches it, and that
// helper's provenance check rejects a browser outright. The loopback constraint
// belongs on the outbound half instead, which is relayClientFor's address policy.
func (r *mcpRegistry) handleOAuthRelay(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		httpwire.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpOAuthRelayReq
	if !httpwire.DecodeJSON(w, req, &body) {
		return
	}

	// RESERVE FIRST, and derive everything downstream from the reservation. The
	// authorization URL travels on the attempt rather than being re-read later,
	// because a second read could belong to a different attempt: recordOAuth can
	// replace the record at any point while this request is out, and a code
	// validated against attempt A must never be replayed under attempt B's
	// reservation.
	//
	// A refusal here is also the answer for an unknown server name: a server with
	// no authorization in flight has nothing to relay either way, so the two cases
	// need no separate reply and this one leaks no inventory.
	attempt, err := r.beginOAuthRelay(body.Server)
	if err != nil {
		httpwire.Conflict(w, err.Error())
		return
	}

	target, err := validateRelayAddress(body.RedirectURL, attempt.authURL)
	if err != nil {
		// The reason is safe to return: every one of them describes the shape of
		// the pasted address or its disagreement with the stored URL, and none
		// quotes either. The user is the one who pasted it and cannot fix it
		// without being told which part was wrong.
		r.releaseOAuthRelay(attempt)
		httpwire.BadRequest(w, err.Error())
		return
	}

	status, err := r.replayCallback(req.Context(), target)
	if err != nil {
		r.releaseOAuthRelay(attempt)
		slog.Warn("mcp oauth relay: could not reach the loopback callback listener",
			"server", body.Server, "port", target.Port(), "error", dialErrWithoutURL(err))
		httpwire.WriteJSONStatus(w, http.StatusBadGateway,
			httpwire.ErrorJSON("the local sign-in listener did not answer, so the code was not delivered; the sign-in may have timed out"))
		return
	}
	if status >= http.StatusBadRequest {
		// Delivered, and refused. The reservation goes back, so a corrected paste
		// can still be tried: KAS runs its own state check and its own single-use
		// rule, and a 4xx here is most often that check rejecting this code.
		r.releaseOAuthRelay(attempt)
		slog.Warn("mcp oauth relay: the loopback listener refused the callback",
			"server", body.Server, "port", target.Port(), "status", status)
		httpwire.WriteJSONStatus(w, http.StatusBadGateway,
			httpwire.ErrorJSON("the local sign-in listener rejected that callback (HTTP "+strconv.Itoa(status)+"); start the sign-in again"))
		return
	}

	// Delivered. The reservation taken at the top IS the single-use latch, so
	// success needs no second write and cannot land on another attempt's record.
	slog.Info("mcp oauth relay: delivered a stranded callback to the loopback listener",
		"server", body.Server, "port", target.Port(), "status", status)
	// The state transition is NOT invented here — connected is KAS's to report
	// over `_kiro/mcp/status`, and the token exchange this just unblocked is
	// still in flight. The client refetches status and waits for that frame.
	httpwire.WriteJSON(w, mcpOAuthRelayResp{Status: status})
}

// replayCallback performs the one GET and returns the listener's status.
func (r *mcpRegistry) replayCallback(ctx context.Context, target *url.URL) (int, error) {
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

// dialErrWithoutURL strips the request URL out of an http.Client error before
// it is logged. REQUIRED, not tidiness: net/http wraps every transport failure
// in a *url.Error whose Error() begins `Get "<the full URL>"`, and that URL's
// query is the authorization code. The wrapped cause carries the diagnosis
// (refused, timeout, policy denial) without the target, which is why unwrapping
// one layer is enough and the port is logged as its own field.
func dialErrWithoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// validateRelayAddress checks a pasted callback address against the
// authorization URL KAS advertised, and returns the URL to replay.
//
// Pure, so it is unit-testable and fuzzable without a listener. Every check is
// a refusal rather than a repair: this function never rewrites the pasted
// address, because a "corrected" callback is one neither the provider nor KAS
// agreed to.
//
// WHAT IT RETURNS IS NOT WHAT WAS PASTED. The replay URL is assembled from the
// ADVERTISED callback (KAS's own scheme, host and path) carrying the PASTED
// query, so the only thing the request can influence is the parameters that
// have to be relayed. That is Crew's "request data can never choose a remote
// host" taken to its conclusion: the host is not merely checked against a set of
// literals, it is never read off the paste at all. It also fixes a real
// spelling problem — `localhost` and `127.0.0.1` are both loopback but are
// different dial targets, and KAS's listener is bound to whichever it named.
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
	// The advertised callback, carrying the pasted query. Nothing else of the
	// paste survives into the request.
	advertised.RawQuery = u.RawQuery
	return advertised, nil
}

// parseLoopbackCallback checks a raw address is a plain-http, credential-free,
// fragment-free loopback URL on an unprivileged port. Shared by the paste and by
// the advertised redirect_uri, which is the point: both end up deciding where a
// request is sent, so both are held to the same shape rather than to two
// hand-written near-copies that can drift apart.
func parseLoopbackCallback(raw string) (*url.URL, error) {
	if len(raw) > relayURLCap {
		return nil, errRelayTooLong
	}
	// Before parsing, not after: url.Parse accepts control bytes and percent-
	// decoding can reintroduce them, and this address is replayed into another
	// program's HTTP handler. Printable ASCII only, so no CR/LF can split the
	// request line and no raw space can truncate it.
	if !isPrintableASCII(raw) {
		return nil, errRelayBadBytes
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errRelayUnparsable
	}
	if !strings.EqualFold(u.Scheme, "http") {
		// http, not https: KAS's loopback listener is plain. An https paste is a
		// different address, not this one with a nicer scheme.
		return nil, errRelayNotHTTP
	}
	if u.User != nil {
		return nil, errRelayHasCreds
	}
	// A fragment never reaches a server, so its presence means the address is
	// not the one the browser was sent to.
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

// validateCallbackQuery checks the query is a callback's: every key allowlisted,
// and a non-empty code present. Returns the parsed values so the caller does not
// re-parse and risk disagreeing with what was checked.
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

// matchAdvertisedCallback binds a pasted callback to the authorization URL KAS
// advertised and returns the advertised callback to dial. This is the whole
// security argument for the route, so every half is required rather than
// best-effort.
//
// The advertised `redirect_uri` is itself validated as an unprivileged loopback
// http address. It is KAS's own value, so it should already be one; checking it
// means a corrupt or hostile stored URL cannot aim the dial at something else,
// which matters because this value is what the request is ultimately sent to.
//
// The PORT and PATH must agree with the paste — the port is which listener, the
// path is which handler on it — so a paste cannot walk the loopback interface
// looking for something that answers. The HOST is not compared, because it is
// taken from the advertisement rather than the paste; `localhost` and
// `127.0.0.1` are both loopback and both legitimate spellings for a user to
// have in their address bar.
//
// The `state` match is the CSRF/injection binding. A missing state in the
// stored URL is a REFUSAL, not a waiver: relaying without it would accept any
// code anyone posted, and vibekit's HTTP surface carries no auth of its own.
// Failing closed here means the feature declines rather than becoming a lever,
// and the message says which value is absent so the condition is diagnosable
// instead of mysterious.
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
	// KAS's own value, held to the SAME shape as the paste, because the dial goes
	// here. Any refusal collapses to errRelayNoRedirect rather than surfacing the
	// specific one: the user did not write this value and cannot fix it, so the
	// actionable statement is that the stored sign-in has no usable callback.
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
	// Constant-time: the comparison is against a secret-equivalent value, and a
	// byte-at-a-time early exit would leak it a byte at a time to a caller that
	// can retry. Length is compared first because ConstantTimeCompare returns 0
	// on a length mismatch without comparing, which is not a leak (the length is
	// not the secret) but does mean the guard has to be explicit.
	if len(wantState) != len(pastedState) ||
		subtle.ConstantTimeCompare([]byte(wantState), []byte(pastedState)) != 1 {
		return nil, errRelayStateDrift
	}
	// A copy: the caller overwrites RawQuery, and the parsed advertisement must
	// not become a shared mutable value.
	dial := *want
	return &dial, nil
}

// isLoopbackHost reports whether host is one of the three spellings a loopback
// redirect uses. A fixed set rather than a resolve-and-check: the resolved
// address is re-validated at socket time by relayClientFor's policy, and this gate
// exists to reject the whole class of remote hosts before any lookup happens.
//
// RFC 8252 §7.3 prefers the literals over `localhost` precisely because the name
// depends on a resolver, but KAS advertises whichever it advertises, so all
// three are accepted here and the dial-time policy is what makes the name safe.
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
