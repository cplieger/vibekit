package api

import (
	checkpoint "github.com/cplieger/vibekit/internal/checkpoint/types"
)

// Per-event payload structs for SSE events. The envelope types and
// event-type constants live in events.go; this file contains only the
// payload shapes that change when new events are added or existing
// payloads gain fields.

// TurnEndedPayload is the payload for type="turn_ended".
type TurnEndedPayload struct {
	ChangedFiles map[string]*FileChange `json:"changed_files,omitempty"`
	StopReason   StopReason             `json:"stop_reason,omitempty"`
	CreditsDelta float64                `json:"credits_delta,omitempty"`
	ElapsedMs    float64                `json:"elapsed_ms,omitempty"`
}

// FileChange tracks per-file change stats during a turn.
type FileChange struct {
	LinesAdded   int  `json:"lines_added"`
	LinesRemoved int  `json:"lines_removed"`
	IsNewFile    bool `json:"is_new_file,omitempty"`
}

// ConnectedPayload is the payload for type="connected", the SSE handshake
// event. Floor is the oldest event ID still in the replay ring; Head is
// the newest. Clients with last-seen-id < Floor know they missed events
// and should refetch authoritative state.
type ConnectedPayload struct {
	Floor uint64 `json:"floor"`
	Head  uint64 `json:"head"`
}

// PermissionNeededPayload is the payload for type="permission_needed".
type PermissionNeededPayload struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Title      string `json:"title,omitempty"`
	// Kind forwards the ACP toolCall.kind so the client can style
	// distinctive permission prompts (switch_mode gets a different
	// dialog vs an execute_bash prompt).
	Kind         ToolKind           `json:"kind,omitempty"`
	SubSessionID string             `json:"sub_session_id,omitempty"`
	Options      []PermissionOption `json:"options"`
	RequestID    int64              `json:"request_id"`
}

// PermissionOption is one selectable response in a permission dialog.
type PermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionKindAllowOnce is the wire value kiro-cli sends in
// PermissionOption.Kind to identify the "allow once" choice.
const PermissionKindAllowOnce = "allow_once"

// MessageChunkPayload is the payload for type="message_chunk" (assistant
// streaming deltas). IsReasoning distinguishes reasoning deltas from
// regular content deltas — both flow through the same SSE event but
// land on different fields client-side (Message.Reasoning vs Content).
// BlockIndex addresses the chronological content block this delta
// belongs to (Anthropic's content_block_delta.index): consecutive text
// chunks share an index; a tool_call between text segments bumps the
// next text chunk to a new index. Clients use this to accumulate
// deltas into the right block in Message.Blocks.
type MessageChunkPayload struct {
	MessageID      string `json:"message_id"`
	Delta          string `json:"delta"`
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	BlockIndex     int    `json:"block_index"`
	IsReasoning    bool   `json:"is_reasoning,omitempty"`
}

// CheckpointRestoredPayload is the payload for type="checkpoint_restored".
// Replaces the ad-hoc map[string]any so the wire shape is discoverable
// via IDE completion and typos in key names become compile errors.
type CheckpointRestoredPayload struct {
	Tag          string `json:"tag"`
	MessageCount int    `json:"message_count"`
}

// ErrorCode identifies an SSE error event class. Using a typed string
// prevents typos at construction sites and makes the valid set
// discoverable via IDE completion.
type ErrorCode string

// ErrCodeRecoveryFailed and the following constants define the valid ErrorCode values for SSE error events.
const (
	ErrCodeRecoveryFailed    ErrorCode = "recovery_failed"
	ErrCodeBridgeStartFailed ErrorCode = "bridge_start_failed"
	ErrCodePromptFailed      ErrorCode = "prompt_failed"
	ErrCodeAgentNotFound     ErrorCode = "agent_not_found"
	ErrCodeAgentConfigError  ErrorCode = "agent_config_error"
	ErrCodeRateLimit         ErrorCode = "rate_limit"
	ErrCodeStreamTimeout     ErrorCode = "stream_timeout"
	ErrCodeSpawnFailed       ErrorCode = "spawn_failed"
	ErrCodeSwitchFailed      ErrorCode = "switch_failed"
	ErrCodeCompactionFailed  ErrorCode = "compaction_failed"
)

// ErrorPayload is the payload for type="error". Code lets clients react
// per-class without string-matching.
type ErrorPayload struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// WorkingLabelPayload is the payload for type="working_label". Sent when
// the agent starts or finishes a tool call so the client can show a
// contextual label ("Reading", "Searching", "Running <title>", etc.)
// instead of a generic "Thinking" indicator.
type WorkingLabelPayload struct {
	Label string `json:"label"`
}

