package hub

import (
	"vibekit/internal/api"
)

// validMessageID reports whether id is safe to echo on SSE and store
// on disk as the ID field of a message. Delegates to api.ValidMessageID
// — the single source of truth.
func validMessageID(id string) bool {
	return api.ValidMessageID(id)
}

// validIdent reports whether s is a safe agent or model identifier.
// Delegates to api.ValidIdent — the single source of truth.
func validIdent(s string) bool {
	return api.ValidIdent(s)
}

// validChatID reports whether id is safe to use as a chat identifier.
// Delegates to api.ValidChatID — the single source of truth for chat
// ID validation. Empty strings are rejected separately via
// command.ErrMissingChatID so the error message is more specific.
func validChatID(id api.ChatID) bool {
	return api.ValidChatID(string(id))
}
