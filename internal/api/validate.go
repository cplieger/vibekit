package api

import (
	"regexp"
	"strings"
)

// --- Message/Request ID validation ---

// maxMessageIDBytes caps the length of client-supplied message ids.
const maxMessageIDBytes = 128

// maxRequestIDBytes caps client-supplied request_id length.
const maxRequestIDBytes = 128

// validMessageIDRe restricts client-supplied ids to a safe character set.
var validMessageIDRe = regexp.MustCompile(`^[A-Za-z0-9_.\-:]+$`)

// ValidMessageID reports whether id is safe to echo on SSE and store
// on disk as the ID field of a message. This is the single source of
// truth; hub and command packages delegate here.
func ValidMessageID(id string) bool {
	if id == "" || len(id) > maxMessageIDBytes {
		return false
	}
	return validMessageIDRe.MatchString(id)
}

// ValidRequestID reports whether the given request_id is safe to use
// as an idempotency cache key. Empty is valid (field is optional).
func ValidRequestID(id string) bool {
	if id == "" {
		return true
	}
	if len(id) > maxRequestIDBytes {
		return false
	}
	return validMessageIDRe.MatchString(id)
}

// --- Chat ID validation ---

// ErrMsgInvalidChatID is the single source of truth for the HTTP error
// message returned when a chat ID fails validation.
const ErrMsgInvalidChatID = "invalid chat_id"

// ValidChatID reports whether id is a valid chat identifier. Accepts
// ULIDs, UUIDs, and the legacy "chat-<ms>" shape: alphanumerics, hyphens,
// and underscores only. Rejects empty, >128 chars, and anything containing
// path separators or traversal segments. This is the single source of truth
// for chat ID validation; hub/command.go and chat/store.go both delegate here.
func ValidChatID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// --- Identifier validation ---

// identRe is the character set for agent and model identifiers.
var identRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// ValidIdent reports whether s is safe to use as an agent or model
// identifier. Empty strings pass (the field is optional); non-empty
// strings must match identRe AND must not start with '.' or '-' AND
// must not be all-dot strings. This is the single source of truth;
// bridge/bridge.go and hub/command.go both delegate here.
func ValidIdent(s string) bool {
	if s == "" {
		return true
	}
	if !identRe.MatchString(s) {
		return false
	}
	if s[0] == '.' || s[0] == '-' {
		return false
	}
	for _, r := range s {
		if r != '.' {
			return true
		}
	}
	return false
}

// ValidSessionID reports whether s is safe to use as an ACP session id.
// Rejects empty strings, strings over 128 bytes, path separators, NUL,
// and parent-dir references. Session ids are concatenated into filesystem
// paths under ~/.kiro/sessions/cli; this function is the single source of
// truth for that safety gate. bridge/bridge.go delegates here.
func ValidSessionID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	if strings.ContainsAny(s, "/\\\x00") {
		return false
	}
	if s == "." || s == ".." || strings.Contains(s, "..") {
		return false
	}
	return true
}
