// v3 (KAS) host-mediated client requests.
//
// kiro-cli acp --agent-engine v3 runs with --auth=acp-callback and makes
// server->client requests on the session-creation critical path:
//
//	_kiro/auth/getAccessToken -> {accessToken, expiresAt, profileArn?}
//	_kiro/terminal/shell_type -> {shellType}
//
// Without an answer to getAccessToken KAS runs unauthenticated: sessions
// still open, but every service-backed surface fails. It also sends
// _kiro/openExternalUrl (declared as a client capability) when an MCP
// server needs a browser OAuth page opened. v1/v2 never send any of these.
//
// Tokens come from kiro-cli itself: internal/kiroauth shells the CLI's
// `chat _ get-kas-token` subcommand, the same interface the CLI's own TUI
// host uses. The CLI owns the login store and the refresh chain; vibekit
// performs no token refresh of its own — a second refresher would fork the
// rotating refresh-token chain.

package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"sync/atomic"

	"github.com/cplieger/vibekit/internal/kiroauth"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// v3-only server->client request method names.
const (
	methodKiroGetAccessToken = "_kiro/auth/getAccessToken" //nolint:gosec // G101: ACP method name, not a credential
	methodKiroShellType      = "_kiro/terminal/shell_type"
)

// handleKiroClientRequest answers the v3-only server->client requests.
// Returns true if msg was one of them (so translateACPEvent stops),
// false otherwise. Safe to call for every incoming request regardless of
// engine: v1/v2 never send these methods.
func (in *inbound) handleKiroClientRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) bool {
	switch msg.Method {
	case methodKiroGetAccessToken:
		// kiroauth.Token can block under a mutex while the CLI refreshes
		// SSO-OIDC, so answering synchronously would stall the forward
		// goroutine. Dispatch async under inflight with a FRESH runtime-scoped
		// context — the per-event ctx is cancelled the moment
		// translateACPEvent returns, which would make Bridge.Respond drop the
		// token write.
		in.lifetime.inflight.Go(func() {
			actx, cancel := in.lifetime.derivedContext()
			defer cancel()
			in.respondKiroAccessToken(actx, chatID, msg)
		})
		return true
	case methodKiroShellType:
		in.respondBridge(ctx, chatID, msg, kiroShellTypeResult(), nil)
		return true
	case methodKiroOpenExternalURL:
		in.respondKiroOpenExternalURL(ctx, chatID, msg)
		return true
	default:
		return false
	}
}

// respondKiroOpenExternalURL answers the v3 _kiro/openExternalUrl request
// (A→C, {url}) — KAS sends it, most often for an MCP server's OAuth
// authorization page, only because vibekit declares the capability.
//
// Does NOT auto-open: a browser popup-blocks window.open() not driven by a
// user gesture, and this fires from an SSE event. Instead it acks the
// request (so the agent's OAuth redirect completes) and broadcasts an
// open_external_url event for a clickable affordance. Only http/https URLs
// are accepted; any other scheme is rejected with an RPC error.
func (in *inbound) respondKiroOpenExternalURL(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var p struct {
		URL string `json:"url"`
	}
	if msg.Params != nil {
		_ = json.Unmarshal(msg.Params, &p)
	}
	if !isSafeExternalURL(p.URL) {
		slog.Warn("v3 openExternalUrl: rejecting unsafe scheme", "chat_id", chatID)
		in.respondBridge(ctx, chatID, msg, nil, &vibekit.RPCError{
			Code:    -32602,
			Message: "openExternalUrl: only http/https URLs are allowed",
		})
		return
	}
	// Ack first so the agent's OAuth redirect isn't blocked on the UI.
	in.respondBridge(ctx, chatID, msg, map[string]any{"success": true}, nil)
	in.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventOpenExternalURL, chatID, vibekit.OpenExternalURLPayload{URL: p.URL}))
}

// isSafeExternalURL reports whether u parses and uses the http or https
// scheme. Mirrors the client-side isSafeURL guard (static-src/url-safety.ts)
// so an unexpected scheme (file:, javascript:, custom app schemes) is never
// broadcast to a browser.
func isSafeExternalURL(u string) bool {
	if u == "" {
		return false
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

// authTokenLatch remembers the last outcome of vending a KAS access token,
// so readiness can report a dead sign-in without probing identity — a probe
// would spawn a subprocess and could block under a mutex for as long as
// kiroauth.cliTimeout, which /api/health must not do.
//
// NOT sticky: it clears on the next success, because the failure it serves
// is an expired SSO refresh chain, and the sign-in that fixes it is exactly
// what makes the latch stale.
//
// Holds the outcome, not the reason: kiro-cli's error text already reaches
// the log line and the SSE error frame, and /api/health is unauthenticated,
// so it must not carry text that can name a path on the volume.
type authTokenLatch struct {
	failed atomic.Bool
}

// record folds one vend outcome in. A nil error clears.
func (l *authTokenLatch) record(err error) {
	l.failed.Store(err != nil)
}

// AuthTokenUnavailable reports whether the last attempt to vend a KAS access
// token failed. The readiness handler's sign-in leg (internal/server) reads
// this; it is one atomic load and spawns nothing.
func (in *inbound) AuthTokenUnavailable() bool {
	if in.authLatch == nil {
		return false
	}
	return in.authLatch.failed.Load()
}

// respondKiroAccessToken vends the get-kas-token result. On failure it
// returns a JSON-RPC error so KAS surfaces a clear auth failure rather
// than hanging, and broadcasts the failure so the browser can offer a
// sign-in: KAS's own answer to this error is to run unauthenticated, which
// looks like a session that opens and then fails every turn.
func (in *inbound) respondKiroAccessToken(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	result, err := in.kiroAccessTokenResult(ctx)
	if err != nil {
		slog.Error("v3 auth: token unavailable", "chat_id", chatID, "error", err)
		in.respondBridge(ctx, chatID, msg, nil, err)
		// The message is kiro-cli's own text, not a rendering of it: it names
		// which leg of the login chain is dead (expired refresh token, no
		// profile, a missing binary), and no wording invented here could be more
		// specific. Broadcast AFTER the RPC error so the agent's request is never
		// waiting on a client fan-out.
		in.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code:    vibekit.ErrCodeAuthTokenUnavailable,
			Message: err.Error(),
		}))
		return
	}
	in.respondBridge(ctx, chatID, msg, result, nil)
}

// kiroAccessTokenResult vends the result for the v3 _kiro/auth/getAccessToken
// host request by asking kiro-cli for its current token (see
// kiroauth.CLISource). Shared by the chat-bridge responder and the utility
// bridge (answerHostRequest) so the wire keys live in exactly one place — which
// is also why the latch is written HERE rather than in the responder above:
// both callers vend through this function, so both outcomes reach readiness.
func (in *inbound) kiroAccessTokenResult(ctx context.Context) (map[string]any, error) {
	result, err := in.vendKiroAccessToken(ctx)
	if in.authLatch != nil {
		in.authLatch.record(err)
	}
	return result, err
}

func (in *inbound) vendKiroAccessToken(ctx context.Context) (map[string]any, error) {
	if in.kiroToken == nil {
		return nil, kiroauth.ErrNoSource
	}
	return in.kiroToken.Token(ctx)
}

// kiroShellTypeResult is the answer to the v3 _kiro/terminal/shell_type host
// request. vibekit's container shell is bash; KAS ignores extra keys.
func kiroShellTypeResult() map[string]any {
	return map[string]any{"shellType": "bash"}
}
