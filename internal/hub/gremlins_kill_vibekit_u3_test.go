package hub

// Mutant-killing tests for unit vibekit-u3 (internal/hub).
//
// Targets surviving gremlins mutants in:
//   - translate.go:150 (3x CONDITIONALS_NEGATION) — the subagent
//     attribution condition in handleSessionUpdate.
//   - utility_bridge.go:99 (CONDITIONALS_BOUNDARY) — the `>=` prompt-cap
//     recycle guard in UtilityPrompt.
//   - utility_bridge.go:110 (INCREMENT_DECREMENT) — the `promptCount++`.
//
// Tests only; no production code is edited. Reuses the package's existing
// fakes/helpers (newTestHub, newFakeBridge, newUtilityBridge, mustJSON,
// maxUtilityPrompts, bridgeIdle). All new identifiers are prefixed
// gk_vibekit_u3_ to avoid collisions with sibling units sharing this package.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// --- helpers (prefixed to avoid collisions with sibling units) ---

// gk_vibekit_u3_registerParent registers a bridge for chatID whose
// SessionID is parentSession, so h.parentACPSession(chatID) returns it.
func gk_vibekit_u3_registerParent(t *testing.T, h *Hub, chatID api.ChatID, parentSession string) {
	t.Helper()
	sb, _ := h.bridge.mgr.getOrInsert(chatID)
	br := newFakeBridge()
	br.sessionID = parentSession
	sb.bridge = br
	sb.state = bridgeIdle
}

// gk_vibekit_u3_captureSubSession installs a capturing sub-handler for the
// agent_message_chunk kind, drives handleSessionUpdate with a notification
// carrying sessionID, and returns the subSessionID the dispatcher computed
// plus whether the handler ran (false => sub-dispatch returned early).
func gk_vibekit_u3_captureSubSession(t *testing.T, h *Hub, chatID api.ChatID, sessionID string) (got string, called bool) {
	t.Helper()
	h.sessUpdateHandlers = map[api.ACPUpdateKind]sessionUpdateHandler{
		api.ACPUpdateAgentChunk: func(_ context.Context, _ api.ChatID, _ json.RawMessage, sub string) {
			got = sub
			called = true
		},
	}
	update := mustJSON(t, map[string]any{
		"sessionUpdate": string(api.ACPUpdateAgentChunk),
		"content":       map[string]any{"type": "text", "text": "x"},
	})
	params := mustJSON(t, map[string]any{
		"sessionId": sessionID,
		"update":    update,
	})
	msg := &api.RPCResponse{Method: "session/update", Params: params}
	h.handleSessionUpdate(context.Background(), chatID, msg)
	return got, called
}

// gk_vibekit_u3_newUtilBridge builds a utilityBridge whose factory hands out
// a fresh fakeBridge on each call (so recycle visibly swaps the instance) and
// whose model catalog is empty.
func gk_vibekit_u3_newUtilBridge() *utilityBridge {
	return newUtilityBridge(
		context.Background(),
		func() api.ACPBridge { return newFakeBridge() },
		func() []api.SessionModel { return nil },
	)
}

// --- translate.go:150 — subagent attribution condition ---

