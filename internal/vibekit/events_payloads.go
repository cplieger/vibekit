package vibekit

// Per-event payload structs for SSE events; the envelopes and event-type
// constants live in events.go.

// TurnEndedPayload is the payload for type="turn_ended".
type TurnEndedPayload struct {
	ChangedFiles map[string]*FileChange `json:"changed_files,omitempty"`
	// Refusal accompanies stop_reason "refusal"; also persisted on the message.
	Refusal *RefusalInfo `json:"refusal,omitempty"`
	// Outcome is the turn's RESULT. StopReason beside it is the wire's raw text and
	// no consumer may branch on it: the enum is OPEN.
	Outcome    TurnOutcome `json:"outcome,omitempty"`
	StopReason StopReason  `json:"stop_reason,omitempty"`
	// Model answered this turn; empty when the turn produced no buffer.
	Model        string  `json:"model,omitempty"`
	CreditsDelta float64 `json:"credits_delta,omitempty"`
	ElapsedMs    float64 `json:"elapsed_ms,omitempty"`
	// Truncated means the model stopped at a bound: completed, answer cut off.
	Truncated bool `json:"truncated,omitempty"`
}

// FileChange tracks per-file change stats during a turn.
type FileChange struct {
	LinesAdded   int  `json:"lines_added"`
	LinesRemoved int  `json:"lines_removed"`
	IsNewFile    bool `json:"is_new_file,omitempty"`
}

// ConnectedPayload is the payload for type="connected", the SSE handshake event.
// Floor is the oldest event ID still in the replay ring and Head the newest; a client
// with last-seen-id below Floor missed events and must refetch authoritative state.
type ConnectedPayload struct {
	// Workspace is the absolute workspace root. Every ACP-supplied path reaches the
	// client workspace-RELATIVE while /api/file* is container-ABSOLUTE; this joins them.
	Workspace string `json:"workspace,omitempty"`
	Floor     uint64 `json:"floor"`
	Head      uint64 `json:"head"`
}

// AlwaysAllowBlock names why a permission card must not offer to persist a rule for
// the command it asks about. A code, not KAS's reason string: KAS owns the verdict,
// vibekit owns the copy.
type AlwaysAllowBlock string

// AlwaysAllowBlockUnparseable means KAS could not derive a shell pattern matching this
// command, so a saved rule would never fire.
const AlwaysAllowBlockUnparseable AlwaysAllowBlock = "unparseable"

