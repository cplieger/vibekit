package agent

// Can the RECORD be trusted yet, and WHOSE turn is it — the two facts the chat store's
// HTTP surface cannot know for itself. The in-flight reply is appended to the chat file
// only at turn end, so a client reads an absent carrier as a TERMINAL `unknown`; and a
// step's turn folds onto the launching chat, so openness alone reports a run's work there.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestTurnOpenState_ClosedOnAnIdleChat(t *testing.T) {
	h, _, _ := newTestHub()
	if h.TurnOpenState("c1").Open {
		t.Error("an idle chat reports a turn open; the record is final and should read as such")
	}
}

func TestTurnOpenState_OpenWhileATurnIsOpen(t *testing.T) {
	h, _, _ := newTestHub()
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	got := h.TurnOpenState("c1")
	if !got.Open {
		t.Error("an open turn reports the record final, which is what makes the client " +
			"derive a terminal outcome for a turn that is running")
	}
	if !got.OwnTurn() {
		t.Errorf("a prompt turn reads as somebody else's (%+v), so the chat renders idle "+
			"while its own reply streams", got)
	}
	// Scoped to the chat asked about, not to "any chat is busy".
	if h.TurnOpenState("c2").Open {
		t.Error("an unrelated chat reports a turn open")
	}
}

// `turnFinalizing` counts as OPEN: the carrier's persistence and broadcast have not
// completed, so the record is still provisional at the moment a refetch is most likely to
// race it.
func TestTurnOpenState_OpenWhileFinalizing(t *testing.T) {
	h, _, _ := newTestHub()
	h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)

	// Claiming without finishing IS the window between a closer claiming and its effects landing.
	turn, won := h.coord.turns.claimOpen(t.Context(), "c1")
	if !won {
		t.Fatal("claimOpen lost the claim on a freshly opened turn")
	}
	if !h.TurnOpenState("c1").Open {
		t.Error("a finalizing turn reports the record final, so a refetch inside the " +
			"persist window derives a verdict from a carrier that has not landed")
	}
	h.coord.turns.finish(turn, vibekit.TurnResult{})
	if h.TurnOpenState("c1").Open {
		t.Error("a finished turn still reports open")
	}
}

// What makes the read safe to call from HTTP: both call sites answer for every chat a
// reader merely OPENS, and `lifecycleFor` creates a lifecycle on first use that only a
// bridge teardown or delete removes — so asking through it leaks an entry per chat read.
func TestTurnOpenState_RecordsNothingAboutTheChatItWasAskedAbout(t *testing.T) {
	h, _, _ := newTestHub()
	reg := h.coord.turns

	reg.mu.Lock()
	before := len(reg.chats)
	reg.mu.Unlock()

	for range 3 {
		if h.TurnOpenState("never-had-a-turn").Open {
			t.Fatal("a chat that never had a turn reports one open")
		}
	}

	reg.mu.Lock()
	after := len(reg.chats)
	_, minted := reg.chats["never-had-a-turn"]
	reg.mu.Unlock()

	if minted {
		t.Error("reading the state minted a lifecycle for the chat it was asked about; " +
			"an HTTP read path would then leave one per chat opened, dropped only by forget")
	}
	if after != before {
		t.Errorf("the registry grew from %d to %d entries across three reads", before, after)
	}
}

