package api

// Chat domain types: session state, messages, tool calls, plans, usage,
// session modes/models. These are the persisted and over-the-wire shapes
// the store, hub, bridge, and push packages operate on.

import (
	"encoding/json"
	"slices"
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
	EventCompactFailed EventKind = "compaction_failed"
	// EventInfraSafetyBlocked marks an ENFORCE-mode Infrastructure-Safety
	// refusal: KAS blocked an infra-as-code write/shell tool call upstream
	// (it never issued the fs/write_text_file request, so nothing was
	// written). Persisted as a permanent inline event so the refusal is part
	// of the transcript rather than a fleeting banner. Content carries the
	// violated safety properties. See translate/safety.go.
	EventInfraSafetyBlocked EventKind = "infra_safety_blocked"
)

// ToolKind identifies the category of a tool invocation. Values are
// assigned by kiro-cli and flow through the ACP protocol unchanged.
// The full set spans both the original ACP kinds and the richer
// classifications kiro-cli's translate layer emits for permission
// dialogs and tool-card rendering.
type ToolKind string

// ToolKindExecute and the following constants define the valid ToolKind
// values for classifying tool invocations. KAS v3's zToolKind enum emits
// only read/edit/delete/move/search/execute/think/fetch/switch_mode/other.
// The remaining kinds (shell, hook, write, command, browser, mcp) are never
// emitted on v3 but are retained: they still back WorkingLabelForKind's
// label table and keep older persisted chat files that recorded them
// renderable. Hook activity in particular arrives as kind "other" tagged
// _meta.kiro.hookAsk, NOT ToolKindHook (see translate.ACPKiroMeta).
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
	// v3 (KAS) session/update sub-kinds. v3 moves context-usage stats and
	// the available-command catalog into session/update instead of the v2
	// _kiro.dev/metadata and _kiro.dev/commands/available notifications.
	ACPUpdateSessionInfo       ACPUpdateKind = "session_info_update"
	ACPUpdateCommandsAvailable ACPUpdateKind = "available_commands_update"
	// config_option_update carries the live model/mode/effort catalog;
	// usage_update carries context-window usage ({size, used, cost?}).
	// Both are v3-only session/update sub-kinds (KAS 2.12).
	ACPUpdateConfigOption ACPUpdateKind = "config_option_update"
	ACPUpdateUsage        ACPUpdateKind = "usage_update"
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
	// AgentSubtaskID is the subtask id of the agent that produced this
	// block ("" = top-level agent). Set from the emitting event's
	// _meta.kiro.agentSubtaskId; lets the client group a subagent's
	// blocks and render them nested.
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
}

// ToolCall is a tool invocation inside an assistant message. One assistant
// message may have multiple tool calls; each can be updated in place as
// status changes (pending → in_progress → completed/failed).
type ToolCall struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Kind   ToolKind   `json:"kind"`
	Status ToolStatus `json:"status"`
	Output string     `json:"output,omitempty"`
	// SubSessionID is the v2 subagent-session attribution (inert on v3;
	// all subagent updates ride the parent session id there).
	SubSessionID string `json:"sub_session_id,omitempty"`
	// AgentSubtaskID is set from a tool call's _meta.kiro.agentSubtaskId.
	// On v3 (KAS) a subagent surfaces as an ordinary tool_call with
	// _meta.kiro.kind=="agent-subtask"; this id links the subagent card
	// to its nested agent_message_chunk / agent_thought_chunk deltas
	// (which carry the same id) so the client can render them nested.
	AgentSubtaskID string          `json:"agent_subtask_id,omitempty"`
	Input          json.RawMessage `json:"input,omitempty"`
	Locations      []ToolLocation  `json:"locations,omitempty"`
	Diffs          []ToolDiff      `json:"diffs,omitempty"`
	DurationMs     int             `json:"duration_ms,omitempty"`
	Ts             int64           `json:"ts"`
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

