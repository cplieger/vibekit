package vibekit

// Chat domain types: the persisted and over-the-wire shapes for session state,
// messages, tool calls, plans, usage and session modes/models.

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
	// KAS blocked the tool call upstream, so nothing was written. Persisted rather
	// than bannered so the refusal is part of the transcript; Content carries the
	// violated safety properties.
	EventInfraSafetyBlocked EventKind = "infra_safety_blocked"
	// EventTurnOutcome is the CARRIER for a turn that emitted nothing: with no
	// assistant message there is nowhere else to stamp TurnOutcome, so a failed or
	// refused empty turn would read `completed` on reload. Persisted only when no
	// other marker carries the outcome and only when it is not `completed`, since a
	// row that changes no reading is one the transcript does not need.
	EventTurnOutcome EventKind = "turn_outcome"
	// EventStepNotice is a message a workflow STEP sent into its launching chat.
	// KAS keeps it as `{type:"user", source:"steer"}`, which without this kind
	// replays as a user bubble — the transcript then claims the reader typed the
	// step's own question. Not dropped like a workflow-progress row: that row is
	// machine state, this is the only durable copy of the question.
	EventStepNotice EventKind = "step_notice"
)

// ToolKind identifies the category of a tool invocation, assigned by kiro-cli and
// flowing through ACP unchanged.
type ToolKind string

// ToolKindExecute and the following constants define the valid ToolKind values.
// KAS v3 emits only read/edit/delete/move/search/execute/think/fetch/switch_mode/
// other; the rest are retained because they back WorkingLabelForKind's label table
// and keep older persisted chat files renderable. Hook activity arrives as kind
// "other" tagged _meta.kiro.hookAsk, NOT ToolKindHook.
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
	// ACPUpdateSessionInfo and the two below are v3 (KAS) sub-kinds.
	// available_commands_update arrives too and is deliberately NOT decoded: an
	// unhandled sub-kind falls through handleSessionUpdate silently, and the
	// slash-command catalog has no consumer.
	ACPUpdateSessionInfo ACPUpdateKind = "session_info_update"
	// ACPUpdateConfigOption carries the live model/mode/effort catalog;
	// ACPUpdateUsage carries context-window usage. Both v3-only.
	ACPUpdateConfigOption ACPUpdateKind = "config_option_update"
	ACPUpdateUsage        ACPUpdateKind = "usage_update"
)

// BlockType discriminates content blocks in an assistant message's chronological
// content array, mirroring Anthropic's `content_block.type` so text, tool calls and
// thinking traces render inline as the agent emits them.
type BlockType string

const (
	// BlockText is a markdown text segment from the agent.
	BlockText BlockType = "text"
	// BlockToolUse is a tool invocation. Only the ToolCallID is here; the full
	// ToolCall lives in Message.ToolCalls, so a status update touches no block.
	BlockToolUse BlockType = "tool_use"
	// BlockThinking is an extended-thinking trace segment.
	BlockThinking BlockType = "thinking"
)

