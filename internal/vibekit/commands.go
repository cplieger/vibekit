package vibekit

// Wire shapes for POST /api/command; routing lives in agent/command.go.

import "encoding/json"

// CommandType identifies the kind of client command posted to /api/command.
type CommandType string

// Command type constants; each is a key in agent/command.go's dispatch map.
const (
	CmdCreateChat          CommandType = "create_chat"
	CmdResumeSession       CommandType = "resume_session"
	CmdForkChat            CommandType = "fork_chat"
	CmdPrompt              CommandType = "prompt"
	CmdCancel              CommandType = "cancel"
	CmdDeleteChat          CommandType = "delete_chat"
	CmdSwitchModel         CommandType = "switch_model"
	CmdPermissionResponse  CommandType = "permission_response"
	CmdElicitationResponse CommandType = "elicitation_response"
	CmdUserInputResponse   CommandType = "user_input_response"
	CmdRewindChat          CommandType = "rewind_chat"
	CmdCompact             CommandType = "compact"
	CmdSetEffort           CommandType = "set_effort"
	CmdSetDraft            CommandType = "set_draft"
	CmdSetAttachments      CommandType = "set_attachments"
	CmdSetMode             CommandType = "set_mode"
	CmdCreateHook          CommandType = "create_hook"
	CmdSetSupervisedMode   CommandType = "set_supervised_mode"
	CmdSteer               CommandType = "steer"
	CmdSteerClear          CommandType = "steer_clear"
	// The four tab commands. Membership is a mutation, so it rides this envelope
	// rather than a second REST surface with its own failure semantics.
	CmdOpenTab     CommandType = "open_tab"
	CmdCloseTab    CommandType = "close_tab"
	CmdReorderTabs CommandType = "reorder_tabs"
	CmdPinTab      CommandType = "pin_tab"
)

