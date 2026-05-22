package translate

// _kiro.dev/mcp/* notification handlers.

import (
	"context"
	"encoding/json"
	"log/slog"

	"vibekit/internal/api"
)

// HandleMCPInitialized processes MCP server_initialized notifications.
func (t *Translator) HandleMCPInitialized(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
	var p struct {
		ServerName string `json:"serverName"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.ServerName == "" {
		slog.Debug("mcp: invalid server_initialized payload", "error", err)
		return
	}
	t.deps.MCPRecordConnected(ctx, p.ServerName)
}

// HandleMCPOAuth processes MCP oauth_request notifications.
func (t *Translator) HandleMCPOAuth(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
	var p struct {
		ServerName string `json:"serverName"`
		OAuthURL   string `json:"oauthUrl"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.ServerName == "" || p.OAuthURL == "" {
		slog.Debug("mcp: invalid oauth_request payload", "error", err)
		return
	}
	t.deps.MCPRecordOAuth(ctx, p.ServerName, p.OAuthURL)
}

// HandleMCPInitFailure processes MCP server_init_failure notifications.
func (t *Translator) HandleMCPInitFailure(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
	var p struct {
		ServerName string `json:"serverName"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.ServerName == "" {
		slog.Debug("mcp: invalid server_init_failure payload", "error", err)
		return
	}
	t.deps.MCPRecordInitFailure(ctx, p.ServerName, p.Error)
}
