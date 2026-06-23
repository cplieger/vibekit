package api

// Chat domain types: session state, messages, tool calls, plans, usage,
// session modes/models, agent metadata. These are the persisted and
// over-the-wire shapes the store, hub, bridge, and push packages
// operate on.

import (
	"encoding/json"
)

// Role identifies the speaker of a message.
type Role string

// RoleUser and the following constants define the valid Role values for a chat message.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleEvent     Role = "event" // system / ui events rendered inline
)

// EventKind identifies subtypes of RoleEvent messages.
type EventKind string

// EventInterrupted and the following constants define the valid EventKind values for inline event messages.
const (
	EventInterrupted   EventKind = "interrupted"
	EventCancelled     EventKind = "cancelled"
	EventModelSwitched EventKind = "model_switched" // fresh ACP session with a new model
	EventCompacted     EventKind = "compacted"      // kiro-cli's native /compact, carries summary
	EventCrew          EventKind = "crew"           // kiro-cli's subagent orchestration (list_update snapshots)
	EventAgentSwitched EventKind = "agent_switched" // kiro-cli's agent hand-off mid-session
	EventCompactFailed EventKind = "compaction_failed"
	EventInbox         EventKind = "inbox" // subagent-to-parent message
)

// ToolKind identifies the category of a tool invocation. Values are
// assigned by kiro-cli and flow through the ACP protocol unchanged.
// The full set spans both the original ACP kinds and the richer
// classifications kiro-cli's translate layer emits for permission
// dialogs and tool-card rendering.
type ToolKind string

// ToolKindExecute and the following constants define the valid ToolKind values for classifying tool invocations.
const (
	ToolKindExecute    ToolKind = "execute"
	ToolKindShell      ToolKind = "shell"
	ToolKindRead       ToolKind = "read"
	ToolKindSearch     ToolKind = "search"
	ToolKindFetch      ToolKind = "fetch"
	ToolKindEdit       ToolKind = "edit"
	ToolKindThink      ToolKind = "think"
	ToolKindHook       ToolKind = "hook"
	ToolKindWrite      ToolKind = "write"
	ToolKindDelete     ToolKind = "delete"
	ToolKindMove       ToolKind = "move"
	ToolKindCommand    ToolKind = "command"
	ToolKindBrowser    ToolKind = "browser"
	ToolKindSwitchMode ToolKind = "switch_mode"
	ToolKindMCP        ToolKind = "mcp"
	ToolKindOther      ToolKind = "other"
)

// ToolStatus is the lifecycle state of a tool invocation.
type ToolStatus string

// ToolPending and the following constants define the ToolStatus lifecycle states for a tool invocation.
const (
	ToolPending    ToolStatus = "pending"
	ToolInProgress ToolStatus = "in_progress"
	ToolCompleted  ToolStatus = "completed"
	ToolFailed     ToolStatus = "failed"
)

// ACPUpdateKind identifies the subtype of an ACP session/update
// notification. Using typed constants prevents typos in the dispatch
// map and makes the protocol surface discoverable.
type ACPUpdateKind string

// ACPUpdateAgentChunk and the following constants define the valid ACPUpdateKind values for ACP session notifications.
const (
	ACPUpdateAgentChunk   ACPUpdateKind = "agent_message_chunk"
	ACPUpdateThoughtChunk ACPUpdateKind = "agent_thought_chunk"
	ACPUpdateToolCall     ACPUpdateKind = "tool_call"
	ACPUpdateToolUpdate   ACPUpdateKind = "tool_call_update"
	ACPUpdatePlan         ACPUpdateKind = "plan"
	ACPUpdateModeChange   ACPUpdateKind = "current_mode_update"
	ACPUpdateSteering     ACPUpdateKind = "steering_inclusion"
)

// BlockType discriminates content blocks in an assistant message's
// chronological content array. Mirrors Anthropic's `content_block.type`
// from the Messages API streaming spec — the same model claude-code
// uses to render text, tool calls, and thinking traces inline as the
// agent emits them rather than splitting them into separate sections.
type BlockType string

