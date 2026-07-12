// v3 (KAS) host-mediated client requests.
//
// kiro-cli acp --agent-engine v3 runs with --auth=acp-callback and makes
// server->client requests on the session-creation critical path:
//
//	_kiro/auth/getAccessToken -> {accessToken, expiresAt, profileArn?}
//	_kiro/terminal/shell_type -> {shellType}
//
// Without answers, session/new stalls and no turn completes. It also sends
// _kiro/openExternalUrl (only because we declare that client capability;
// see internal/bridge/bridge.go initialize) when an MCP server needs a
// browser OAuth page opened — answered here too. v1/v2 never send any of
// these, so handleKiroClientRequest is a safe no-op there.
//
// The getAccessToken reply carries profileArn (from kiro-cli's state DB via
// internal/kiroauth) so KAS routes to the profile's region and account-level
// _kiro/account/getUsage can identify the account.
//
// Auth tokens are read (and refreshed when near expiry) from the ambient
// kiro-cli SSO cache via internal/kiroauth. KAS rejects a token inside its
// ~180s refresh buffer, so kiroauth refreshes via SSO-OIDC and writes the
// rotated token back to the cache before we vend it.

package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/kiroauth"
)

// v3-only server->client request method names.
const (
	methodKiroGetAccessToken = "_kiro/auth/getAccessToken" //nolint:gosec // G101: ACP method name, not a credential
	methodKiroShellType      = "_kiro/terminal/shell_type"
)

// kiroTokenReader vends (and refreshes) the ambient kiro-cli SSO access
// token for the v3 auth callback. Package-level singleton: the reader
// serializes refreshes internally so concurrent bridge callbacks don't
// double-refresh the rotating token.
var kiroTokenReader = kiroauth.NewReader("")

// kiroProfileReader sources the CodeWhisperer profile ARN from kiro-cli's
// state DB, included in the getAccessToken reply so KAS routes to the
// profile's region and _kiro/account/getUsage can identify the account.
// Package-level singleton (modtime-cached) so it isn't re-read per call.
var kiroProfileReader = kiroauth.NewProfileReader("")

// handleKiroClientRequest answers the v3-only server->client requests.
// Returns true if msg was one of them (so translateACPEvent stops),
// false otherwise. Safe to call for every incoming request regardless of
// engine: v1/v2 never send these methods.
func (h *Hub) handleKiroClientRequest(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) bool {
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
func (h *Hub) respondKiroOpenExternalURL(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		URL string `json:"url"`
	}
	if msg.Params != nil {
		_ = json.Unmarshal(msg.Params, &p)
	}
	if !isSafeExternalURL(p.URL) {
		slog.Warn("v3 openExternalUrl: rejecting unsafe scheme", "chat_id", chatID)
		h.respondBridge(ctx, chatID, msg, nil, &api.RPCError{
			Code:    -32602,
			Message: "openExternalUrl: only http/https URLs are allowed",
		})
		return
	}
	// Ack first so the agent's OAuth redirect isn't blocked on the UI.
	h.respondBridge(ctx, chatID, msg, map[string]any{"success": true}, nil)
	h.Broadcast(ctx, api.NewEvent(api.EventOpenExternalURL, chatID, api.OpenExternalURLPayload{URL: p.URL}))
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

// respondKiroAccessToken vends {accessToken, expiresAt} from the SSO
// cache, refreshing first when near expiry. On failure it returns a
// JSON-RPC error so KAS surfaces a clear auth failure rather than hanging.
func (h *Hub) respondKiroAccessToken(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	result, err := kiroAccessTokenResult(ctx)
	if err != nil {
		slog.Error("v3 auth: token unavailable", "chat_id", chatID, "error", err)
		h.respondBridge(ctx, chatID, msg, nil, err)
		return
	}
	h.respondBridge(ctx, chatID, msg, result, nil)
}

// kiroAccessTokenResult vends the {accessToken, expiresAt} result for the v3
// _kiro/auth/getAccessToken host request, refreshing the SSO token when near
// expiry. Shared by the chat-bridge responder and the utility bridge
// (answerUtilityHostRequest) so the wire keys live in exactly one place.
func kiroAccessTokenResult(ctx context.Context) (map[string]any, error) {
	tok, exp, err := kiroTokenReader.Token(ctx)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"accessToken": tok, "expiresAt": exp}
	// profileArn is best-effort: KAS routes to the profile's region with
	// it and falls back to a default region without it (turns still work).
	// It IS required for _kiro/account/getUsage, which returns
	// "Invalid profileArn." when the reply omits it. Omit on read failure
	// rather than failing the whole auth callback.
	if arn, aErr := kiroProfileReader.Arn(); aErr == nil && arn != "" {
		res["profileArn"] = arn
	} else if aErr != nil {
		slog.Debug("v3 auth: profile arn unavailable", "error", aErr)
	}
	return res, nil
}

// kiroShellTypeResult is the answer to the v3 _kiro/terminal/shell_type host
// request. vibekit's container shell is bash; KAS ignores extra keys.
func kiroShellTypeResult() map[string]any {
	return map[string]any{"shellType": "bash"}
}
