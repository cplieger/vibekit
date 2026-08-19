// v3 (KAS) host-mediated client requests.
//
// kiro-cli acp --agent-engine v3 runs with --auth=acp-callback and makes
// server->client requests on the session-creation critical path:
//
//	_kiro/auth/getAccessToken -> {accessToken, expiresAt, profileArn?}
//	_kiro/terminal/shell_type -> {shellType}
//
// Without an answer to getAccessToken KAS runs unauthenticated: sessions
// still open, but every service-backed surface (the model registry, turns)
// fails. It also sends _kiro/openExternalUrl (only because we declare that
// client capability; see internal/bridge/bridge.go initialize) when an MCP
// server needs a browser OAuth page opened — answered here too. v1/v2
// never send any of these, so handleKiroClientRequest is a safe no-op
// there.
//
// Tokens come from kiro-cli itself: internal/kiroauth shells the CLI's
// internal `chat _ get-kas-token` subcommand — the same interface the
// CLI's own TUI host uses for this exact callback. The CLI owns the login
// store and the refresh chain; the reply carries accessToken, expiresAt
// and profileArn (plus authMethod/provider when present), so KAS routes to
// the profile's region and _kiro/account/getUsage can identify the
// account. vibekit performs no token refresh of its own — a second
// refresher would fork the rotating refresh-token chain.

package hub

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
func (h *Hub) handleKiroClientRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) bool {
	switch msg.Method {
	case methodKiroGetAccessToken:
		// kiroauth.Token can perform a blocking (<=15s) SSO-OIDC refresh
		// under a mutex, so answering synchronously here stalls the forward
		// goroutine (translateACPEvent) and backs up this chat's ACP event
		// processing. Dispatch async under inflight (so Shutdown drains it)
		// with a FRESH hub-scoped context — the per-event ctx is cancelled
		// by translateACPEvent's defer the moment it returns, which would
		// make Bridge.Respond drop the token write (C1). getAccessToken is
		// answered on the forward goroutine, after Start() returns, so the
		// bridge is already registered in the manager; respondBridge (used
		// by respondKiroAccessToken) resolves it the same as the sync path.
		h.lifecycle.inflight.Go(func() {
			actx, cancel := h.hubContext()
			defer cancel()
			h.respondKiroAccessToken(actx, chatID, msg)
		})
		return true
	case methodKiroShellType:
		h.respondBridge(ctx, chatID, msg, kiroShellTypeResult(), nil)
		return true
	case methodKiroOpenExternalURL:
		h.respondKiroOpenExternalURL(ctx, chatID, msg)
		return true
	default:
		return false
	}
}

// respondKiroOpenExternalURL answers the v3 _kiro/openExternalUrl request
// (A→C, {url}). KAS sends it — most often for an MCP server's OAuth
// authorization page — only because vibekit declares the openExternalUrl
// client capability in the initialize handshake; the same URL is also
// surfaced via _kiro/mcp/status, so this is an additive proactive channel.
//
// We do NOT auto-open: a browser popup-blocks window.open() that isn't
// driven by a user gesture, and this fires from an SSE event. Instead we
// ack the request (so the agent's OAuth redirect completes) and broadcast
// an open_external_url event; the client renders a clickable affordance
// the user activates. Only http/https URLs are accepted — any other
// scheme is rejected with an RPC error and not broadcast.
func (h *Hub) respondKiroOpenExternalURL(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	var p struct {
		URL string `json:"url"`
	}
	if msg.Params != nil {
		_ = json.Unmarshal(msg.Params, &p)
	}
	if !isSafeExternalURL(p.URL) {
		slog.Warn("v3 openExternalUrl: rejecting unsafe scheme", "chat_id", chatID)
		h.respondBridge(ctx, chatID, msg, nil, &vibekit.RPCError{
			Code:    -32602,
			Message: "openExternalUrl: only http/https URLs are allowed",
		})
		return
	}
	// Ack first so the agent's OAuth redirect isn't blocked on the UI.
	h.respondBridge(ctx, chatID, msg, map[string]any{"success": true}, nil)
	h.Broadcast(ctx, vibekit.NewEvent(vibekit.EventOpenExternalURL, chatID, vibekit.OpenExternalURLPayload{URL: p.URL}))
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

// authTokenLatch remembers the last outcome of vending a KAS access token.
//
// It exists so readiness can report a dead sign-in without probing identity.
// /api/health is a lock and two field reads per request and must stay that way —
// asking kiro-cli whether it still holds a token is a subprocess spawn, and
// kiroauth.Token can block up to 15s on an SSO-OIDC refresh under a mutex, so a
// probe on the health path would turn a monitor's poll into a process launch.
// The vend already happens on the session-creation critical path, so the fact is
// free to observe there; nothing else has to go looking for it.
//
// NOT sticky. It clears on the next success, because the failure this serves is
// an expired SSO refresh chain and the sign-in that fixes it is exactly what
// makes the latch stale. A latch that only ever set would keep reporting a
// signed-out runtime after the user signed back in.
//
// It holds the outcome and not the reason: kiro-cli's own error text goes to the
// log line and to the SSE error frame at the vend site, both of which already
// have it, and the readiness envelope must not carry it — /api/health is
// unauthenticated and that text can name a path on the volume, which is the same
// reason kiroReasonText serves fixed literals.
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
func (h *Hub) AuthTokenUnavailable() bool {
	if h.authLatch == nil {
		return false
	}
	return h.authLatch.failed.Load()
}

// respondKiroAccessToken vends the get-kas-token result. On failure it
// returns a JSON-RPC error so KAS surfaces a clear auth failure rather
// than hanging, and broadcasts the failure so the browser can offer a
// sign-in: KAS's own answer to this error is to run unauthenticated, which
// looks like a session that opens and then fails every turn.
func (h *Hub) respondKiroAccessToken(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	result, err := h.kiroAccessTokenResult(ctx)
	if err != nil {
		slog.Error("v3 auth: token unavailable", "chat_id", chatID, "error", err)
		h.respondBridge(ctx, chatID, msg, nil, err)
		// The message is kiro-cli's own text, not a rendering of it: it names
		// which leg of the login chain is dead (expired refresh token, no
		// profile, a missing binary), and no wording invented here could be more
		// specific. Broadcast AFTER the RPC error so the agent's request is never
		// waiting on a client fan-out.
		h.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
			Code:    vibekit.ErrCodeAuthTokenUnavailable,
			Message: err.Error(),
		}))
		return
	}
	h.respondBridge(ctx, chatID, msg, result, nil)
}

// kiroAccessTokenResult vends the result for the v3 _kiro/auth/getAccessToken
// host request by asking kiro-cli for its current token (see
// kiroauth.CLISource). Shared by the chat-bridge responder and the utility
// bridge (answerHostRequest) so the wire keys live in exactly one place — which
// is also why the latch is written HERE rather than in the responder above:
// both callers vend through this function, so both outcomes reach readiness.
func (h *Hub) kiroAccessTokenResult(ctx context.Context) (map[string]any, error) {
	result, err := h.vendKiroAccessToken(ctx)
	if h.authLatch != nil {
		h.authLatch.record(err)
	}
	return result, err
}

func (h *Hub) vendKiroAccessToken(ctx context.Context) (map[string]any, error) {
	if h.kiroToken == nil {
		return nil, kiroauth.ErrNoSource
	}
	return h.kiroToken.Token(ctx)
}

// kiroShellTypeResult is the answer to the v3 _kiro/terminal/shell_type host
// request. vibekit's container shell is bash; KAS ignores extra keys.
func kiroShellTypeResult() map[string]any {
	return map[string]any{"shellType": "bash"}
}