const (
	// BlockText is a markdown text segment from the agent.
	BlockText BlockType = "text"
	// BlockToolUse is a tool invocation. Only the ToolCallID is carried
	// in the block; the full ToolCall lives in Message.ToolCalls so
	// status updates (pending → in_progress → completed) don't need
	// to touch the block array.
	BlockToolUse BlockType = "tool_use"
	// BlockThinking is an extended-thinking trace segment.
	BlockThinking BlockType = "thinking"
)

// Block is one entry in an assistant message's chronological content
// array. Position in Message.Blocks IS the order in which the agent
// emitted the block — text → tool → text → tool, etc. — so the client
// renders them inline as they happened rather than concatenating all
// text into one bubble at the top with tools dumped below.
//
// Replay-compatible: messages persisted before this field existed have
// Blocks=nil. Renderers fall back to the legacy Content + ToolCalls
// layout when Blocks is empty.
type Block struct {
	// Type is the discriminator: text | tool_use | thinking.
	Type BlockType `json:"type"`
	// Text carries the markdown text for Type=BlockText. Accumulated
	// across MessageChunkPayload events targeting this block index.
	Text string `json:"text,omitempty"`
	// Thinking carries the reasoning text for Type=BlockThinking.
	Thinking string `json:"thinking,omitempty"`
	// ToolCallID references a tool by ID in Message.ToolCalls for
	// Type=BlockToolUse. Empty for other types.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall is a tool invocation inside an assistant message. One assistant
// message may have multiple tool calls; each can be updated in place as
// status changes (pending → in_progress → completed/failed).
type ToolCall struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Kind         ToolKind        `json:"kind"`
	Status       ToolStatus      `json:"status"`
	Output       string          `json:"output,omitempty"`
	SubSessionID string          `json:"sub_session_id,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Locations    []ToolLocation  `json:"locations,omitempty"`
	Diffs        []ToolDiff      `json:"diffs,omitempty"`
	DurationMs   int             `json:"duration_ms,omitempty"`
	Ts           int64           `json:"ts"`
}

// ToolLocation is a file path (and optional line) the agent is working
// with. Sent by kiro-cli in tool_call and tool_call_update notifications.
// Used by the editor to scroll to the file the agent is accessing or
// modifying.
type ToolLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ToolDiff is a before/after text change from a write tool call. Sent
// by kiro-cli in tool_call notifications for edit operations. Path is
// workspace-relative (absolute paths from kiro-cli are normalised via
// hub.relPath before being stored here); OldText/NewText are the
// changed fragments, not full-file contents.
type ToolDiff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text"`
}

// PlanStatus is the lifecycle state of a plan entry.
type PlanStatus string

// PlanPending and the following constants define the PlanStatus lifecycle states for a plan entry.
const (
	PlanPending    PlanStatus = "pending"
	PlanInProgress PlanStatus = "in_progress"
	PlanCompleted  PlanStatus = "completed"
)

// PlanEntry is one item in an agent-authored plan.
type PlanEntry struct {
	Content  string     `json:"content"`
	Priority string     `json:"priority"`
	Status   PlanStatus `json:"status"`
}

// CrewStatus is the lifecycle state of a crew subagent. The full set
// reflects what kiro-cli actually emits via _kiro.dev/subagent/list_update;
// the comment on CrewSubagent.Status previously called these "other
// kiro-cli types" — they are now first-class enum values.
type CrewStatus string

// CrewWorking and the following constants define the CrewStatus lifecycle states for a crew subagent.
const (
	CrewWorking    CrewStatus = "working"
	CrewTerminated CrewStatus = "terminated"
	CrewError      CrewStatus = "error"
	CrewPending    CrewStatus = "pending"
)