// CodeReference is one licensed-code attribution surfaced by the agent
// (v3/KAS _kiro/code_references). KAS emits it when a completion reproduces
// a recognizable chunk of a referenced open-source file and the account's
// code-reference tracker is enabled. The KAS ACP layer maps every reference
// down to these three fields (licenseName + repository + url); the raw
// CodeWhisperer recommendationContentSpan and information fields are dropped
// upstream before we ever see them, so there is no span to map a reference
// to a specific message region — attributions are turn-scoped and persisted
// on the assistant Message they arrived during.
type CodeReference struct {
	LicenseName string `json:"license_name"`
	Repository  string `json:"repository,omitempty"`
	URL         string `json:"url,omitempty"`
}

// RefusalInfo is the structured refusal metadata KAS attaches when the model
// declines to continue a conversation (modelStopReason "content_filtered";
// kiro-cli 2.13+). It rides the refusal explanation chunk's update-level
// _meta.kiro.refusal and the turn ends with stopReason "refusal". The
// explanation text itself streams as ordinary assistant content, so only the
// classification fields are kept here; persisted on the assistant Message so
// the refusal callout survives reload. RecommendedModel, when set, names a
// model the service suggests switching to.
type RefusalInfo struct {
	Category         string `json:"category,omitempty"`
	RecommendedModel string `json:"recommended_model,omitempty"`
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

// Message is one entry in a chat transcript. Tool calls are embedded in
// assistant messages (not standalone messages). Event messages carry an
// EventKind for inline rendering (compression, cancellation, restart).
type Message struct {
	// ChangedFiles is part of the per-turn summary (credits · elapsed · files
	// changed) shown in the assistant turn's footer, set on the final assistant
	// message at turn_ended so the footer survives reload. It was previously an
	// un-keyed direct DOM write in the client, which double-rendered on SSE
	// replay and vanished on refresh. Mirrors the turn_ended SSE payload shape.
	// (Field order in this struct is govet-fieldalignment-optimal, not logical.)
	ChangedFiles map[string]*FileChange `json:"changed_files,omitempty"`
	Role         Role                   `json:"role"`
	Content      string                 `json:"content,omitempty"`
	// Reasoning is the agent's "thinking" trace for this turn —
	// extended-thinking models emit it as a parallel stream alongside
	// Content. Persisted on the same message so the one-message-per-turn
	// invariant holds; rendered above the content bubble in the UI.
	Reasoning string `json:"reasoning,omitempty"`
	// CheckpointTag is the REAL checkpoint tag the server allocated for
	// this turn (set only on the user message that started a turn whose
	// agent produced at least one file snapshot). It is the turn-canonical
	// tag ("N", never "N.K") the client passes verbatim to restore_checkpoint
	// / undo_edit. Empty when the turn produced no snapshot; persisted so it
	// survives reload.
	CheckpointTag string     `json:"checkpoint_tag,omitempty"`
	EventKind     EventKind  `json:"event_kind,omitempty"`
	ID            string     `json:"id"`
	ToolCalls     []ToolCall `json:"tool_calls,omitempty"`
	// Blocks is the chronologically-ordered content array — text / tool_use /
	// thinking blocks in the order the agent emitted them, each stamped with an
	// agent_subtask_id (empty for the parent agent, set for a subagent). It is
	// the canonical render model; the client normalizes legacy Content/ToolCalls
	// into Blocks on replay so there is a single render path.
	Blocks []Block `json:"blocks,omitempty"`
	// CodeReferences carries licensed-code attributions the agent flagged
	// during this turn (v3/KAS _kiro/code_references). Turn-scoped: the wire
	// carries no span, so it annotates the whole assistant turn. Persisted here
	// so the chip survives reload.
	CodeReferences []CodeReference `json:"code_references,omitempty"`
	// Refusal marks this assistant turn as a model refusal (kiro-cli 2.13
	// contract): the message content IS the refusal explanation, and this
	// carries the category + recommended-model metadata the client uses to
	// render the distinct refusal callout (chip + rewind / switch-model CTAs).
	Refusal *RefusalInfo `json:"refusal,omitempty"`
	Plan    []PlanEntry  `json:"plan,omitempty"`
	// TurnCredits / TurnElapsedMs complete the turn footer summary alongside
	// ChangedFiles (above). The values also ride the turn_ended SSE for the
	// live render; omitempty drops the zero cases (a read-only turn has none).
	TurnCredits   float64 `json:"turn_credits,omitempty"`
	TurnElapsedMs float64 `json:"turn_elapsed_ms,omitempty"`
	Ts            int64   `json:"ts"`
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
//
// On v3 (KAS) the availableModes list is unified: it carries both the
// bundled workflow modes (vibe/spec/quick-spec/bug-fix/plan/autonomous)
// AND every workspace custom agent (.kiro/agents/*), each switchable via
// session/set_mode. Source distinguishes them ("bundled" vs "workspace")
// so the picker can group built-in modes above custom agents.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"` // "bundled" | "workspace" (v3 _meta.kiro.source)
}

// SessionModel describes one model the running agent can swap to, as
// declared by kiro-cli's session/new response. Replaces our prior
// shell-out to `kiro-cli chat --list-models`.
type SessionModel struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description,omitempty"`
	RateMultiplier float64 `json:"rate_multiplier,omitempty"`
	// HasEffort reports whether this model supports a reasoning-effort level.
	// KAS's config_option_update stamps _meta.kiro.hasEffort on each model
	// choice (true when the model has effort levels). The model picker hides
	// the effort row for the current model when the catalog carries
	// has_effort:true on some model but not the current one; a catalog with no
	// has_effort anywhere (e.g. the pre-session REST list) safely shows it. A
	// non-effort model omits the field (client reads it as undefined), which is
	// why the client's gate keys off "any model advertises effort" rather than
	// a per-model false.
	HasEffort bool `json:"has_effort,omitempty"`
}

