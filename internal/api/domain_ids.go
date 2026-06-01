package api

import "vibekit/internal/ids"

// SecretMask is the placeholder value returned for every secret on
// public reads. Clients send this unchanged on update to keep the
// stored value; any other value replaces it.
const SecretMask = "***"

// DefaultChatName is the fallback name for newly created chats when the
// client does not supply one.
const DefaultChatName = "New conversation"

// MaxChatNameBytes caps the byte length of chat names at creation and
// rename boundaries. All code paths that set Chat.Name should enforce
// this limit.
const MaxChatNameBytes = 512

// NewChatID generates a fresh UUIDv7 chat identifier. Time-ordered,
// globally unique, standard format.
func NewChatID() ChatID {
	return ChatID(ids.NewMessageID())
}

// ErrMsgUtilityUnavailable is the canonical error message returned when
// the utility bridge (LLM prompt function) is not wired. Used by both
// the git and server packages to keep the error string in one place.
const ErrMsgUtilityUnavailable = "utility bridge not available"

// ChatID is a typed wrapper for chat identifiers. The underlying string
// marshals identically to JSON, preserving the wire contract.
type ChatID string

// String implements fmt.Stringer for logging convenience.
func (c ChatID) String() string { return string(c) }

// SessionID is a typed wrapper for ACP session identifiers. Values are
// validated via ValidSessionID before assignment; the type makes
// invalid-state propagation a compile-time-visible decision.
type SessionID string

// Valid reports whether the session id passes path-safety validation.
func (s SessionID) Valid() bool { return ValidSessionID(string(s)) }

// String implements fmt.Stringer for logging convenience.
func (s SessionID) String() string { return string(s) }

// ModelID is a typed wrapper for model identifiers. Values are
// validated via ValidIdent before assignment; the type makes
// invalid-state propagation a compile-time-visible decision.
type ModelID string

// Valid reports whether the model id passes identifier validation.
// Empty is valid (the field is optional).
func (m ModelID) Valid() bool { return ValidIdent(string(m)) }

// String implements fmt.Stringer for logging convenience.
func (m ModelID) String() string { return string(m) }
