package hub

// Tests for the Bug-1 tag-surfacing (stampTurnCheckpointTag), the
// Bug-2 restore-during-turn busy-guard (409), and the Bug-3 unknown-tag
// mapping (404) on the restore/undo command handlers.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// seedMessages replaces chatID's message list with msgs (creating the chat).
func seedMessages(t *testing.T, h *Hub, chatID api.ChatID, msgs []api.Message) {
	t.Helper()
	err := h.chatStore.Mutate(context.Background(), chatID, func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = append([]api.Message(nil), msgs...)
		return true
	})
	if err != nil {
		t.Fatalf("seed messages: %v", err)
	}
}

// userMsgCheckpointTag returns the CheckpointTag persisted on message msgID.
func userMsgCheckpointTag(t *testing.T, h *Hub, chatID api.ChatID, msgID string) string {
	t.Helper()
	c, ok := h.chatStore.Get(context.Background(), chatID)
	if !ok {
		t.Fatalf("chat %q not found", chatID)
	}
	for i := range c.Messages {
		if c.Messages[i].ID == msgID {
			return c.Messages[i].CheckpointTag
		}
	}
	t.Fatalf("message %q not found", msgID)
	return ""
}

// TestStampTurnCheckpointTag_MatchesAllocateTag ties the surfaced wire
// value to the tag the server actually allocated: the FIRST snapshot of
// turn 1 gets the 1-based tag "1" (allocateTag), and that exact tag is
// what lands on the turn's user message — no 0-based recompute.
func TestStampTurnCheckpointTag_MatchesAllocateTag(t *testing.T) {
	h, s, _ := newCheckpointHub(t)
	ctx := context.Background()
	seedMessages(t, h, "c1", []api.Message{{ID: "u1", Role: api.RoleUser}})

	// Drive the real allocation path (AdvanceTurn → Snapshot → allocateTag).
	s.AdvanceTurn(ctx, "c1", 1)
	tag, err := s.Snapshot(ctx, "c1", "f.go", []byte("v1"), 1)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if string(tag) != "1" {
		t.Fatalf("first-write tag = %q, want \"1\" (1-based allocateTag)", tag)
	}

	h.stampTurnCheckpointTag(ctx, "c1", string(tag))
	if got := userMsgCheckpointTag(t, h, "c1", "u1"); got != "1" {
		t.Errorf("surfaced checkpoint_tag = %q, want \"1\"", got)
	}
}

// TestStampTurnCheckpointTag_OnlyTurnCanonical verifies that only the
// turn-canonical tag ("N") is stamped — per-tool tags ("N.K") are
// ignored so the surfaced tag always reverts the WHOLE turn — and that
// the stamp is set-once (a later tag can't clobber it).
func TestStampTurnCheckpointTag_OnlyTurnCanonical(t *testing.T) {
	h, _, _ := newCheckpointHub(t)
	ctx := context.Background()
	seedMessages(t, h, "c1", []api.Message{{ID: "u1", Role: api.RoleUser}})

	// A dotted per-tool tag must not stamp.
	h.stampTurnCheckpointTag(ctx, "c1", "1.2")
	if got := userMsgCheckpointTag(t, h, "c1", "u1"); got != "" {
		t.Errorf("dotted tag stamped %q, want empty", got)
	}
	// The empty tag is a no-op too.
	h.stampTurnCheckpointTag(ctx, "c1", "")
	if got := userMsgCheckpointTag(t, h, "c1", "u1"); got != "" {
		t.Errorf("empty tag stamped %q, want empty", got)
	}
	// The turn-canonical tag stamps.
	h.stampTurnCheckpointTag(ctx, "c1", "1")
	if got := userMsgCheckpointTag(t, h, "c1", "u1"); got != "1" {
		t.Errorf("canonical stamp = %q, want \"1\"", got)
	}
	// Set-once: a subsequent canonical tag must not overwrite.
	h.stampTurnCheckpointTag(ctx, "c1", "2")
	if got := userMsgCheckpointTag(t, h, "c1", "u1"); got != "1" {
		t.Errorf("stamp after set-once = %q, want \"1\" (unchanged)", got)
	}
}

// TestStampTurnCheckpointTag_TargetsLastUserMessage confirms the tag
// lands on the CURRENT turn's prompt (the most recent user message),
// even when a trailing event message follows it, and never on an older
// user turn.
func TestStampTurnCheckpointTag_TargetsLastUserMessage(t *testing.T) {
	h, _, _ := newCheckpointHub(t)
	ctx := context.Background()
	seedMessages(t, h, "c1", []api.Message{
		{ID: "u1", Role: api.RoleUser},
		{ID: "a1", Role: api.RoleAssistant},
		{ID: "u2", Role: api.RoleUser},
		{ID: "e1", Role: api.RoleEvent},
	})

	h.stampTurnCheckpointTag(ctx, "c1", "3")
	if got := userMsgCheckpointTag(t, h, "c1", "u2"); got != "3" {
		t.Errorf("last user message tag = %q, want \"3\"", got)
	}
	if got := userMsgCheckpointTag(t, h, "c1", "u1"); got != "" {
		t.Errorf("older user message tag = %q, want empty", got)
	}
}

// TestCmdRestoreCheckpoint_InFlightTurnIs409 pins the busy-guard: a
// restore while a turn is in flight on the chat's bridge is rejected
// with 409 so the client can retry once idle (rather than racing the
// agent's writes / the assistant-buffer flush).
func TestCmdRestoreCheckpoint_InFlightTurnIs409(t *testing.T) {
	h, _, _ := newCheckpointHub(t)
	ctx := context.Background()
	if err := h.chatStore.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	sb, err := h.coord.GetOrCreateBridge(ctx, "c1", "")
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if !sb.TryAcquireForPrompt() {
		t.Fatal("could not simulate in-flight turn (acquire prompt lock)")
	}
	// Do NOT release — the turn stays "in flight" for the duration.

	rec := postCmd(t, h, api.ClientCommand{
		Type: "restore_checkpoint", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{"tag":"1"}`),
	})
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
}

// TestCmdRestoreCheckpoint_UnknownTagIs404 pins the Bug-3 fix: an unknown
// tag now maps to 404, not 500. With no bridge for the chat the busy-guard
// is skipped, so the request reaches Restore and surfaces ErrTagNotFound.
func TestCmdRestoreCheckpoint_UnknownTagIs404(t *testing.T) {
	h, _, _ := newCheckpointHub(t)
	rec := postCmd(t, h, api.ClientCommand{
		Type: "restore_checkpoint", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{"tag":"42"}`),
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%q", rec.Code, rec.Body.String())
	}
}
