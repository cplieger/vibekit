package vibekit

// Chat domain types: the persisted and over-the-wire chat shapes the store,
// agent, bridge and push packages operate on.

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
	// EventInfraSafetyBlocked marks an ENFORCE-mode Infrastructure-Safety refusal:
	// KAS blocked the tool call upstream, so nothing was written. Persisted as a
	// permanent inline event, and Content carries the violated safety properties.
	EventInfraSafetyBlocked EventKind = "infra_safety_blocked"
	// EventTurnOutcome is the CARRIER for a turn that emitted nothing: with no
	// assistant message there is nowhere else to stamp TurnOutcome. Persisted only
	// when no other marker carries the outcome and that outcome is not `completed`.
	EventTurnOutcome EventKind = "turn_outcome"
	// EventStepNotice is a message a workflow STEP sent into the chat that launched
	// its run, replayed off KAS's durable copy. That copy persists as a steer-source
	// user row, so without this kind it replays as a USER BUBBLE. Not dropped the way
	// a progress row is: it is the only durable copy of a question the ask registry
	// holds in memory.
	EventStepNotice EventKind = "step_notice"
)

// ToolKind identifies the category of a tool invocation. Values are assigned by
// kiro-cli and flow through the ACP protocol unchanged.
type ToolKind string

// ToolKindExecute and the following constants classify tool invocations. KAS v3
// emits only read/edit/delete/move/search/execute/think/fetch/switch_mode/other;
// the rest are retained because they back WorkingLabelForKind's label table and
// keep older persisted chat files renderable. Hook activity arrives as kind
// "other" tagged _meta.kiro.hookAsk, NOT ToolKindHook (see translate.ACPKiroMeta).
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

// ACPUpdateKind identifies the subtype of an ACP session/update notification.
type ACPUpdateKind string

// ACPUpdateAgentChunk and the following constants define the valid ACPUpdateKind values for ACP session notifications.
const (
	ACPUpdateAgentChunk   ACPUpdateKind = "agent_message_chunk"
	ACPUpdateThoughtChunk ACPUpdateKind = "agent_thought_chunk"
	ACPUpdateToolCall     ACPUpdateKind = "tool_call"
	ACPUpdateToolUpdate   ACPUpdateKind = "tool_call_update"
	ACPUpdatePlan         ACPUpdateKind = "plan"
	ACPUpdateModeChange   ACPUpdateKind = "current_mode_update"
	// ACPUpdateSessionInfo and the two below are v3 (KAS) sub-kinds: v3 moves
	// context-usage stats into session/update. available_commands_update arrives too
	// and is deliberately NOT decoded — the catalog has no consumer.
	ACPUpdateSessionInfo ACPUpdateKind = "session_info_update"
	// ACPUpdateConfigOption carries the live model/mode/effort catalog;
	// ACPUpdateUsage carries context-window usage. Both v3-only (KAS 2.12).
	ACPUpdateConfigOption ACPUpdateKind = "config_option_update"
	ACPUpdateUsage        ACPUpdateKind = "usage_update"
)

// BlockType discriminates content blocks in an assistant message's chronological
// content array. Mirrors Anthropic's `content_block.type` from the Messages API.
type BlockType string

const (
	// BlockText is a markdown text segment from the agent.
	BlockText BlockType = "text"
	// BlockToolUse is a tool invocation. Only the ToolCallID is in the block; the
	// full ToolCall lives in Message.ToolCalls, so a status update skips the array.
	BlockToolUse BlockType = "tool_use"
	// BlockThinking is an extended-thinking trace segment.
	BlockThinking BlockType = "thinking"
)

