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

// An ADMITTED prompt is a turn in flight from every client's point of view: the user row
// is persisted and broadcast and `thinking` is latched, so answering false made
// `turn_open: false` mean two different things.
func TestHasOpenTurn_TrueForAnAdmittedPromptWithNoTurnMinted(t *testing.T) {
	h, _, _ := newTestHub()
	if !h.coord.TryReserveTurn("c1", vibekit.TurnSourcePrompt) {
		t.Fatal("a fresh chat refused a prompt reservation")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	if !h.HasOpenTurn("c1") {
		t.Error("a chat whose prompt is admitted but whose Turn is not minted reports no " +
			"turn open, so the client's heal clears `thinking` under a live prompt")
	}
}

// A shell reservation is held across appendShellUserMessage's chat-file write, so the
// `!cmd` user row is persisted and broadcast before StartTurn mints anything. A `!cmd`
// turn emits no chunks either, so the client's one-chunk recovery cannot reach it.
func TestHasOpenTurn_TrueForAnAdmittedShellCommand(t *testing.T) {
	h, _, _ := newTestHub()
	if !h.coord.TryReserveTurn("c1", vibekit.TurnSourceLocalShell) {
		t.Fatal("a fresh chat refused a shell reservation")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	if !h.HasOpenTurn("c1") {
		t.Error("a chat holding a shell reservation reports no turn open")
	}
}

// A PRIME's own reservation is vibekit's transcript replay, so no client latched anything
// for it — the same exclusion the open-turn branch above already makes.
func TestHasOpenTurn_FalseForAPrimeReservation(t *testing.T) {
	h, _, _ := newTestHub()
	if !h.coord.TryReserveTurn("c1", vibekit.TurnSourcePrime) {
		t.Fatal("a fresh chat refused a prime reservation")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	if h.HasOpenTurn("c1") {
		t.Error("a prime reservation reports the record provisional")
	}
}

// THE COLD-SPAWN CASE, and the one the branch ORDER exists for: during the prime window
// the open turn is the PRIME while the reservation beside it is the PROMPT's. Returning
// from the open-turn branch whenever any turn is open would answer false here, which is
// the longest part of the very window the reservation term exists to close.
func TestHasOpenTurn_TrueForAnOpenPrimeWithAPromptReservationBesideIt(t *testing.T) {
	h, _, _ := newTestHub()
	if !h.coord.TryReserveTurn("c1", vibekit.TurnSourcePrompt) {
		t.Fatal("a fresh chat refused a prompt reservation")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrime)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	if !h.HasOpenTurn("c1") {
		t.Error("a prime running over an admitted prompt reports no turn open: the " +
			"open-turn branch answered for the prime and never read the reservation")
	}
}

// An open PROMPT turn short-circuits on its own, with no reservation involved: the
// reservation term widens the answer and must not be the only thing producing it.
func TestHasOpenTurn_TrueForAnOpenPromptTurnWithNoReservation(t *testing.T) {
	h, _, _ := newTestHub()
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	if !h.HasOpenTurn("c1") {
		t.Error("an open prompt turn with no reservation reports no turn open")
	}
}
