package command

import (
	"errors"
	"fmt"
	"regexp"

	"vibekit/internal/api"
	"vibekit/internal/pending"
)

// maxRequestIDBytes caps client-supplied request_id length.
const maxRequestIDBytes = 128

// maxPromptBytes caps the text field of a prompt command.
const maxPromptBytes = 512 * 1024

// maxMessageIDBytes caps the length of client-supplied message ids.
const maxMessageIDBytes = 128

// validMessageIDRe restricts client-supplied ids to a safe character set.
var validMessageIDRe = regexp.MustCompile(`^[A-Za-z0-9_.\-:]+$`)

// Static command errors returned to the client.
var (
	errMissingChatID    = errors.New("missing chat_id")
	errInvalidPayload   = errors.New("invalid payload")
	errEmptyPrompt      = errors.New("empty prompt")
	errPromptTooLong    = errors.New("prompt too long")
	errMissingMessageID = errors.New("missing message_id")
	errNoBridge         = errors.New("no bridge")
	errBusy             = errors.New("busy")
	errChatNotFound     = errors.New("chat not found")

	errResolveBadAction  = errors.New("action must be accept or reject")
	errResolveMissingID  = errors.New("tool_call_id is required")
	errSetSupervisedMode = errors.New("chat not found")
	errResolveUnknown    = errors.New("no such pending change")
	errMergedTooLarge    = fmt.Errorf("merged_text exceeds %d byte cap", pending.Cap)

	errTaskRequired      = errors.New("task is required")
	errSubSessionAndText = errors.New("sub_session_id and text are required")
)

// validChatID reports whether id is safe to use as a chat identifier.
func validChatID(id api.ChatID) bool {
	return api.ValidChatID(string(id))
}

// validRequestID reports whether the given request_id is safe to use
// as an idempotency cache key.
func validRequestID(id string) bool {
	if id == "" {
		return true
	}
	if len(id) > maxRequestIDBytes {
		return false
	}
	return validMessageIDRe.MatchString(id)
}

// ValidMessageID reports whether id is safe to echo on SSE and store
// on disk as the ID field of a message.
func ValidMessageID(id string) bool {
	if id == "" || len(id) > maxMessageIDBytes {
		return false
	}
	return validMessageIDRe.MatchString(id)
}

// ValidIdent reports whether s is a safe agent or model identifier.
func ValidIdent(s string) bool {
	return api.ValidIdent(s)
}
