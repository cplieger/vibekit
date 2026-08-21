package agent

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestAbandonInFlightTurn_ReleasesTheBuffer pins the mechanism behind the
// two-turns-in-one-message defect.
//
// CmdPrompt's error arm returned without any call that takes the assistant
// buffer, so a failed turn left the buffer in place with Started == true. The
// next prompt's ensureTurnStarted then saw a started buffer, skipped
// message_created, and appended the new turn's deltas to the dead turn's blocks
// under the dead turn's message id. The visible result was a single assistant
// message containing two replies, the second one rendered under the first one's
// turn header, and no way for the user to tell which was which.
//
// The assertion is on the buffer being GONE afterwards, because that is the
// state the next turn reads. Asserting on the persisted message instead would
// pass even if the buffer were left behind.
func TestAbandonInFlightTurn_ReleasesTheBuffer(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx := t.Context()

	// Start a turn the way streaming does, then abandon it.
	buf := h.bridge.assistantBufs.GetOrInit("c1")
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString("half an answer")

	h.AbandonInFlightTurn(ctx, "c1")

	if _, ok := h.coord.TakeBuffer("c1"); ok {
		t.Error("the assistant buffer survived AbandonInFlightTurn; the next turn " +
			"would extend this dead turn's blocks under its message id")
	}
}

// TestAbandonInFlightTurn_PersistsThePartial is the direction half of the fix,
// and it is the reason this is a new method rather than a call to the existing
// FlushInFlightTurnOnSwitch.
//
// Both are "a turn ended badly, release the buffer", and they must resolve the
// partial in OPPOSITE directions. A model switch discards it: the user asked for
// a different answer, so the abandoned one is moot. A failed prompt keeps it:
// the user watched that text stream in, and invariant 1 says the client never
// displays what the server has not persisted. Dropping it here would make the
// transcript diverge on reload, which is the vanishing-message class this
// codebase has already paid for once.
func TestAbandonInFlightTurn_PersistsThePartial(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx := t.Context()

	const partial = "the model got this far before the pipe died"
	buf := h.bridge.assistantBufs.GetOrInit("c1")
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString(partial)

	h.AbandonInFlightTurn(ctx, "c1")

	chat, ok := h.chatStore.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat record vanished")
	}

	var sawPartial, sawInterrupted bool
	for i := range chat.Messages {
		m := &chat.Messages[i]
		if m.Role == vibekit.RoleAssistant && m.Content == partial {
			sawPartial = true
		}
		if m.Role == vibekit.RoleEvent && m.EventKind == vibekit.EventInterrupted {
			sawInterrupted = true
		}
	}
	if !sawPartial {
		t.Errorf("the partial assistant text was not persisted; the client showed it "+
			"and a reload would lose it. messages=%d", len(chat.Messages))
	}
	if !sawInterrupted {
		t.Error("no interrupted event message; the transcript shows a truncated reply " +
			"with nothing saying why it stops")
	}
}

// TestAbandonInFlightTurn_NoBufferIsANoOp guards the common case: most prompt
// failures happen before the model emits anything (a dead bridge, a fatal RPC
// error on send), so there is no buffer and nothing to persist. Writing an empty
// assistant message or a stray interrupted badge for those would put noise in
// every transcript that ever saw a transient error.
func TestAbandonInFlightTurn_NoBufferIsANoOp(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx := t.Context()

	before, ok := h.chatStore.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat record missing")
	}
	countBefore := len(before.Messages)

	h.AbandonInFlightTurn(ctx, "c1")

	after, _ := h.chatStore.Get(ctx, "c1")
	if got := len(after.Messages); got != countBefore {
		t.Errorf("messages went from %d to %d with no turn in flight; want no change",
			countBefore, got)
	}
}

// TestAbandonInFlightTurn_CarriesTheInterruptReason closes the loop on the
// tool-interruption fix: the cause has to arrive on the DIVIDER, because that is
// the only place a user sees it.
//
// The distinction being pinned is against a user cancel and against an ordinary
// failed prompt, both of which reach this same method and both of which leave the
// Content empty on purpose — a failed prompt already sends its reason as an error
// frame, and a user cancel needs no explanation. A turn kiro-cli abandoned
// without answering sends no frame of any kind, so an empty divider here would
// make it indistinguishable from a stop the user performed themselves.
func TestAbandonInFlightTurn_CarriesTheInterruptReason(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx := t.Context()

	const reason = "Stopped by kiro-cli's tool-use security filter"

	// Stage a turn the way streaming does, then interrupt it the way the sentinel
	// detector does — through the coordinator, so the wiring is exercised rather
	// than the bridge's method in isolation.
	buf := h.bridge.assistantBufs.GetOrInit("c1")
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString("about to call a tool")

	sb, _ := h.bridge.mgr.orInsert("c1")
	if !sb.tryAcquireForPrompt() {
		t.Fatal("fresh bridge must be acquirable")
	}
	pctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sb.BeginPromptCall(cancel)

	h.coord.InterruptTurn("c1", reason)

	// The prompt context must be dead, or the blocked Call never returns and the
	// chat answers 409 busy to every later Send.
	select {
	case <-pctx.Done():
	default:
		t.Error("InterruptTurn left the prompt context live")
	}

	h.AbandonInFlightTurn(ctx, "c1")

	chat, ok := h.chatStore.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat record vanished")
	}
	var divider *vibekit.Message
	for i := range chat.Messages {
		if m := &chat.Messages[i]; m.Role == vibekit.RoleEvent && m.EventKind == vibekit.EventInterrupted {
			divider = m
		}
	}
	if divider == nil {
		t.Fatal("no interrupted event message")
	}
	if divider.Content != reason {
		t.Errorf("divider content = %q, want %q — without it the transcript cannot say "+
			"who stopped the turn", divider.Content, reason)
	}
}

// TestAbandonInFlightTurn_LeavesTheDividerEmptyForOrdinaryFailures is the other
// half of the pair above, and the reason it is a separate test: a reason that
// leaked onto every failed turn would be worse than none, because it would name a
// cause that did not apply.
func TestAbandonInFlightTurn_LeavesTheDividerEmptyForOrdinaryFailures(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx := t.Context()

	buf := h.bridge.assistantBufs.GetOrInit("c1")
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString("half an answer")

	h.AbandonInFlightTurn(ctx, "c1")

	chat, _ := h.chatStore.Get(ctx, "c1")
	for i := range chat.Messages {
		m := &chat.Messages[i]
		if m.Role == vibekit.RoleEvent && m.EventKind == vibekit.EventInterrupted && m.Content != "" {
			t.Errorf("an ordinary failed turn got divider content %q; the error frame is "+
				"where that turn's reason belongs", m.Content)
		}
	}
}