// PermissionNeededPayload is the payload for type="permission_needed".
type PermissionNeededPayload struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Title      string `json:"title,omitempty"`
	// Kind forwards the ACP toolCall.kind so the client can style the prompt per kind.
	Kind         ToolKind `json:"kind,omitempty"`
	SubSessionID string   `json:"sub_session_id,omitempty"`
	// RunID + NodeID attribute a WORKFLOW STEP's ask to its run, stamped whichever
	// bridge the ask arrived on, so a card can name the step and a run tab can render
	// an ask keyed to the launching chat.
	RunID  string `json:"run_id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
	// AlwaysAllowBlocked, when set, blocks the persist-a-rule offer on this card.
	AlwaysAllowBlocked AlwaysAllowBlock   `json:"always_allow_blocked,omitempty"`
	Options            []PermissionOption `json:"options"`
	// Files is the turn's staged file list, present ONLY on a turn approval
	// (`_meta.kiro.type == "turn_approval"`), which expects per-file decisions back.
	Files     []ApprovalFile `json:"files,omitempty"`
	RequestID int64          `json:"request_id"`
}

// ApprovalFile is one file a turn wants to write, as offered for review.
type ApprovalFile struct {
	// Path is workspace-relative (KAS sends it absolute; translate normalizes).
	Path string `json:"path"`
	// SnapshotURI addresses the pre-image, so a diff is a snapshot read.
	SnapshotURI string `json:"snapshot_uri,omitempty"`
	// ActionID is KAS's pending-action id and THE KEY the decision map must use. An id
	// omitted from the response counts as a REJECT, not as unspecified.
	ActionID string `json:"action_id"`
}

// PermissionOption is one selectable response in a permission dialog.
type PermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// DecisionKind names which of the three asks a settled decision was: one event settles
// all three, so the kind tells the client which card to retire.
type DecisionKind string

// The three decision kinds, one per *_needed event that can be answered.
const (
	DecisionKindPermission  DecisionKind = "permission"
	DecisionKindElicitation DecisionKind = "elicitation"
	DecisionKindUserInput   DecisionKind = "user_input"
)

// SettledBy names what answered a decision. Attribution rather than a bare "it's
// gone": a card vanishing with no reason reads as a bug.
type SettledBy string

const (
	// SettledByUser means a person answered through a vibekit client — not this one,
	// since the answering client retires its own card before sending the answer.
	SettledByUser SettledBy = "user"
	// SettledByUnattended means nobody answered and vibekit answered for the absent
	// user when the unattended floor's budget expired (agent/run_unattended.go).
	SettledByUnattended SettledBy = "unattended"
	// SettledByMoot means the question stopped being answerable: a workflow step's ask
	// whose node moved on, or whose run ended still holding it. Only a run ask can
	// carry it — a request-shaped ask is claimed by whoever answers its JSON-RPC call.
	SettledByMoot SettledBy = "moot"
)

// DecisionSettledPayload is the payload for type="decision_settled": the request named
// here was answered, and not by the surface reading this. Every surface offers the same
// decision at once while only one answer is accepted, so the losers must retire the card.
type DecisionSettledPayload struct {
	Kind      DecisionKind `json:"kind"`
	SettledBy SettledBy    `json:"settled_by"`
	RequestID int64        `json:"request_id"`
}

// DecisionKindForEvent maps a tracked *_needed event to its decision kind, reporting
// false for anything else — a false is tracker misuse, not a wire case.
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

// MessageChunkPayload is the payload for type="message_chunk" (assistant streaming
// deltas). IsReasoning separates reasoning deltas from content deltas. BlockIndex
// addresses the chronological content block (Anthropic's content_block_delta.index);
// it may go BACKWARDS mid-turn, so clients accumulate by index, not by newest.
type MessageChunkPayload struct {
	// Refusal tags this delta as the model-refusal explanation, on at most one chunk
	// per turn, right before the turn ends with stop_reason "refusal".
	Refusal        *RefusalInfo `json:"refusal,omitempty"`
	MessageID      string       `json:"message_id"`
	Delta          string       `json:"delta"`
	AgentSubtaskID string       `json:"agent_subtask_id,omitempty"`
	BlockIndex     int          `json:"block_index"`
	// Seq is the delta's 1-based sequence number within the turn. A client that
	// ingested a turn_state snapshot drops chunks at or below its chunk_seq watermark.
	Seq         int64 `json:"seq,omitempty"`
	IsReasoning bool  `json:"is_reasoning,omitempty"`
}

// TurnStatePayload is the payload for type="turn_state": the connect-time synthesis of
// a chat's in-flight turn. Synthesized per busy chat in the SSE OnConnect replay, never
// broadcast live, so a reconnecting client renders the accumulated turn immediately and
// learns authoritatively that the chat is busy.
type TurnStatePayload struct {
	// Message is the in-flight assistant message accumulated so far; omitted when the
	// turn has not produced content yet (busy signal only).
	Message *Message `json:"message,omitempty"`
	// Status/Description replay the agent's last self-declared chat_status.
	// Authoritative here, the turn being verifiably in flight, unlike the live event.
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
	// ChunkSeq is the last delta folded into Message (see MessageChunkPayload.Seq).
	ChunkSeq int64 `json:"chunk_seq,omitempty"`
	// WorkflowStep marks a replayed turn belonging to a workflow RUN rather than to
	// this chat. Contract: APPLY the snapshot, do NOT set thinking — the chat's own
	// agent is idle, so nothing would ever clear it.
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
	// ErrCodeModeNotApplied means session/set_mode was refused at spawn; this event is
	// the only thing naming the request, since the chat's record holds the ACTUAL mode.
	ErrCodeModeNotApplied ErrorCode = "mode_not_applied"
	// ErrCodeModelNotServed means an explicitly-picked model is absent from the set
	// this account's session advertises, so it was refused before the wire.
	ErrCodeModelNotServed ErrorCode = "model_not_served"
	// ErrCodeAuthTokenUnavailable means kiro-cli could not vend a KAS access token. The
	// answer is a SIGN-IN, not a retry: sessions still open and every service-backed
	// surface fails.
	ErrCodeAuthTokenUnavailable ErrorCode = "auth_token_unavailable" //nolint:gosec // G101: an SSE error code, not a credential
)

// ErrorPayload is the payload for type="error"; Code lets clients react per-class.
type ErrorPayload struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// WorkingLabelPayload is the payload for type="working_label", a contextual label for
// the client's activity indicator, sent when a tool call starts or finishes.
type WorkingLabelPayload struct {
	Label string `json:"label"`
}

// CodeReferencesPayload is the payload for type="code_references": the licensed-code
// attributions on the in-flight assistant turn. References is the full deduped list, so
// a later notification REPLACES rather than appends.
type CodeReferencesPayload struct {
	MessageID  string          `json:"message_id"`
	References []CodeReference `json:"references"`
}

// SafetyStatus is the v3 (KAS) Infrastructure-Safety gate state (GateStatus), which
// evaluates infrastructure-as-code tool calls against remotely-formalized properties.
type SafetyStatus string

// SafetyStatusIdle and the following are the KAS GateStatus values carried on
// _kiro/safety/statusChanged.
const (
	SafetyStatusIdle        SafetyStatus = "idle"
	SafetyStatusFormalizing SafetyStatus = "formalizing"
	SafetyStatusEvaluating  SafetyStatus = "evaluating"
	SafetyStatusBlocked     SafetyStatus = "blocked"
	SafetyStatusError       SafetyStatus = "error"
)

// SafetyStatusPayload is the payload for type="safety_status", translated from the v3
// (KAS) _kiro/safety/statusChanged notification. KAS only emits it when the client
// declares the infrastructureSafety capability AND an AWS governance flag is on, so it
// normally never fires. Distinct from supervised mode, KAS's autopilot gate.
type SafetyStatusPayload struct {
	Status            SafetyStatus `json:"status"`
	Detail            string       `json:"detail,omitempty"`
	ToolID            string       `json:"tool_id,omitempty"`
	BlockedProperties []string     `json:"blocked_properties,omitempty"`
}

// SafetyProperty is one formalized Infrastructure-Safety property, authored OUT-OF-BAND
// by KAS via a remote MCP tool: there is no client RPC to create, set or toggle one.
type SafetyProperty struct {
	Description string `json:"description"`
	Index       int    `json:"index,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
}