// Chat is the full persisted chat. Serialized as <dir>/<id>.json.
type Chat struct {
	Name                string         `json:"name"`
	Model               string         `json:"model,omitempty"`
	ACPSessionID        string         `json:"acp_session_id,omitempty"`
	CurrentModeID       string         `json:"current_mode_id,omitempty"`
	ParentChatID        ChatID         `json:"parent_chat_id,omitempty"`
	CompactionWatermark string         `json:"compaction_watermark,omitempty"`
	Summary             string         `json:"summary,omitempty"`
	OldestCheckpointTag string         `json:"oldest_checkpoint_tag,omitempty"`
	ID                  string         `json:"id"`
	AvailableModels     []SessionModel `json:"available_models,omitempty"`
	AvailableModes      []SessionMode  `json:"available_modes,omitempty"`
	Messages            []Message      `json:"messages"`
	CurrentPlan         []PlanEntry    `json:"current_plan,omitempty"`
	// PriorACPSessionIDs are the KAS sessions this chat USED to run on,
	// oldest first. ACPSessionID is only the current one, and a chat
	// routinely changes session: a failed session/load blanks it, a model
	// switch fallback recreates it. Each of those sessions still holds that
	// period's transcript and pre-images on disk, so retention has to key on
	// the whole CHAIN — a chat's data is its vibekit file plus every session
	// directory in its chain, and they live and die together.
	//
	// Never trimmed: an entry here is a directory the reaper must spare, so
	// dropping one deletes history. Maintained by RecordSession.
	PriorACPSessionIDs []string `json:"prior_acp_session_ids,omitempty"`
	Usage              Usage    `json:"usage"`
	CreatedAt          int64    `json:"created_at"`
	UpdatedAt          int64    `json:"updated_at"`
	// ArchivedAt is the unix-milli timestamp recorded when the chat was
	// moved to the archive dir. Purge ages from this, NOT the file mtime:
	// a skipped/failed post-archive summary write leaves mtime at the
	// chat's last-activity time, which would purge an old-but-just-archived
	// chat almost immediately. Zero for active chats and for legacy archives
	// written before this field existed (purge falls back to mtime then).
	ArchivedAt     int64 `json:"archived_at,omitempty"`
	RewindFromTurn int   `json:"rewind_from_turn,omitempty"`
	MessageCount   int   `json:"message_count"`
	SupervisedMode bool  `json:"supervised_mode,omitempty"`
}

