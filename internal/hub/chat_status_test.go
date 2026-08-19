package hub

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestChatStatusCache covers the one turn_state input the assistant buffer
// cannot supply. chat_status arrives on KAS's focus_update channel, so it lives
// on no message and in no replay — deleting the turn mirror without this would
// have silently dropped the label from every mid-turn reconnect.
func TestChatStatusCache(t *testing.T) {
	c := newChatStatusCache()

	if got := c.Get("nobody"); got.Status != "" {
		t.Errorf("unknown chat returned %q, want the zero payload", got.Status)
	}

	c.Set("c1", vibekit.ChatStatusPayload{Status: "in_progress", Description: "reading files"})
	got := c.Get("c1")
	if got.Status != "in_progress" || got.Description != "reading files" {
		t.Errorf("got %+v, want in_progress/reading files", got)
	}

	// Newest wins: the agent re-declares as focus shifts.
	c.Set("c1", vibekit.ChatStatusPayload{Status: "waiting_on_user", Description: "needs a decision"})
	if got := c.Get("c1"); got.Status != "waiting_on_user" {
		t.Errorf("got %q, want the latest status", got.Status)
	}

	// Cleared at turn end, so a later connect cannot report a finished turn's
	// label as current — the same reason the live event is ephemeral.
	c.Clear("c1")
	if got := c.Get("c1"); got.Status != "" {
		t.Errorf("status %q survived the turn", got.Status)
	}

	// An empty chat id is ignored rather than creating a junk entry: global
	// events carry no chat.
	c.Set("", vibekit.ChatStatusPayload{Status: "in_progress"})
	if got := c.Get(""); got.Status != "" {
		t.Error("an empty chat id was recorded")
	}
}
