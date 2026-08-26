package vibekit

import (
	"crypto/rand"
	"encoding/hex"
)

// SecretMask is the placeholder value returned for every secret on
// public reads. Clients send this unchanged on update to keep the
// stored value; any other value replaces it.
const SecretMask = "***"

// DefaultChatName is the fallback name for newly created chats when the
// client does not supply one.
const DefaultChatName = "New conversation"

// MaxDraftBytes caps a chat's persisted composer draft.
//
// Two orders of magnitude below a prompt's 512 KiB cap on purpose. A draft is
// re-saved on a 600ms debounce while the user types and is read back on every
// chat open, so its cost is paid repeatedly where a prompt's is paid once; and
// text long enough to exceed this has been pasted rather than typed, which is
// what attachments are for. Shared by the command boundary (which answers 413)
// and the store (which refuses defensively).
const MaxDraftBytes = 16 * 1024

// MaxAttachments caps how many files one chat may have staged beside its draft.
//
// A product limit rather than a decode bound: the pill row is a single line
// under the composer and a prompt naming dozens of files is a directory, which
// the agent reads with its own tools. It sits beside MaxDraftBytes because the
// two bound the same thing — the composer state a debounced autosave rewrites —
// and are enforced at the same two layers: the command boundary answers 413,
// the store refuses defensively.
const MaxAttachments = 32

// MaxAttachmentPathBytes caps one staged path. Deliberately MaxChatNameBytes
// rather than a new number: both are a single filesystem-shaped string the
// client sends, and PATH_MAX is 4096 on Linux while every path this app can
// produce comes from its own file browser under the workspace root.
const MaxAttachmentPathBytes = MaxChatNameBytes

// MaxChatNameBytes caps the byte length of chat names at creation and
// rename boundaries. All code paths that set Chat.Name should enforce
// this limit.
const MaxChatNameBytes = 512

// NewChatID mints a chat identifier: "c-" followed by the hex of 16 bytes.
//
// crypto/rand, not math/rand/v2. The id ADDRESSES a conversation: it is the path
// segment of /chat/{id}, the name of the chat's own JSON file, and the key the
// ACP session chain hangs off, so a guessable id is one a stranger can name
// (go-rulebook §5). rand.Read has been documented since Go 1.24 never to fail,
// so there is no error to return and no branch a caller could act on.
//
// This function did not exist, and the comment here said adding one back "would
// mean the server had invented a chat the client cannot address". That reasoning
// belonged to the rewind BRANCH, the server's only chat-creating path at the
// time: it minted a second chat as a side effect of reverting the one you were
// in, with nothing awaiting a response, so the new id reached no caller. A
// create that RETURNS its chat has no such problem — create_chat, fork_chat and
// resume_session each hand the minted chat back in their response, which is what
// closes the window Session.ghost used to mark and what makes minting here
// correct rather than a regression.
//
// The shape satisfies ids.ValidChatID, and so does the c-<ts>-<rand> the client
// used to mint, so no chat data moves.
func NewChatID() ChatID {
	var b [16]byte
	rand.Read(b[:])
	return ChatID("c-" + hex.EncodeToString(b[:]))
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
// validated via ids.ValidSessionID before assignment; the type makes
// invalid-state propagation a compile-time-visible decision.
type SessionID string

// String implements fmt.Stringer for logging convenience.
func (s SessionID) String() string { return string(s) }

// ModelID is a typed wrapper for model identifiers. Values are
// validated via ids.ValidIdent before assignment; the type makes
// invalid-state propagation a compile-time-visible decision.
type ModelID string

// String implements fmt.Stringer for logging convenience.
func (m ModelID) String() string { return string(m) }
