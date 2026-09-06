package vibekit

// Per-event payload structs for SSE events; the envelope types live in events.go.

// TurnEndedPayload is the payload for type="turn_ended".
type TurnEndedPayload struct {
	ChangedFiles map[string]*FileChange `json:"changed_files,omitempty"`
	// Refusal accompanies stop_reason "refusal"; also persisted on the message, so
	// this copy is for the live render.
	Refusal *RefusalInfo `json:"refusal,omitempty"`
	// Outcome is the turn's RESULT and what a client reads. StopReason travels beside
	// it as the wire's raw text because the enum is OPEN, and no consumer may branch
	// on that text: an unmeasured value maps to `unknown`.
	Outcome    TurnOutcome `json:"outcome,omitempty"`
	StopReason StopReason  `json:"stop_reason,omitempty"`
	// Model answered this turn. Persisted on the message too (Message.TurnModel) so the
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

// ConnectedPayload is the payload for type="connected", the SSE handshake. Floor is the
// oldest event ID still in the replay ring, Head the newest; a client whose last-seen id
// is below Floor missed events and must refetch authoritative state.
type ConnectedPayload struct {
	// Workspace is the absolute workspace root, needed from the first frame because the
	// client cannot derive it: ACP-supplied paths arrive workspace-RELATIVE while the
	// /api/file* surface is container-ABSOLUTE, and opening a changed file rejoins them.
	Workspace string `json:"workspace,omitempty"`
	Floor     uint64 `json:"floor"`
	Head      uint64 `json:"head"`
}

// AlwaysAllowBlock names why a permission card must not offer to persist a rule for the
// command it is asking about. A CODE rather than KAS's reason string: KAS owns the
// VERDICT, vibekit owns the copy, and a code extends by enum member.
type AlwaysAllowBlock string

// AlwaysAllowBlockUnparseable is the one verdict KAS reports today: no shell pattern would
// match this command, so a saved rule would be a permanent no-op in permissions.yaml.
const AlwaysAllowBlockUnparseable AlwaysAllowBlock = "unparseable"

// PermissionNeededPayload is the payload for type="permission_needed".
type PermissionNeededPayload struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Title      string `json:"title,omitempty"`
	// Kind forwards the ACP toolCall.kind so the client can style distinctive prompts
	// (switch_mode gets a different dialog from an execute_bash prompt).
	Kind         ToolKind `json:"kind,omitempty"`
	SubSessionID string   `json:"sub_session_id,omitempty"`
	// RunID + NodeID attribute a WORKFLOW STEP's ask to its run, stamped from the
	// step-session registry whichever bridge the ask arrived on, so a run tab can render
	// the ask of a run it is watching even though the ask is keyed to the launching chat.
	RunID  string `json:"run_id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
	// AlwaysAllowBlocked names why the card must not offer to persist a rule for this
	// command. Empty means the offer stands.
	AlwaysAllowBlocked AlwaysAllowBlock   `json:"always_allow_blocked,omitempty"`
	Options            []PermissionOption `json:"options"`
	// Files is the turn's staged file list, present ONLY on a turn approval. Such an
	// approval arrives as an ordinary session/request_permission, so it rides this payload
	// rather than a second event; it expects per-file decisions back.
	Files     []ApprovalFile `json:"files,omitempty"`
	RequestID int64          `json:"request_id"`
}

// ApprovalFile is one file a turn wants to write, as offered for review.
type ApprovalFile struct {
	// Path is workspace-relative (KAS sends it absolute; translate normalizes).
	Path string `json:"path"`
	// SnapshotURI addresses the pre-image, so a diff is a snapshot read.
	SnapshotURI string `json:"snapshot_uri,omitempty"`
	// ActionID is KAS's pending-action id and THE KEY the decision map must use. KAS
	// restores every id the response omits, so omission counts as a REJECT.
	ActionID string `json:"action_id"`
}

// PermissionOption is one selectable response in a permission dialog.
type PermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// DecisionKind names which of the three asks a settled decision was. They share one
// pending-request tracker, so one event settles all three and the kind is what tells the
// client which card to retire.
type DecisionKind string

// The three decision kinds, one per *_needed event that can be answered.
const (
	DecisionKindPermission  DecisionKind = "permission"
	DecisionKindElicitation DecisionKind = "elicitation"
	DecisionKindUserInput   DecisionKind = "user_input"
)

// SettledBy names what answered a decision. Attribution rather than a bare "it's gone":
// the cases call for different reactions from the reader.
type SettledBy string

const (
	// SettledByUser means a person answered through a vibekit client. A client receiving
	// this while still showing the card is not that person.
	SettledByUser SettledBy = "user"
	// SettledByUnattended means nobody answered, so vibekit answered for the absent user
	// when the unattended floor's budget expired (agent/run_unattended.go). Named so an
	// operator knows a machine made the choice, and on which side it defaults.
	SettledByUnattended SettledBy = "unattended"
	// SettledByMoot means NOBODY answered and nobody had to: the question stopped being
	// answerable. The other two both ASSERT an answer, so retiring a discarded ask under
	// one of them would tell the reader their question was decided. Only a run ask can
	// carry it — a request-shaped ask is claimed by whoever answers its JSON-RPC request.
	SettledByMoot SettledBy = "moot"
)

// DecisionSettledPayload is the payload for type="decision_settled": the request named
// here was answered, and not by the surface reading this. A decision is offered on EVERY
// surface at once while only one answer is accepted, so without this event the losers keep
// a live-looking card for a closed question.
type DecisionSettledPayload struct {
	Kind      DecisionKind `json:"kind"`
	SettledBy SettledBy    `json:"settled_by"`
	RequestID int64        `json:"request_id"`
}

// DecisionKindForEvent maps a tracked *_needed event to its decision kind, reporting false
// for anything else. A false is tracker misuse rather than a wire case, and a bool beats an
// empty-string fallback: an unknown kind is a value no client can switch on.
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

// DecisionRunID reads the workflow run a tracked decision's payload belongs to, answering
// "" for a payload that is not one of the three or carries no run. The tracker is keyed
// {chat, requestID} — what an ANSWER arrives naming — so a run-scoped clear has to read the
// payload. All three are matched as VALUES, which is how each *_needed event is broadcast.
func DecisionRunID(payload any) string {
	switch p := payload.(type) {
	case PermissionNeededPayload:
		return p.RunID
	case ElicitationNeededPayload:
		return p.RunID
	case UserInputNeededPayload:
		return p.RunID
	default:
		return ""
	}
}

// MessageChunkPayload is the payload for type="message_chunk" (assistant streaming deltas).
// BlockIndex addresses the content block this delta belongs to and may go BACKWARDS
// mid-turn: a tool_call bumps its own subtask's next text chunk to a new index while an
// interleaved OTHER subtask does not. Accumulate BY INDEX, never into the newest block.
type MessageChunkPayload struct {
	// Refusal tags this delta as the model-refusal explanation, set on at most one chunk
	// per turn so the live renderer can style the callout without waiting for turn_ended.
	Refusal        *RefusalInfo `json:"refusal,omitempty"`
	MessageID      string       `json:"message_id"`
	Delta          string       `json:"delta"`
	AgentSubtaskID string       `json:"agent_subtask_id,omitempty"`
	BlockIndex     int          `json:"block_index"`
	// Seq is the delta's 1-based sequence number within the turn. A client that ingested a
	// connect-time turn_state snapshot drops chunks at or below its chunk_seq watermark —
	// they are already folded in — instead of double-appending them.
	Seq         int64 `json:"seq,omitempty"`
	IsReasoning bool  `json:"is_reasoning,omitempty"`
}

// TurnStatePayload is the payload for type="turn_state": one per busy chat in the SSE
// OnConnect replay, NEVER broadcast live, so a reconnecting client renders the accumulated
// turn immediately and learns authoritatively that the chat is busy.
type TurnStatePayload struct {
	// Message is the in-flight assistant message as accumulated so far. Omitted when the
	// turn has produced no content yet (busy signal only).
	Message *Message `json:"message,omitempty"`
	// Status/Description replay the agent's last self-declared chat_status. Authoritative
	// here because the turn is verifiably in flight, unlike the live event, which is
	// cleared on gaps precisely so a bare replay cannot resurrect a stale "in_progress".
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
	// ChunkSeq is the last delta folded into Message (see MessageChunkPayload.Seq).
	ChunkSeq int64 `json:"chunk_seq,omitempty"`
	// WorkflowStep marks a turn a workflow RUN opened on the launching chat's session.
	// Contract: APPLY the snapshot, do NOT set thinking. The snapshot is the only copy of
	// an in-flight step's transcript, so the event must still be emitted — but the chat's
	// own agent is idle, and a client reading this as the chat working says so for the
	// whole run, on every reconnect, with nothing to clear it.
	WorkflowStep bool `json:"workflow_step,omitempty"`
}

// ErrorCode identifies an SSE error event class.
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
	// ErrCodeModeNotApplied means session/set_mode was refused at spawn. The chat's record
	// holds the ACTUAL mode, so this event is the only thing that names the request.
	ErrCodeModeNotApplied ErrorCode = "mode_not_applied"
	// ErrCodeModelNotServed means an explicitly-picked model is absent from the set this
	// account's session advertises, so it was refused before the wire rather than rejected
	// mid-prompt on every later turn.
	ErrCodeModelNotServed ErrorCode = "model_not_served"
	// ErrCodeAuthTokenUnavailable means kiro-cli could not vend a KAS access token. The
	// answer is a SIGN-IN rather than a retry: KAS runs unauthenticated without one, so
	// sessions still open and every service-backed surface fails.
	ErrCodeAuthTokenUnavailable ErrorCode = "auth_token_unavailable" //nolint:gosec // G101: an SSE error code, not a credential
)

// ErrorPayload is the payload for type="error". Code lets clients react
// per-class without string-matching.
type ErrorPayload struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	// TurnScoped reports that this failure finalized a turn now carrying the same Message
	// durably, so the reason is already in that turn's card. A property of the EMISSION,
	// not of the Code: three of the five emitters behind ErrCodePromptFailed and
	// ErrCodeRecoveryFailed never open a turn, so no per-code answer fits both groups.
	// Absent means NO, the safe direction — report the failure rather than trust an inline
	// row that may not exist.
	TurnScoped bool `json:"turn_scoped,omitempty"`
}

// WorkingLabelPayload is the payload for type="working_label", sent when the agent starts
// or finishes a tool call so the client can show a contextual label.
type WorkingLabelPayload struct {
	Label string `json:"label"`
}

// CodeReferencesPayload is the payload for type="code_references": the licensed-code
// attributions on the in-flight assistant turn. References is the full deduped list, so a
// later notification REPLACES rather than appends. Also persisted on the Message.
type CodeReferencesPayload struct {
	MessageID  string          `json:"message_id"`
	References []CodeReference `json:"references"`
}

// SafetyStatus is the v3 (KAS) Infrastructure-Safety gate state, which evaluates
// infrastructure-as-code tool calls against remotely-formalized safety properties. Typed so
// wiregen emits a TS union.
type SafetyStatus string

// SafetyStatusIdle and the following constants are the v3 (KAS) GateStatus values carried
// on _kiro/safety/statusChanged.
const (
	SafetyStatusIdle        SafetyStatus = "idle"
	SafetyStatusFormalizing SafetyStatus = "formalizing"
	SafetyStatusEvaluating  SafetyStatus = "evaluating"
	SafetyStatusBlocked     SafetyStatus = "blocked"
	SafetyStatusError       SafetyStatus = "error"
)

// SafetyStatusPayload is the payload for type="safety_status", translated from the v3 (KAS)
// _kiro/safety/statusChanged notification and rendered as a transient banner. KAS installs
// the gate only under an AWS governance flag, off by default on Builder-ID accounts, so this
// normally never fires. Distinct from supervised mode, which is KAS's autopilot gate.
type SafetyStatusPayload struct {
	Status            SafetyStatus `json:"status"`
	Detail            string       `json:"detail,omitempty"`
	ToolID            string       `json:"tool_id,omitempty"`
	BlockedProperties []string     `json:"blocked_properties,omitempty"`
}

// SafetyProperty is one formalized Infrastructure-Safety property. Authored OUT-OF-BAND by
// KAS via a remote MCP tool; there is no client RPC to create, set or toggle one, so vibekit
// only ever displays them.
type SafetyProperty struct {
	Description string `json:"description"`
	Index       int    `json:"index,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
}