// ClientCommand is the envelope for every command the browser posts. Type
// determines how Payload unmarshals; idempotency is the Idempotency-Key header
// (internal/server/idempotency.go), never a body field. A payload's own
// request_id means something else: the ACP request id being answered.
type ClientCommand struct {
	Type    CommandType     `json:"type"`
	ChatID  ChatID          `json:"chat_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// PromptCommand is the payload for type="prompt".
//
// Text is required, trimmed, capped at 512 KiB; oversize returns HTTP 413.
// MessageID is required (validMessageID). Model is optional (validIdent).
// Attachments are resolved via resolveInsideWorkDir before read.
type PromptCommand struct {
	Text        string       `json:"text"`
	MessageID   string       `json:"message_id"`
	Model       string       `json:"model,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is a file staged beside a prompt; its extension decides whether it
// travels as a document content block or as a text path reference.
type Attachment struct {
	Path string `json:"path"` // workspace-relative
	Name string `json:"name"`
}

// CreateChatCommand is the payload for type="create_chat".
//
// Name is optional, capped at maxChatNameBytes, defaulting to "New
// conversation". Model is optional (validIdent). Invalid input returns HTTP 400.
type CreateChatCommand struct {
	Name  string `json:"name,omitempty"`
	Model string `json:"model,omitempty"`
	// OpID correlates every attempt of one create gesture, so a repeat resolves
	// to the chat the first attempt made. Optional; unlike Idempotency-Key it
	// covers the fall-through past that cache's TTL (command/create_ledger.go).
	OpID string `json:"op_id,omitempty"`
}

// ResumeSessionCommand is the payload for type="resume_session": adopt a KAS
// session as a new chat, bound to SessionID at creation so the next bridge takes
// the session/load path and the replay projection supplies the transcript.
type ResumeSessionCommand struct {
	// SessionID is the KAS session to adopt. Validated with ids.ValidSessionID.
	SessionID string `json:"session_id"`
	// Name seeds the chat title, normally the session title KAS reported.
	Name string `json:"name,omitempty"`
	// OpID correlates every attempt of one resume. See CreateChatCommand.OpID.
	OpID string `json:"op_id,omitempty"`
}

// ForkChatCommand is the payload for type="fork_chat": start a TANGENT off
// another chat, beginning with the parent's conversation behind it and diverging
// from there, with nothing syncing the two afterwards.
//
// The new chat's id is minted SERVER-SIDE and returned, so the envelope's ChatID
// is normally empty; ParentChatID names the one being forked. Title is optional
// and rides `_meta.kiro.title` into KAS's session metadata; it is not the chat
// name, which stays the ordinary naming precedence.
type ForkChatCommand struct {
	ParentChatID ChatID `json:"parent_chat_id"`
	Title        string `json:"title,omitempty"`
	// OpID correlates every attempt of one fork; without it a retry would fork
	// again, producing a second session and chat. See CreateChatCommand.OpID.
	OpID string `json:"op_id,omitempty"`
}

// ForkOutcomePrimed and ForkOutcomeForked are the two paths fork_chat can take.
// `forked` means KAS returned a session id carrying the parent's context;
// `primed` is the degraded path where the fork was refused and the parent's
// transcript is injected as a bounded priming prompt instead. The tangent opens
// either way; only its fidelity differs.
const (
	ForkOutcomeForked = "forked"
	ForkOutcomePrimed = "primed"
)

// SwitchModelCommand is the payload for type="switch_model". Ends the current
// ACP session and starts a fresh one with the new model, priming it with the
// transcript: kiro-cli cannot swap models mid-session. A non-empty Model must
// match validIdent. Agent changes are deliberately NOT part of this command —
// the agent owns its own mode switches.
type SwitchModelCommand struct {
	// Empty or "auto" keeps the current model: a bare restart of the bridge,
	// useful when the session is wedged.
	Model string `json:"model,omitempty"`
}

// PermissionResponseCommand is the payload for type="permission_response".
type PermissionResponseCommand struct {
	// FileDecisions answers a TURN APPROVAL: action id → accept. KAS treats an
	// OMITTED id as a reject, so the client sends a decision per offered file.
	FileDecisions map[string]bool `json:"file_decisions,omitempty"`
	OptionID      string          `json:"option_id"`
	RequestID     int64           `json:"request_id"`
}

// ElicitationResponseCommand is the payload for type="elicitation_response".
// RequestID echoes the elicitation_needed event. Action is "accept" | "decline"
// | "cancel"; Content carries the filled form values on accept only, forwarded
// verbatim to kiro-cli.
type ElicitationResponseCommand struct {
	Action    string          `json:"action"`
	Content   json.RawMessage `json:"content,omitempty"`
	RequestID int64           `json:"request_id"`
}

// UserInputActionAnswered and UserInputActionDismissed are the accepted actions
// for type="user_input_response". KAS treats anything non-answered as dismissed
// and advances the agent to its next phase, so no other values exist.
const (
	UserInputActionAnswered  = "answered"
	UserInputActionDismissed = "dismissed"
)

// UserInputResponseCommand is the payload for type="user_input_response".
// RequestID echoes the user_input_needed event. Answer is the answer TEXT
// (kiro-cli's contract is a plain string): an option's title, "Title [Sub1,
// Sub2]" for sub-options, or the typed text. Required when Action is "answered".
type UserInputResponseCommand struct {
	Action    string `json:"action"`
	Answer    string `json:"answer,omitempty"`
	RequestID int64  `json:"request_id"`
}

// RewindChatCommand is the payload for type="rewind_chat": revert THIS chat to
// a past turn, dropping the addressed message and everything after it and
// rolling the files back.
//
// MessageID must name a USER message, which is what KAS's revert verb addresses
// and enforces. Vibekit resolves a turn footer's Rewind to the FOLLOWING turn's
// user message, never the clicked turn's own: KAS drops the addressed message
// inclusive, so keeping turn N means addressing turn N+1.
type RewindChatCommand struct {
	MessageID string `json:"message_id"`
}

// SetEffortCommand is the payload for type="set_effort", applying a reasoning
// effort level to the active session.
type SetEffortCommand struct {
	// Level is a tier id from the model's own catalog, shape-validated only:
	// KAS owns the vocabulary.
	Level EffortLevel `json:"level"`
}

// SetDraftCommand is the payload for type="set_draft": the composer text typed
// into this chat and not sent.
//
// Text is capped at MaxDraftBytes and may be EMPTY — that is how a sent or
// abandoned message is cleared, so it is a value rather than a missing field. A
// NO-OP on a chat that is not a server record yet, because auto-creating one
// would put a sidebar row on every client for a chat nobody has sent anything to.
type SetDraftCommand struct {
	Text string `json:"text"`
}

// SetAttachmentsCommand is the payload for type="set_attachments": the paths
// staged beside this chat's draft, the whole list every time rather than an add
// or a remove, because a per-file delta would need an ordering the wire does not
// carry. An empty list is a VALUE, and like set_draft this is a NO-OP on a chat
// that is not a server record yet. Capped at MaxAttachments entries of
// MaxAttachmentPathBytes each, and confined at the READ (BuildPromptBlocks)
// rather than here.
type SetAttachmentsCommand struct {
	Paths []string `json:"paths"`
}

// SetModeCommand is the payload for type="set_mode". ModeID names an entry in
// the chat's AvailableModes — bundled workflow modes and workspace custom agents
// alike. Applied to a live session in place; for a chat whose bridge has not
// started the mode is persisted and applied at session/new (StartOpts.Mode).
type SetModeCommand struct {
	ModeID string `json:"mode_id"`
}

// EffortLevel is a typed enum for reasoning effort levels.
type EffortLevel string

// Well-known effort levels, used where vibekit itself picks a tier. Not the
// valid set: the per-model catalog owns which tiers exist.
const (
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortXHigh  EffortLevel = "xhigh"
	EffortMax    EffortLevel = "max"
)

// Valid reports whether e is plausibly an effort-level id: a short lowercase
// token (letter first; letters, digits or hyphens; at most 32 bytes).
//
// Deliberately a SHAPE check, not a closed set: the tier vocabulary is per model
// and upstream-owned, so a model shipped after this build must work unchanged.
// KAS stays the authority on which tiers a session accepts.
func (e EffortLevel) Valid() bool {
	if len(e) == 0 || len(e) > 32 {
		return false
	}
	for i := range len(e) {
		c := e[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c == '-' || (c >= '0' && c <= '9')):
		default:
			return false
		}
	}
	return true
}

