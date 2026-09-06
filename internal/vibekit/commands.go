package vibekit

// Client commands: the wire shapes for POST /api/command envelopes +
// per-type payloads. Command routing lives in agent/command.go; this file
// just declares the contracts.

import "encoding/json"

// CommandType identifies the kind of client command posted to
// /api/command. Using a typed string instead of bare literals makes
// typos a compile error and the full command vocabulary discoverable.
type CommandType string

// Command type constants. Every value here corresponds to a key in
// agent/command.go's registerCommandHandlers dispatch map.
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
	// The four tab commands. Membership is a MUTATION, so it rides this envelope
	// like every other one (invariant 1) rather than a second REST surface with
	// its own failure semantics — which would also have needed two client verbs
	// api-client.ts does not have.
	//
	// There is no close_chat any more. The × on a chat tab is close_tab, which
	// runs the same teardown; keeping both would have left two commands that mean
	// the same gesture and disagree about the tab.
	CmdOpenTab     CommandType = "open_tab"
	CmdCloseTab    CommandType = "close_tab"
	CmdReorderTabs CommandType = "reorder_tabs"
	CmdPinTab      CommandType = "pin_tab"
)

// ClientCommand is the envelope for every command the browser posts.
// Type determines how Payload unmarshals.
//
// There is no request_id. Idempotency is the Idempotency-Key HEADER, handled by
// one middleware for every mutating route (see internal/server/idempotency.go).
// It used to be a body field here, which forced a second dedup cache inside the
// command dispatcher — one that had no in-flight marker, so two concurrent
// duplicates both executed. Payloads that still carry a request_id (permission
// and elicitation responses) mean something else by it: the ACP request id being
// answered.
type ClientCommand struct {
	Type    CommandType     `json:"type"`
	ChatID  ChatID          `json:"chat_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// PromptCommand is the payload for type="prompt".
//
// Text is required, trimmed, and capped at 512 KiB; oversize returns
// HTTP 413. MessageID is required and must match the ULID/id character
// set enforced by the runtime's validMessageID check (128-byte cap, no
// control chars). Model is optional; if non-empty it must match
// validIdent (ASCII alphanumerics plus `_.-`, 1-128 bytes).
// Attachments are resolved via resolveInsideWorkDir before read.
type PromptCommand struct {
	Text        string       `json:"text"`
	MessageID   string       `json:"message_id"` // client-generated ULID
	Model       string       `json:"model,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"` // files attached via pill row
}

// Attachment is a file attached to a prompt. The server reads the file
// and decides whether to send it as a document content block (PDF, DOCX,
// etc.) or a text path reference based on the extension.
type Attachment struct {
	Path string `json:"path"` // workspace-relative path
	Name string `json:"name"` // display name (filename.ext)
}

// CreateChatCommand is the payload for type="create_chat".
//
// Name is optional and capped at 512 bytes (maxChatNameBytes); empty
// values default to "New conversation". Model is optional; if non-empty
// it must match validIdent (ASCII alphanumerics plus `_.-`, 1-128
// bytes). Validation failures return HTTP 400.
type CreateChatCommand struct {
	Name  string `json:"name,omitempty"`
	Model string `json:"model,omitempty"`
	// OpID correlates every attempt of ONE create gesture, so a repeat resolves
	// to the chat the first attempt made instead of minting a second one. It is
	// NOT the Idempotency-Key: that is a header covering a retry inside the
	// idempotency cache's TTL, and this covers the fall-through past it (see
	// command/create_ledger.go). Optional — a caller that sends none gets a fresh
	// chat per attempt, because there is no key to record one under. Validated
	// with ValidIdent: it reaches an in-memory map key, so the bound is what
	// matters.
	OpID string `json:"op_id,omitempty"`
}

// ResumeSessionCommand is the payload for type="resume_session": adopt a KAS
// session the previous-session picker listed (GET /api/sessions) as a new
// chat. The chat is created already bound to SessionID, so the next bridge
// takes the session/load path and the replay projection supplies the
// transcript — vibekit copies no messages.
type ResumeSessionCommand struct {
	// SessionID is the KAS session to adopt. Validated with ids.ValidSessionID.
	SessionID string `json:"session_id"`
	// Name seeds the chat title, normally the session title KAS reported.
	Name string `json:"name,omitempty"`
	// OpID correlates every attempt of one resume, so a retry answers with the
	// chat already bound to this session rather than binding a second chat to it.
	// See CreateChatCommand.OpID.
	OpID string `json:"op_id,omitempty"`
}

// ForkChatCommand is the payload for type="fork_chat": start a TANGENT off
// another chat — a new chat that begins with the parent's real conversation
// behind it and then diverges, with nothing syncing the two afterwards.
//
// The new chat's id is MINTED SERVER-SIDE and returned in the response, so the
// envelope's ChatID is normally empty; ParentChatID names the one being forked.
// The server calls KAS's own `session/fork` on the parent's live session and
// binds the reply's session id to the new chat, so the context is the parent's
// actual context rather than a re-narration of it.
//
// Title is optional and rides `_meta.kiro.title` into KAS's session metadata,
// which is where its own /tangent puts a tangent's name. Vibekit does not use it
// as the chat name — that stays the ordinary naming precedence (the agent's focus
// title, then the first prompt's truncation).
type ForkChatCommand struct {
	ParentChatID ChatID `json:"parent_chat_id"`
	Title        string `json:"title,omitempty"`
	// OpID correlates every attempt of one fork. Without it a retry would ask KAS
	// to fork again, producing a second session and a second chat — and on the
	// primed fallback, spending the priming budget twice. See
	// CreateChatCommand.OpID.
	OpID string `json:"op_id,omitempty"`
}

// ForkOutcomePrimed and ForkOutcomeForked are the two paths fork_chat can take,
// reported back so the client and the log agree on which one ran.
//
// `forked` is the real thing: KAS returned a new session id carrying the parent's
// context. `primed` is the degraded path — the fork was refused, so the tangent
// is a fresh chat whose first session gets the parent's transcript injected as an
// invisible priming prompt, bounded by the priming budget. The tangent opens
// either way; only its fidelity differs.
const (
	ForkOutcomeForked = "forked"
	ForkOutcomePrimed = "primed"
)

// SwitchModelCommand is the payload for type="switch_model". Ends the
// current ACP session and starts a fresh one with a new model, priming
// the replacement with the chat transcript so kiro-cli has context.
// kiro-cli cannot swap models mid-session, so we rebuild the bridge.
// Agent changes are intentionally NOT part of this command — the agent
// itself is responsible for switching modes, and the user cannot force
// an agent change on a live chat from the client.
//
// Model accepted forms: empty or "auto" keep the current model (a bare
// restart of the bridge, useful when the session is wedged); any other
// value must match validIdent (ASCII alphanumerics plus `_.-`, 1-128
// bytes).
type SwitchModelCommand struct {
	// Target model id. Empty or "auto" = keep current model (a bare
	// restart of the bridge, useful when the session is wedged).
	Model string `json:"model,omitempty"`
}

// PermissionResponseCommand is the payload for type="permission_response".
type PermissionResponseCommand struct {
	// FileDecisions answers a TURN APPROVAL: action id → accept. Absent on an
	// ordinary tool permission.
	//
	// KAS treats an OMITTED id as a reject (it restores every action not in the
	// accepted set), so a client sending a partial map silently discards the files
	// it left out. The client sends a decision per offered file.
	FileDecisions map[string]bool `json:"file_decisions,omitempty"`
	OptionID      string          `json:"option_id"`
	RequestID     int64           `json:"request_id"`
}

// ElicitationResponseCommand is the payload for type="elicitation_response".
// RequestID echoes the value from the elicitation_needed event. Action is
// "accept" | "decline" | "cancel"; Content carries the filled form values
// (an object) only on accept and is forwarded verbatim to kiro-cli.
type ElicitationResponseCommand struct {
	Action    string          `json:"action"`
	Content   json.RawMessage `json:"content,omitempty"`
	RequestID int64           `json:"request_id"`
}

// UserInputActionAnswered and UserInputActionDismissed are the accepted
// actions for type="user_input_response". "answered" carries the user's
// answer text; "dismissed" (dialog closed / question skipped) makes the
// agent advance to its next phase — KAS treats anything non-answered that
// way, so no other action values exist.
const (
	UserInputActionAnswered  = "answered"
	UserInputActionDismissed = "dismissed"
)

// UserInputResponseCommand is the payload for type="user_input_response".
// RequestID echoes the value from the user_input_needed event. Answer is
// the user's answer TEXT (kiro-cli's contract is a plain string the model
// reads): a clicked option sends its title; an option with sub-options
// sends "Title [Sub1, Sub2]" (the TUI's format); free-form sends the typed
// text. Required (non-empty) when Action is "answered".
type UserInputResponseCommand struct {
	Action    string `json:"action"`
	Answer    string `json:"answer,omitempty"`
	RequestID int64  `json:"request_id"`
}

// RewindChatCommand is the payload for type="rewind_chat".
//
// Reverts THIS chat to a past turn: the addressed message and everything after
// it are dropped and the files roll back. It used to branch a second chat.
//
// A message id, not a turn index, because that is what KAS's revert verb
// addresses — and it must name a USER message, which KAS enforces (a non-user
// target comes back `success:false` naming the type it found).
//
// Vibekit resolves a turn footer's Rewind to the FOLLOWING turn's user message
// (`turns.ts` sends `turns[i+1].trigger`), never to the clicked turn's own: KAS
// drops the addressed message inclusive, so keeping turn N means addressing
// turn N+1. A reader who assumed the nearest PRECEDING user message would
// conclude a rewind reverts to the start of the clicked turn, which is one turn
// too far back.
type RewindChatCommand struct {
	MessageID string `json:"message_id"`
}

// SetEffortCommand is the payload for type="set_effort".
// Applies a reasoning effort level to the active session.
type SetEffortCommand struct {
	// Level is a tier id from the model's own catalog ("low".."max", "none"
	// on models that offer it). Shape-validated only; KAS owns the vocabulary.
	Level EffortLevel `json:"level"`
}

// SetDraftCommand is the payload for type="set_draft": the composer text the
// user has typed into this chat and not sent.
//
// Text is capped at MaxDraftBytes and may be empty — an empty draft is how a
// sent or abandoned message is cleared, so it is a value rather than a missing
// field. The command is a NO-OP on a chat that is not a server record yet
// (every chat is client-side until its first prompt); auto-creating one would
// put a row in every client's sidebar for a chat nobody has sent anything to.
type SetDraftCommand struct {
	Text string `json:"text"`
}

// SetAttachmentsCommand is the payload for type="set_attachments": the files the
// user has staged beside this chat's draft and not yet sent.
//
// Paths only, and the whole list every time rather than an add or a remove: the
// client holds the authoritative pill row and saves it on the draft's own
// debounce, so a per-file delta would need an ordering the wire does not carry.
// An empty list is a VALUE, not a missing field — it is how a sent or emptied row
// is cleared — and like set_draft this is a NO-OP on a chat that is not a server
// record yet, because auto-creating one would put a sidebar row on every client
// for a chat nobody has sent anything to.
//
// Capped at MaxAttachments entries of MaxAttachmentPathBytes each. The paths are
// NOT confined to the workspace here: BuildPromptBlocks resolves each one at
// send time and falls back to a bare path reference on escape, so confinement
// lives at the read rather than at the parking spot, and a command that only
// hands the list back to the client that sent it has nothing to confine it for.
type SetAttachmentsCommand struct {
	Paths []string `json:"paths"`
}

// SetModeCommand is the payload for type="set_mode". ModeID is the id of
// an entry in the chat's AvailableModes — on v3 that spans the bundled
// workflow modes (vibe/spec/plan/…) AND workspace custom agents, all
// switched via session/set_mode. Applied to the live session in place;
// for a chat whose bridge hasn't started yet the mode is persisted and
// applied when session/new completes (StartOpts.Mode).
type SetModeCommand struct {
	ModeID string `json:"mode_id"`
}

// EffortLevel is a typed enum for reasoning effort levels.
type EffortLevel string

// Well-known effort levels, used where vibekit itself picks a tier (the
// utility bridge's per-task effort). Not the valid set: Valid is a shape
// check, and the per-model catalog owns which tiers exist.
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
// Deliberately a SHAPE check, not a closed set: the tier vocabulary is per
// model and upstream-owned (gpt-luna ships a "none" tier the old five-member
// enum rejected at the command boundary), so a model shipped after this build
// must work unchanged. KAS stays the authority on which tiers a session
// accepts — set_effort is fail-fast against the live session, and the client
// only marks tiers the model's own catalog lists, so a shape-valid but
// unknown level self-heals instead of persisting a lie.
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
// `autopilot` config option. It gates nothing itself: KAS holds the turn's
// writes and asks for one approval, so there is no vibekit-side queue for a
// toggle to drain, and no resolve/trust/partial companion commands.
type SetSupervisedModeCommand struct {
	Enabled bool `json:"enabled"`
}

// SteerCommand is the payload for type="steer": a message that joins the
// RUNNING turn rather than waiting for it to end.
//
// This is what a prompt typed mid-turn becomes. It replaced a client-side
// queue that held the text until `turn_ended` and then sent it as a NEW turn —
// so the agent finished the work you were trying to redirect before it read the
// redirection. The other half of that queue was a "send now" button that
// CANCELLED the turn to jump ahead, discarding whatever it had done since its
// last durable step. A steer needs neither: KAS merges the message into the
// live turn's context at the next node boundary.
//
// Text is required, trimmed, and capped at maxSteerBytes. MessageID is
// client-generated like a prompt's and travels so the id space stays the
// client's; KAS prefixes it `steer-` if it is not already.
//
// There is no Attachments field, and that is a wire constraint rather than an
// omission: `_session/steer` takes a plain string, so there is nowhere for a
// content block to go. The client degrades an attachment to the path reference
// it already falls back to for unsupported types.
type SteerCommand struct {
	Text      string `json:"text"`
	MessageID string `json:"message_id"`
}

// OpenTabCommand is the payload for type="open_tab": open a tab for something
// that ALREADY EXISTS.
//
// It never mints. A chat is created by create_chat, which opens its tab through
// the same coordinator, so (Kind, Ref) here is a key and never a create
// directive — the two were one operation before this split, which is how a
// client ended up minting chat ids so it would have something to open.
//
// An open for a (Kind, Ref) that is already open mutates nothing, bumps no
// version and emits no event, and the response's `created:false` is what makes
// that observable. Without it, a client that resolves on the event would wait
// forever for a frame the server correctly never sends.
type OpenTabCommand struct {
	// Kind must be one of the nine (TabKind.Valid).
	Kind TabKind `json:"kind"`
	// Ref is required for every kind but a singleton, where it must be empty. A
	// chat ref is validated as a chat id AND checked against the chat store,
	// which is what makes an open racing a delete a refusal rather than a tab
	// pointing at nothing.
	Ref string `json:"ref,omitempty"`
	// Parent names an already-open tab to hang this one under. A parent that is
	// not open promotes the new tab to top level rather than refusing it.
	Parent string `json:"parent,omitempty"`
	// OpID correlates the frame this open produces with the dispatch that asked
	// for it. See CreateChatCommand.OpID for how it differs from
	// Idempotency-Key.
	OpID string `json:"op_id,omitempty"`
	// Owns means closing this tab tears down what it shows. The client decides
	// it because only the caller knows whether it launched the thing.
	Owns bool `json:"owns,omitempty"`
}

// CloseTabCommand is the payload for type="close_tab".
//
// Closing an id that is not open is NOT an error: two devices can close the same
// tab, and the response's empty `closed` list already says nothing happened.
//
// For a CHAT tab this also runs the chat teardown that close_chat used to be a
// client command for — cancel the turn, cancel the chat's runs, tear the bridge
// down — and KEEPS the record. Under retention a closed chat is a chat without a
// tab, and reopening it session/loads everything back.
type CloseTabCommand struct {
	ID   string `json:"id"`
	OpID string `json:"op_id,omitempty"`
}

// ReorderTabsCommand is the payload for type="reorder_tabs": the whole expanded
// order, every open tab exactly once.
//
// There is deliberately NO base-version precondition. The exact-set check IS the
// precondition and it is sufficient — an order naming the set the server holds
// cannot have been derived from a set it does not hold — while a version
// precondition would discard a perfectly valid drag whenever any unrelated
// mutation landed first, and a pin elsewhere bumps the version without changing
// the order. A set mismatch is a 409.
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
