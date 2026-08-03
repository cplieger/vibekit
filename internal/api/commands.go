package api

// Client commands: the wire shapes for POST /api/command envelopes +
// per-type payloads. Command routing lives in hub/command.go; this file
// just declares the contracts.

import "encoding/json"

// CommandType identifies the kind of client command posted to
// /api/command. Using a typed string instead of bare literals makes
// typos a compile error and the full command vocabulary discoverable.
type CommandType string

// Command type constants. Every value here corresponds to a key in
// hub/command.go's registerCommandHandlers dispatch map.
const (
	CmdCreateChat          CommandType = "create_chat"
	CmdResumeSession       CommandType = "resume_session"
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
	CmdSetMode             CommandType = "set_mode"
	CmdCreateHook          CommandType = "create_hook"
	CmdSetSupervisedMode   CommandType = "set_supervised_mode"
)

// ClientCommand is the envelope for every command the browser posts.
// Type determines how Payload unmarshals.
type ClientCommand struct {
	Type      CommandType     `json:"type"`
	RequestID string          `json:"request_id"`
	ChatID    ChatID          `json:"chat_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// PromptCommand is the payload for type="prompt".
//
// Text is required, trimmed, and capped at 512 KiB; oversize returns
// HTTP 413. MessageID is required and must match the ULID/id character
// set enforced by the hub's validMessageID check (128-byte cap, no
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
}

// ResumeSessionCommand is the payload for type="resume_session": adopt a KAS
// session the previous-session picker listed (GET /api/sessions) as a new
// chat. The chat is created already bound to SessionID, so the next bridge
// takes the session/load path and the replay projection supplies the
// transcript — vibekit copies no messages.
type ResumeSessionCommand struct {
	// SessionID is the KAS session to adopt. Validated with ValidSessionID.
	SessionID string `json:"session_id"`
	// Name seeds the chat title, normally the session title KAS reported.
	Name string `json:"name,omitempty"`
}

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
// target comes back `success:false` naming the type it found). Vibekit resolves
// a click on any turn to its nearest preceding user message before sending.
type RewindChatCommand struct {
	MessageID string `json:"message_id"`
}

// SetEffortCommand is the payload for type="set_effort".
// Applies a reasoning effort level to the active session.
type SetEffortCommand struct {
	Level EffortLevel `json:"level"` // "low" | "medium" | "high" | "xhigh" | "max"
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

// Valid effort level constants.
const (
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortXHigh  EffortLevel = "xhigh"
	EffortMax    EffortLevel = "max"
)

// Valid reports whether e is a recognised effort level.
func (e EffortLevel) Valid() bool {
	switch e {
	case EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return true
	}
	return false
}

// SetSupervisedModeCommand is the payload for type="set_supervised_mode".
// Records the chat's supervised choice and, on a running session, sets KAS's
// `autopilot` config option. It gates nothing itself: KAS holds the turn's
// writes and asks for one approval, so there is no vibekit-side queue for a
// toggle to drain, and no resolve/trust/partial companion commands.
type SetSupervisedModeCommand struct {
	Enabled bool `json:"enabled"`
}
