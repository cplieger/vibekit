package agent

// `user_message_id_assigned` at the DISPATCHER, because two things about it are only
// true one level up from the handler: the id rides `update._meta.kiro`, and the frame is
// attributed from the SESSION it arrived on rather than from anything in its payload.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// userMessageIDParams is the whole `session/update` params object, `_meta` nested inside
// `update` where KAS puts it. Building it here rather than handing the handler an update
// object is the point: read one level up, every field of it is a zero value.
func userMessageIDParams(t *testing.T, sessionID, kasID string) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": string(vibekit.ACPUpdateSessionInfo),
			"_meta": map[string]any{
				"kiro": map[string]any{
					"kind":          "user_message_id_assigned",
					"userMessageId": kasID,
				},
			},
		},
	})
}

// seedPromptRow gives the chat one prompt-class user row for the stamp to land on.
func seedPromptRow(cs *fakeChatStore, chatID vibekit.ChatID, msgID string) {
	cs.Chats[chatID] = &vibekit.Chat{
		ID:       string(chatID),
		Messages: []vibekit.Message{{ID: msgID, Role: vibekit.RoleUser, Content: "prompt"}},
	}
}

// kasIDOfFirstRow reads the stamp back off the store.
func kasIDOfFirstRow(t *testing.T, h *Runtime, chatID vibekit.ChatID) string {
	t.Helper()
	c, ok := h.chatStore.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q missing", chatID)
	}
	if len(c.Messages) == 0 {
		t.Fatalf("chat %q has no messages", chatID)
	}
	return c.Messages[0].KASMessageID
}

// The id KAS assigns on the chat's OWN session names the prompt vibekit just persisted,
// and it is the only id `_kiro/checkpoint/revertMultiple` accepts — so it has to reach
// the record, through a frame whose `_meta` sits one level in.
func TestHandleSessionUpdate_StampsTheKASMessageIDFromTheChatsOwnSession(t *testing.T) {
	const chatID = vibekit.ChatID("chat-own")
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	registerParentSession(t, h, chatID, "parent-A")
	seedPromptRow(cs, chatID, "m-1")

	h.handleSessionUpdate(t.Context(), chatID, &vibekit.RPCResponse{
		Method: "session/update",
		Params: userMessageIDParams(t, "parent-A", "kas-own"),
	})

	if got := kasIDOfFirstRow(t, h, chatID); got != "kas-own" {
		t.Errorf("kas_message_id = %q, want kas-own", got)
	}
}

// A workflow step's answer is an ordinary `session/prompt` on the STEP's session, so KAS
// assigns that prompt an id too. The frame carries no `_meta.kiro.workflow` and is
// byte-identical to the chat's own, so only the session says it is not the reader's —
// and stamping it would point rewind at a row the id does not name.
func TestHandleSessionUpdate_AStepSessionsAssignedIDStampsNothing(t *testing.T) {
	const (
		chatID  = vibekit.ChatID("chat-step-id")
		stepSID = "step-session-1"
	)
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	registerParentSession(t, h, chatID, "parent-A")
	h.translator.RecordStepSession(stepSID, "wf_1", "build")
	seedPromptRow(cs, chatID, "m-1")

	h.handleSessionUpdate(t.Context(), chatID, &vibekit.RPCResponse{
		Method: "session/update",
		Params: userMessageIDParams(t, stepSID, "kas-step"),
	})

	if got := kasIDOfFirstRow(t, h, chatID); got != "" {
		t.Errorf("kas_message_id = %q, want empty: a step's id names a row in the step's own log", got)
	}
}