// SafetyPropertiesPayload is the payload for type="safety_properties". Reason is the KAS
// PropertyChangeReason (formalized/toggled/expired). Same gating and authoring caveats as
// SafetyStatusPayload.
type SafetyPropertiesPayload struct {
	Reason     string           `json:"reason,omitempty"`
	Properties []SafetyProperty `json:"properties"`
}

// GovernanceFeatures is the org/account feature-flag set carried by the v3 (KAS)
// _kiro/governance/state notification. Every field is the RESOLVED effective value, so a
// Builder-ID login reads permissive with isEnterprise=false and no disabledReason.
// Infrastructure-Safety is NOT here: it rides a separate isFeatureEnabled channel, so the
// safety banner is gated by KAS's own emission rather than by any field here.
type GovernanceFeatures struct {
	// MCPEnabled reports whether the MCP subsystem is permitted; false means enterprise
	// governance suppressed MCP entirely.
	MCPEnabled bool `json:"mcp_enabled"`
	// WebToolsEnabled reports whether built-in web tools are permitted.
	WebToolsEnabled bool `json:"web_tools_enabled"`
	// UsageAnalytics reports whether usage analytics collection is enabled.
	UsageAnalytics bool `json:"usage_analytics"`
	// ContentCollection reports whether content collection is enabled.
	ContentCollection bool `json:"content_collection"`
	// PromptLogging reports whether prompt logging is enabled.
	PromptLogging bool `json:"prompt_logging"`
	// CodeReferenceTracker governs whether KAS emits _kiro/code_references at all, so the
	// attribution chip is dormant unless this is true.
	CodeReferenceTracker bool `json:"code_reference_tracker"`
	// AutonomousAgents reports whether autonomous agent runs are permitted.
	AutonomousAgents bool `json:"autonomous_agents"`
}