// Kills the three CONDITIONALS_NEGATION mutants on
//
//	if env.Params.SessionID != "" && parent != "" && env.Params.SessionID != parent
//
// (cols 26, 42, 72). subSessionID is set to the notification's sessionId
// ONLY when all three hold. Each case pins the exact subSessionID the
// dispatcher passes to the sub-handler:
//
//   - subagent: all three true -> subSessionID == sessionId. Flipping ANY of
//     the three `!=` to `==` makes the && chain false, so subSessionID
//     becomes "" — this single case kills all three.
//   - parent_match (sessionId == parent): the 3rd `!=` is false -> "". The
//     `==` mutant on col 72 makes it true -> subSessionID becomes sessionId;
//     asserting "" catches it (isolated kill for col 72).
//   - no_parent (parent == ""): the 2nd `!=` is false -> "". The `==` mutant
//     on col 42 makes it true -> subSessionID becomes sessionId; asserting ""
//     catches it (isolated kill for col 42).
func TestGkVibekitU3_HandleSessionUpdate_SubSessionAttribution(t *testing.T) {
	cases := []struct {
		name       string
		chatID     api.ChatID
		registerPS string // parent session to register; "" => no bridge (parent == "")
		sessionID  string
		want       string
	}{
		{
			name:       "subagent_when_session_nonempty_parent_set_and_differs",
			chatID:     "gk-u3-chat-sub",
			registerPS: "gk-u3-parent-A",
			sessionID:  "gk-u3-sub-B",
			want:       "gk-u3-sub-B",
		},
		{
			name:       "parent_when_session_equals_parent",
			chatID:     "gk-u3-chat-match",
			registerPS: "gk-u3-same",
			sessionID:  "gk-u3-same",
			want:       "",
		},
		{
			name:       "parent_when_no_parent_bridge",
			chatID:     "gk-u3-chat-noparent",
			registerPS: "",
			sessionID:  "gk-u3-sub-C",
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			defer h.Shutdown()
			if tc.registerPS != "" {
				gk_vibekit_u3_registerParent(t, h, tc.chatID, tc.registerPS)
			}
			got, called := gk_vibekit_u3_captureSubSession(t, h, tc.chatID, tc.sessionID)
			if !called {
				t.Fatalf("handleSessionUpdate did not invoke the sub-handler (sub-dispatch returned early)")
			}
			if got != tc.want {
				t.Errorf("handleSessionUpdate subSessionID = %q, want %q (parent=%q, sessionID=%q)",
					got, tc.want, tc.registerPS, tc.sessionID)
			}
		})
	}
}

// --- utility_bridge.go:99 — prompt-cap recycle guard (>=) ---

// Kills 99:34 (CONDITIONALS_BOUNDARY on `ub.promptCount >= maxUtilityPrompts`).
// At the exact boundary (promptCount == maxUtilityPrompts) with the bridge
// already started, the original `>=` recycles: reset() stops the old bridge
// and zeroes promptCount, start() swaps in a fresh bridge, then promptCount++
// lands at 1. The `>` mutant does NOT recycle at the boundary: the old bridge
// stays and promptCount++ lands at maxUtilityPrompts+1 (21).
func TestGkVibekitU3_UtilityPrompt_RecyclesAtPromptCap(t *testing.T) {
	ub := gk_vibekit_u3_newUtilBridge()
	br0 := newFakeBridge()
	ub.bridge = br0
	ub.started = true
	ub.promptCount = maxUtilityPrompts // exact boundary
	defer ub.Stop()

	if _, err := ub.UtilityPrompt(context.Background(), "p"); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if ub.bridge == br0 {
		t.Errorf("UtilityPrompt at prompt cap did not recycle the bridge (> mutant skips reset)")
	}
	if ub.promptCount != 1 {
		t.Errorf("promptCount after recycle = %d, want 1 (> mutant leaves it at %d)",
			ub.promptCount, maxUtilityPrompts+1)
	}
}

// --- utility_bridge.go:110 — promptCount++ ---

// Kills 110:16 (INCREMENT_DECREMENT on `ub.promptCount++`). A fresh bridge
// (started=false) skips the recycle branch, start() runs, then promptCount++
// takes 0 -> 1. The `--` mutant takes 0 -> -1.
func TestGkVibekitU3_UtilityPrompt_IncrementsPromptCount(t *testing.T) {
	ub := gk_vibekit_u3_newUtilBridge()
	defer ub.Stop()

	if _, err := ub.UtilityPrompt(context.Background(), "p"); err != nil {
		t.Fatalf("UtilityPrompt error = %v, want nil", err)
	}

	if ub.promptCount != 1 {
		t.Errorf("promptCount after one prompt = %d, want 1 (-- mutant yields -1)", ub.promptCount)
	}
}