// SafetyPropertiesPayload is the payload for type="safety_properties", translated from
// _kiro/safety/propertiesChanged. Reason is the KAS PropertyChangeReason
// (formalized/toggled/expired); same gating caveat as SafetyStatusPayload.
type SafetyPropertiesPayload struct {
	Reason     string           `json:"reason,omitempty"`
	Properties []SafetyProperty `json:"properties"`
}

// GovernanceFeatures is the org/account feature-flag set carried by the v3 (KAS)
// _kiro/governance/state notification. Every field is the RESOLVED effective value.
// Infrastructure-Safety is NOT here: it rides a separate modelConfigProvider channel,
// so the safety banner is gated by KAS's own emission, not by a field here.
type GovernanceFeatures struct {
	// MCPEnabled reports whether the MCP subsystem is permitted.
	MCPEnabled bool `json:"mcp_enabled"`
	// WebToolsEnabled reports whether built-in web tools are permitted.
	WebToolsEnabled bool `json:"web_tools_enabled"`
	// UsageAnalytics reports whether usage analytics collection is enabled.
	UsageAnalytics bool `json:"usage_analytics"`
	// ContentCollection reports whether content collection is enabled.
	ContentCollection bool `json:"content_collection"`
	// PromptLogging reports whether prompt logging is enabled.
	PromptLogging bool `json:"prompt_logging"`
	// CodeReferenceTracker reports whether licensed-code reference tracking is enabled;
	// it governs whether KAS emits _kiro/code_references at all.
	CodeReferenceTracker bool `json:"code_reference_tracker"`
	// AutonomousAgents reports whether autonomous agent runs are permitted.
	AutonomousAgents bool `json:"autonomous_agents"`
}