// GovernanceStatePayload is the payload for type="governance_state". Governance is
// account-global, so the wire sessionId is dropped and the SSE is broadcast with an empty
// chat id; GET /api/governance serves the cached copy so a fresh page load can read it with
// no chat open. Clients MUST gate affordances only when Known is true — the all-false zero
// value otherwise reads as "everything disabled" when the policy is simply unobserved.
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

// OpenExternalURLPayload is the payload for type="open_external_url". Browsers popup-block a
// window.open() not driven by a user gesture, so the client surfaces a banner link rather
// than auto-opening. Only http/https URLs are broadcast.
type OpenExternalURLPayload struct {
	URL string `json:"url"`
}

// RPC error code constants for typed dispatch in retry logic.
const (
	// RPCCodeInternal is the JSON-RPC 2.0 "Internal error" code (-32603).
	RPCCodeInternal = -32603
	// RPCCodeMethodNotFound is the JSON-RPC 2.0 "Method not found" code (-32601). KAS
	// answers its own unknown ext-methods with -32603 and switches on nothing, so either
	// settles its promise — but -32603 means "I broke", and labelling a deliberate refusal
	// as an internal fault makes vibekit's logs lie about which side has the problem.
	RPCCodeMethodNotFound = -32601
	// RPCCodeNotIdle indicates the session is still processing a prior turn.
	RPCCodeNotIdle = -32001
	// RPCCodeBridgeExited says the ACP bridge process exited. COLLISION: -32000 is also
	// KAS's code for a MAPPED backend error, throttles included. They never reach one
	// reader today because Call intercepts the bridge-exited frame by POINTER IDENTITY, but
	// a `switch re.Code` on this constant would swallow every throttle. Classify by
	// errors.Is(err, ErrBridgeExited), never by this code.
	RPCCodeBridgeExited = -32000
)