// Block is one entry in an assistant message's chronological content array.
// Within ONE agent's stream, position IS the order that agent emitted the block,
// so the client renders text and tools inline as they happened.
//
// ACROSS streams it is NOT a global chronology: a parent and its delegates share
// one array, and internal/buffer extends the newest block of the delta's OWN
// subtask. Order is per AgentSubtaskID. Messages persisted before this field
// existed have Blocks=nil, and renderers fall back to Content + ToolCalls.
type Block struct {
	// Type is the discriminator: text | tool_use | thinking.
	Type BlockType `json:"type"`
	// Text carries the markdown text for Type=BlockText, accumulated across chunks.
	Text string `json:"text,omitempty"`
	// Thinking carries the reasoning text for Type=BlockThinking.
	Thinking string `json:"thinking,omitempty"`
	// ToolCallID references a tool in Message.ToolCalls for Type=BlockToolUse.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// AgentSubtaskID is the subtask id of the agent that produced this block ("" =
	// top-level), from _meta.kiro.agentSubtaskId; lets the client render it nested.
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
}

// ToolCall is a tool invocation inside an assistant message. Each can be updated
// in place as status changes (pending → in_progress → completed/failed).
type ToolCall struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Kind   ToolKind   `json:"kind"`
	Status ToolStatus `json:"status"`
	Output string     `json:"output,omitempty"`
	// SubSessionID is the v2 subagent-session attribution (inert on v3).
	SubSessionID string `json:"sub_session_id,omitempty"`
	// AgentSubtaskID is set from a tool call's _meta.kiro.agentSubtaskId. On v3 a
	// subagent surfaces as an ordinary tool_call with _meta.kiro.kind agent-subtask;
	// this id links the card to its nested deltas, which carry the same id.
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	// WorkflowID names the run a `run_workflow` invocation started, from the terminal
	// update's `rawOutput.workflowId`. Empty on every other tool call, and on this one
	// until the run is created. It makes the invocation the RUN's card: the client
	// keys a run card on it, and a step's blocks arrive carrying the same id in their
	// `agent_subtask_id`, so the two sides join with no guessing.
	WorkflowID string `json:"workflow_id,omitempty"`
	// TerminalID links an execute tool call to the agent terminal running it, from
	// the ACP type:"terminal" content block. It makes the CARD the terminal's
	// rendering surface. Empty on every tool call that spawned no process.
	TerminalID string `json:"terminal_id,omitempty"`
	// Checkpoint is KAS's snapshot mapping for a tool call that wrote a file, from
	// _meta.kiro.checkpoint; nil when it touched no file. Ahead of the slices below
	// for govet fieldalignment: a trailing pointer would extend the GC scan region.
	Checkpoint *ToolCheckpoint `json:"checkpoint,omitempty"`
	// Disclosed names the skill or steering document a `disclose_context` call
	// loaded, from _meta.kiro.disclosedContext. The only signal that a skill's body
	// reached the model, so the transcript renders it, not a generic tool card.
	Disclosed *ToolDisclosed `json:"disclosed,omitempty"`
	// Denial is KAS's structured reason for a call the Cedar policy refused, from
	// _meta.kiro.policyDenial. Present so a refusal reads as a refusal rather than a
	// tool failure, and names the rule, since the user owns the policy.
	Denial *ToolDenial `json:"denial,omitempty"`
	// Truncated is what the STORE dropped to bound this call on disk, nil on every
	// call that fitted. Grouped with the pointers above for govet fieldalignment.
	Truncated *ToolTruncation `json:"truncated,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Locations []ToolLocation  `json:"locations,omitempty"`
	Diffs     []ToolDiff      `json:"diffs,omitempty"`
	// OutputSpans styles ranges of Output. Parsed once server-side by
	// internal/ansitext, so Output stays plain searchable text and the client never
	// builds HTML from agent-controlled bytes.
	OutputSpans []TextSpan `json:"output_spans,omitempty"`
	Ts          int64      `json:"ts"`
	DurationMs  int        `json:"duration_ms,omitempty"`
	// OutputBytes is the PERSISTED output's length and DiffCount the persisted
	// number of diffs: what the reveal will fetch. Set ONLY alongside HasFull; where
	// the store also cut the call, Truncated carries the size before THAT cut.
	OutputBytes int `json:"output_bytes,omitempty"`
	DiffCount   int `json:"diff_count,omitempty"`
	// HasFull says Input, Output and Diffs here are a PREVIEW, and the whole of what
	// the record kept is at GET /api/chats/{id}/tools/{id}. Set by the transcript
	// read path alone, because only a page load or scroll-up reads a preview.
	HasFull bool `json:"has_full,omitempty"`
}

// ToolTruncation is what the store DROPPED to bound what one tool call costs the
// record, each cut field carrying its size BEFORE the cut so a reader renders
// "truncated, N bytes" instead of showing less than happened. A zero field was
// not cut.
type ToolTruncation struct {
	// OutputBytes and InputBytes are each field's original length.
	OutputBytes int `json:"output_bytes,omitempty"`
	InputBytes  int `json:"input_bytes,omitempty"`
	// DiffBytes is the original diff total and DiffCount the original count. Diffs
	// are dropped WHOLE: a cut before/after pair would describe an edit nobody made.
	DiffBytes int `json:"diff_bytes,omitempty"`
	DiffCount int `json:"diff_count,omitempty"`
}

// ToolCallBulk is GET /api/chats/{id}/tools/{toolCallID}: the whole of one tool
// call's PERSISTED content, for a card whose preview said HasFull. Only the three
// fields the transcript previews; title, kind and status are on the card already.
type ToolCallBulk struct {
	// Strings before slices, and no field order here carries meaning: this is
	// betteralign's answer for the smallest GC scan region (govet fieldalignment).
	Output      string          `json:"output,omitempty"`
	ID          string          `json:"id"`
	Diffs       []ToolDiff      `json:"diffs,omitempty"`
	OutputSpans []TextSpan      `json:"output_spans,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
}

