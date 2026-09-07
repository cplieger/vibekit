package agent

import (
	"context"
	"testing"
	"time"

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
			// An agent need never declare a focus, so this is the ordinary case.
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

// The ordering: emit() CLEARS the chat's status as the turn_ended event goes out, so a
// read taken at the push site finds the entry gone and falls back to the literal —
// silently, and indistinguishably from an agent that never declared anything.
func TestEmitTurnEnded_PushBodyCarriesAgentText(t *testing.T) {
	cs := newFakeChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New(context.Background(), "/tmp/push-desc", func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// Recorded the way translate/focus.go's chat_status path records it.
	h.bus.chatStatus.Set("c1", vibekit.ChatStatusPayload{
		Status:      "in_progress",
		Description: "Wiring the PR status poller",
	})

	epoch := h.StartTurn(ctx, "c1", vibekit.TurnSourcePrompt)
	resp := &vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.SettleTurnOnResponse(ctx, "c1", epoch, 0, resp)

	select {
	case body := <-fp.sends:
		if body != "Wiring the PR status poller" {
			t.Errorf("push body = %q, want the agent's own line", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a non-cancelled turn")
	}

	// Still cleared afterwards, or a later connect reports a finished turn's label as current.
	if got := h.bus.chatStatus.Get("c1"); got.Description != "" {
		t.Errorf("the chat status survived turn end: %+v", got)
	}
}

// The push may not claim success over a failure: the description is what the agent was
// DOING, so pushing it for a failed turn gives the off-screen reader the agent's own words
// for work that did not land. The bodies are hardcoded rather than read back through
// DefaultFailureReason, because an expectation computed by the code under test passes for
// any mapping.
func TestEmitTurnEnded_PushReadsTheSeverity(t *testing.T) {
	cases := []struct {
		name string
		stop vibekit.StopReason
		want string // "" means no push at all
	}{
		{name: "clean", stop: vibekit.StopReasonEndTurn, want: "Wiring the PR status poller"},
		{name: "failed", stop: vibekit.StopReasonError, want: "The agent reported an error and the turn stopped."},
		{name: "refused", stop: vibekit.StopReasonRefusal, want: "The model declined to continue."},
		// STOPPED: the reader asked for the cancel, and an unreadable end claims nothing.
		{name: "cancelled", stop: vibekit.StopReasonCancelled, want: ""},
		{name: "unknown", stop: vibekit.StopReasonUnknown, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newFakeChatStore()
			fp := &recordingPush{sends: make(chan string, 4)}
			h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
			cs.Bus = h
			h.mcpRegistry.SignalReady()
			ctx := t.Context()
			_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
			// Present for every case, so an arm leaking it fails visibly rather than absently.
			h.bus.chatStatus.Set("c1", vibekit.ChatStatusPayload{
				Status: "in_progress", Description: "Wiring the PR status poller",
			})

			epoch := h.StartTurn(ctx, "c1", vibekit.TurnSourcePrompt)
			h.SettleTurnOnResponse(ctx, "c1", epoch, 0,
				&vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": string(tc.stop)})})

			if tc.want == "" {
				select {
				case body := <-fp.sends:
					t.Errorf("a %q turn pushed %q; a turn that merely stopped reports nothing", tc.stop, body)
				case <-time.After(200 * time.Millisecond):
				}
				return
			}
			select {
			case body := <-fp.sends:
				if body != tc.want {
					t.Errorf("push body = %q, want %q", body, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("a %q turn pushed nothing", tc.stop)
			}
		})
	}
}

// A chat notification must still travel as a chat SUBJECT rather than a bare key, or it
// loses the per-chat coalescing tag.
func TestEmitTurnEnded_PushSubjectIsTheChat(t *testing.T) {
	cs := newFakeChatStore()
	fp := &recordingPush{sends: make(chan string, 4)}
	h := New(context.Background(), "/tmp/push-subject", func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	ctx := t.Context()
	_ = cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	epoch := h.StartTurn(ctx, "c1", vibekit.TurnSourcePrompt)
	resp := &vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})}
	h.SettleTurnOnResponse(ctx, "c1", epoch, 0, resp)

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