// ToolCallPayload is the payload for type="tool_call". BlockIndex is the tool_use block's
// position in the assistant message's Blocks array, so the card lands between the right
// surrounding text blocks.
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

// TerminalOutputPayload is the payload for type="terminal_output". Data is PLAIN text with
// escape sequences parsed off server-side, so the browser never builds HTML out of
// agent-controlled bytes; Spans style ranges of it. Offset is where this chunk begins in the
// terminal's accumulated output, in the UTF-16 code units the spans use — and the spans carry
// ABSOLUTE offsets, so a client painting one chunk must subtract this base.
type TerminalOutputPayload struct {
	TerminalID string     `json:"terminal_id"`
	Data       string     `json:"data"`
	Spans      []TextSpan `json:"spans,omitempty"`
	Offset     int        `json:"offset"`
}

// TerminalExitedPayload is the payload for type="terminal_exited". A signal-killed process
// carries Signal with ExitCode omitted; a normal exit carries ExitCode (>=0) with Signal
// empty. Mirrors KAS's zTerminalExitStatus, so a signal death never reports exit_code:-1.
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

// DraftChangedPayload is the payload for type="draft_changed": the composer state one chat
// now holds, sent so an idle device converges on a draft it is not typing. Both halves travel
// on every frame and BOTH writers fill both, because the event describes the COMPOSER rather
// than the field that moved. The set_draft reply carries only a byte count, which is not a
// contradiction: that goes back to the device that already has the words.
type DraftChangedPayload struct {
	Text        string   `json:"text"`
	Attachments []string `json:"attachments,omitempty"`
}

