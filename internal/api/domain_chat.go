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
	// v3 (KAS) session/update sub-kinds. v3 moves context-usage stats into
	// session/update instead of the v2 _kiro.dev/metadata notification. The
	// available_commands_update sub-kind arrives too and is deliberately NOT
	// decoded: unhandled sub-kinds fall through silently in handleSessionUpdate,
	// and the catalog has no consumer (see handlers/system.ts for why there is
	// no palette).
	ACPUpdateSessionInfo ACPUpdateKind = "session_info_update"
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
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	// Checkpoint is KAS's snapshot mapping for a tool call that wrote a
	// file, taken from _meta.kiro.checkpoint. Nil for every tool call that
	// touched no file — which is most of them. Placed ahead of the slices
	// below to keep govet fieldalignment happy: a trailing pointer would
	// extend the GC scan region past a slice's non-pointer len/cap words.
	Checkpoint *ToolCheckpoint `json:"checkpoint,omitempty"`
	// Disclosed names the skill or steering document a `disclose_context` call
	// loaded, from _meta.kiro.disclosedContext. Nil on every other tool call.
	// This is the only signal that a skill's body actually reached the model, so
	// it is what the transcript renders instead of a generic tool card.
	Disclosed *ToolDisclosed `json:"disclosed,omitempty"`
	// Denial is KAS's structured reason for a call the Cedar policy refused,
	// from _meta.kiro.policyDenial. Nil unless the policy denied it. Present so a
	// refusal reads as a refusal rather than a tool failure, and names the rule
	// responsible, since the user owns the policy.
	Denial     *ToolDenial     `json:"denial,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Locations  []ToolLocation  `json:"locations,omitempty"`
	Diffs      []ToolDiff      `json:"diffs,omitempty"`
	DurationMs int             `json:"duration_ms,omitempty"`
	Ts         int64           `json:"ts"`
}

// ToolDisclosed identifies a skill or steering document loaded into context by
// the agent's own `disclose_context` call. Type is "skill" or "steering".
type ToolDisclosed struct {
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	URI         string `json:"uri"`
}

// ToolDenial is the policy verdict that refused a tool call.
//
// Rule is the matched rule, and it is the load-bearing field: a denial that
// names its rule is one click from editing it, which is what makes "configure
// permissions however you like" actionable. Scope and Source say WHERE the rule
// lives (user or workspace `permissions.yaml`), so the user knows which file to
// open.
type ToolDenial struct {
	Rule       *ToolDenialRule `json:"rule,omitempty"`
	Capability string          `json:"capability"`
	Resource   string          `json:"resource"`
	Scope      string          `json:"scope"`
	Source     string          `json:"source"`
}

// ToolDenialRule is the Cedar rule that produced a denial. Effect is "deny" or
// "ask" (an unanswered ask that timed out reaches here as a denial).
type ToolDenialRule struct {
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	Match      []string `json:"match,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
}