// GovernanceStatePayload is the payload for type="governance_state", translated from the
// v3 (KAS) _kiro/governance/state notification: the feature-flag policy KAS pushes on
// every session/new and session/load. Broadcast with an empty chat id because governance
// is account-global, and also served at GET /api/governance for a chat-less page load.
type GovernanceStatePayload struct {
	// DisabledReason is a human-readable reason from enterprise governance (e.g. why
	// MCP is off); empty on a normal account.
	DisabledReason string `json:"disabled_reason,omitempty"`
	// Features is the resolved effective feature-flag set.
	Features GovernanceFeatures `json:"features"`
	// Known is true once the real policy has been observed. Clients MUST only gate
	// affordances when it is true: the all-false zero value means "not yet known".
	Known bool `json:"known"`
	// IsEnterprise reports whether this is an enterprise/managed account.
	IsEnterprise bool `json:"is_enterprise,omitempty"`
}

// OpenExternalURLPayload is the payload for type="open_external_url": the agent asks the
// client to open a URL, most often an MCP server's OAuth page. The client surfaces a
// clickable affordance rather than auto-opening, because browsers popup-block a
// window.open() with no user gesture. Only http/https URLs are broadcast.
type OpenExternalURLPayload struct {
	URL string `json:"url"`
}

// RPC error code constants for typed dispatch in retry logic.
const (
	// RPCCodeInternal is the JSON-RPC 2.0 "Internal error" code (-32603).
	RPCCodeInternal = -32603
	// RPCCodeMethodNotFound is the JSON-RPC 2.0 "Method not found" code (-32601), the
	// answer when vibekit is asked for a method it does not implement. Not -32603,
	// which means "I broke" and would make the logs lie about which side has a problem.
	RPCCodeMethodNotFound = -32601
	// RPCCodeNotIdle indicates the session is still processing a prior turn.
	RPCCodeNotIdle = -32001
	// RPCCodeBridgeExited is a server-defined code for the ACP bridge process exiting.
	// COLLISION: -32000 is also KAS's code for a MAPPED backend error, throttles
	// included. Classify by errors.Is(err, ErrBridgeExited), never by this code — a
	// `switch re.Code` on it would silently swallow every throttle.
	RPCCodeBridgeExited = -32000
)

// ToolCallPayload is the payload for type="tool_call". BlockIndex is the tool_use
// block's position in the assistant message's chronological Blocks array.
type ToolCallPayload struct {
	MessageID  string   `json:"message_id"`
	ToolCall   ToolCall `json:"tool_call"`
	BlockIndex int      `json:"block_index"`
}

