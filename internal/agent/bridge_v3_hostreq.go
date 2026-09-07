// v3 (KAS) host-mediated client requests.
//
// The kiro-cli relay consumes the access-token request when started with
// --auth-method cli. KAS still sends _kiro/terminal/shell_type during session
// creation and _kiro/openExternalUrl when an MCP server needs browser OAuth.

package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"

	"github.com/cplieger/vibekit/internal/vibekit"
)

const methodKiroShellType = "_kiro/terminal/shell_type"

// handleKiroClientRequest answers the v3-only server-to-client requests.
func (in *inbound) handleKiroClientRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) bool {
	switch msg.Method {
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

// respondKiroOpenExternalURL acknowledges a safe URL before broadcasting it.
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
	// Ack first so the agent's OAuth redirect is not blocked on the UI.
	in.respondBridge(ctx, chatID, msg, map[string]any{"success": true}, nil)
	in.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventOpenExternalURL, chatID, vibekit.OpenExternalURLPayload{URL: p.URL}))
}

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

func kiroShellTypeResult() map[string]any {
	return map[string]any{"shellType": "bash"}
}