// TextSpan styles the half-open range [Start,End) of a sibling text field.
//
// It mirrors internal/ansitext.Span; the wire type lives here because
// internal/vibekit owns every shape codegen projects into TypeScript. Attrs values
// match web-terminal-engine's vt.WireRun.A, but the COLOUR encoding deliberately
// differs: a palette INDEX survives into a persisted chat file without baking
// today's theme into it, and the transcript's palette is CSS custom properties.
type TextSpan struct {
	// Start is the inclusive offset into the styled text, in UTF-16 CODE UNITS
	// rather than bytes, because the consumer indexes with JavaScript string
	// offsets. A byte offset would point mid-character on any box-drawing glyph.
	Start int `json:"start"`
	// End is the exclusive offset into the styled text, in UTF-16 code units.
	End int `json:"end"`
	// FG is the foreground colour: -1 for default, 0-255 for a palette index,
	// or 0x1000000|RGB for 24-bit colour.
	FG int32 `json:"fg"`
	// BG is the background colour, encoded like FG.
	BG int32 `json:"bg"`
	// Attrs is a bitfield: 1=bold, 2=italic, 4=underline, 8=inverse, 16=strike,
	// 32=dim, 64=hidden, 128=blink, 256=overline, 512=double-underline.
	Attrs uint16 `json:"attrs"`
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
// Rule is the load-bearing field: a denial that names its rule is one click from
// editing it. Scope and Source say WHERE the rule lives (user or workspace
// `permissions.yaml`).
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

// ToolCheckpoint is KAS's pre/post-image mapping for one file write, persisted
// verbatim so a diff is a snapshot read plus a file read with no derivation.
//
// Original and Modified are `kiro-snapshot-v2://…` URIs — opaque handles, NOT
// filesystem paths, deliberately stored unparsed; Local is a `file://` URI. All
// three are independently optional and a consumer must tolerate any subset
// (probed 2026-08-02, kiro-cli 2.16.0): a CREATE has no pre-image. Granularity is
// per-file-write, so multi-file attribution is not recoverable from this field.
type ToolCheckpoint struct {
	// Original is the pre-image snapshot URI. Empty on a file creation.
	Original string `json:"original,omitempty"`
	// Modified is the post-image snapshot URI.
	Modified string `json:"modified,omitempty"`
	// Local is the `file://` URI of the live file on disk.
	Local string `json:"local,omitempty"`
}

// ToolLocation is a file path (and optional line) the agent is working with, from
// kiro-cli's tool_call and tool_call_update notifications.
type ToolLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ToolDiff is a before/after text change from a write tool call. Path is
// workspace-relative (agent.relPath normalises kiro-cli's absolute paths).
//
// OldText/NewText carry the WHOLE FILE for KAS's edit tools, and a hunk pair is
// also accepted, so a consumer must DIFF the two sides rather than count their
// newlines: counting reported a whole file removed and re-added for a one-line
// edit. internal/buffer/linediff.go is the one line-delta primitive, twinned by
// diff.ts's lineDelta.
type ToolDiff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text"`
}