// ToolCallUpdatePayload is the payload for type="tool_call_update": a DELTA addressed by
// id, carrying only what this frame changed. Every field is omitempty and means
// "unchanged" when absent; OutputDelta's meaning depends on OutputReplace. turn_state
// remains the whole-object channel — a reconnecting client has no delta base.
type ToolCallUpdatePayload struct {
	// The three metadata blocks, each sent whole when it changed; none accumulates.
	Checkpoint *ToolCheckpoint `json:"checkpoint,omitempty"`
	Disclosed  *ToolDisclosed  `json:"disclosed,omitempty"`
	Denial     *ToolDenial     `json:"denial,omitempty"`
	MessageID  string          `json:"message_id"`
	ToolCallID string          `json:"tool_call_id"`
	// Title and Kind: KAS sends them nullish on most updates, so absent is "keep".
	Title  string     `json:"title,omitempty"`
	Kind   ToolKind   `json:"kind,omitempty"`
	Status ToolStatus `json:"status,omitempty"`
	// OutputDelta is normally the text to APPEND; when OutputReplace is set it is the
	// whole output instead. The replace case is load-bearing: at completion a terminal's
	// full stream wins over the ACP fragments already on the card (adoptTerminalOutput).
	OutputDelta string `json:"output_delta,omitempty"`
	// The four late identity attachments: each is adopted once, on at most one frame.
	TerminalID     string `json:"terminal_id,omitempty"`
	SubSessionID   string `json:"sub_session_id,omitempty"`
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	WorkflowID     string `json:"workflow_id,omitempty"`
	// OutputSpans style the WHOLE output at absolute offsets, so they are sent entire
	// whenever they change. Empty for output carrying no escape sequence.
	OutputSpans []TextSpan `json:"output_spans,omitempty"`
	// DiffsAppended are the diffs this frame added; diffs only ever append, so there is
	// no replace case.
	DiffsAppended []ToolDiff `json:"diffs_appended,omitempty"`
	// Locations are REPLACED wholesale when present.
	Locations []ToolLocation `json:"locations,omitempty"`
	// The two non-pointer scalars last, so the GC scan region stops above them (govet
	// fieldalignment). OutputReplace's meaning is on OutputDelta.
	DurationMs    int  `json:"duration_ms,omitempty"`
	OutputReplace bool `json:"output_replace,omitempty"`
}

// TerminalOutputPayload is the payload for type="terminal_output". Data is PLAIN text
// with escape sequences parsed off and hidden Unicode stripped; Spans style ranges of it
// at ABSOLUTE UTF-16 offsets across the whole stream, so a client painting one chunk must
// subtract Offset, where this chunk's Data begins.
type TerminalOutputPayload struct {
	TerminalID string     `json:"terminal_id"`
	Data       string     `json:"data"`
	Spans      []TextSpan `json:"spans,omitempty"`
	Offset     int        `json:"offset"`
}

// TerminalExitedPayload is the payload for type="terminal_exited". A signal-killed
// process carries Signal with ExitCode omitted; a normal exit carries ExitCode (>=0) with
// Signal empty, because KAS's zTerminalExitStatus requires exitCode>=0.
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

// DraftChangedPayload is the payload for type="draft_changed": the composer state one
// chat now holds, sent so an idle device converges on a draft it is not typing. Both
// halves travel on every frame, because the event describes the composer rather than the
// field that moved.
type DraftChangedPayload struct {
	Text        string   `json:"text"`
	Attachments []string `json:"attachments,omitempty"`
}

// CompactionStartedPayload is the payload for type="compaction_started".
type CompactionStartedPayload struct{}

// ChatStatusPayload is the payload for type="chat_status": the agent's self-declared
// activity, from the KAS focus_update channel. Status is one of in_progress |
// waiting_on_user | completed | idle. Ephemeral by design, never persisted, so a
// reconnect gap resets tabs to neutral instead of replaying a stale "in_progress".
type ChatStatusPayload struct {
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
}

// ChatStatusWaitingOnUser is the one ChatStatusPayload.Status value the server branches
// on, and the only one the connect-time replay retains: its meaning outlives its turn.
const ChatStatusWaitingOnUser = "waiting_on_user"

// MCPConfigChangedPayload is the payload for type="mcp_config_changed".
type MCPConfigChangedPayload struct{}

// ForgesChangedPayload is the payload for type="forges_changed".
// Sent after a forge is connected, disconnected, or re-probed.
type ForgesChangedPayload struct{}

// HooksChangedPayload is the payload for type="hooks_changed", emitted workspace-global
// after a hook is created, toggled, or changed on disk. The client refetches
// GET /api/hooks on receipt.
type HooksChangedPayload struct{}

// ToolJobChangedPayload is the payload for type="tool_job_changed", broadcast
// workspace-global on every tool-job state transition. The job carries no output tail;
// output streams via tool_job_output.
type ToolJobChangedPayload struct {
	Job *ToolJob `json:"job"`
}