// CompactionStartedPayload is the payload for type="compaction_started".
type CompactionStartedPayload struct{}

// ChatStatusPayload is the payload for type="chat_status": the agent's self-declared activity,
// off the KAS focus_update channel. Status is in_progress | waiting_on_user | completed | idle.
// Ephemeral by design, so a restart or reconnect gap resets tabs to neutral instead of
// replaying a stale "in_progress".
type ChatStatusPayload struct {
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
}

// ChatStatusWaitingOnUser is the one Status value the server branches on, as KAS's
// focus_update channel spells it: the only status whose meaning OUTLIVES its turn, so the
// only one the connect-time replay retains. Every other value travels through opaque.
const ChatStatusWaitingOnUser = "waiting_on_user"

// MCPConfigChangedPayload is the payload for type="mcp_config_changed".
type MCPConfigChangedPayload struct{}

// ForgesChangedPayload is the payload for type="forges_changed".
// Sent after a forge is connected, disconnected, or re-probed.
type ForgesChangedPayload struct{}

// HooksChangedPayload is the payload for type="hooks_changed", workspace-global and empty.
// The client refetches GET /api/hooks on receipt.
type HooksChangedPayload struct{}

// ToolJobChangedPayload is the payload for type="tool_job_changed", workspace-global, on
// every job state transition. The job carries no output tail; output streams via
// tool_job_output.
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

