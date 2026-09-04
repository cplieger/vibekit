package vibekit

// Per-event payload structs for SSE events. The envelope types and
// event-type constants live in events.go; this file contains only the
// payload shapes that change when new events are added or existing
// payloads gain fields.

// TurnEndedPayload is the payload for type="turn_ended".
type TurnEndedPayload struct {
	ChangedFiles map[string]*FileChange `json:"changed_files,omitempty"`
	// Refusal accompanies stop_reason "refusal": the model declined to
	// continue and the final assistant chunk carried this metadata
	// (also persisted on the message; here for the live render).
	Refusal *RefusalInfo `json:"refusal,omitempty"`
	// Outcome is the turn's RESULT, and it is what a client reads. StopReason
	// travels beside it as the wire's raw text because the enum is OPEN, and no
	// consumer may branch on that text: an unmeasured value maps to `unknown`, and
	// grading it from the string is how six banner codes came to decide whether a
	// turn was over.
	Outcome    TurnOutcome `json:"outcome,omitempty"`
	StopReason StopReason  `json:"stop_reason,omitempty"`
	// Model is the model that answered this turn, for the live footer render.
	// The same value is persisted on the message (Message.TurnModel) so the
	// footer survives a reload; empty when the turn produced no buffer.
	Model        string  `json:"model,omitempty"`
	CreditsDelta float64 `json:"credits_delta,omitempty"`
	ElapsedMs    float64 `json:"elapsed_ms,omitempty"`
	// Truncated is a turn the model stopped at a bound: completed, answer cut off.
	Truncated bool `json:"truncated,omitempty"`
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
	// Workspace is the absolute workspace root, and the client needs it from
	// the first frame because it cannot derive it. Every ACP-supplied path
	// reaches the client workspace-RELATIVE (translate.relPath strips this
	// prefix so a turn footer reads "hello.sh"), while the /api/file* surface
	// has a container-ABSOLUTE namespace. Opening a changed file rejoins the
	// two, and this is the missing half of that join.
	Workspace string `json:"workspace,omitempty"`
	Floor     uint64 `json:"floor"`
	Head      uint64 `json:"head"`
}

// AlwaysAllowBlock names why a permission card must not offer to persist a rule
// for the command it is asking about.
//
// A CODE rather than KAS's own reason string, decided at the translate seam:
// upstream's sentence is long, it names a permissions file the vibekit user
// never hand-edits, and it carries a cmd.exe/PowerShell tail unreachable in
// this container. KAS owns the VERDICT; vibekit owns the copy. A code also
// extends by enum member rather than by a second boolean.
type AlwaysAllowBlock string

// AlwaysAllowBlockUnparseable is the one verdict KAS reports today: it could not
// derive a shell pattern that would match this command, so a saved rule would
// never fire and persisting one would be a permanent no-op in permissions.yaml.
const AlwaysAllowBlockUnparseable AlwaysAllowBlock = "unparseable"