// CodeReference is one licensed-code attribution surfaced by the agent (v3/KAS
// _kiro/code_references), emitted when a completion reproduces a recognizable
// chunk of a referenced open-source file.
//
// The KAS ACP layer drops the recommendationContentSpan upstream, so there is no
// span to map a reference to a message region: attributions are turn-scoped.
type CodeReference struct {
	LicenseName string `json:"license_name"`
	Repository  string `json:"repository,omitempty"`
	URL         string `json:"url,omitempty"`
}

// RefusalInfo is the structured refusal metadata KAS attaches when the model
// declines to continue (modelStopReason "content_filtered"; kiro-cli 2.13+). The
// explanation text streams as ordinary assistant content, so only the
// classification fields are kept here. RecommendedModel, when set, names a model
// the service suggests switching to.
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

// Message is one entry in a chat transcript. Tool calls are embedded in assistant
// messages, not standalone; an event message carries an EventKind.
type Message struct {
	// ChangedFiles is part of the per-turn summary shown in the assistant turn's
	// footer, set on the final assistant message at turn_ended so the footer survives
	// reload. (Field order in this struct is fieldalignment-optimal, not logical.)
	ChangedFiles map[string]*FileChange `json:"changed_files,omitempty"`
	Role         Role                   `json:"role"`
	Content      string                 `json:"content,omitempty"`
	// Reasoning is the agent's thinking trace for this turn, persisted on the same
	// message so the one-message-per-turn invariant holds.
	Reasoning string    `json:"reasoning,omitempty"`
	EventKind EventKind `json:"event_kind,omitempty"`
	ID        string    `json:"id"`
	// TurnOutcome is how this turn ENDED, stamped on the message that finalized it —
	// the last assistant message, or an EventTurnOutcome marker when the turn emitted
	// nothing. Its presence is also what CLOSES a turn for the two turn projections:
	// the next message after it opens a new segment. A message persisted before this
	// field existed carries none, so those turns never close.
	TurnOutcome TurnOutcome `json:"turn_outcome,omitempty"`
	// TurnStopReasonRaw is the wire's stop reason verbatim. Kept because the enum is
	// OPEN — KAS exceeds ACP spec v1's closed union — so an unmeasured value survives
	// in the record. No consumer may branch on it; TurnOutcome is what they read.
	TurnStopReasonRaw StopReason `json:"turn_stop_reason_raw,omitempty"`
	// TurnModel is the model that answered this turn, stamped at turn_ended. It
	// belongs on the MESSAGE because the chat's Model is the CURRENT one: reading
	// that at render time would relabel every historical turn on a model switch.
	TurnModel string     `json:"turn_model,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Blocks is the chronologically-ordered content array, each block stamped with an
	// agent_subtask_id. The canonical render model: the client normalizes legacy
	// Content/ToolCalls into Blocks on replay so there is a single render path.
	Blocks []Block `json:"blocks,omitempty"`
	// CodeReferences carries licensed-code attributions the agent flagged during
	// this turn. Turn-scoped: the wire carries no span.
	CodeReferences []CodeReference `json:"code_references,omitempty"`
	// Refusal marks this assistant turn as a model refusal (kiro-cli 2.13 contract):
	// the message content IS the refusal explanation, and this carries the category
	// and recommended model the client's refusal callout renders.
	Refusal *RefusalInfo `json:"refusal,omitempty"`
	Plan    []PlanEntry  `json:"plan,omitempty"`
	// Attachments are the files the user attached to THIS prompt, stamped on the user
	// message so a sent turn can render them as pills. It has to live on the record:
	// BuildPromptBlocks folds each one into a content block on the way OUT, so by
	// read time nothing else recovers the list. Absent on a turn opened by a steer,
	// which takes a plain string and carries no structured attachment list.
	Attachments []Attachment `json:"attachments,omitempty"`
	// TurnCredits / TurnElapsedMs complete the turn footer alongside ChangedFiles.
	// omitempty drops the zero cases: a read-only turn has none.
	TurnCredits   float64 `json:"turn_credits,omitempty"`
	TurnElapsedMs float64 `json:"turn_elapsed_ms,omitempty"`
	Ts            int64   `json:"ts"`
	// TurnTruncated marks a turn the model stopped at a bound: it completed, and its
	// answer is cut off. Stored though derivable, so the Go and TypeScript
	// projections do not each re-implement the mapping. Last because it is the only
	// bool: a bool between the strings above pads them apart (fieldalignment).
	TurnTruncated bool `json:"turn_truncated,omitempty"`
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

// MeteringItem is one usage dimension from kiro-cli's meteringUsage array.
// UnitPlural is the canonical identifier ("credits", "tokens", "requests").
type MeteringItem struct {
	UnitSingular string  `json:"unit_singular"`
	UnitPlural   string  `json:"unit_plural"`
	Value        float64 `json:"value"`
}

// SessionMode describes one mode the running agent supports, from
// `modes.availableModes` on session/new or session/load, kept on the chat so the
// UI renders a mode pill without re-querying the bridge.
//
// On v3 (KAS) the list is unified: bundled workflow modes AND every workspace
// custom agent (.kiro/agents/*), each switchable via session/set_mode. Source
// distinguishes them so the picker can group built-ins above custom agents.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"` // "bundled" | "workspace" (v3 _meta.kiro.source)
}

