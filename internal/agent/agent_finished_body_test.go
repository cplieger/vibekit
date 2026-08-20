package agent

// D104, second half: the agent-finished notification says what the agent was
// doing instead of the fixed literal "Agent finished".
//
// The description is already server-side — it arrives on KAS's focus_update channel
// (the model's update_session_information tool) and lives in the runtime's chat-status
// cache — so this needed no wire field and no new call site. What it DID need was
// getting the ordering right, which is what the second test here pins.

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestAgentFinishedBodyFrom(t *testing.T) {
	cases := []struct {
		name string
		desc string
		want string
	}{
		{
			name: "TheAgentsOwnLine",
			desc: "Reviewing the MCP validation accumulation",
			want: "Reviewing the MCP validation accumulation",
		},
		{
			// An agent need never call update_session_information, so this is the
			// ordinary case rather than a defensive branch.
			name: "EmptyFallsBackToTheLiteral", desc: "", want: defaultAgentFinishedBody,
		},
		{
			name: "WhitespaceOnlyFallsBackToo", desc: "  \n\t ", want: defaultAgentFinishedBody,
		},
		{
			name: "SurroundingWhitespaceIsTrimmed",
			desc: "  Fixing the poller  ", want: "Fixing the poller",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentFinishedBodyFrom(tc.desc); got != tc.want {
				t.Errorf("agentFinishedBodyFrom(%q) = %q, want %q", tc.desc, got, tc.want)
			}
		})
	}
}

// TestEmitTurnEnded_PushBodyCarriesAgentText is the ordering test, and it is the one
// that matters: emit() CLEARS the chat's status as the turn_ended event goes out, so
// a read taken at the push site would always find the entry gone and always fall
// back to the literal — silently, and indistinguishably from an agent that never
// declared anything.
func TestEmitTurnEnded_PushBodyCarriesAgentText(t *testing.T) {
	cs := newFakeChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New(context.Background(), "/tmp/push-desc", func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.signalReady()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// The agent declared what it was doing mid-turn, exactly as
	// translate/focus.go's chat_status path records it.
	h.sse.chatStatus.Set("c1", vibekit.ChatStatusPayload{
		Status:      "in_progress",
		Description: "Wiring the PR status poller",
	})

	resp := &vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.EmitTurnEndedWithStats(ctx, "c1", resp, command.TurnStats{})

	select {
	case body := <-fp.sends:
		if body != "Wiring the PR status poller" {
			t.Errorf("push body = %q, want the agent's own line", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a non-cancelled turn")
	}

	// And the status is still cleared afterwards, so a later connect cannot report
	// a finished turn's label as current.
	if got := h.sse.chatStatus.Get("c1"); got.Description != "" {
		t.Errorf("the chat status survived turn end: %+v", got)
	}
}

// TestEmitTurnEnded_PushSubjectIsTheChat pins the reuse of the per-chat coalescing
// tag through the generalised subject: a chat notification must still travel as a
// chat subject, not as a bare key.
func TestEmitTurnEnded_PushSubjectIsTheChat(t *testing.T) {
	cs := newFakeChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New(context.Background(), "/tmp/push-subject", func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.signalReady()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	resp := &vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.EmitTurnEndedWithStats(ctx, "c1", resp, command.TurnStats{})

	select {
	case <-fp.sends:
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a non-cancelled turn")
	}
	if fp.subject.ChatID != "c1" {
		t.Errorf("subject chat id = %q, want %q", fp.subject.ChatID, "c1")
	}
	if fp.subject.Key != "" {
		t.Errorf("a chat notification carries a subject key %q; the chat id IS its subject", fp.subject.Key)
	}
}
