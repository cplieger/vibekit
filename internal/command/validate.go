package command

import (
	"errors"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// maxPromptBytes caps the text field of a prompt command.
const maxPromptBytes = 512 * 1024

// Static command errors returned to the client.
var (
	ErrMissingChatID  = errors.New("missing chat_id")
	ErrInvalidPayload = errors.New("invalid payload")
	errEmptyPrompt    = errors.New("empty prompt")
	errPromptTooLong  = errors.New("prompt too long")
	errDraftTooLong   = errors.New("draft too long")
	// set_attachments' two refusals: too many entries (413), a bad entry (400).
	errTooManyAttachments   = errors.New("too many attachments")
	errBadAttachmentPath    = errors.New("attachment path is empty or too long")
	errMissingMessageID     = errors.New("missing message_id")
	errNoBridge             = errors.New("no bridge")
	errRewindTargetNotFound = errors.New("rewind target is not a user message in this chat")
	// Prose, not a code: the client appends the reason to "Couldn't rewind
	// chat: ", so it reaches the user verbatim.
	errRewindNoSession         = errors.New("this chat has no agent session yet, so there is nothing to roll back to")
	errRewindSessionNotResumed = errors.New("this chat's original session could not be resumed, so there is nothing to roll back to")
	// Both stand in for an internal error the client must not be shown; only
	// the second is worth retrying, once the resume settles.
	errRewindNoBridge      = errors.New("this chat's agent session could not be started, so the rewind was not attempted")
	errRewindReplayPending = errors.New("this chat's history is still being restored, so the rewind was not attempted — try again in a moment")
	errBusy                = errors.New("busy")
	// errAlreadyAnswered is the 409 for a decision another surface settled
	// first. A code rather than prose: the client keys off it.
	errAlreadyAnswered = errors.New("already_answered")
	// errChatNotCreated is the 409 for a chat absent after a Mutate that
	// reported no error — a client-supplied id naming a tombstoned chat.
	errChatNotCreated = errors.New("chat could not be created")
	ErrChatNotFound   = errors.New("chat not found")
)

// validChatID reports whether id is safe to use as a chat identifier.
func validChatID(id vibekit.ChatID) bool {
	return ids.ValidChatID(string(id))
}

// ValidMessageID reports whether id is safe to echo on SSE and store on disk.
func ValidMessageID(id string) bool {
	return ids.ValidMessageID(id)
}

// ValidIdent reports whether s is a safe agent or model identifier.
func ValidIdent(s string) bool {
	return ids.ValidIdent(s)
}
