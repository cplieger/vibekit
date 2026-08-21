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
	ErrMissingChatID        = errors.New("missing chat_id")
	ErrInvalidPayload       = errors.New("invalid payload")
	errEmptyPrompt          = errors.New("empty prompt")
	errPromptTooLong        = errors.New("prompt too long")
	errDraftTooLong         = errors.New("draft too long")
	errMissingMessageID     = errors.New("missing message_id")
	errNoBridge             = errors.New("no bridge")
	errRewindTargetNotFound = errors.New("rewind target is not a user message in this chat")
	errBusy                 = errors.New("busy")
	// errAlreadyAnswered is the 409 for a decision another surface settled
	// first: a second tab, or the unattended floor's deadline. A code rather
	// than prose because the client keys off it to explain the collapsed card.
	errAlreadyAnswered = errors.New("already_answered")
	ErrChatNotFound    = errors.New("chat not found")
)

// validChatID reports whether id is safe to use as a chat identifier.
func validChatID(id vibekit.ChatID) bool {
	return ids.ValidChatID(string(id))
}

// ValidMessageID reports whether id is safe to echo on SSE and store
// on disk as the ID field of a message. Delegates to ids.ValidMessageID.
func ValidMessageID(id string) bool {
	return ids.ValidMessageID(id)
}

// ValidIdent reports whether s is a safe agent or model identifier.
func ValidIdent(s string) bool {
	return ids.ValidIdent(s)
}
