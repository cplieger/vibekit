package vibekit

// MCP runtime types: the event payloads broadcast to clients when kiro-cli
// reports MCP server state via its _kiro.dev/mcp/* notifications.
//
// The persisted config types live in internal/mcp.

// There is no MCPConfig interface here. The name census the hub reads is
// declared at that consumer (internal/hub's mcpNameSets); *mcp.Store implements
// it and the composition root's hub.WithMCPConfig call forces the check.
//
// Two members it never had are worth recording. No ACPServers: vibekit no longer
// sends servers inline on session/new — it renders KAS's own hot-reloading
// config file, and KAS merges `client > file-based`, so an inline copy would
// silently outrank the file. No SetKnownTools: a connected server's tool names
// are runtime state that arrives with its prompts and resources, so they live in
// the hub's registry rather than being written back into a user-config file on
// every notification.

// Origin records where an MCP server came from, so a runtime status row can say
// so and the UI can withhold the edit affordances for one vibekit does not own.
//
// The three values partition the name sets MCPConfig exposes: OriginUser is
// ConfiguredNames, OriginPower is AllNames minus ConfiguredNames, and
// OriginUnknown is everything KAS reports that neither set names.
type Origin string

// OriginUser, OriginPower, and OriginUnknown are the provenance values a
// runtime MCP status row can carry.
const (
	// OriginUser is a server from vibekit's own config — the MCP page owns its
	// row and every edit affordance on it.
	OriginUser Origin = "user"
	// OriginPower is a server an installed Power contributed via the
	// `powers.mcpServers` block of the config file vibekit renders. vibekit
	// cannot edit or delete it; the row is read-only.
	OriginPower Origin = "power"
	// OriginUnknown is a server KAS reports that vibekit cannot attribute: a
	// workspace-level config it does not read, a block a future KAS adds, or a
	// powers block that failed to parse. Read-only, same as OriginPower — the
	// distinction is what the row TELLS the user, not what it lets them do.
	OriginUnknown Origin = "unknown"
)

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

// MCPServerState is the lifecycle status of one MCP server KAS reported.
// Exported so the hub's mcpRegistry and the /api/mcp/status endpoint
// share a single typed enum with compile-time safety.
//
// The five values are "idle" (configured but no bridge running), "connected"
// (KAS reported the server initialised), "needs_auth" (KAS sent an
// authorization URL), "failed" (KAS reported an init failure) and "disabled"
// (KAS reports the server as off). They are declared as constants where they
// are produced — the hub's mcpRegistry, which serves /api/mcp/status.
//
// "disabled" is only ever recorded for a server vibekit did NOT configure. A
// configured server's off state is its config row's own `enabled: false`, which
// the UI already renders, so recording a second copy of it would put a runtime
// row beside a config row that disagrees with nothing.
type MCPServerState string