// CodeReferencesPayload is the payload for type="code_references". Carries
// the licensed-code attributions accumulated on the in-flight assistant
// turn (v3/KAS). MessageID targets that turn's assistant message so the
// client attaches the chip to it; References is the full deduped list so
// the client can render idempotently (a later notification replaces rather
// than appends). Also persisted on the Message so the chip survives reload.
type CodeReferencesPayload struct {
	MessageID  string          `json:"message_id"`
	References []CodeReference `json:"references"`
}

// KnowledgeIndexingPayload is the payload for type="knowledge_indexing".
// Translated from the KAS _kiro/knowledge/indexingStarted (Status="started",
// carries FileCount) and _kiro/knowledge/indexingCompleted (Status="success"
// or a failure string, carries ItemCount on success) notifications. Emitted
// globally (no chat_id) because the knowledge store is workspace-global; the
// client refetches GET /api/knowledge on receipt. Note: these fire only for
// agent-declared knowledge_bases sync at session start — a user-initiated add
// reports progress through the `show` active-operations list instead (verified
// live), so the client also polls while an entry is still indexing.
type KnowledgeIndexingPayload struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	FileCount int    `json:"file_count,omitempty"`
	ItemCount int    `json:"item_count,omitempty"`
}

// SafetyStatus is the v3 (KAS) Infrastructure-Safety gate state (GateStatus).
// The gate evaluates infrastructure-as-code tool calls (Terraform, CFN, CDK,
// Docker, k8s, …) against remotely-formalized safety properties. Typed so the
// valid set is discoverable and wiregen emits a TS union.
type SafetyStatus string

// SafetyStatusIdle and the following constants are the v3 (KAS) GateStatus
// values carried on _kiro/safety/statusChanged.
const (
	SafetyStatusIdle        SafetyStatus = "idle"
	SafetyStatusFormalizing SafetyStatus = "formalizing"
	SafetyStatusEvaluating  SafetyStatus = "evaluating"
	SafetyStatusBlocked     SafetyStatus = "blocked"
	SafetyStatusError       SafetyStatus = "error"
)

// SafetyStatusPayload is the payload for type="safety_status", translated from
// the v3 (KAS) _kiro/safety/statusChanged notification. It surfaces the
// Infrastructure-Safety gate's live state so the client can show a transient
// status banner. Defensive/forward-looking: KAS only installs the gate (and so
// only emits this) when the client declares the infrastructureSafety capability
// AND an AWS governance flag (infraSafetyMonitor/infraSafetyEnforce) is on — off
// by default on individual/Builder-ID accounts, so this normally never fires.
// Distinct from vibekit's own Supervised write-gate (see vibekit-supervised.md).
type SafetyStatusPayload struct {
	Status            SafetyStatus `json:"status"`
	Detail            string       `json:"detail,omitempty"`
	ToolID            string       `json:"tool_id,omitempty"`
	BlockedProperties []string     `json:"blocked_properties,omitempty"`
}

