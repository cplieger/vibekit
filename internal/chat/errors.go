package chat

import (
	"errors"
	"fmt"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// ErrTombstoned reports that a write was DECLINED because the chat id was
// deleted within the tombstone window: nothing reached disk and nothing was
// broadcast. It is the third outcome of the store's single write path, and
// the only one that is not nil — an applied write and a no-op both report
// nil, so without a value of its own a refusal was indistinguishable from
// success.
//
// A caller that must not proceed on a refused write branches on it with
// errors.Is.
var ErrTombstoned = errors.New("chat: id was recently deleted")

// The typed StoreError / ErrorKind pair that used to live here is DELETED,
// not relocated: its two consumers are gone (chats no longer move, and the
// archived-chat endpoints it served no longer exist). Do not reintroduce it
// speculatively.

// errInvalidChatID returns the canonical error for a malformed chat ID.
// Single source of truth for the error message format.
func errInvalidChatID(id vibekit.ChatID) error {
	return fmt.Errorf("invalid chat id: %q", id)
}

// errChatIDMismatch reports a chat whose own ID disagrees with the chat file
// it was about to be written to. Never silently retargeted: the destination
// is derived from the id the caller asked for and serialised by THAT id's
// mutex, so writing an object carrying a different id would put a whole
// chat over another chat's file under the wrong lock.
func errChatIDMismatch(want vibekit.ChatID, got string) error {
	return fmt.Errorf("chat %q holds id %q: refusing to write it over another chat's file", want, got)
}

// errMsgChatNotFound is the canonical HTTP response message for
// missing or tombstoned chats. Single source of truth so all
// not-found responses use the same string.
const errMsgChatNotFound = "chat not found"
