package agent

// StartTurn's refusal on an already-dead context, and the shutdown window that made it
// load-bearing: a turn opened once the process has decided to stop has no closer left
// that both persists and announces, so it reached the wire with no terminal frame.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// A turn is never opened on a context that is already dead, whatever the chat's state.
// Asserted per SOURCE because the acknowledgeable arm reaches displaceEngineTurn before
// the open, so a guard placed after it would pass a source-blind assertion.
func TestStartTurn_RefusesAnAlreadyDeadContext(t *testing.T) {
	cases := []struct {
		name   string
		source vibekit.TurnOpenSource
	}{
		{name: "prompt", source: vibekit.TurnSourcePrompt},
		{name: "prime", source: vibekit.TurnSourcePrime},
		{name: "empty_retry", source: vibekit.TurnSourceEmptyRetry},
		{name: "local_shell", source: vibekit.TurnSourceLocalShell},
	}
	for _, tc := range cases {
		source := tc.source
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			seedChat(t, cs, "c1")

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if epoch := h.coord.StartTurn(ctx, "c1", source); epoch != 0 {
				t.Errorf("StartTurn(cancelled ctx, %v) = %d, want 0: a dead context opens nothing", source, epoch)
			}
			if _, open := h.coord.turns.openEpoch("c1"); open {
				t.Errorf("StartTurn(cancelled ctx, %v) left a turn open on the chat", source)
			}
		})
	}
}

// The shutdown window this closes end to end: a prompt parked in its bridge spawn when
// Shutdown lands must still reach a TERMINAL frame. Distinct from
// TestPromptTurn_ShutdownPreGoroutineStillDrainsTheTurn, which asserts the same outcome
// but reaches it through whichever closer wins a race — so it passes most of the time
// with the defect present. This one pins the mechanism: no turn opens at all, so the
// prompt takes its own zero-epoch branch and the error frame is not a race's byproduct.
func TestPromptTurn_ShutdownBeforeTheTurnOpensStartsNoTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	entered, gate := gateSpawn(h)
	defer close(gate)

	if rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "prompt", ChatID: "c1",
		Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("prompt ack = %d, body %s", rec.Code, rec.Body.String())
	}
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}

	// No turn was ever minted, which is what makes the terminal frame below deterministic
	// rather than a closer race's byproduct.
	if epoch, open := h.coord.turns.openEpoch("c1"); open {
		t.Errorf("a turn (epoch %d) is still open after shutdown", epoch)
	}
	types := extractTypes(t, bufferedSince(h, 0))
	if missing := missingEvents(types, string(vibekit.EventError)); missing != nil {
		t.Errorf("events = %v, want the prompt's terminal error frame", types)
	}
}