// SafetyProperty is one formalized Infrastructure-Safety property. Properties
// are authored OUT-OF-BAND: KAS "formalizes" them via a remote MCP tool
// (evaluate_infrastructure_safety at runtime.us-east-1.kiro.dev) from the
// agent's own infrastructure work — there is no client RPC to create, set, or
// toggle one, so vibekit only ever displays them, never authors them.
type SafetyProperty struct {
	Description string `json:"description"`
	Index       int    `json:"index,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
}

// SafetyPropertiesPayload is the payload for type="safety_properties",
// translated from the v3 (KAS) _kiro/safety/propertiesChanged notification.
// Reason is the KAS PropertyChangeReason (formalized/toggled/expired). Same
// gating + authoring caveats as SafetyStatusPayload.
type SafetyPropertiesPayload struct {
	Reason     string           `json:"reason,omitempty"`
	Properties []SafetyProperty `json:"properties"`
}

// GovernanceFeatures is the org/account feature-flag set carried by the v3
// (KAS) _kiro/governance/state notification (KAS's GovernanceFeatures object,
// verified against the KAS 2.12 acp-server bundle + a live probe). Every field
// is the RESOLVED effective value: MCPEnabled/WebToolsEnabled/AutonomousAgents
// are the negation of a "…GovernanceDisabled" flag; the analytics/logging/
// tracker fields are direct "…GovernanceEnabled" flags. On an individual /
// Builder-ID login these resolve to a permissive default (mcp/webTools/
// autonomousAgents on; analytics/promptLogging/codeReferenceTracker off;
// contentCollection on) with isEnterprise=false and no disabledReason.
//
// NOTE: Infrastructure-Safety (infraSafetyMonitor/infraSafetyEnforce) is NOT
// here — it is a separate modelConfigProvider.isFeatureEnabled channel, not a
// governance feature (verified: absent from _kiro/governance/state). So the
// safety banner (translate/safety.go) is gated by KAS's own emission, not by
// any field on this struct.
type GovernanceFeatures struct {
	// MCPEnabled reports whether the MCP subsystem is permitted. When false,
	// enterprise governance has suppressed MCP entirely (vibekit disables the
	// add-server affordance and shows a disabled state in Settings → Tools).
	MCPEnabled bool `json:"mcp_enabled"`
	// WebToolsEnabled reports whether built-in web tools are permitted.
	WebToolsEnabled bool `json:"web_tools_enabled"`
	// UsageAnalytics reports whether usage analytics collection is enabled.
	UsageAnalytics bool `json:"usage_analytics"`
	// ContentCollection reports whether content collection is enabled.
	ContentCollection bool `json:"content_collection"`
	// PromptLogging reports whether prompt logging is enabled.
	PromptLogging bool `json:"prompt_logging"`
	// CodeReferenceTracker reports whether licensed-code reference tracking is
	// enabled — the flag that governs whether KAS emits _kiro/code_references
	// (the attribution chip in code-refs.ts is dormant unless this is true).
	CodeReferenceTracker bool `json:"code_reference_tracker"`
	// AutonomousAgents reports whether autonomous agent runs are permitted.
	AutonomousAgents bool `json:"autonomous_agents"`
}

// GovernanceStatePayload is the payload for type="governance_state", translated
// from the v3 (KAS) _kiro/governance/state notification (buildNotification:
// {sessionId, isEnterprise, features, disabledReason}). The account/workspace
// feature-flag policy KAS pushes on every session/new + session/load, and
// re-pushes on a prompt when it changes; vibekit caches the latest hub-side and
// also serves it at GET /api/governance so a fresh page load can read it with
// no chat open. The wire sessionId is used only for subagent-copy dedup and is
// dropped from this payload — governance is account-global, so the SSE is
// broadcast with an empty chat id (every client receives it).
//
// Known distinguishes "the server has told us the real policy" (Known=true, on
// every SSE broadcast and a warm REST snapshot) from "not yet observed"
// (Known=false, a cold REST snapshot before any bridge has started). Clients
// MUST only gate/annotate affordances when Known is true — the all-false zero
// value would otherwise read as "everything disabled" when we simply don't know
// yet.
type GovernanceStatePayload struct {
	// DisabledReason is a human-readable reason surfaced by enterprise
	// governance (e.g. why MCP is off); empty/absent on a normal account.
	DisabledReason string `json:"disabled_reason,omitempty"`
	// Features is the resolved effective feature-flag set.
	Features GovernanceFeatures `json:"features"`
	// Known is true once the real policy has been observed (see the type doc).
	Known bool `json:"known"`
	// IsEnterprise reports whether this is an enterprise/managed account.
	IsEnterprise bool `json:"is_enterprise,omitempty"`
}

// OpenExternalURLPayload is the payload for type="open_external_url".
// The agent (v3/KAS) asks the client to open a URL for the user, most
// often an MCP server's OAuth authorization page. Browsers popup-block a
// window.open() not driven by a user gesture, so the client surfaces a
// clickable affordance (a banner link) the user activates rather than
// auto-opening. Only http/https URLs are broadcast (server-side scheme
// guard in hub/bridge_v3_auth.go; the client re-checks before rendering).
type OpenExternalURLPayload struct {
	URL string `json:"url"`
}

// RPC error code constants for typed dispatch in retry logic.
const (
	// RPCCodeInternal is the JSON-RPC 2.0 "Internal error" code (-32603).
	RPCCodeInternal = -32603
	// RPCCodeNotIdle indicates the session is still processing a prior turn.
	RPCCodeNotIdle = -32001
	// RPCCodeBridgeExited is a server-defined code indicating the ACP bridge process exited.
	RPCCodeBridgeExited = -32000
)

// ToolCallPayload is the payload for type="tool_call". BlockIndex is
// the position of the tool_use block in the assistant message's
// chronological Blocks array — the client uses it to insert the tool
// card at the right spot relative to surrounding text blocks.
type ToolCallPayload struct {
	MessageID  string   `json:"message_id"`
	ToolCall   ToolCall `json:"tool_call"`
	BlockIndex int      `json:"block_index"`
}

// ToolCallUpdatePayload is the payload for type="tool_call_update".
type ToolCallUpdatePayload struct {
	MessageID string   `json:"message_id"`
	ToolCall  ToolCall `json:"tool_call"`
}

// TerminalOutputPayload is the payload for type="terminal_output".
type TerminalOutputPayload struct {
	TerminalID string `json:"terminal_id"`
	Data       string `json:"data"`
}

// TerminalExitedPayload is the payload for type="terminal_exited". A
// signal-killed process carries Signal (e.g. "killed") with ExitCode
// omitted; a normal exit carries ExitCode (>=0) with Signal empty. This
// mirrors KAS's zTerminalExitStatus, which requires exitCode>=0 and a
// separate signal string, so a signal death never reports exit_code:-1.
type TerminalExitedPayload struct {
	ExitCode   *int   `json:"exit_code,omitempty"`
	TerminalID string `json:"terminal_id"`
	Signal     string `json:"signal,omitempty"`
}

// TerminalCreatedPayload is the payload for type="terminal_created".
type TerminalCreatedPayload struct {
	TerminalID string   `json:"terminal_id"`
	Command    string   `json:"command"`
	Args       []string `json:"args"`
}

// ModeChangedPayload is the payload for type="mode_changed".
type ModeChangedPayload struct {
	ModeID string `json:"mode_id"`
}

// ConflictDetectedPayload is the payload for type="conflict_detected".
// Type alias to checkpoint/types.ConflictPayload — single source of truth.
type ConflictDetectedPayload = checkpoint.ConflictPayload

// ChatDeletedPayload is the payload for type="chat_deleted".
type ChatDeletedPayload struct {
	ID string `json:"id"`
}

// AvailableCommand is one entry in the slash-command catalogue surfaced
// by kiro-cli's _kiro.dev/commands/available notification. The wire
// shape carries opaque metadata; clients consume only Name and Description.
type AvailableCommand struct {
	Meta        map[string]any `json:"meta,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
}