// SessionChain returns every KAS session id this chat has run on, current
// one last. This is the reaper's keep-set for the chat: any session
// directory in it holds part of the chat's history.
func (c *Chat) SessionChain() []string {
	return sessionChain(c.ACPSessionID, c.PriorACPSessionIDs)
}

// sessionChain composes the current session id and the retired ones into the
// chat's full chain. Shared by Chat and ChatHeader so the two views cannot
// disagree about what a chat's retention set is.
func sessionChain(current string, prior []string) []string {
	if current == "" {
		return prior
	}
	chain := make([]string, 0, len(prior)+1)
	chain = append(chain, prior...)
	return append(chain, current)
}

// RecordSession points the chat at session id, retiring whatever it was on
// into the chain first. Pass "" to detach from the current session without
// forgetting it (a failed session/load), which is the case that used to lose
// the id outright.
//
// Idempotent: re-recording the current id, or an id already in the chain, is
// a no-op, so a caller does not have to check first.
func (c *Chat) RecordSession(id string) {
	if c.ACPSessionID == id {
		return
	}
	if c.ACPSessionID != "" && !slices.Contains(c.PriorACPSessionIDs, c.ACPSessionID) {
		c.PriorACPSessionIDs = append(c.PriorACPSessionIDs, c.ACPSessionID)
	}
	c.ACPSessionID = id
	// A revisited id lives in exactly one place: the current field.
	if id != "" {
		c.PriorACPSessionIDs = slices.DeleteFunc(c.PriorACPSessionIDs, func(s string) bool { return s == id })
	}
}

// Header returns the chat's metadata without messages. Used for list
// endpoints and SSE broadcasts when messages are not needed.
func (c *Chat) Header() ChatHeader {
	return ChatHeader{
		ID:                  c.ID,
		Name:                c.Name,
		Model:               c.Model,
		ACPSessionID:        c.ACPSessionID,
		PriorACPSessionIDs:  c.PriorACPSessionIDs,
		CurrentModeID:       c.CurrentModeID,
		AvailableModes:      c.AvailableModes,
		AvailableModels:     c.AvailableModels,
		Usage:               c.Usage,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
		MessageCount:        len(c.Messages),
		SupervisedMode:      c.SupervisedMode,
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
	Model               string         `json:"model,omitempty"`
	ACPSessionID        string         `json:"acp_session_id,omitempty"`
	CurrentModeID       string         `json:"current_mode_id,omitempty"`
	ID                  string         `json:"id"`
	CompactionWatermark string         `json:"compaction_watermark,omitempty"`
	OldestCheckpointTag string         `json:"oldest_checkpoint_tag,omitempty"`
	Summary             string         `json:"summary,omitempty"`
	AvailableModels     []SessionModel `json:"available_models,omitempty"`
	AvailableModes      []SessionMode  `json:"available_modes,omitempty"`
	// PriorACPSessionIDs mirrors Chat's. Carried on the header because the
	// retention sweep derives its keep-list from header reads rather than
	// loading every chat in full.
	PriorACPSessionIDs []string `json:"prior_acp_session_ids,omitempty"`
	Usage              Usage    `json:"usage"`
	CreatedAt          int64    `json:"created_at"`
	UpdatedAt          int64    `json:"updated_at"`
	MessageCount       int      `json:"message_count"`
	SupervisedMode     bool     `json:"supervised_mode,omitempty"`
}

// SessionChain returns every KAS session id the chat has run on, current one
// last. Same set as Chat.SessionChain.
func (h *ChatHeader) SessionChain() []string {
	return sessionChain(h.ACPSessionID, h.PriorACPSessionIDs)
}
