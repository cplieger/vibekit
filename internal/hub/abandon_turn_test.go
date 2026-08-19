package hub

import (
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