// Block is one entry in an assistant message's chronological content array. Within
// ONE agent's stream, position IS emission order, so the client renders inline.
//
// ACROSS streams it is NOT a global chronology: a parent and its delegates share
// one array and internal/buffer extends the newest block of the delta's OWN
// subtask, so a parent delta can land BEHIND a delegate's block. Order is per
// AgentSubtaskID; Content carries arrival order. Nil on a message persisted
// before the field existed, where a renderer falls back to Content + ToolCalls.
type Block struct {
	// Type is the discriminator: text | tool_use | thinking.
	Type BlockType `json:"type"`
	// Text is the markdown for Type=BlockText, accumulated across the
	// MessageChunkPayload events targeting this block index.
	Text string `json:"text,omitempty"`
	// Thinking carries the reasoning text for Type=BlockThinking.
	Thinking string `json:"thinking,omitempty"`
	// ToolCallID references a Message.ToolCalls entry for Type=BlockToolUse.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// AgentSubtaskID is the subtask id of the agent that produced this block, ""
	// for the top-level one. It is what lets the client nest a subagent's blocks.
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
	// AgentSubtaskID is from a tool call's _meta.kiro.agentSubtaskId. On v3 a
	// subagent surfaces as an ordinary tool_call, and this id is what links its
	// card to the nested deltas carrying the same id.
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	// WorkflowID names the run a `run_workflow` invocation started, from the
	// terminal update's `rawOutput.workflowId`; empty until the run is created and
	// on every other tool call. It makes the invocation the RUN's card — a step's
	// blocks carry the same id in `agent_subtask_id`, so the two join with no
	// accumulation — and is the handle for `/run/{id}`.
	WorkflowID string `json:"workflow_id,omitempty"`
	// TerminalID links an execute tool call to the agent terminal running it, which
	// is what lets the CARD be that terminal's rendering surface. Empty on every
	// tool call that spawned no process.
	TerminalID string `json:"terminal_id,omitempty"`
	// Checkpoint is KAS's snapshot mapping for a tool call that wrote a file, nil
	// for the majority that touched none. Ahead of the slices below for govet
	// fieldalignment: a trailing pointer would extend the GC scan region past a
	// slice's non-pointer len/cap words.
	Checkpoint *ToolCheckpoint `json:"checkpoint,omitempty"`
	// Disclosed names the skill or steering document a `disclose_context` call
	// loaded. The only signal that a skill's body reached the model, which is why
	// the transcript renders it instead of a generic tool card.
	Disclosed *ToolDisclosed `json:"disclosed,omitempty"`
	// Denial is KAS's structured reason for a call the Cedar policy refused, nil
	// unless it did. Present so a refusal reads as a refusal rather than a tool
	// failure, and names the rule responsible, since the user owns the policy.
	Denial    *ToolDenial     `json:"denial,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Locations []ToolLocation  `json:"locations,omitempty"`
	Diffs     []ToolDiff      `json:"diffs,omitempty"`
	// OutputSpans styles ranges of Output, parsed once server-side so Output stays
	// plain searchable text and the client never builds HTML from agent bytes.
	// Empty for the ~99.75% of real command outputs carrying no escape.
	OutputSpans []TextSpan `json:"output_spans,omitempty"`
	DurationMs  int        `json:"duration_ms,omitempty"`
	Ts          int64      `json:"ts"`
}

// TextSpan styles the half-open range [Start,End) of a sibling text field. It
// mirrors internal/ansitext.Span, and the wire type lives here because that package
// stays a stdlib-only leaf that knows nothing about the wire.
//
// Attrs matches web-terminal-engine's vt.WireRun.A so both renderers share one
// attribute vocabulary. The COLOUR encoding deliberately differs: a palette INDEX
// survives into a persisted chat file without baking today's theme into it.
type TextSpan struct {
	// Start is the inclusive offset in UTF-16 CODE UNITS, not bytes, because the
	// consumer indexes with JavaScript string offsets: a byte offset would point
	// mid-character the moment output carried a box-drawing glyph or accented name.
	Start int `json:"start"`
	// End is the exclusive offset into the styled text, in UTF-16 code units.
	End int `json:"end"`
	// FG is the foreground colour: -1 default, 0-255 a palette index, or
	// 0x1000000|RGB for 24-bit.
	FG int32 `json:"fg"`
	// BG is the background colour, encoded like FG.
	BG int32 `json:"bg"`
	// Attrs is a bitfield: 1 bold, 2 italic, 4 underline, 8 inverse, 16 strike,
	// 32 dim, 64 hidden, 128 blink, 256 overline, 512 double-underline.
	Attrs uint16 `json:"attrs"`
}

// ToolDisclosed identifies a skill or steering document loaded into context by
// the agent's own `disclose_context` call. Type is "skill" or "steering".
type ToolDisclosed struct {
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	URI         string `json:"uri"`
}

// ToolDenial is the policy verdict that refused a tool call. Rule is the
// load-bearing field — a denial naming its rule is one click from editing it — and
// Scope plus Source say which `permissions.yaml` to open.
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
// verbatim so a diff is a snapshot read plus a file read. Original and Modified are
// opaque `kiro-snapshot-v2://` handles, NOT filesystem paths, deliberately unparsed.
//
// ALL THREE FIELDS ARE INDEPENDENTLY OPTIONAL and a consumer must tolerate any
// subset: a file CREATE has no pre-image, so code treating this as a fixed triplet
// breaks on the first file the agent creates. Granularity is per-file-write, so
// multi-file attribution must not be inferred from it.
type ToolCheckpoint struct {
	// Original is the pre-image snapshot URI. Empty on a file creation.
	Original string `json:"original,omitempty"`
	// Modified is the post-image snapshot URI.
	Modified string `json:"modified,omitempty"`
	// Local is the `file://` URI of the live file on disk.
	Local string `json:"local,omitempty"`
}

// ToolLocation is a file path, and optional line, the agent is working with. The
// editor scrolls to it.
type ToolLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ToolDiff is a before/after text change from a write tool call. Path is
// workspace-relative; agent.relPath normalises it on the way in.
//
// OldText/NewText carry the WHOLE FILE for KAS's edit tools (325 of 413 persisted
// fragments measured whole-file shaped), and a hunk pair is also accepted, so a
// consumer must DIFF the two sides rather than count newlines — counting reported a
// one-line edit as the entire file removed and re-added.
type ToolDiff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text"`
}

