package command

import (
	"errors"
	"fmt"

	"vibekit/internal/api"
	"vibekit/internal/pending"
)

// maxPromptBytes caps the text field of a prompt command.
const maxPromptBytes = 512 * 1024

// Static command errors returned to the client.
var (
	ErrMissingChatID    = errors.New("missing chat_id")
	ErrInvalidPayload   = errors.New("invalid payload")
	errEmptyPrompt      = errors.New("empty prompt")
	errPromptTooLong    = errors.New("prompt too long")
	errMissingMessageID = errors.New("missing message_id")
	errNoBridge         = errors.New("no bridge")
	errNotRewindChat    = errors.New("not a rewind chat (no parent)")
	errBusy             = errors.New("busy")
	ErrChatNotFound     = errors.New("chat not found")

	errResolveBadAction = errors.New("action must be accept or reject")
	errResolveMissingID = errors.New("tool_call_id is required")
	errResolveUnknown   = errors.New("no such pending change")
	errMergedTooLarge   = fmt.Errorf("merged_text exceeds %d byte cap", pending.Cap)

	errTaskRequired      = errors.New("task is required")
	errSubSessionAndText = errors.New("sub_session_id and text are required")
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