// ToolJobOutputPayload is the payload for type="tool_job_output": a coalesced batch of
// output lines from the running tool job.
type ToolJobOutputPayload struct {
	JobID string   `json:"job_id"`
	Lines []string `json:"lines"`
}

// SettingsUpdatedPayload is the payload for type="settings_updated".
type SettingsUpdatedPayload struct{}

// SteerQueuedPayload is the payload for type="steer_queued": a mid-turn steer reached
// KAS's per-session buffer and is waiting for the next node boundary. Text travels even
// to the sender, because this event is the only source after a reconnect.
type SteerQueuedPayload struct {
	SteerID string `json:"steer_id"`
	Text    string `json:"text"`
	// Origin is whose words these are, resolved server-side. NO omitempty: an absent
	// field lets the client invent a fallback that is wrong for a workflow's report.
	Origin SteerOrigin `json:"origin"`
}

// AgentNoticePayload is the payload for type="agent_notice": a progress notice a workflow
// step or subagent reported into the session that launched it. Severity is one of
// info/success/warning/error and maps onto the client's toast levels. vibekit refuses to
// SEND the shape KAS sniffs for, so a notice reaching here is never the user's words.
type AgentNoticePayload struct {
	Severity string `json:"severity"`
	Text     string `json:"text"`
}

// SteerInjectedPayload is the payload for type="steer_injected": the model has now READ
// the steer. Broadcast TWICE for a steer the agent answers, carrying different halves —
// KAS's steering channel sends Text with no Ack, and the assistant text stream's
// acknowledgement marker sends Ack with no Text. The client merges both by SteerID.
type SteerInjectedPayload struct {
	SteerID string `json:"steer_id"`
	Text    string `json:"text"`
	// Ack is the agent's own statement of what it did about the steer, lifted out of the
	// acknowledgement marker. Empty on the read frame and when no marker closed.
	Ack string `json:"ack,omitempty"`
	// Origin is whose words these are, as on SteerQueuedPayload. On BOTH because an
	// agent-injected steer has no queued frame.
	Origin SteerOrigin `json:"origin"`
}

// SteerClearedPayload is the payload for type="steer_cleared": the steers named here were
// dropped from the buffer without reaching the model. An id appearing here after its
// steer_injected is ordinary housekeeping; one appearing WITHOUT an injected is a message
// the user wrote that nothing ever read.
type SteerClearedPayload struct {
	SteerIDs []string `json:"steer_ids"`
}

// TabsChangedPayload is the payload for type="tabs_changed": ONE committed mutation of the
// open-tab set, workspace-global so the chat id is empty. Every field except Version is
// optional, because one mutation touches a different combination of them. REMOVAL IS
// STATED, never inferred: absence from Order never means closure, and a client holding a
// tab the order does not name keeps it and sorts it last.
type TabsChangedPayload struct {
	// Changed is the one tab this mutation added or altered, absent on a close and a
	// reorder. A pointer because "no tab changed" and "a zero-valued tab changed" differ.
	Changed *TabSubject `json:"changed,omitempty"`
	// OpID is the client-minted correlation id from the causing command, echoed back.
	// Empty for a mutation no client asked for. Distinct from Idempotency-Key: no TTL,
	// no cache, no 409 branch.
	OpID string `json:"op_id,omitempty"`
	// RemovedIDs names every tab this mutation closed, per id and explicitly: closing a
	// parent with children is one mutation, so the children arrive here.
	RemovedIDs []string `json:"removed_ids,omitempty"`
	// Order is the EXPANDED list — every open tab including children, in the order the
	// collection now holds — sent whenever membership or a position moved.
	Order []string `json:"order,omitempty"`
	// Version is the collection version this mutation produced and the client's only
	// watermark: at or below local is stale, exactly one past applies, more than one past
	// means a frame was missed so re-list. ONLY AN EVENT MAY ADVANCE IT — adopting a
	// command response's version would make another device's in-flight frame read stale.
	Version uint64 `json:"version"`
}