// SessionModel describes one model the running agent can swap to, as declared by
// kiro-cli's session/new response.
type SessionModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// DefaultEffortLevel is the level this MODEL defaults to, from the model choice's
	// `_meta.kiro.defaultEffortLevel`. vibekit uses it for a chat with no session yet,
	// where no live level exists to read. NOT persisted onto Chat.Effort: seeding a
	// chat's CHOICE from a service default would pin it to every later session.
	DefaultEffortLevel string  `json:"default_effort_level,omitempty"`
	RateMultiplier     float64 `json:"rate_multiplier,omitempty"`
	// HasEffort reports whether this model supports a reasoning-effort level, from
	// `_meta.kiro.hasEffort`. kiro-cli 2.19.1 stamps it on every model choice (probed
	// 2026-08-25 against KAS 0.48.0): `auto` carries false, every real model true. So
	// a chat still on `auto` HIDES the effort row, which is correct — KAS builds no
	// effortLevel option for a model with no tiers and silently drops any level sent.
	// Chat.EffortLevels answers the same question from the other side.
	HasEffort bool `json:"has_effort,omitempty"`
}

// SessionEffortLevel is one reasoning-effort tier the running session offers,
// from the `effortLevel` config option's own `options[]`.
//
// The tiers are NOT a fixed five and NOT a per-model list: kiro-cli 2.18.0 builds
// its picker from this option and errors when the list is empty, so the list IS
// the capability. A tier absent here is a level the service rejects.
type SessionEffortLevel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Chat is the full persisted chat. Serialized as <dir>/<id>.json.
type Chat struct {
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	ACPSessionID  string `json:"acp_session_id,omitempty"`
	CurrentModeID string `json:"current_mode_id,omitempty"`
	// Effort is the chat's reasoning-effort level ("low".."max"), applied at
	// session/new through StartOpts.Effort and live through CmdSetEffort.
	//
	// A plain string, not EffortLevel: this is persisted state, so a value written
	// by a different build must decode rather than throw. The command boundary
	// validates with EffortLevel.Valid().
	Effort string `json:"effort,omitempty"`
	// Draft is the composer text typed but not sent, so switching chat tabs stops
	// bleeding one chat's half-written message into another. Server-side rather than
	// localStorage so it follows the user across devices.
	//
	// Deliberately NOT on ChatHeader: a debounced autosave would put the draft in a
	// chat_updated frame every 600ms of typing, which every client re-renders and
	// which races the caret of the tab that typed it. Retention rides the chat file,
	// so Store.SetDraft must not touch UpdatedAt, which the purge ages from.
	Draft               string `json:"draft,omitempty"`
	CompactionWatermark string `json:"compaction_watermark,omitempty"`
	ID                  string `json:"id"`
	// Attachments are the workspace paths staged beside the draft and not yet sent —
	// the DRAFT'S TWIN, saved on the same debounce, and absent from ChatHeader for
	// the same reason Draft is.
	//
	// Paths, not contents: the file is read at send time by BuildPromptBlocks, which
	// is also where the path is confined to the workspace. Storing bytes here would
	// put a 10 MiB image in the chat file for a prompt that may never be sent.
	// Store.SetAttachments keeps Draft's retention contract: no UpdatedAt stamp.
	Attachments []string `json:"attachments,omitempty"`
	// ServedModelIDs is every model id this chat's last session advertised,
	// UNFILTERED, unlike the picker's catalog, which drops end-of-life entries. The
	// `--model` launch flag is built BEFORE session/new returns a catalog, so at
	// spawn time the previous session's advertised set is the only evidence about
	// whether the stored model is still one the account can run. Empty means
	// unknowable, and ModelServed then allows the send.
	ServedModelIDs []string `json:"served_model_ids,omitempty"`
	// EffortLevels is the reasoning-effort vocabulary the last session advertised.
	// Per-chat rather than per-workspace because it is the vocabulary of THIS chat's
	// model: two chats on different models disagree about which tiers exist. EMPTY
	// means the current model has no tiers at all, which is how kiro-cli's own TUI
	// decides to refuse the command.
	EffortLevels []SessionEffortLevel `json:"effort_levels,omitempty"`
	// EffortActive is the level the session is RUNNING at, from that option's
	// `currentValue`. Distinct from Effort, which is what this chat CHOSE: a chat
	// that never picked has an empty Effort and still runs at a level.
	EffortActive string    `json:"effort_active,omitempty"`
	Messages     []Message `json:"messages"`
	// PriorACPSessionIDs are the KAS sessions this chat USED to run on, oldest
	// first, and a chat routinely changes session: a failed session/load blanks it,
	// a model switch fallback recreates it. Each of those sessions still holds that
	// period's transcript and pre-images on disk, so retention keys on the whole
	// CHAIN. Never trimmed: an entry here is a directory the reaper must spare.
	// Maintained by RecordSession.
	PriorACPSessionIDs []string `json:"prior_acp_session_ids,omitempty"`
	Usage              Usage    `json:"usage"`
	CreatedAt          int64    `json:"created_at"`
	UpdatedAt          int64    `json:"updated_at"`
	// A rewind reverts the chat it is in, so nothing here records a parent chat or
	// the turn a chat started at.
	MessageCount   int  `json:"message_count"`
	SupervisedMode bool `json:"supervised_mode,omitempty"`
}