// ToolCheckpoint is KAS's pre/post-image mapping for one file write,
// persisted verbatim so a diff is a snapshot read plus a file read with no
// derivation. vibekit captures no snapshots of its own; KAS does it
// unconditionally and hands the addresses over on the wire.
//
// Original and Modified are `kiro-snapshot-v2://<sessionId>:<snapshotId>/?originalPath=<relpath>`
// URIs — opaque handles, NOT filesystem paths, deliberately stored
// unparsed. Local is a `file://` URI for the live file.
//
// **All three fields are independently optional and a consumer must
// tolerate any subset** (probed 2026-08-02, kiro-cli 2.16.0): a file
// CREATE has no pre-image, so it arrives as {Modified, Local} with Original
// empty, while overwriting an existing file yields all three. Code that
// treats this as a fixed triplet breaks on the first file the agent
// creates.
//
// Granularity is per-file-write, not per-turn, and a multi-file tool
// (semantic_rename) carries no per-file mapping at all — KAS's
// checkpoint.fileChanges has zero occurrences in the corpus — so
// multi-file attribution is not recoverable from this field and must not
// be inferred from it.
type ToolCheckpoint struct {
	// Original is the pre-image snapshot URI. Empty on a file creation.
	Original string `json:"original,omitempty"`
	// Modified is the post-image snapshot URI.
	Modified string `json:"modified,omitempty"`
	// Local is the `file://` URI of the live file on disk.
	Local string `json:"local,omitempty"`
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
	Reasoning string     `json:"reasoning,omitempty"`
	EventKind EventKind  `json:"event_kind,omitempty"`
	ID        string     `json:"id"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
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
	CompactionWatermark string         `json:"compaction_watermark,omitempty"`
	ID                  string         `json:"id"`
	AvailableModels     []SessionModel `json:"available_models,omitempty"`
	// ServedModelIDs is every model id this chat's last session advertised,
	// UNFILTERED, which AvailableModels is not (it drops end-of-life entries for
	// the picker). It is persisted for one reason: the `--model` launch flag is
	// built BEFORE session/new returns a catalog, so at spawn time the previous
	// session's advertised set is the only evidence about whether the stored model
	// is still one the account can run. Empty means unknowable, and ModelServed
	// then allows the send. Deliberately NOT on ChatHeader: the client validates
	// nothing, it renders AvailableModels.
	ServedModelIDs []string      `json:"served_model_ids,omitempty"`
	AvailableModes []SessionMode `json:"available_modes,omitempty"`
	Messages       []Message     `json:"messages"`
	CurrentPlan    []PlanEntry   `json:"current_plan,omitempty"`
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
	// There is no ParentChatID and no RewindFromTurn. Both described a rewind
	// BRANCH — a second chat truncated at a turn, pointing back at the chat it
	// came from. A rewind reverts the chat it is in now, so nothing has a parent
	// and no chat records which turn it started at. (api.WorkflowRun keeps its
	// own ParentChatID; that one names the chat that LAUNCHED a run and is
	// unrelated.)
	MessageCount   int  `json:"message_count"`
	SupervisedMode bool `json:"supervised_mode,omitempty"`
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
		CompactionWatermark: c.CompactionWatermark,
	}
}

// ChatHeader is the metadata-only view of a Chat. Field order is driven
// by fieldalignment packing, not Chat's field order; both structs
// serialise to JSON independently so the visual mismatch is harmless.
type ChatHeader struct {
	Name                string         `json:"name"`
	Model               string         `json:"model,omitempty"`
	ACPSessionID        string         `json:"acp_session_id,omitempty"`
	CurrentModeID       string         `json:"current_mode_id,omitempty"`
	ID                  string         `json:"id"`
	CompactionWatermark string         `json:"compaction_watermark,omitempty"`
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

// ResumableSession is one stored KAS session offered by the previous-session
// picker (GET /api/sessions). Adopts kiro-cli's own `--resume-picker`
// capability: KAS owns the inventory and the transcript, so vibekit carries no
// archive of its own. See hub/session_list.go for the wire provenance.
//
// Field order is fieldalignment's, not the JSON's.
type ResumableSession struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	AgentMode string `json:"agent_mode,omitempty"`
	// Status is KAS's own session status: idle | failed | waiting_on_user.
	Status string `json:"status,omitempty"`
	// Description is the agent's self-declared focus for that session, present
	// on a minority of rows (88 of 399 measured).
	Description string `json:"description,omitempty"`
	// ChatID names the vibekit chat that already owns this session, empty when
	// no chat does. A claimed session is one the user can simply open, so the
	// picker offers it differently rather than duplicating the chat.
	ChatID    string `json:"chat_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// WorkflowRun is one previous workflow run, listed beside previous chats in
// the history surface (GET /api/sessions) and reviewable read-only.
//
// Sourced from _kiro/workflow/list, NOT from session/list. session/list's
// workflow rows are STEP sessions — measured 93 of them across 6 runs, with
// one run's loop contributing 76 — and their status is idle regardless of the
// run's outcome, so they can be neither counted nor judged as runs.
type WorkflowRun struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name"`
	// Status is run-level: paused / completed / failed.
	Status string `json:"status,omitempty"`
	// ParentChatID is the vibekit chat that launched the run, resolved through
	// the launching session's chain. Empty for a run with no vibekit parent
	// (launched from the TUI, or by a chat vibekit no longer keeps).
	ParentChatID string `json:"parent_chat_id,omitempty"`
	UpdatedAt    int64  `json:"updated_at"`
	CreatedAt    int64  `json:"created_at,omitempty"`
	StartedAt    int64  `json:"started_at,omitempty"`
}
