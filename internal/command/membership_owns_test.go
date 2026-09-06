package command

// The Owns value the create path opens a chat tab with.
//
// No server code reads vibekit.TabSubject.Owns — the store copies it onto the
// subject and the command boundary copies it off the payload, and that is all.
// So the value is a CLIENT contract, with two readers that fail in opposite
// directions when it is wrong: the unacknowledged-work cue filters on it, so a
// false drops every chat out of the cue, and the client-local teardown returns
// early on it, so a false leaks the store row, the transcript view and the
// composer entry on every close.
//
// CreateChatAndOpen is the one tab-opening site create_chat, fork_chat and
// resume_session share, so one case covers all three.
//
// Deliberately no second assertion against tabs.Store.List: Open RETURNS the
// subject it stored, from one composite literal, so an in-process read of the
// set is the same value rather than a second read path. The disk round trip a
// restarted server serves from is internal/tabs' own contract, pinned there.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestCreateChatAndOpen_OpensAnOwnedTab pins the value the create path supplies:
// a chat tab owns the chat it shows.
func TestCreateChatAndOpen_OpensAnOwnedTab(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)

	opened := createChat(t, mem, "op-owns")

	if !opened.Subject.Owns {
		t.Errorf("the created tab's Owns = false, want true: a chat tab owns the chat it shows")
	}
}

// TestCreateChatAndOpen_ATangentsSubTabIsOwnedToo is the case whose reasoning
// would reintroduce the defect: a tangent's tab nests under its parent's, and a
// reader can take that for "the parent owns it". A sub-tab is a LAYOUT fact
// (TabSubject.Parent); a fork has its own session and its own bridge, so its tab
// is the only surface that owns them.
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

	// Fatal, not Errorf: with no nesting the Owns assertion below would pass
	// for a top-level tab and assert nothing about a sub-tab.
	if tangent.Subject.Parent != parent.Subject.ID {
		t.Fatalf("the tangent's Parent = %q, want the parent's tab %q", tangent.Subject.Parent, parent.Subject.ID)
	}
	if !tangent.Subject.Owns {
		t.Errorf("a sub-tab's Owns = false, want true: nesting is layout, not authority")
	}
}