// SetSupervisedModeCommand is the payload for type="set_supervised_mode".
// Records the chat's supervised choice and, on a running session, sets KAS's
// `autopilot` config option. It gates nothing itself: KAS holds the turn's writes
// and asks for one approval, so there is no vibekit-side queue to drain.
type SetSupervisedModeCommand struct {
	Enabled bool `json:"enabled"`
}

// SteerCommand is the payload for type="steer": a message that joins the RUNNING
// turn rather than waiting for it to end, which is what a prompt typed mid-turn
// becomes. KAS merges it into the live turn at the next node boundary.
//
// Text is required, trimmed, and capped at maxSteerBytes. There is no
// Attachments field, and that is a wire constraint: `_session/steer` takes a
// plain string, so a content block has nowhere to go.
type SteerCommand struct {
	Text      string `json:"text"`
	MessageID string `json:"message_id"`
}

// OpenTabCommand is the payload for type="open_tab": open a tab for something
// that ALREADY EXISTS. It never mints, so (Kind, Ref) is a key and never a create
// directive — a chat is created by create_chat.
//
// An open for a (Kind, Ref) that is already open mutates nothing, bumps no
// version and emits no event; the response's `created:false` is what makes that
// observable, so a client resolving on the event does not wait forever.
type OpenTabCommand struct {
	// Kind must be one of the nine (TabKind.Valid).
	Kind TabKind `json:"kind"`
	// Ref is required for every kind but a singleton, where it must be empty. A
	// chat ref is checked against the chat store, which is what makes an open
	// racing a delete a refusal rather than a tab pointing at nothing.
	Ref string `json:"ref,omitempty"`
	// Parent names an already-open tab to hang this one under; one that is not
	// open promotes the new tab to top level rather than refusing it.
	Parent string `json:"parent,omitempty"`
	// OpID correlates the frame this open produces with the dispatch that asked
	// for it. See CreateChatCommand.OpID.
	OpID string `json:"op_id,omitempty"`
	// Owns means closing this tab tears down what it shows. The client decides,
	// because only the caller knows whether it launched the thing.
	Owns bool `json:"owns,omitempty"`
}

// CloseTabCommand is the payload for type="close_tab".
//
// Closing an id that is not open is NOT an error: two devices can close the same
// tab. For a CHAT tab this also runs the chat teardown — cancel the turn, cancel
// the chat's runs, tear the bridge down — and KEEPS the record under retention,
// so reopening it session/loads everything back.
type CloseTabCommand struct {
	ID   string `json:"id"`
	OpID string `json:"op_id,omitempty"`
}

// ReorderTabsCommand is the payload for type="reorder_tabs": the whole expanded
// order, every open tab exactly once.
//
// There is deliberately NO base-version precondition. The exact-set check IS
// sufficient — an order naming the set the server holds cannot have come from a
// set it does not hold — while a version precondition would discard a valid drag
// whenever an unrelated mutation landed first. A set mismatch is a 409.
type ReorderTabsCommand struct {
	OpID  string   `json:"op_id,omitempty"`
	Order []string `json:"order"`
}

// PinTabCommand is the payload for type="pin_tab". Idempotent in both
// directions: a tab already in that state bumps no version and emits nothing.
type PinTabCommand struct {
	ID     string `json:"id"`
	OpID   string `json:"op_id,omitempty"`
	Pinned bool   `json:"pinned"`
}