// SessionChain returns every KAS session id this chat has run on, current one
// last. The reaper's keep-set: any directory in it holds part of the history.
func (c *Chat) SessionChain() []string {
	return sessionChain(c.ACPSessionID, c.PriorACPSessionIDs)
}

// ComposerState is the pair a chat's composer holds between sends: the text typed
// and not sent, and the files staged beside it.
//
// It exists so the two writers can report what landed — Store.SetDraft and
// Store.SetAttachments each write one half and the draft_changed broadcast needs
// both, where re-reading the chat would put a file read back on CmdSetDraft's hot
// path. Not persisted: the chat file holds the two fields directly.
type ComposerState struct {
	Text        string
	Attachments []string
}

// Composer returns the chat's current composer state.
func (c *Chat) Composer() ComposerState {
	return ComposerState{Text: c.Draft, Attachments: slices.Clone(c.Attachments)}
}

// sessionChain composes the current session id and the retired ones into the
// chat's full chain. Shared by Chat and ChatHeader so the two views cannot
// disagree about what a chat's retention set is.
//
// The returned slice is freshly allocated on EVERY branch: handing back
// PriorACPSessionIDs itself would let a caller's append rewrite the chat's own
// retention set, and copying on only one branch is worse than never copying.
func sessionChain(current string, prior []string) []string {
	chain := make([]string, 0, len(prior)+1)
	chain = append(chain, prior...)
	if current == "" {
		return chain
	}
	return append(chain, current)
}

