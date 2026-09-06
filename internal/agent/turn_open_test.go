package agent

// Can the RECORD be trusted yet? `hasOpenTurn` is the fact the chat store's HTTP
// surface cannot know for itself.
//
// The in-flight reply lives in an in-memory buffer and is appended to the chat file
// once, at turn end, so a turn in flight has NO carrier in `GET /api/chats/{id}` —
// and the client's derivation reads an absent carrier as "nothing closed this turn"
// and answers `unknown`, a TERMINAL verdict during the one window in which nothing
// can know one. This predicate is what lets the response state the liveness instead.

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

// TestHasOpenTurn_TrueWhileFinalizing is the state the predicate exists for as much
// as `turnOpen` is: `openFactsLocked` counts `turnFinalizing` as OPEN because the
// carrier's persistence and broadcast have not completed, so the record is still
// provisional at exactly the moment a refetch is most likely to race it.
func TestHasOpenTurn_TrueWhileFinalizing(t *testing.T) {
	h, _, _ := newTestHub()
	h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)

	// Claiming moves the chat to turnFinalizing without finishing it, which is the
	// window between a closer claiming and its effects landing.
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

// TestHasOpenTurn_RecordsNothingAboutTheChatItWasAskedAbout is the read-path half,
// and it is what makes this predicate safe to call from HTTP.
//
// Its two call sites are `GET /api/chats/{id}` and `GET /api/chats/{id}/turns`, so it
// answers for every chat a reader merely OPENS. `lifecycleFor` creates a lifecycle on
// first use and only `forget` (bridge teardown or delete) removes one, so asking
// through it left an entry and its `changed` channel behind per chat read. The
// sibling predicate `HasLiveBridge` is a plain locked map read with no such effect,
// and that asymmetry is the tell.
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

// TestHasOpenTurn_FalseForAPrimeTurn mirrors replayTurnState's own exclusion rather
// than inventing one: a prime's frames are vibekit's transcript replay and it
// persists no carrier, so a prime turn does not make the record provisional.
func TestHasOpenTurn_FalseForAPrimeTurn(t *testing.T) {
	h, _, _ := newTestHub()
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrime)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	if h.HasOpenTurn("c1") {
		t.Error("a prime turn reports the record provisional; its replay persists no " +
			"carrier, so nothing is pending for the reader to wait on")
	}
}