// PermissionNeededPayload is the payload for type="permission_needed".
type PermissionNeededPayload struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Title      string `json:"title,omitempty"`
	// Kind forwards the ACP toolCall.kind so the client can style
	// distinctive permission prompts (switch_mode gets a different
	// dialog vs an execute_bash prompt).
	Kind         ToolKind `json:"kind,omitempty"`
	SubSessionID string   `json:"sub_session_id,omitempty"`
	// RunID + NodeID attribute a WORKFLOW STEP's ask to its run. Stamped from
	// the step-session registry whichever bridge the ask arrived on, so the
	// same fields serve both launch shapes: an agent-launched run's ask (on
	// the chat bridge, chat_id set) and a manually launched one's (on the run
	// bridge, chat_id `run:<id>`). What they buy the client: the card can say
	// WHICH step is asking, and a run tab can render the ask of a run it is
	// watching even though the ask is keyed to the launching chat.
	RunID  string `json:"run_id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
	// AlwaysAllowBlocked, when set, names why the card must not offer to persist
	// a rule for this command. Empty means the offer stands.
	AlwaysAllowBlocked AlwaysAllowBlock   `json:"always_allow_blocked,omitempty"`
	Options            []PermissionOption `json:"options"`
	// Files is the turn's staged file list, present ONLY on a turn approval
	// (`_meta.kiro.type == "turn_approval"`). A turn approval arrives as an
	// ordinary session/request_permission, which is why it rides this payload
	// rather than a second event: the only difference is that it carries files and
	// expects per-file decisions back.
	Files     []ApprovalFile `json:"files,omitempty"`
	RequestID int64          `json:"request_id"`
}

// ApprovalFile is one file a turn wants to write, as offered for review.
type ApprovalFile struct {
	// Path is workspace-relative (KAS sends it absolute; translate normalizes).
	Path string `json:"path"`
	// SnapshotURI addresses the pre-image, so a diff is a snapshot read.
	SnapshotURI string `json:"snapshot_uri,omitempty"`
	// ActionID is KAS's pending-action id and THE KEY the decision map must use.
	// KAS applies the accepted ids and restores the rest, so an id omitted from
	// the response counts as a REJECT rather than as unspecified.
	ActionID string `json:"action_id"`
}

// PermissionOption is one selectable response in a permission dialog.
type PermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// DecisionKind names which of the three asks a settled decision was. All three
// are request/response shaped and share one pending-request tracker, so one
// event settles all three and the kind is what tells the client which card to
// retire.
type DecisionKind string

// The three decision kinds, one per *_needed event that can be answered.
const (
	DecisionKindPermission  DecisionKind = "permission"
	DecisionKindElicitation DecisionKind = "elicitation"
	DecisionKindUserInput   DecisionKind = "user_input"
)

// SettledBy names what answered a decision. Attribution rather than a bare
// "it's gone": a card vanishing with no reason reads as a bug, and the two cases
// call for different reactions from the reader.
type SettledBy string

const (
	// SettledByUser means a person answered through a vibekit client. A client
	// that receives this while still showing the card is not that person — the
	// answering client retires its own card before it sends the answer.
	SettledByUser SettledBy = "user"
	// SettledByUnattended means nobody answered, so vibekit answered for the
	// absent user when the unattended floor's budget expired (see
	// agent/run_unattended.go). Named explicitly because an operator reading the
	// collapsed card needs to know a machine made this choice, and on which
	// side it defaults.
	SettledByUnattended SettledBy = "unattended"
	// SettledByMoot means NOBODY answered and nobody had to: the question stopped
	// being answerable on its own. A workflow step's ask reaches this when its node
	// moves on (answered from the TUI, or failed, or aborted) and when its run ends
	// while still holding it.
	//
	// It exists because the other two both ASSERT an answer, and a card retired
	// under one of them tells the reader their question was decided when it was
	// discarded. Only a run ask can carry it — a request-shaped ask is claimed by
	// whoever responds to its JSON-RPC request, so there is no third party to
	// retire it out from under them.
	SettledByMoot SettledBy = "moot"
)

// DecisionSettledPayload is the payload for type="decision_settled": the
// request named here has been answered, and not by the surface reading this.
//
// It exists because a decision is offered on EVERY surface at once — every tab,
// plus the run tab watching the same ask — while only one answer is accepted.
// Without this event the losing surfaces keep a live-looking card for a question
// that is closed, and clicking it achieves nothing.
type DecisionSettledPayload struct {
	Kind      DecisionKind `json:"kind"`
	SettledBy SettledBy    `json:"settled_by"`
	RequestID int64        `json:"request_id"`
}

// DecisionKindForEvent maps a tracked *_needed event to its decision kind,
// reporting false for anything else.
//
// The tracker only ever holds those three events, so a false here is a tracker
// misuse rather than a wire case — hence the bool instead of an empty-string
// fallback: an unknown kind on the wire is a value the client cannot switch on,
// and announcing nothing is better than announcing that.
func DecisionKindForEvent(t EventType) (DecisionKind, bool) {
	switch t {
	case EventPermissionNeeded:
		return DecisionKindPermission, true
	case EventElicitationNeeded:
		return DecisionKindElicitation, true
	case EventUserInputNeeded:
		return DecisionKindUserInput, true
	default:
		return "", false
	}
}

// MessageChunkPayload is the payload for type="message_chunk" (assistant
// streaming deltas). IsReasoning distinguishes reasoning deltas from
// regular content deltas — both flow through the same SSE event but
// land on different fields client-side (Message.Reasoning vs Content).
// BlockIndex addresses the chronological content block this delta
// belongs to (Anthropic's content_block_delta.index): consecutive text
// chunks of one subtask share an index; a tool_call between text segments
// of that subtask bumps the next text chunk to a new index, while an
// interleaved OTHER subtask does not — so an index may go BACKWARDS
// mid-turn. Clients accumulate by index rather than assuming the newest
// one, which is what makes a non-monotonic index harmless.
type MessageChunkPayload struct {
	// Refusal tags this delta as the model-refusal explanation (kiro-cli
	// 2.13 _meta.kiro.refusal on the chunk). Set on at most one chunk per
	// turn, right before the turn ends with stop_reason "refusal"; lets the
	// live renderer style the refusal callout without waiting for
	// turn_ended.
	Refusal        *RefusalInfo `json:"refusal,omitempty"`
	MessageID      string       `json:"message_id"`
	Delta          string       `json:"delta"`
	AgentSubtaskID string       `json:"agent_subtask_id,omitempty"`
	BlockIndex     int          `json:"block_index"`
	// Seq is the delta's 1-based sequence number within the turn
	// (assigned by the buffer under its lock). A client that ingested
	// a connect-time turn_state snapshot drops chunks with
	// seq <= the snapshot's chunk_seq watermark — they are already
	// folded into the snapshot — instead of double-appending them.
	Seq         int64 `json:"seq,omitempty"`
	IsReasoning bool  `json:"is_reasoning,omitempty"`
}

// TurnStatePayload is the payload for type="turn_state": the
// connect-time synthesis of a chat's in-flight turn (P6). Synthesized
// per busy chat in the SSE OnConnect replay — never broadcast live —
// so a reconnecting or freshly-loaded client renders the accumulated
// turn immediately instead of waiting for the next chunk or
// turn_ended, and learns authoritatively that the chat is busy
// (replacing the gap handler's eager thinking-clear guess).
type TurnStatePayload struct {
	// Message is the in-flight assistant message as accumulated so
	// far — the runtime's turn mirror, byte-equivalent to what a
	// never-disconnected client would have rendered. Omitted when the
	// turn hasn't produced content yet (busy signal only).
	Message *Message `json:"message,omitempty"`
	// Status/Description replay the agent's last self-declared
	// chat_status for the busy chat. Authoritative here — the turn is
	// verifiably in flight — unlike the live event, which stays
	// ephemeral and is cleared on gaps precisely because a bare
	// replay could resurrect a stale "in_progress".
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
	// ChunkSeq is the sequence number of the last delta folded into
	// Message (see MessageChunkPayload.Seq).
	ChunkSeq int64 `json:"chunk_seq,omitempty"`
	// WorkflowStep marks a replayed turn that belongs to a workflow RUN rather
	// than to this chat: a chat-parented run executes on the launching chat's
	// session, so a step's frames opened this turn.
	//
	// The contract for a client: APPLY the snapshot, do NOT set thinking. The
	// snapshot is the only copy of an in-flight step's transcript (it is never
	// persisted), so the event still has to be emitted — but the chat's own agent
	// is idle, and a client that reads this turn as the chat working says so for
	// the whole run, on every reconnect, with nothing to clear it.
	WorkflowStep bool `json:"workflow_step,omitempty"`
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
	ErrCodeSwitchFailed      ErrorCode = "switch_failed"
	ErrCodeCompactionFailed  ErrorCode = "compaction_failed"
	// ErrCodeModeNotApplied means session/set_mode was refused at spawn, so the
	// session is running a different mode from the one the chat asked for. The
	// chat's record holds the ACTUAL mode (the pill must not lie), which is why
	// this event is the only thing that names the request.
	ErrCodeModeNotApplied ErrorCode = "mode_not_applied"
	// ErrCodeModelNotServed means an explicitly-picked model is absent from the
	// set this account's session advertises, so it was refused before the wire
	// rather than accepted locally and rejected mid-prompt on every later turn.
	ErrCodeModelNotServed ErrorCode = "model_not_served"
	// ErrCodeAuthTokenUnavailable means kiro-cli could not vend a KAS access
	// token for the _kiro/auth/getAccessToken host request. Distinct from every
	// other code here because the answer is a SIGN-IN rather than a retry: KAS
	// runs unauthenticated without that token, so sessions still open and every
	// service-backed surface (the model registry, turns) fails. In practice this
	// is an expired SSO refresh chain after a week away, not a first boot.
	ErrCodeAuthTokenUnavailable ErrorCode = "auth_token_unavailable" //nolint:gosec // G101: an SSE error code, not a credential
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
// Distinct from supervised mode, which is KAS's autopilot gate (vibekit-acp.md).
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
// re-pushes on a prompt when it changes; vibekit caches the latest runtime-side and
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
// guard in agent/bridge_v3_auth.go; the client re-checks before rendering).
type OpenExternalURLPayload struct {
	URL string `json:"url"`
}

// RPC error code constants for typed dispatch in retry logic.
const (
	// RPCCodeInternal is the JSON-RPC 2.0 "Internal error" code (-32603).
	RPCCodeInternal = -32603
	// RPCCodeMethodNotFound is the JSON-RPC 2.0 "Method not found" code (-32601),
	// the honest answer when vibekit is asked for a method it does not implement.
	//
	// KAS answers its OWN unknown ext-methods with -32603 and switches on nothing,
	// so either code settles its promise. Matching KAS was the first instinct and
	// it was wrong: -32603 means "I broke", and mislabelling a deliberate refusal
	// as an internal fault makes vibekit's logs lie about which side has a problem.
	RPCCodeMethodNotFound = -32601
	// RPCCodeNotIdle indicates the session is still processing a prior turn.
	RPCCodeNotIdle = -32001
	// RPCCodeBridgeExited is a server-defined code indicating the ACP bridge
	// process exited.
	//
	// COLLISION, deliberate but load-bearing: -32000 is also the code KAS uses
	// for a MAPPED backend error, throttles included (it carries
	// `data.{errorType,retryErrorType,requestId}` there). The two never reach
	// the same reader today because the bridge-exited frame is intercepted by
	// POINTER IDENTITY in Call before the code is ever read, so it becomes a
	// TransportError wrapping ErrBridgeExited rather than an RPCError. But a
	// future `switch re.Code` on this constant would silently swallow every
	// throttle. Classify by errors.Is(err, ErrBridgeExited), never by this code.
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

// ToolCallUpdatePayload is the payload for type="tool_call_update": a DELTA
// addressed by id, carrying only what this frame changed.
//
// It used to carry the whole accumulated ToolCall, and two of that object's
// fields accumulate — Output appends and Diffs appends — so every later frame
// for a call re-sent everything the earlier ones had already delivered.
// Measured behind five open tabs: 5.73 MiB of diffs, 4.41 MiB of output,
// 1.49 MiB of input, p99 frame 122 KB, max 186 KB, with one Replace-in-File's
// 184 KB of diffs re-sent on every subsequent frame for that call. Input is
// absent here entirely: an update never changes it.
//
// This is the wire discipline message_chunk already has for text. The server's
// BUFFER still accumulates — turn_state needs the whole object, and so does the
// persist — and turn_state stays the whole-object channel, because a
// reconnecting client has nothing to apply a delta to.
//
// Every field is omitempty and means "unchanged" when absent. The one field that
// is not a plain value is OutputDelta, whose meaning depends on OutputReplace.
type ToolCallUpdatePayload struct {
	// The three metadata blocks, each sent whole when it changed. All three are
	// small and none accumulates.
	Checkpoint *ToolCheckpoint `json:"checkpoint,omitempty"`
	Disclosed  *ToolDisclosed  `json:"disclosed,omitempty"`
	Denial     *ToolDenial     `json:"denial,omitempty"`
	MessageID  string          `json:"message_id"`
	ToolCallID string          `json:"tool_call_id"`
	// Title and Kind: KAS sends them nullish on most updates and refines them on
	// some, so an absent value is "keep", never "clear".
	Title  string     `json:"title,omitempty"`
	Kind   ToolKind   `json:"kind,omitempty"`
	Status ToolStatus `json:"status,omitempty"`
	// OutputDelta is normally the text to APPEND. When OutputReplace is set it is
	// the whole output instead.
	//
	// The replace case is load-bearing rather than a fallback: at completion a
	// terminal's full stream wins over the ACP fragments already on the card
	// (adoptTerminalOutput), so that one frame legitimately shortens or rewrites
	// what came before. A pure-append wire cannot express it, and dropping it
	// would leave a command's output as whatever fragments happened to arrive.
	OutputDelta string `json:"output_delta,omitempty"`
	// The four late identity attachments. Each is adopted once and never
	// overwritten, so each appears on at most one frame per call.
	TerminalID     string `json:"terminal_id,omitempty"`
	SubSessionID   string `json:"sub_session_id,omitempty"`
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	WorkflowID     string `json:"workflow_id,omitempty"`
	// OutputSpans style the WHOLE output at absolute offsets, so they are sent
	// entire whenever they change. Empty for the ~99.75% of real command outputs
	// that carry no escape sequence.
	OutputSpans []TextSpan `json:"output_spans,omitempty"`
	// DiffsAppended are the diffs this frame added. Diffs only ever append, so
	// there is no replace case: KAS repeats a write's diff block on every
	// streaming frame and the card keeps each arrival.
	DiffsAppended []ToolDiff `json:"diffs_appended,omitempty"`
	// Locations are REPLACED wholesale when present, which is what the fold does
	// with them. Small: a path list, not content.
	Locations []ToolLocation `json:"locations,omitempty"`
	// The two non-pointer scalars last, so the GC scan region stops above them
	// (govet fieldalignment, ordered by betteralign). OutputReplace's meaning is
	// on OutputDelta.
	DurationMs    int  `json:"duration_ms,omitempty"`
	OutputReplace bool `json:"output_replace,omitempty"`
}

// TerminalOutputPayload is the payload for type="terminal_output".
//
// Data is PLAIN text with escape sequences already parsed off and hidden
// Unicode already stripped; Spans style ranges of it. The parse happens
// server-side (internal/ansitext) so the browser never builds HTML out of
// agent-controlled bytes.
//
// Offset is where this chunk's Data begins in the terminal's accumulated
// output, in the same UTF-16 code units the spans are addressed in. It is
// load-bearing rather than diagnostic: the spans carry ABSOLUTE offsets across
// the terminal's whole stream, so a client painting one chunk has to subtract
// this base to index into the chunk it was handed.
type TerminalOutputPayload struct {
	TerminalID string     `json:"terminal_id"`
	Data       string     `json:"data"`
	Spans      []TextSpan `json:"spans,omitempty"`
	Offset     int        `json:"offset"`
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

// ChatDeletedPayload is the payload for type="chat_deleted".
type ChatDeletedPayload struct {
	ID string `json:"id"`
}

// DraftChangedPayload is the payload for type="draft_changed": the composer
// state one chat now holds, sent so an idle device converges on a draft it is
// not typing.
//
// Both halves travel on every frame, and BOTH writers fill both, because the
// event describes the composer rather than the field that moved: a receiver
// applying only what changed would have to know which command fired, and a
// second event type for the twin would be two channels to order.
//
// The set_draft REPLY deliberately carries only a byte count, and this payload
// carrying the text is not a contradiction. That reply goes back to the device
// that just sent the words, where echoing them is pure cost; this goes to the
// devices that do not have them, which is the only thing it is for.
type DraftChangedPayload struct {
	Text        string   `json:"text"`
	Attachments []string `json:"attachments,omitempty"`
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

// ChatStatusWaitingOnUser is the one ChatStatusPayload.Status value the server
// branches on, as KAS's focus_update channel spells it. It is the only status
// whose meaning OUTLIVES its turn, so it is the one the connect-time replay
// retains (see agent.chatStatusCache.ClearAtTurnEnd). Every other value KAS
// sends on that channel travels through as an opaque string.
const ChatStatusWaitingOnUser = "waiting_on_user"

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

// --- HTTP response types for MCP endpoints ---

// SteerQueuedPayload is the payload for type="steer_queued": a mid-turn steer
// reached KAS's per-session buffer and is waiting for the next node boundary.
//
// Text is carried even though the sending client already has it, because this
// event is the ONLY source for every other device — and for the sender itself
// after a reconnect. The chip row is a projection of server state, so it has to
// be reconstructible from the events alone.
type SteerQueuedPayload struct {
	SteerID string `json:"steer_id"`
	Text    string `json:"text"`
	// Origin is whose words these are, resolved server-side against the steers
	// this server sent. NO omitempty: an absent field lets the client invent a
	// fallback, and the one it would pick is wrong for a workflow's report.
	Origin SteerOrigin `json:"origin"`
}

// AgentNoticePayload is the payload for type="agent_notice": a progress notice a
// workflow step or a subagent reported into the session that launched it.
//
// KAS decides this by sniffing the text for a `[notification/<severity>]` prefix
// and delivers it through the steering buffer, which is the only inbound channel
// into a live turn. vibekit refuses to SEND that shape (command/steer.go), so a
// notice reaching here is never the user's words.
//
// Severity is one of info/success/warning/error and is what makes this its own
// event rather than a field on a steer: it maps onto the client's toast levels,
// and the whole point of separating them is that a consumer never has to decide
// whose voice a message is in.
//
// There is no id. A steer needs one because its chip has to be updated when the
// model reads it and cleared when the boundary drops it; a notice has no later
// state, so nothing would ever address it.
type AgentNoticePayload struct {
	Severity string `json:"severity"`
	Text     string `json:"text"`
}

// SteerInjectedPayload is the payload for type="steer_injected": the model has
// now READ the steer. This is the moment the chip stops being a promise.
//
// It is broadcast TWICE for a steer the agent answers, and the two frames carry
// different halves. KAS's own steering channel produces the first, when the model
// reads the steer, with Text and no Ack. The second comes off the assistant TEXT
// stream when the agent's `[STEERING steer-<id>: …]` acknowledgement marker
// closes (translate/steer_marker.go), with Ack and no Text: reading a steer and
// acting on it are separate moments, so they cannot share one frame, and the
// client merges both onto the chip by SteerID.
type SteerInjectedPayload struct {
	SteerID string `json:"steer_id"`
	Text    string `json:"text"`
	// Ack is the agent's own statement of what it did about the steer, lifted
	// out of the acknowledgement marker KAS asks it to emit and which vibekit
	// hides from the transcript as machinery. That statement is strictly better
	// information than a check glyph, so the chip carries it: "read" becomes
	// "read: rebased onto main instead". Empty on the read frame, and empty when
	// the agent closed its response without a marker.
	Ack string `json:"ack,omitempty"`
	// Origin is whose words these are, as on SteerQueuedPayload. On BOTH because an
	// agent-injected steer has no queued frame (KAS 2.21.0 broadcasts one only from
	// `_session/steer`), so for the case Origin names, this frame is the only one.
	Origin SteerOrigin `json:"origin"`
}

// SteerClearedPayload is the payload for type="steer_cleared": the steers named
// here were dropped from the buffer without reaching the model.
//
// KAS clears at every turn boundary — normal end, prompt error, and cancel —
// and on an explicit steer_clear, bumping an epoch so a concurrent append is
// refused. So an id appearing here after its steer_injected is ordinary
// housekeeping, while one appearing WITHOUT an injected is a message the user
// wrote that nothing ever read. The client has to be able to tell those apart,
// which is why injected is its own event.
type SteerClearedPayload struct {
	SteerIDs []string `json:"steer_ids"`
}

// TabsChangedPayload is the payload for type="tabs_changed": ONE committed
// mutation of the open-tab set.
//
// Workspace-global, so the chat id is empty. Every field except Version is
// optional because one mutation touches a different combination of them: an open
// carries Changed and Order, a close carries RemovedIDs and Order, a pin carries
// Changed alone, a reorder carries Order alone.
//
// REMOVAL IS STATED, never inferred. That is the whole point of the shape: the
// arrangement used to travel as one whole list, so the client read "absent from
// the incoming list" as "closed elsewhere" and closed tabs nobody closed (live
// instance, 2026-08-25). Absence from Order still never means closure — a client
// holding a tab the order does not name keeps its position and sorts last.
type TabsChangedPayload struct {
	// Changed is the one tab this mutation added or altered, absent on a close
	// and on a reorder. A pointer because "no tab changed" and "a zero-valued
	// tab changed" are different facts and the client branches on which.
	Changed *TabSubject `json:"changed,omitempty"`
	// OpID is the client-minted correlation id from the command that caused this
	// mutation, echoed back so the caller can match the frame to its own
	// dispatch. Empty for a mutation no client asked for (a retention close, a
	// load-time prune). Distinct from Idempotency-Key, which keeps its
	// retry-safety job: this has no TTL, no cache and no 409 branch.
	OpID string `json:"op_id,omitempty"`
	// RemovedIDs names every tab this mutation closed, per id and explicitly. A
	// close of a parent with children is one mutation, so this is where the
	// children arrive.
	RemovedIDs []string `json:"removed_ids,omitempty"`
	// Order is the EXPANDED list: every open tab including children, in the order
	// the collection now holds. Sent whenever the membership or the position
	// moved, so a client never has to derive a position from a delta.
	Order []string `json:"order,omitempty"`
	// Version is the collection version this mutation produced, and it is the
	// client's only watermark. Three rules, exhaustive: at or below local is a
	// duplicate or stale frame and is ignored, exactly one past applies, more
	// than one past means a frame was missed so stop applying and re-list.
	//
	// ONLY AN EVENT MAY ADVANCE THE LOCAL VERSION. A command response carries the
	// version for diagnostics and a client must not adopt it: adopting a
	// response's v+2 would make another device's in-flight v+1 read as stale, so
	// it would be dropped and no gap would ever be detectable.
	Version uint64 `json:"version"`
}