// CommandsUpdatedPayload is the payload for type="commands_updated".
type CommandsUpdatedPayload struct {
	Commands []AvailableCommand `json:"commands"`
	Prompts  []AvailableCommand `json:"prompts,omitempty"`
}

// CompactionStartedPayload is the payload for type="compaction_started".
type CompactionStartedPayload struct{}

// ChatStatusPayload is the payload for type="chat_status": the agent's
// self-declared activity for a chat, sourced from the KAS focus_update
// channel (the model's update_session_information tool). Status is one of
// in_progress | waiting_on_user | completed | idle; Description is a short
// "what I'm working on" line. Ephemeral by design — never persisted, so a
// restart or reconnect gap resets tabs to a neutral state instead of
// replaying a stale "in_progress".
type ChatStatusPayload struct {
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
}

// MCPConfigChangedPayload is the payload for type="mcp_config_changed".
type MCPConfigChangedPayload struct{}

// ForgesChangedPayload is the payload for type="forges_changed".
// Sent after a forge is connected, disconnected, or re-probed.
type ForgesChangedPayload struct{}

// HooksChangedPayload is the payload for type="hooks_changed". Emitted
// (empty, workspace-global — no chat_id) after a hook is created, toggled,
// or its .kiro/hooks/*.json file changes on disk (KAS _kiro/hooks/didChange
// on the utility bridge). The client refetches GET /api/hooks on receipt.
type HooksChangedPayload struct{}

// ToolJobChangedPayload is the payload for type="tool_job_changed".
// Broadcast (workspace-global — no chat_id) on every tool-job state
// transition: enqueued, started, done, failed, cancelled. The job
// carries no output tail; output streams via tool_job_output.
type ToolJobChangedPayload struct {
	Job *ToolJob `json:"job"`
}

// ToolJobOutputPayload is the payload for type="tool_job_output":
// a coalesced batch of output lines from the running tool job.
type ToolJobOutputPayload struct {
	JobID string   `json:"job_id"`
	Lines []string `json:"lines"`
}

// SettingsUpdatedPayload is the payload for type="settings_updated".
type SettingsUpdatedPayload struct{}

// --- HTTP response types for checkpoint and MCP endpoints ---

// CheckpointDiffResponse is the typed response for the checkpoint diff endpoint.
type CheckpointDiffResponse struct {
	Files []checkpoint.FileChange `json:"files"`
}

// CheckpointRestorePreviewResponse is the typed response for restore-preview.
type CheckpointRestorePreviewResponse struct {
	Files []string `json:"files"`
}

// CheckpointConflictsResponse is the typed response for the conflicts endpoint.
type CheckpointConflictsResponse struct {
	Conflicts []checkpoint.ConflictPayload `json:"conflicts"`
}