// SteerQueuedPayload is the payload for type="steer_queued": a mid-turn steer reached KAS's
// per-session buffer and is waiting for the next node boundary. Text travels even though the
// sender has it, because the chip row is a projection of server state and must be
// reconstructible from the events alone — for every other device and for the sender after a
// reconnect.
type SteerQueuedPayload struct {
	SteerID string `json:"steer_id"`
	Text    string `json:"text"`
	// Origin is whose words these are, resolved server-side. NO omitempty: an absent field
	// lets the client invent a fallback, and the one it would pick is wrong for a
	// workflow's report.
	Origin SteerOrigin `json:"origin"`
}

// AgentNoticePayload is the payload for type="agent_notice": a progress notice a workflow step
// or subagent reported into the session that launched it. KAS decides this by sniffing the text
// for a `[notification/<severity>]` prefix and delivers it through the steering buffer, and
// vibekit refuses to SEND that shape, so a notice here is never the user's words. Severity
// (info/success/warning/error) is why this is its own event rather than a field on a steer: a
// consumer never has to decide whose voice a message is in. There is no id — a notice has no
// later state to address.
type AgentNoticePayload struct {
	Severity string `json:"severity"`
	Text     string `json:"text"`
}

// SteerInjectedPayload is the payload for type="steer_injected": the model has now READ the
// steer. Broadcast TWICE for a steer the agent answers, carrying different halves — KAS's
// steering channel sends Text with no Ack, then the assistant TEXT stream sends Ack with no
// Text when the `[STEERING steer-<id>: …]` marker closes. Reading a steer and acting on it are
// separate moments, so the client merges both onto the chip by SteerID.
type SteerInjectedPayload struct {
	SteerID string `json:"steer_id"`
	Text    string `json:"text"`
	// Ack is the agent's own statement of what it did, lifted out of the acknowledgement
	// marker vibekit hides from the transcript: "read" becomes "read: rebased onto main
	// instead". Empty on the read frame, and empty when the agent emitted no marker.
	Ack string `json:"ack,omitempty"`
	// Origin is whose words these are, as on SteerQueuedPayload. On BOTH because an
	// agent-injected steer has no queued frame, so for the case Origin names this frame is
	// the only one.
	Origin SteerOrigin `json:"origin"`
}

// SteerClearedPayload is the payload for type="steer_cleared": the steers named here were
// dropped from the buffer without reaching the model. KAS clears at every turn boundary and on
// an explicit steer_clear, so an id appearing after its steer_injected is housekeeping, while
// one appearing WITHOUT an injected is a message nothing ever read — which is why injected is
// its own event.
type SteerClearedPayload struct {
	SteerIDs []string `json:"steer_ids"`
}

// TabsChangedPayload is the payload for type="tabs_changed": ONE committed mutation of the
// open-tab set, workspace-global, so the chat id is empty. REMOVAL IS STATED, never inferred:
// absence from Order never means closure, and a client holding a tab the order does not name
// keeps its position and sorts last.
type TabsChangedPayload struct {
	// Changed is the one tab this mutation added or altered, absent on a close and a
	// reorder. A pointer because "no tab changed" and "a zero-valued tab changed" are
	// different facts the client branches on.
	Changed *TabSubject `json:"changed,omitempty"`
	// OpID is the client-minted correlation id echoed back so a caller can match the frame
	// to its own dispatch; empty for a mutation no client asked for. Distinct from
	// Idempotency-Key: this has no TTL, no cache and no 409 branch.
	OpID string `json:"op_id,omitempty"`
	// RemovedIDs names every tab this mutation closed, per id. Closing a parent with
	// children is one mutation, so this is where the children arrive.
	RemovedIDs []string `json:"removed_ids,omitempty"`
	// Order is the EXPANDED list, children included, so a client never derives a position
	// from a delta.
	Order []string `json:"order,omitempty"`
	// Version is the client's only watermark, with three exhaustive rules: at or below local
	// is ignored, exactly one past applies, more than one past means re-list. ONLY AN EVENT
	// MAY ADVANCE IT — adopting a response's v+2 would make another device's in-flight v+1
	// read as stale, so no gap could ever be detected.
	Version uint64 `json:"version"`
}