// CrewSubagent is one subagent in a `crew` event message, sourced from
// kiro-cli's `_kiro.dev/subagent/list_update` notification. Each
// kiro-cli snapshot replaces the full list; we persist the last seen
// snapshot on the message so reloading a chat shows the final state.
type CrewSubagent struct {
	SessionID    string     `json:"session_id"`
	SessionName  string     `json:"session_name"`  // short user-facing label ("count_to_3")
	AgentName    string     `json:"agent_name"`    // kiro-cli agent config used
	InitialQuery string     `json:"initial_query"` // task the subagent was given
	Status       CrewStatus `json:"status"`        // see CrewStatus enum above
	StatusMsg    string     `json:"status_msg,omitempty"`
	Group        string     `json:"group"` // grouping key from kiro-cli, e.g. "crew-<desc>"
	Role         string     `json:"role,omitempty"`
	DependsOn    []string   `json:"depends_on,omitempty"`
}

// Crew is the live state of one crew message. Subagents is the latest
// snapshot; Group is the shared group id across the subagents (used
// for message identity when kiro-cli emits subsequent updates).
// PendingStages lists subagents that are planned but not yet spawned.
type Crew struct {
	Group         string             `json:"group"`
	Subagents     []CrewSubagent     `json:"subagents"`
	PendingStages []CrewPendingStage `json:"pending_stages,omitempty"`
}

// CrewPendingStage is a planned subagent that hasn't spawned yet.
// kiro-cli emits these in the pendingStages array of list_update.
type CrewPendingStage struct {
	Name      string   `json:"name"`
	AgentName string   `json:"agent_name"`
	Role      string   `json:"role,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
}

// Message is one entry in a chat transcript. Tool calls are embedded in
// assistant messages (not standalone messages). Event messages carry an
// EventKind for inline rendering (compression, cancellation, restart,
// subagent orchestration).
type Message struct {
	// Crew is populated only for EventKind=crew messages; holds the
	// latest subagent snapshot from kiro-cli's list_update stream.
	Crew    *Crew  `json:"crew,omitempty"`
	ID      string `json:"id"`
	Role    Role   `json:"role"`
	Content string `json:"content,omitempty"`
	// Reasoning is the agent's "thinking" trace for this turn —
	// extended-thinking models emit it as a parallel stream alongside
	// Content. Persisted on the same message so the one-message-per-turn
	// invariant holds; rendered above the content bubble in the UI.
	Reasoning string      `json:"reasoning,omitempty"`
	EventKind EventKind   `json:"event_kind,omitempty"`
	ToolCalls []ToolCall  `json:"tool_calls,omitempty"`
	Plan      []PlanEntry `json:"plan,omitempty"`
	// Blocks is the chronologically-ordered content array — text /
	// tool_use / thinking blocks in the order the agent emitted them.
	// Mirrors Anthropic's Messages API content array. Newly-streamed
	// assistant messages populate Blocks alongside Content / ToolCalls
	// (the latter two are kept for back-compat with replay of older
	// messages that don't have Blocks). The client prefers Blocks when
	// non-empty and falls back to Content + ToolCalls otherwise.
	Blocks []Block `json:"blocks,omitempty"`
	Ts     int64   `json:"ts"`
}

// Usage is a chat's last-known context and billing snapshot.
type Usage struct {
	MeteringItems []MeteringItem `json:"metering_items,omitempty"`
	ContextPct    float64        `json:"context_pct"`
	ContextSize   int            `json:"context_size"`
	Credits       float64        `json:"credits"`
	TurnCount     int            `json:"turn_count"`
	LastTurnMs    float64        `json:"last_turn_ms"`
	HasRealData   bool           `json:"has_real_data"`
}

// MeteringItem is one usage dimension reported by kiro-cli's
// meteringUsage array. UnitPlural is the canonical identifier
// ("credits", "tokens", "requests"); UnitSingular is its singular form.
type MeteringItem struct {
	UnitSingular string  `json:"unit_singular"`
	UnitPlural   string  `json:"unit_plural"`
	Value        float64 `json:"value"`
}

// SessionMode describes one mode the running agent supports. Populated
// from the `modes.availableModes` field of kiro-cli's session/new or
// session/load response; kept on the chat so the UI can render a mode
// pill without re-querying the bridge.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModel describes one model the running agent can swap to, as
// declared by kiro-cli's session/new response. Replaces our prior
// shell-out to `kiro-cli chat --list-models`.
type SessionModel struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description,omitempty"`
	RateMultiplier float64 `json:"rate_multiplier,omitempty"`
}

