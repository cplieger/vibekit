package api

import "context"

// MCP runtime types: the interface the hub uses to pull enabled server
// configs, and the event payloads broadcast to clients when kiro-cli
// reports MCP server state via its _kiro.dev/mcp/* notifications.
//
// The persisted config types live in internal/mcp; keeping the runtime
// contract here avoids a cycle (hub → mcp) when mcp's RegisterRoutes
// lives in the same package as its storage.

// MCPConfig is the hub's view of the user's configured MCP servers.
// The hub does not care about persistence or validation; it only needs
// to know which ACP server configs to pass on bridge spawn and which
// raw records to expose for pre-warm.
type MCPConfig interface {
	// ACPServers returns the enabled servers shaped for kiro-cli's
	// session/new and session/load mcpServers parameter. Each entry
	// is an opaque map whose shape follows the ACP spec's
	// McpServerStdio / McpServerHttp / McpServerSse schema —
	// internal/mcp populates per-transport fields; the hub passes
	// through verbatim.
	ACPServers(ctx context.Context) []map[string]any
	// EnabledNames returns the set of enabled server names. Used by the
	// hub to filter server_initialized notifications that belong to
	// disabled entries (defensive — kiro-cli shouldn't send them, but
	// we don't trust the boundary).
	EnabledNames(ctx context.Context) map[string]struct{}
	// SetKnownTools persists the tool list for a server so the UI can
	// show suggestions in the per-tool deny section.
	SetKnownTools(ctx context.Context, name string, tools []string)
}

// --- SSE payloads ---
//
// The hub emits one event per MCP state transition. Payloads are
// globally scoped (no chat_id) because MCP state is shared across all
// chats within the same container.

// MCPConnectedPayload is the payload for type="mcp_connected", emitted
// when kiro-cli reports _kiro.dev/mcp/server_initialized.
type MCPConnectedPayload struct {
	Server string `json:"server"`
}

// MCPPrewarmPayload is the payload for type="mcp_prewarm", emitted
// when a prewarm install starts, succeeds, or fails. The UI uses this
// to show "Installing..." next to the server name in the MCP panel.
type MCPPrewarmPayload struct {
	Package string `json:"package"`
	State   string `json:"state"` // "installing", "done", "failed"
}

// MCPOAuthPayload is the payload for type="mcp_oauth_needed", emitted
// when kiro-cli reports _kiro.dev/mcp/oauth_request. URL is the
// provider's authorisation endpoint; the user completes the flow in a
// new tab.
type MCPOAuthPayload struct {
	Server string `json:"server"`
	URL    string `json:"url"`
}

// MCPFailedPayload is the payload for type="mcp_failed", emitted
// when kiro-cli reports _kiro.dev/mcp/server_init_failure.
type MCPFailedPayload struct {
	Server string `json:"server"`
	Error  string `json:"error"`
}

// MCPDisconnectedPayload is the payload for type="mcp_disconnected".
// Emitted when the hub's last bridge exits: kiro-cli's MCP subprocesses
// shut down with it, so no configured server is currently live.
// Clients use this to clear their runtime-state map.
type MCPDisconnectedPayload struct {
	Server string `json:"server"`
}

// MCPSnapshotServer is one entry in a hub-to-steering MCP registry
// snapshot. Defined here to keep the steering package decoupled from
// internal/hub.
type MCPSnapshotServer struct {
	Name string `json:"name"`
}

// --- Discovery (prompts + resources advertised by a connected server) ---
//
// On v3 (KAS) a connected MCP server's prompts and resources arrive in
// the _kiro/mcp/status notification (alongside its tools). The registry
// caches them per server and the /api/mcp/status endpoint surfaces them so
// the Settings → Tools UI can list what a server exposes and fetch a
// specific prompt/resource on demand via _kiro/mcp/getPrompt /
// _kiro/mcp/getResource. Shapes verified against the KAS 2.12 acp-server
// bundle + a live probe.

// MCPPromptArg describes one argument of an MCP prompt.
type MCPPromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// MCPPromptInfo describes one prompt a connected MCP server advertises.
// PromptName is the machine id passed to _kiro/mcp/getPrompt; Name is the
// human-readable display title (they differ — e.g. "Simple Prompt" vs
// "simple-prompt"). Arguments lists the prompt's parameters, if any.
type MCPPromptInfo struct {
	Name        string         `json:"name"`
	PromptName  string         `json:"prompt_name"`
	Description string         `json:"description,omitempty"`
	Arguments   []MCPPromptArg `json:"arguments,omitempty"`
}

// MCPResourceInfo describes one resource a connected MCP server advertises.
// URI is the identifier passed to _kiro/mcp/getResource.
type MCPResourceInfo struct {
	Name        string `json:"name"`
	URI         string `json:"uri"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

// MCPServerState is the lifecycle status of one configured MCP server.
// Exported so the hub's mcpRegistry and the /api/mcp/status endpoint
// share a single typed enum with compile-time safety.
type MCPServerState string

// MCPStateIdle and the following constants define the valid MCPServerState lifecycle values.
const (
	MCPStateIdle      MCPServerState = "idle"       // configured but no bridge running
	MCPStateConnected MCPServerState = "connected"  // kiro-cli reported server_initialized
	MCPStateOAuth     MCPServerState = "needs_auth" // kiro-cli sent oauth_request
	MCPStateFailed    MCPServerState = "failed"     // kiro-cli sent server_init_failure
)