// An ADMITTED prompt is a turn in flight from every client's point of view: the user row
// is persisted and broadcast and `thinking` is latched, so answering false made
// `turn_open: false` mean two different things.
func TestTurnOpenState_OpenForAnAdmittedPromptWithNoTurnMinted(t *testing.T) {
	h, _, _ := newTestHub()
	if !h.coord.TryReserveTurn("c1", vibekit.TurnSourcePrompt) {
		t.Fatal("a fresh chat refused a prompt reservation")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	got := h.TurnOpenState("c1")
	if !got.Open {
		t.Error("a chat whose prompt is admitted but whose Turn is not minted reports no " +
			"turn open, so the client's heal clears `thinking` under a live prompt")
	}
	if !got.OwnTurn() {
		t.Errorf("an admitted prompt reads as somebody else's turn (%+v)", got)
	}
}

// A shell reservation is held across appendShellUserMessage's chat-file write, so the
// `!cmd` user row is persisted and broadcast before StartTurn mints anything. A `!cmd`
// turn emits no chunks either, so the client's one-chunk recovery cannot reach it.
func TestTurnOpenState_OpenForAnAdmittedShellCommand(t *testing.T) {
	h, _, _ := newTestHub()
	if !h.coord.TryReserveTurn("c1", vibekit.TurnSourceLocalShell) {
		t.Fatal("a fresh chat refused a shell reservation")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	if !h.TurnOpenState("c1").Open {
		t.Error("a chat holding a shell reservation reports no turn open")
	}
}

// An open PROMPT turn is answered without any reservation involved: the reservation term
// widens the answer and must not be the only thing producing it.
func TestTurnOpenState_OpenForAnOpenPromptTurnWithNoReservation(t *testing.T) {
	h, _, _ := newTestHub()
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	if !h.TurnOpenState("c1").Open {
		t.Error("an open prompt turn with no reservation reports no turn open")
	}
}

// A workflow STEP's turn is the RUN's work, so the chat's own agent is idle. Open stays
// TRUE — it licenses the client to keep the live-turn marker the step's unpersisted
// content depends on — and the owner marker is what stops the chat reading as running.
func TestTurnOpenState_DisownsAWorkflowStepTurn(t *testing.T) {
	h, _, _ := newTestHub()
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourceWorkflowStep)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	got := h.TurnOpenState("c1")
	if !got.Open {
		t.Error("a workflow step's turn reports nothing open, so the client retracts the " +
			"live-turn marker and the next newest-page fetch deletes the step's content")
	}
	if !got.WorkflowStep {
		t.Error("a workflow step's turn claims to be this chat's own, so the launching chat " +
			"renders its last finished turn as running for the length of the run")
	}
	if got.OwnTurn() {
		t.Errorf("a workflow step's turn reads as the chat's own (%+v)", got)
	}
}

// THE COLD-SPAWN WINDOW, and the reason the reservation is read even when a turn is
// already open: cmdPrompt reserves the slot and displaces the engine's turn only at
// StartTurn, one bridge spawn later. The open turn alone reports it as the RUN's work.
func TestTurnOpenState_KeepsAnAdmittedPromptOwnedOverAnOpenStepTurn(t *testing.T) {
	h, _, _ := newTestHub()
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourceWorkflowStep)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })
	if !h.coord.TryReserveTurn("c1", vibekit.TurnSourcePrompt) {
		t.Fatal("a chat holding a step turn refused a prompt reservation, so this window " +
			"is unreachable and the test asserts nothing")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })

	got := h.TurnOpenState("c1")
	if !got.Open {
		t.Error("a chat holding both a step turn and an admitted prompt reports nothing open")
	}
	if got.WorkflowStep || !got.OwnTurn() {
		t.Errorf("an admitted prompt is disowned by the step turn it is about to displace "+
			"(%+v), so the reader's own prompt renders idle for the whole bridge spawn", got)
	}
}

// The connect handshake's busy set and this state answer ONE client question through two
// channels, so they are one predicate. Two spellings is how they come to disagree.
func TestTurnOpenState_OwnTurnAgreesWithTheConnectBusySet(t *testing.T) {
	cases := []struct {
		name  string
		setUp func(t *testing.T, h *Runtime)
	}{
		{"idle", func(*testing.T, *Runtime) {}},
		{"prompt turn", func(t *testing.T, h *Runtime) {
			epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
			t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })
		}},
		{"workflow step turn", func(t *testing.T, h *Runtime) {
			epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourceWorkflowStep)
			t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })
		}},
		{"admitted prompt", func(t *testing.T, h *Runtime) {
			if !h.coord.TryReserveTurn("c1", vibekit.TurnSourcePrompt) {
				t.Fatal("a fresh chat refused a prompt reservation")
			}
			t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })
		}},
		{"admitted prompt over a step turn", func(t *testing.T, h *Runtime) {
			epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourceWorkflowStep)
			t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })
			if !h.coord.TryReserveTurn("c1", vibekit.TurnSourcePrompt) {
				t.Fatal("a chat holding a step turn refused a prompt reservation")
			}
			t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })
		}},
		{"admitted shell command", func(t *testing.T, h *Runtime) {
			if !h.coord.TryReserveTurn("c1", vibekit.TurnSourceLocalShell) {
				t.Fatal("a fresh chat refused a shell reservation")
			}
			t.Cleanup(func() { h.coord.ReleaseTurnReservation("c1") })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			tc.setUp(t, h)

			busy := false
			for _, id := range h.coord.turns.busyChatIDs() {
				if id == "c1" {
					busy = true
				}
			}
			if got := h.TurnOpenState("c1"); got.OwnTurn() != busy {
				t.Errorf("OwnTurn() = %v from %+v, but busy_chats says %v; the handshake and "+
					"the transcript GET disagree about whether this chat is running",
					got.OwnTurn(), got, busy)
			}
		})
	}
}