// Chat is the full persisted chat. Serialized as <dir>/<id>.json.
type Chat struct {
	CrewMessageIDs      map[string]string `json:"crew_message_ids,omitempty"`
	Name                string            `json:"name"`
	Agent               string            `json:"agent,omitempty"`
	Model               string            `json:"model,omitempty"`
	ACPSessionID        string            `json:"acp_session_id,omitempty"`
	CurrentModeID       string            `json:"current_mode_id,omitempty"`
	ParentChatID        ChatID            `json:"parent_chat_id,omitempty"`
	CompactionWatermark string            `json:"compaction_watermark,omitempty"`
	Summary             string            `json:"summary,omitempty"`
	OldestCheckpointTag string            `json:"oldest_checkpoint_tag,omitempty"`
	ID                  string            `json:"id"`
	AvailableModels     []SessionModel    `json:"available_models,omitempty"`
	AvailableModes      []SessionMode     `json:"available_modes,omitempty"`
	Messages            []Message         `json:"messages"`
	CurrentPlan         []PlanEntry       `json:"current_plan,omitempty"`
	Usage               Usage             `json:"usage"`
	CreatedAt           int64             `json:"created_at"`
	UpdatedAt           int64             `json:"updated_at"`
	RewindFromTurn      int               `json:"rewind_from_turn,omitempty"`
	MessageCount        int               `json:"message_count"`
	SupervisedMode      bool              `json:"supervised_mode,omitempty"`
	AutoApproveCrew     bool              `json:"auto_approve_crew,omitempty"`
}

// Header returns the chat's metadata without messages. Used for list
// endpoints and SSE broadcasts when messages are not needed.
func (c *Chat) Header() ChatHeader {
	return ChatHeader{
		ID:                  c.ID,
		Name:                c.Name,
		Agent:               c.Agent,
		Model:               c.Model,
		ACPSessionID:        c.ACPSessionID,
		CurrentModeID:       c.CurrentModeID,
		AvailableModes:      c.AvailableModes,
		AvailableModels:     c.AvailableModels,
		Usage:               c.Usage,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
		MessageCount:        len(c.Messages),
		SupervisedMode:      c.SupervisedMode,
		AutoApproveCrew:     c.AutoApproveCrew,
		ParentChatID:        c.ParentChatID,
		CompactionWatermark: c.CompactionWatermark,
		OldestCheckpointTag: c.OldestCheckpointTag,
		Summary:             c.Summary,
	}
}

// ChatHeader is the metadata-only view of a Chat. Field order is driven
// by fieldalignment packing, not Chat's field order; both structs
// serialise to JSON independently so the visual mismatch is harmless.
type ChatHeader struct {
	ParentChatID        ChatID         `json:"parent_chat_id,omitempty"`
	Name                string         `json:"name"`
	Agent               string         `json:"agent,omitempty"`
	Model               string         `json:"model,omitempty"`
	ACPSessionID        string         `json:"acp_session_id,omitempty"`
	CurrentModeID       string         `json:"current_mode_id,omitempty"`
	ID                  string         `json:"id"`
	CompactionWatermark string         `json:"compaction_watermark,omitempty"`
	OldestCheckpointTag string         `json:"oldest_checkpoint_tag,omitempty"`
	Summary             string         `json:"summary,omitempty"`
	AvailableModels     []SessionModel `json:"available_models,omitempty"`
	AvailableModes      []SessionMode  `json:"available_modes,omitempty"`
	Usage               Usage          `json:"usage"`
	CreatedAt           int64          `json:"created_at"`
	UpdatedAt           int64          `json:"updated_at"`
	MessageCount        int            `json:"message_count"`
	SupervisedMode      bool           `json:"supervised_mode,omitempty"`
	AutoApproveCrew     bool           `json:"auto_approve_crew,omitempty"`
}
