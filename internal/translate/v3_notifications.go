package translate

// v3 (KAS) _kiro/* extension-notification handlers whose payloads are
// reshaped from their v2 _kiro.dev/* counterparts (so they can't just
// reuse the v2 handler). Shape-compatible v3 notifications (rate_limit,
// customAgent/not_found, customAgent/config_error) reuse the v2 handlers
// directly via the dispatch table and are not repeated here.
//
// Wire shapes verified against the KAS 2.12 acp-server bundle; see
// kiro-cli-research.md "v3 _kiro/* wire surface".

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// v3MCPStatus is the _kiro/mcp/status payload. v3 consolidates v2's
// per-server server_initialized / server_init_failure / oauth_request
// notifications into one list keyed by a status enum.
type v3MCPStatus struct {
	Servers []v3MCPServer `json:"servers"`
}

// v3MCPServer is one entry in the _kiro/mcp/status servers list. Fields
// present depend on status: connected carries tools[] + prompts[] +
// resources[]; failed carries errorMessage + (for auth failures)
// authorizationUrl.
type v3MCPServer struct {
	Name             string `json:"name"`
	Status           string `json:"status"` // connecting | connected | failed | disabled
	ErrorMessage     string `json:"errorMessage"`
	AuthorizationURL string `json:"authorizationUrl"`
	Tools            []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Prompts   []v3MCPPrompt   `json:"prompts"`
	Resources []v3MCPResource `json:"resources"`
}

// v3MCPPrompt mirrors one prompt entry in a connected server's status.
// promptName is the machine id (passed to _kiro/mcp/getPrompt); name is
// the display title. Verified against the KAS 2.12 bundle + live probe.
type v3MCPPrompt struct {
	Name        string `json:"name"`
	PromptName  string `json:"promptName"`
	Description string `json:"description"`
	Arguments   []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Required    bool   `json:"required"`
	} `json:"arguments"`
}

// v3MCPResource mirrors one resource entry in a connected server's status.
type v3MCPResource struct {
	Name        string `json:"name"`
	URI         string `json:"uri"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

// HandleMCPStatus maps the consolidated v3 _kiro/mcp/status notification
// onto the same MCP-registry state the v2 mcp/* handlers drive: connected
// servers record their tool names + connected state; failed servers record
// an init failure, or an OAuth prompt when an authorization URL is present.
func (t *Translator) HandleMCPStatus(ctx context.Context, _ api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[v3MCPStatus](msg, "mcp/status")
	if !ok {
		return
	}
	for i := range p.Servers {
		s := &p.Servers[i]
		if s.Name == "" {
			continue
		}
		switch s.Status {
		case "connected":
			// Tools, prompts and resources all ride this ONE notification, so they
			// land in one registry write. They used to split: the tool names went to
			// the MCP config store — a disk write, on a notification path, of
			// agent-derived data into a user-intent file.
			t.MCP().RecordConnected(ctx, s.Name, mcpToolNames(s.Tools), mcpPrompts(s.Prompts), mcpResources(s.Resources))
		case "failed":
			if s.AuthorizationURL != "" {
				t.MCP().RecordOAuth(ctx, s.Name, s.AuthorizationURL)
				continue
			}
			t.MCP().RecordInitFailure(ctx, s.Name, s.ErrorMessage)
		case "disabled":
			// A vibekit-configured server's off state is already on its config
			// row, which is what the MCP page renders it from — so the recorder
			// drops this frame for one, exactly as the default arm used to. It
			// keeps the frame only for a server vibekit never configured, where
			// this is the ONLY evidence the server exists: without it, a Power's
			// disabled server is invisible on a page that claims to list the
			// agent's integrations.
			t.MCP().RecordDisabled(ctx, s.Name)
		default:
			// connecting: transient, not terminal. Recording it would paint a row
			// the same notification's next frame for this server replaces.
		}
	}
	t.MCP().SignalReady()
}

// mcpToolNames extracts the tool-name list from a v3 MCP server entry.
func mcpToolNames(tools []struct {
	Name string `json:"name"`
},
) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Name != "" {
			names = append(names, tool.Name)
		}
	}
	return names
}

// mcpPrompts maps the wire prompt entries to the api discovery type,
// dropping entries with no machine promptName (unaddressable).
func mcpPrompts(in []v3MCPPrompt) []api.MCPPromptInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.MCPPromptInfo, 0, len(in))
	for _, p := range in {
		if p.PromptName == "" {
			continue
		}
		info := api.MCPPromptInfo{Name: p.Name, PromptName: p.PromptName, Description: p.Description}
		for _, a := range p.Arguments {
			if a.Name == "" {
				continue
			}
			info.Arguments = append(info.Arguments, api.MCPPromptArg{Name: a.Name, Description: a.Description, Required: a.Required})
		}
		out = append(out, info)
	}
	return out
}

// mcpResources maps the wire resource entries to the api discovery type,
// dropping entries with no uri (unaddressable).
func mcpResources(in []v3MCPResource) []api.MCPResourceInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.MCPResourceInfo, 0, len(in))
	for _, res := range in {
		if res.URI == "" {
			continue
		}
		out = append(out, api.MCPResourceInfo{Name: res.Name, URI: res.URI, Description: res.Description, MimeType: res.MimeType})
	}
	return out
}

// _kiro/sessions/changed (the v3 session-inventory diff) has no client
// consumer: on v3 subagents are tool calls, not sessions, so there is no
// session list to maintain. The method is noop'd in the hub dispatch.
