package translate

// _kiro.dev/mcp/* notification handlers.

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// HandleMCPInitialized processes MCP server_initialized notifications.
func (t *Translator) HandleMCPInitialized(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		ServerName string `json:"serverName"`
	}](msg, "mcp/server_initialized")
	if !ok || p.ServerName == "" {
		return
	}
	t.MCP().RecordConnected(ctx, p.ServerName)
}

// HandleMCPOAuth processes MCP oauth_request notifications.
func (t *Translator) HandleMCPOAuth(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		ServerName string `json:"serverName"`
		OAuthURL   string `json:"oauthUrl"`
	}](msg, "mcp/oauth_request")
	if !ok || p.ServerName == "" || p.OAuthURL == "" {
		return
	}
	t.MCP().RecordOAuth(ctx, p.ServerName, p.OAuthURL)
}

// HandleMCPInitFailure processes MCP server_init_failure notifications.
func (t *Translator) HandleMCPInitFailure(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		ServerName string `json:"serverName"`
		Error      string `json:"error"`
	}](msg, "mcp/server_init_failure")
	if !ok || p.ServerName == "" {
		return
	}
	t.MCP().RecordInitFailure(ctx, p.ServerName, p.Error)
}
