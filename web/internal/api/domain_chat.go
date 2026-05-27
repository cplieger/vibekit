package api

// Chat domain types: session state, messages, tool calls, plans, usage,
// session modes/models, agent metadata. These are the persisted and
// over-the-wire shapes the store, hub, bridge, and push packages
// operate on.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// SecretMask is the placeholder value returned for every secret on
// public reads. Clients send this unchanged on update to keep the
// stored value; any other value replaces it.
const SecretMask = "***"

// DefaultChatName is the fallback name for newly created chats when the
// client does not supply one.
const DefaultChatName = "New conversation"

// MaxChatNameBytes caps the byte length of chat names at creation and
// rename boundaries. All code paths that set Chat.Name should enforce
// this limit.
const MaxChatNameBytes = 512

// NewChatID generates a fresh UUIDv7 chat identifier. Time-ordered,
// globally unique, standard format. Used by rewind_chat to create
// server-generated chat IDs (unlike the original tangent flow where
// the client generated the ID).
func NewChatID() ChatID {
	return ChatID(newUUIDv7())
}

func newUUIDv7() string { //nolint:gosec // G115: bit-shift truncation is intentional for UUIDv7 encoding
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:])
}

// ErrMsgUtilityUnavailable is the canonical error message returned when
// the utility bridge (LLM prompt function) is not wired. Used by both
// the git and server packages to keep the error string in one place.
const ErrMsgUtilityUnavailable = "utility bridge not available"

// ChatID is a typed wrapper for chat identifiers. The underlying string
// marshals identically to JSON, preserving the wire contract.
type ChatID string

// String implements fmt.Stringer for logging convenience.
func (c ChatID) String() string { return string(c) }

// SessionID is a typed wrapper for ACP session identifiers. Values are
// validated via ValidSessionID before assignment; the type makes
// invalid-state propagation a compile-time-visible decision.
type SessionID string

// Valid reports whether the session id passes path-safety validation.
func (s SessionID) Valid() bool { return ValidSessionID(string(s)) }

// String implements fmt.Stringer for logging convenience.
func (s SessionID) String() string { return string(s) }

// ModelID is a typed wrapper for model identifiers. Values are
// validated via ValidIdent before assignment; the type makes
// invalid-state propagation a compile-time-visible decision.
type ModelID string

// Valid reports whether the model id passes identifier validation.
// Empty is valid (the field is optional).
func (m ModelID) Valid() bool { return ValidIdent(string(m)) }

// String implements fmt.Stringer for logging convenience.
func (m ModelID) String() string { return string(m) }

// Role identifies the speaker of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleEvent     Role = "event" // system / ui events rendered inline
)

// EventKind identifies subtypes of RoleEvent messages.
type EventKind string

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

const (
	ACPUpdateAgentChunk   ACPUpdateKind = "agent_message_chunk"
	ACPUpdateThoughtChunk ACPUpdateKind = "agent_thought_chunk"
	ACPUpdateToolCall     ACPUpdateKind = "tool_call"
	ACPUpdateToolUpdate   ACPUpdateKind = "tool_call_update"
	ACPUpdatePlan         ACPUpdateKind = "plan"
	ACPUpdateModeChange   ACPUpdateKind = "current_mode_update"
	ACPUpdateSteering     ACPUpdateKind = "steering_inclusion"
)

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
// Enables "follow-along" features where the editor scrolls to the file
// the agent is accessing or modifying.
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
	Ts        int64       `json:"ts"`
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
	ID           string `json:"id"`
	Name         string `json:"name"`
	Agent        string `json:"agent,omitempty"`
	Model        string `json:"model,omitempty"`
	ACPSessionID string `json:"acp_session_id,omitempty"`
	// CurrentModeID tracks the mode the agent currently reports via
	// ACP's current_mode_update. Empty when the agent declares no
	// modes.
	CurrentModeID       string `json:"current_mode_id,omitempty"`
	ParentChatID        ChatID `json:"parent_chat_id,omitempty"`
	CompactionWatermark string `json:"compaction_watermark,omitempty"`
	// Summary is a one-line description of the chat generated when
	// the chat is archived. Populated asynchronously via the utility
	// bridge; remains empty for very short or utility-bridge-less
	// chats. Rendered under the title in the History tab.
	Summary string `json:"summary,omitempty"`
	// OldestCheckpointTag is populated at header-build time from the
	// checkpoint store (not persisted on disk — it's always derived).
	// Empty when the chat has no shadow repo yet.
	OldestCheckpointTag string         `json:"oldest_checkpoint_tag,omitempty"`
	AvailableModels     []SessionModel `json:"available_models,omitempty"`
	CurrentPlan         []PlanEntry    `json:"current_plan,omitempty"`
	AvailableModes      []SessionMode  `json:"available_modes,omitempty"`
	Messages            []Message      `json:"messages"`
	Usage               Usage          `json:"usage"`
	CreatedAt           int64          `json:"created_at"`
	UpdatedAt           int64          `json:"updated_at"`
	// SupervisedMode gates file-mutating agent operations: when true,
	// every fs/write_text_file and fs/delete_file arriving at the
	// bridge is staged in the hub's pending.Store and must be
	// user-accepted before it lands on disk. When false (the default),
	// writes apply immediately — the pre-Supervised behaviour.
	// Orthogonal to the kiro-cli --trust-* flag (which governs whether
	// the agent needs per-call approval before emitting the write at
	// all); Supervised gates the write AFTER kiro-cli has already
	// decided to emit it.
	SupervisedMode  bool `json:"supervised_mode,omitempty"`
	AutoApproveCrew bool `json:"auto_approve_crew,omitempty"`
	// RewindFromTurn records which turn index this chat was rewound
	// from. Zero when not a rewind chat. Used by the sidebar to show
	// "rewind from turn N" label.
	RewindFromTurn int `json:"rewind_from_turn,omitempty"`
	// MessageCount is the persisted message count (may differ from
	// len(Messages) during pagination).
	MessageCount int `json:"message_count"`
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
