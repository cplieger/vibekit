package chat

import (
	"fmt"

	"github.com/cplieger/vibekit/internal/api"
)

// The typed StoreError / ErrorKind pair that used to live here is DELETED, not
// relocated. It existed for two consumers, both of which are gone: the archive
// service's id-in-use mapping (chats no longer move, so nothing can collide with
// a restore) and writeChatErr's errors.As + switch dispatch (the archived-chat
// endpoints it served no longer exist). Nothing in production constructed one,
// and nothing consumed an ErrorKind.
//
// Do not reintroduce it speculatively: the store's remaining errors are either
// wrapped filesystem errors, which callers already handle with errors.Is against
// os.ErrNotExist, or the two canonical values below.

// errInvalidChatID returns the canonical error for a malformed chat ID.
// Single source of truth for the error message format.
func errInvalidChatID(id api.ChatID) error {
	return fmt.Errorf("invalid chat id: %q", id)
}

// errChatIDMismatch reports a chat whose own ID disagrees with the chat file it
// was about to be written to. Loud rather than repaired, and never silently
// retargeted: the destination is derived from the id the caller asked for and
// serialised by THAT id's mutex, so writing an object carrying a different id
// would put a whole chat over another chat's file, under the wrong lock, racing
// any legitimate write to it. The only way to reach this is a file whose stored
// id is not its filename, so the message names both halves for the operator who
// has to go and look.
func errChatIDMismatch(want api.ChatID, got string) error {
	return fmt.Errorf("chat %q holds id %q: refusing to write it over another chat's file", want, got)
}

// errMsgChatNotFound is the canonical HTTP response message for
// missing or tombstoned chats. Single source of truth so all
// not-found responses use the same string.
const errMsgChatNotFound = "chat not found"