// CodeReference is one licensed-code attribution, emitted when a completion
// reproduces a recognizable chunk of a referenced open-source file.
//
// KAS drops CodeWhisperer's recommendationContentSpan upstream, so there is no span
// to map a reference to a message region: attributions are TURN-scoped.
type CodeReference struct {
	LicenseName string `json:"license_name"`
	Repository  string `json:"repository,omitempty"`
	URL         string `json:"url,omitempty"`
}

// RefusalInfo is the refusal metadata KAS attaches when the model declines to
// continue a conversation, and the turn then ends with stopReason "refusal". The
// explanation streams as ordinary assistant content, so only the classification is
// kept here; persisted so the callout survives reload.
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
	// ChangedFiles is part of the per-turn footer summary, set on the final assistant
	// message at turn_ended so the footer survives reload. Field order in this struct
	// is govet-fieldalignment-optimal, not logical.
	ChangedFiles map[string]*FileChange `json:"changed_files,omitempty"`
	Role         Role                   `json:"role"`
	Content      string                 `json:"content,omitempty"`
	// Reasoning is the agent's thinking trace, a parallel stream alongside Content.
	// On the same message so the one-message-per-turn invariant holds.
	Reasoning string    `json:"reasoning,omitempty"`
	EventKind EventKind `json:"event_kind,omitempty"`
	ID        string    `json:"id"`
	// TurnOutcome is how this turn ENDED, stamped on the message that finalized
	// it: the durable half of a fact otherwise carried only by the live
	// turn_ended SSE. Its presence also CLOSES a turn for both projections, so an
	// older message never closes one.
	TurnOutcome TurnOutcome `json:"turn_outcome,omitempty"`
	// TurnStopReasonRaw is the wire's stop reason verbatim, kept because the enum is
	// OPEN: an unmeasured value stays recoverable rather than flattened to `unknown`.
	// No consumer may branch on it; TurnOutcome is what they read.
	TurnStopReasonRaw StopReason `json:"turn_stop_reason_raw,omitempty"`
	// TurnFailureReason is WHY the turn ended badly, stamped by the same code that
	// stamps TurnOutcome so exactly one persisted message per turn carries both.
	//
	// Sanitized and byte-capped at the write, because its usual source is the
	// agent's own text. Absent on older records, so the client keeps a per-outcome
	// default.
	TurnFailureReason string `json:"turn_failure_reason,omitempty"`
	// TurnModel is the model that answered this turn. It belongs on the MESSAGE and
	// not only on the Chat, whose Model is the CURRENT one: rendering that would
	// relabel every historical turn the moment the user switched models.
	TurnModel string     `json:"turn_model,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Blocks is the canonical render model, in emission order. The client normalizes
	// legacy Content/ToolCalls into Blocks on replay so there is a single render path.
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
	// Attachments are the files attached to THIS prompt, on the user message so a
	// sent turn renders them as header pills. It must live on the record:
	// BuildPromptBlocks folds each one into a content block on the way OUT, so a
	// turn read back has nothing to recover the list from.
	//
	// Absent on older records and on a turn opened by a steer, which takes a plain
	// string and so carries no structured list.
	Attachments []Attachment `json:"attachments,omitempty"`
	// TurnCredits / TurnElapsedMs complete the turn footer summary alongside
	// ChangedFiles (above). The values also ride the turn_ended SSE for the
	// live render; omitempty drops the zero cases (a read-only turn has none).
	TurnCredits   float64 `json:"turn_credits,omitempty"`
	TurnElapsedMs float64 `json:"turn_elapsed_ms,omitempty"`
	Ts            int64   `json:"ts"`
	// TurnTruncated marks a turn the model stopped at a bound: it completed and
	// its answer is cut off. Stored though derivable from the raw stop reason, so
	// the Go and TypeScript projections do not each re-implement the mapping.
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

// MeteringItem is one usage dimension reported by kiro-cli's
// meteringUsage array. UnitPlural is the canonical identifier
// ("credits", "tokens", "requests"); UnitSingular is its singular form.
type MeteringItem struct {
	UnitSingular string  `json:"unit_singular"`
	UnitPlural   string  `json:"unit_plural"`
	Value        float64 `json:"value"`
}

// SessionMode describes one mode the running agent supports, from
// `modes.availableModes` on session/new or session/load; kept on the chat so the
// mode pill renders without re-querying the bridge.
//
// The v3 list is unified — bundled workflow modes AND every workspace custom
// agent, all switchable via session/set_mode — so Source ("bundled" vs
// "workspace") is what lets the picker group them.
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
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// DefaultEffortLevel is the level this MODEL defaults to, from
	// `_meta.kiro.defaultEffortLevel`; used for a chat with no session yet.
	//
	// NOT persisted onto Chat.Effort: seeding a chat's CHOICE from a service
	// default would pin it to every later session through StartOpts.Effort.
	DefaultEffortLevel string  `json:"default_effort_level,omitempty"`
	RateMultiplier     float64 `json:"rate_multiplier,omitempty"`
	// HasEffort reports whether this model offers reasoning-effort tiers, from
	// `_meta.kiro.hasEffort`. `auto` carries false, so a chat on it hides the
	// effort row: KAS builds no effortLevel option for a tierless model and
	// silently drops a level sent to one. Chat.EffortLevels answers the same
	// question from the other side.
	HasEffort bool `json:"has_effort,omitempty"`
}

// SessionEffortLevel is one reasoning-effort tier the running session offers,
// from the `effortLevel` config option's own `options[]` (value + name).
//
// The tiers are NOT a fixed five and NOT a per-model list on the model choice:
// kiro-cli 2.18.0 builds its picker from this option and errors "Effort is not
// available on the current model" when the list is empty, so the list IS the
// capability. Sending a tier that is absent here is a level the service rejects.
type SessionEffortLevel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// CatalogState is GET /api/config-template's verdict on the pre-session model
// catalog.
//
// Three-valued, and the ceiling is KAS's: it omits the `model` option
// identically for an unresolved cache and for an unentitled account, so
// CatalogEmpty states no cause and copy over it must not invent one. That also
// bounds the retry — the template is a pure cache read, so retrying converges on
// CatalogUnavailable, never on CatalogEmpty.
type CatalogState string

const (
	// CatalogReady means a `model` config option was decoded. Its entry list may
	// still be empty after the [Deprecated]/[Legacy] filter — KAS answered with a
	// catalog either way, which is what separates this from CatalogEmpty.
	CatalogReady CatalogState = "ready"
	// CatalogEmpty means the reply decoded and carried no `model` option at all.
	CatalogEmpty CatalogState = "empty"
	// CatalogUnavailable means vibekit never got an answer it could read.
	CatalogUnavailable CatalogState = "unavailable"
)

// CatalogReason discriminates the two ways a read fails, on CatalogUnavailable
// only. They are categorically different — one says try again, the other says the
// contract moved — and the flattened body could say neither.
type CatalogReason string

const (
	// CatalogReasonRPC means the read itself failed: retrying may succeed.
	CatalogReasonRPC CatalogReason = "rpc"
	// CatalogReasonDecode means the reply arrived unreadable: the contract moved.
	CatalogReasonDecode CatalogReason = "decode"
)

// ConfigTemplateResponse is the GET /api/config-template reply: the pre-session
// mode + model catalog, plus the verdict that says which outcome produced it.
//
// Named …Response rather than …Payload because it is bound to no SSE event;
// internal/wirespec's binding tests key on the Payload suffix, and
// auth.WhoamiResponse is the precedent.
type ConfigTemplateResponse struct {
	DefaultModel string `json:"default_model,omitempty"`
	// EffortActive is the `effortLevel` option's currentValue: the tier a
	// fresh session would run at. Pre-session, this is the only evidence of
	// a live level.
	EffortActive string `json:"effort_active,omitempty"`
	// Catalog carries NO omitempty deliberately: wiregen emits a required
	// TypeScript field for a field without it, so a client cannot invent a
	// fallback for the one value the whole retry policy reads.
	Catalog       CatalogState  `json:"catalog"`
	CatalogReason CatalogReason `json:"catalog_reason,omitempty"`
	// The three lists are non-null on every path, degrade branches included, so a
	// generated decoder requiring an array cannot fail on a failure body.
	Modes        []SessionMode        `json:"modes"`
	Models       []SessionModel       `json:"models"`
	EffortLevels []SessionEffortLevel `json:"effort_levels"`
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
	// A plain string, not EffortLevel: persisted state, so a value written by a
	// different build must decode rather than throw. The command boundary
	// validates with EffortLevel.Valid().
	Effort string `json:"effort,omitempty"`
	// Draft is the composer text typed but not sent. Server-side so it follows
	// the user across devices.
	//
	// NOT on ChatHeader: a debounced autosave there would put the draft in a
	// chat_updated frame every 600ms, which every client re-renders and which
	// races the caret of the tab that typed it. Store.SetDraft leaves UpdatedAt
	// alone, so typing does not reset the retention clock.
	Draft               string `json:"draft,omitempty"`
	CompactionWatermark string `json:"compaction_watermark,omitempty"`
	ID                  string `json:"id"`
	// Attachments are the workspace paths staged beside the draft, the draft's
	// twin: same debounce, same absence from ChatHeader, same UpdatedAt contract.
	//
	// Paths, not contents. BuildPromptBlocks reads and confines each one at SEND
	// time, so a 10 MiB image never enters the chat file for a prompt that may
	// never be sent.
	Attachments     []string       `json:"attachments,omitempty"`
	AvailableModels []SessionModel `json:"available_models,omitempty"`
	// ServedModelIDs is every model id the last session advertised, UNFILTERED —
	// AvailableModels drops end-of-life entries for the picker, so an entitlement
	// check against it would refuse a deprecated model the account still has.
	// Persisted because the model is chosen BEFORE session/new returns a catalog.
	// Empty means unknowable, and ModelServed then allows the send.
	ServedModelIDs []string      `json:"served_model_ids,omitempty"`
	AvailableModes []SessionMode `json:"available_modes,omitempty"`
	// EffortLevels is the effort vocabulary the last session advertised, from the
	// `effortLevel` option's `options[]`. Persisted beside AvailableModels so the
	// control renders before any frame arrives. EMPTY means the model offers no
	// tiers, which is how kiro-cli's own TUI decides to refuse the command.
	EffortLevels []SessionEffortLevel `json:"effort_levels,omitempty"`
	// EffortActive is the level the session is RUNNING at, from that option's
	// `currentValue`. Distinct from Effort, the chat's own CHOICE: a chat that
	// never picked has an empty Effort and still runs at a level. Only the choice
	// travels into a later session through StartOpts.Effort.
	EffortActive string    `json:"effort_active,omitempty"`
	Messages     []Message `json:"messages"`
	// PriorACPSessionIDs are the KAS sessions this chat used to run on, oldest
	// first; ACPSessionID is only the current one. Each still holds that period's
	// transcript and pre-images on disk, so retention keys on the whole CHAIN.
	//
	// Never trimmed: an entry here is a directory the reaper must spare, so
	// dropping one deletes history. Maintained by RecordSession.
	PriorACPSessionIDs []string `json:"prior_acp_session_ids,omitempty"`
	Usage              Usage    `json:"usage"`
	CreatedAt          int64    `json:"created_at"`
	UpdatedAt          int64    `json:"updated_at"`
	// A rewind reverts the chat it is in, so a chat has no parent and records no
	// starting turn. WorkflowRun.ParentChatID is unrelated: it names a launcher.
	MessageCount   int  `json:"message_count"`
	SupervisedMode bool `json:"supervised_mode,omitempty"`
}

// SessionChain returns every KAS session id this chat has run on, current
// one last. This is the reaper's keep-set for the chat: any session
// directory in it holds part of the chat's history.
func (c *Chat) SessionChain() []string {
	return sessionChain(c.ACPSessionID, c.PriorACPSessionIDs)
}

// ComposerState is the pair a chat's composer holds between sends: the text
// typed and not sent, and the files staged beside it.
//
// Returned by Store.SetDraft and Store.SetAttachments so the draft_changed
// broadcast gets both halves without a second chat-file read.
//
// Not a wire type and not persisted: DraftChangedPayload crosses the wire.
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
// Freshly allocated on EVERY branch: returning PriorACPSessionIDs itself would
// let a caller's append rewrite the chat's retention set, and copying on only
// one branch is worse than never copying — a mutating caller would be correct
// with a session attached and corrupting without one.
func sessionChain(current string, prior []string) []string {
	chain := make([]string, 0, len(prior)+1)
	chain = append(chain, prior...)
	if current == "" {
		return chain
	}
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

// lastTurnOutcome returns the newest persisted turn outcome in msgs — the
// TurnOutcome of the last message carrying one — or "" when none does.
//
// Walks BACKWARDS and stops at the first hit, because the newest outcome is the
// only one that describes how this chat's LAST turn ended, and rows persisted
// after the carrier (a plan row, a compaction event) carry none of their own and
// so must not hide it.
func lastTurnOutcome(msgs []Message) TurnOutcome {
	// Index-only range: Message is far past gocritic's rangeValCopy threshold, so
	// binding the element would copy a whole transcript row per iteration.
	for i := range slices.Backward(msgs) {
		if o := msgs[i].TurnOutcome; o != "" {
			return o
		}
	}
	return ""
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
		LastTurnOutcome:     lastTurnOutcome(c.Messages),
		AvailableModes:      c.AvailableModes,
		AvailableModels:     c.AvailableModels,
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

// ChatHeader is the metadata-only view of a Chat. Field order is driven
// by fieldalignment packing, not Chat's field order; both structs
// serialise to JSON independently so the visual mismatch is harmless.
type ChatHeader struct {
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	ACPSessionID  string `json:"acp_session_id,omitempty"`
	CurrentModeID string `json:"current_mode_id,omitempty"`
	// Effort mirrors Chat's. Carried here because the effort control reads the
	// ACTIVE chat's level, and an empty chat never fetches its full record (the
	// client shows the model picker instead of loading messages), so the header
	// is the only path that reaches every chat. Chat.Draft is deliberately NOT
	// mirrored — see the comment on that field.
	Effort string `json:"effort,omitempty"`
	// LastTurnOutcome is how this chat's NEWEST finished turn ended, DERIVED on
	// every read from the last message carrying a TurnOutcome — a second copy
	// would be a second thing that can be wrong. Here because the header is the
	// only projection reaching every chat, which is what the tab dot needs.
	//
	// Empty for a chat with no finished turn and for older records (invariant 5
	// forbids the backfill). Never `running`.
	LastTurnOutcome TurnOutcome `json:"last_turn_outcome,omitempty"`
	// EffortActive + EffortLevels mirror Chat's, for the same reason Effort does:
	// the control renders from the ACTIVE chat's header, and an empty chat never
	// fetches its full record.
	EffortActive        string               `json:"effort_active,omitempty"`
	EffortLevels        []SessionEffortLevel `json:"effort_levels,omitempty"`
	ID                  string               `json:"id"`
	CompactionWatermark string               `json:"compaction_watermark,omitempty"`
	AvailableModels     []SessionModel       `json:"available_models,omitempty"`
	AvailableModes      []SessionMode        `json:"available_modes,omitempty"`
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
// archive of its own. See agent/session_list.go for the wire provenance.
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

// ReadState is one list read's outcome on GET /api/sessions.
//
// Two-valued where CatalogState is three: an empty list arrives as an empty
// array rather than an omitted field, so `empty` stays derivable from its own
// length and only the read's failure needs stating.
type ReadState string

const (
	// ReadReady means the list read succeeded; an empty list is a real answer.
	ReadReady ReadState = "ready"
	// ReadUnavailable means the read failed, so the list says nothing.
	ReadUnavailable ReadState = "unavailable"
)

// SessionListResponse is the GET /api/sessions reply.
//
// The two lists degrade INDEPENDENTLY — separate verbs on the same bridge, so
// one can fail alone — which is why each carries its own verdict rather than the
// response carrying one. Neither verdict carries omitempty: a reader must not be
// able to read an absent field as success.
type SessionListResponse struct {
	SessionsState ReadState          `json:"sessions_state"`
	RunsState     ReadState          `json:"runs_state"`
	Sessions      []ResumableSession `json:"sessions"`
	Runs          []WorkflowRun      `json:"runs"`
}

// WorkflowRun is one previous workflow run, listed beside previous chats in
// the history surface (GET /api/sessions) and reviewable read-only.
//
// Sourced from _kiro/workflow/list, NOT session/list: that verb's workflow rows
// are STEP sessions reporting idle whatever the run did, so they can be neither
// counted nor judged as runs.
type WorkflowRun struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name"`
	// Status is run-level: paused / completed / failed.
	Status string `json:"status,omitempty"`
	// ParentChatID is the vibekit chat that launched the run, resolved through
	// the launching session's chain. Empty for a run with no vibekit parent
	// (launched from the TUI, or by a chat vibekit no longer keeps).
	ParentChatID string `json:"parent_chat_id,omitempty"`
	// EndReason says why something OTHER than the run stopped it: "overran" (a
	// slot, the idle window or the backstop) or "step_cap". Every bound cancels
	// through the verb the Cancel button reaches, so KAS reports `cancelled`
	// either way and only this separates a bound from a person; a user cancel
	// records nothing. In-memory for the runs THIS process stopped, so one
	// stopped before a restart falls back to the plain status.
	EndReason string `json:"end_reason,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	CreatedAt int64  `json:"created_at,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
}