// RecordSession points the chat at session id, retiring whatever it was on into
// the chain first. Pass "" to detach from the current session without forgetting
// it (a failed session/load). Idempotent: re-recording the current id, or one
// already in the chain, is a no-op.
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
		Effort:              c.Effort,
		EffortLevels:        c.EffortLevels,
		EffortActive:        c.EffortActive,
		Usage:               c.Usage,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
		MessageCount:        len(c.Messages),
		SupervisedMode:      c.SupervisedMode,
		CompactionWatermark: c.CompactionWatermark,
	}
}

// ChatHeader is the metadata-only view of a Chat. Field order is fieldalignment's,
// not Chat's; the two serialise independently, so the mismatch is harmless.
type ChatHeader struct {
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	ACPSessionID  string `json:"acp_session_id,omitempty"`
	CurrentModeID string `json:"current_mode_id,omitempty"`
	// Effort mirrors Chat's, because the effort control reads the ACTIVE chat's
	// level and an empty chat never fetches its full record, so the header is the
	// only path that reaches every chat. Chat.Draft is deliberately NOT mirrored.
	Effort string `json:"effort,omitempty"`
	// EffortActive + EffortLevels mirror Chat's, for the same reason Effort does:
	// the control renders from the ACTIVE chat's header.
	EffortActive string               `json:"effort_active,omitempty"`
	EffortLevels []SessionEffortLevel `json:"effort_levels,omitempty"`
	// The model and mode vocabulary is a WORKSPACE fact served once by
	// agent.Catalog, never mirrored per header.
	ID                  string `json:"id"`
	CompactionWatermark string `json:"compaction_watermark,omitempty"`
	// PriorACPSessionIDs mirrors Chat's, because the retention sweep derives its
	// keep-list from header reads rather than loading every chat in full.
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
// picker (GET /api/sessions). KAS owns the inventory and the transcript, so
// vibekit keeps no archive of its own. Field order is fieldalignment's.
type ResumableSession struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	AgentMode string `json:"agent_mode,omitempty"`
	// Status is KAS's own session status: idle | failed | waiting_on_user.
	Status string `json:"status,omitempty"`
	// Description is the agent's self-declared focus for that session, present
	// on a minority of rows (88 of 399 measured).
	Description string `json:"description,omitempty"`
	// ChatID names the vibekit chat that already owns this session, empty when none
	// does. A claimed session is one the user can simply open.
	ChatID    string `json:"chat_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// WorkflowRun is one previous workflow run, listed beside previous chats in the
// history surface (GET /api/sessions) and reviewable read-only.
//
// Sourced from _kiro/workflow/list, NOT session/list: session/list's workflow rows
// are STEP sessions whose status is idle regardless of the run's outcome, so they
// can be neither counted nor judged as runs.
type WorkflowRun struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name"`
	// Status is run-level: paused / completed / failed.
	Status string `json:"status,omitempty"`
	// ParentChatID is the vibekit chat that launched the run, resolved through the
	// launching session's chain. Empty for a run with no vibekit parent.
	ParentChatID string `json:"parent_chat_id,omitempty"`
	// EndReason says why a run STOPPED when something other than the run itself
	// decided that: "overran" (it blew a wall clock) or "step_cap" (a step blew its
	// turn cap). KAS's status cannot answer it, because both bounds terminate through
	// the same Cancel the button reaches, so the run reports `cancelled` either way.
	// A user cancel records NOTHING, so absence is the third value. Not persisted:
	// vibekit keeps it in memory for the runs it stopped in THIS process.
	EndReason string `json:"end_reason,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	CreatedAt int64  `json:"created_at,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
}
