package command

import (
	"errors"

	"github.com/cplieger/vibekit/internal/api"
)

// maxPromptBytes caps the text field of a prompt command.
const maxPromptBytes = 512 * 1024

// Static command errors returned to the client.
var (
	ErrMissingChatID        = errors.New("missing chat_id")
	ErrInvalidPayload       = errors.New("invalid payload")
	errEmptyPrompt          = errors.New("empty prompt")
	errPromptTooLong        = errors.New("prompt too long")
	errMissingMessageID     = errors.New("missing message_id")
	errNoBridge             = errors.New("no bridge")
	errRewindTargetNotFound = errors.New("rewind target is not a user message in this chat")
	errBusy                 = errors.New("busy")
	ErrChatNotFound         = errors.New("chat not found")
)

// validChatID reports whether id is safe to use as a chat identifier.
func validChatID(id api.ChatID) bool {
	return api.ValidChatID(string(id))
}

// validRequestID reports whether the given request_id is safe to use
// as an idempotency cache key. Delegates to api.ValidRequestID.
func validRequestID(id string) bool {
	return api.ValidRequestID(id)
}

// ValidMessageID reports whether id is safe to echo on SSE and store
// on disk as the ID field of a message. Delegates to api.ValidMessageID.
func ValidMessageID(id string) bool {
	return api.ValidMessageID(id)
}

// ValidIdent reports whether s is a safe agent or model identifier.
func ValidIdent(s string) bool {
	return api.ValidIdent(s)
}
