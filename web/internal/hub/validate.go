package hub

import (
	"regexp"

	"vibekit/internal/api"
)

// maxMessageIDBytes caps the length of client-supplied message ids.
// The generator in transport.ts produces ~40-char strings; 128 gives
// plenty of headroom for custom clients while bounding what the
// server persists and echoes over SSE.
const maxMessageIDBytes = 128

// maxRequestIDBytes caps client-supplied request_id length. The
// idempotency cache uses the request_id verbatim as a map key —
// without a cap a misbehaving client could pin a megabyte-scale
// string in the cache for 5 minutes. 128 matches validMessageID's
// ceiling so every correctly-generated client id passes.
const maxRequestIDBytes = 128

// validMessageIDRe restricts client-supplied message ids to a safe
// character set. Rejects newlines, control characters, and anything
// that could confuse downstream renderers — message IDs round-trip
// through SSE `id:` fields and JSON payloads, so control characters
// here would corrupt the wire framing.
var validMessageIDRe = regexp.MustCompile(`^[A-Za-z0-9_.\-:]+$`)

// validMessageID reports whether id is safe to echo on SSE and store
// on disk as the ID field of a message. Callers that receive the id
// from a client must call this before persisting.
func validMessageID(id string) bool {
	if id == "" || len(id) > maxMessageIDBytes {
		return false
	}
	return validMessageIDRe.MatchString(id)
}

// validIdent reports whether s is a safe agent or model identifier.
// Delegates to api.ValidIdent — the single source of truth.
func validIdent(s string) bool {
	return api.ValidIdent(s)
}

// validChatID reports whether id is safe to use as a chat identifier.
// Delegates to api.ValidChatID — the single source of truth for chat
// ID validation. Empty strings are rejected separately via
// errMissingChatID so the error message is more specific.
func validChatID(id api.ChatID) bool {
	return api.ValidChatID(string(id))
}
