package agent

// Can the RECORD be trusted yet — the fact the chat store's HTTP surface cannot know for
// itself. The in-flight reply is appended to the chat file only at turn end, so a running
// turn has no carrier in the response, and the client's derivation reads an absent carrier
// as a TERMINAL `unknown`.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestHasOpenTurn_FalseOnAnIdleChat(t *testing.T) {
	h, _, _ := newTestHub()
	if h.HasOpenTurn("c1") {
		t.Error("an idle chat reports a turn open; the record is final and should read as such")
	}
}

func TestHasOpenTurn_TrueWhileATurnIsOpen(t *testing.T) {
	h, _, _ := newTestHub()
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	if !h.HasOpenTurn("c1") {
		t.Error("an open turn reports the record final, which is what makes the client " +
			"derive a terminal outcome for a turn that is running")
	}
	// Scoped to the chat asked about, not to "any chat is busy".
	if h.HasOpenTurn("c2") {
		t.Error("an unrelated chat reports a turn open")
	}
}

// `turnFinalizing` counts as OPEN: the carrier's persistence and broadcast have not
// completed, so the record is still provisional at the moment a refetch is most likely to
// race it.
func TestHasOpenTurn_TrueWhileFinalizing(t *testing.T) {
	h, _, _ := newTestHub()
	h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)

	// Claiming without finishing IS the window between a closer claiming and its effects landing.
	turn, won := h.coord.turns.claimOpen(t.Context(), "c1")
	if !won {
		t.Fatal("claimOpen lost the claim on a freshly opened turn")
	}
	if !h.HasOpenTurn("c1") {
		t.Error("a finalizing turn reports the record final, so a refetch inside the " +
			"persist window derives a verdict from a carrier that has not landed")
	}
	h.coord.turns.finish(turn, vibekit.TurnResult{})
	if h.HasOpenTurn("c1") {
		t.Error("a finished turn still reports open")
	}
}

// What makes the predicate safe to call from HTTP: both call sites answer for every chat a
// reader merely OPENS, and `lifecycleFor` creates a lifecycle on first use that only a
// bridge teardown or delete removes — so asking through it leaks an entry per chat read.
func TestHasOpenTurn_RecordsNothingAboutTheChatItWasAskedAbout(t *testing.T) {
	h, _, _ := newTestHub()
	reg := h.coord.turns

	reg.mu.Lock()
	before := len(reg.chats)
	reg.mu.Unlock()

	for range 3 {
		if h.HasOpenTurn("never-had-a-turn") {
			t.Fatal("a chat that never had a turn reports one open")
		}
	}

	reg.mu.Lock()
	after := len(reg.chats)
	_, minted := reg.chats["never-had-a-turn"]
	reg.mu.Unlock()

	if minted {
		t.Error("reading the predicate minted a lifecycle for the chat it was asked about; " +
			"an HTTP read path would then leave one per chat opened, dropped only by forget")
	}
	if after != before {
		t.Errorf("the registry grew from %d to %d entries across three reads", before, after)
	}
}

// Mirrors replayTurnState's own exclusion rather than inventing one: a prime persists no
// carrier, so it does not make the record provisional.
func TestHasOpenTurn_FalseForAPrimeTurn(t *testing.T) {
	h, _, _ := newTestHub()
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrime)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	if h.HasOpenTurn("c1") {
		t.Error("a prime turn reports the record provisional; its replay persists no " +
			"carrier, so nothing is pending for the reader to wait on")
	}
}
