package command

// No server code reads TabSubject.Owns, so it is a CLIENT contract whose two readers fail in
// opposite directions on a false: the unacknowledged-work cue drops every chat, and the
// client-local teardown leaks the store row, the transcript view and the composer entry on
// every close. CreateChatAndOpen is the one tab-opening site all three creates share.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestCreateChatAndOpen_OpensAnOwnedTab(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)

	opened := createChat(t, mem, "op-owns")

	if !opened.Subject.Owns {
		t.Errorf("the created tab's Owns = false, want true: a chat tab owns the chat it shows")
	}
}

// A tangent's tab nests under its parent's, which reads as "the parent owns it". Nesting is
// a LAYOUT fact; a fork has its own session and bridge, so its tab is the only surface that
// owns them.
func TestCreateChatAndOpen_ATangentsSubTabIsOwnedToo(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)

	parent := createChat(t, mem, "op-parent")
	tangent, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID:       "op-tangent",
		Init:       func(c *vibekit.Chat) { c.Name = vibekit.DefaultChatName },
		ParentChat: vibekit.ChatID(parent.Chat.ID),
	})
	if err != nil {
		t.Fatalf("CreateChatAndOpen(parent %q) = %v, want it to succeed", parent.Chat.ID, err)
	}

	// Fatal: with no nesting the Owns assertion below asserts nothing about a sub-tab.
	if tangent.Subject.Parent != parent.Subject.ID {
		t.Fatalf("the tangent's Parent = %q, want the parent's tab %q", tangent.Subject.Parent, parent.Subject.ID)
	}
	if !tangent.Subject.Owns {
		t.Errorf("a sub-tab's Owns = false, want true: nesting is layout, not authority")
	}
}
