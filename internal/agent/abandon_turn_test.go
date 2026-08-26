package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// dividerIn returns the last interrupted-event message in a chat, or nil.
// A helper that cannot fail, so it takes no *testing.T and marks no t.Helper().
func dividerIn(chat *vibekit.Chat) *vibekit.Message {
	var last *vibekit.Message
	for i := range chat.Messages {
		if m := &chat.Messages[i]; m.Role == vibekit.RoleEvent && m.EventKind == vibekit.EventInterrupted {
			last = m
		}
	}
	return last
}

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

	h.AbandonInFlightTurn(ctx, "c1", "the pipe died")

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

	logs := captureLogs(t)
	h.AbandonInFlightTurn(ctx, "c1", "the pipe died")

	chat, ok := h.chatStore.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat record vanished")
	}

	var sawPartial bool
	for i := range chat.Messages {
		if m := &chat.Messages[i]; m.Role == vibekit.RoleAssistant && m.Content == partial {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Errorf("the partial assistant text was not persisted; the client showed it "+
			"and a reload would lose it. messages=%d", len(chat.Messages))
	}
	if dividerIn(chat) == nil {
		t.Error("no interrupted event message; the transcript shows a truncated reply " +
			"with nothing saying why it stops")
	}
	// The divider landed, so nothing reports otherwise. An error line on the
	// ORDINARY abandon means the badge was lost, and one that fires whenever the
	// append succeeded says that for every failed turn a user ever has.
	const persistFailed = "persist interrupted event"
	if out := logs.String(); strings.Contains(out, `"msg":"`+persistFailed+`"`) {
		t.Errorf("a successful append reported %q: %s", persistFailed, out)
	}
}

// TestAbandonInFlightTurn_MarksATurnThatNeverStarted is the case the user
// actually hits, and it INVERTS what this file used to assert.
//
// The old rule was that no buffer means no-op, on the reasoning that a stray
// badge would be noise in "every transcript that ever saw a transient error".
// That was wrong in the one direction that matters: a 429 or a capacity refusal
// answers BEFORE the first chunk, so there is no buffer, so the turn appended
// nothing at all — and turns.ts deriveOutcome, which reads persisted messages
// only, then classified it `completed` and hasTurnSummary suppressed its footer.
// A rate-limited turn was pixel-identical to a clean short answer, and the whole
// account of the failure lived in a hover tooltip that a reload discarded.
//
// The badge is not noise. It is the only durable record that the turn failed.
func TestAbandonInFlightTurn_MarksATurnThatNeverStarted(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx := t.Context()

	const reason = "Too many requests, please wait before trying again."
	h.AbandonInFlightTurn(ctx, "c1", reason)

	chat, ok := h.chatStore.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat record missing")
	}
	divider := dividerIn(chat)
	if divider == nil {
		t.Fatal("AbandonInFlightTurn(ctx, c1, <throttle>) appended no interrupted event " +
			"with no buffer in flight; the turn renders as a clean short answer")
	}
	if divider.Content != reason {
		t.Errorf("divider content = %q, want %q", divider.Content, reason)
	}
	// No assistant message: there was nothing to persist, and an empty one would
	// render as a blank reply bubble under the request.
	for i := range chat.Messages {
		if m := &chat.Messages[i]; m.Role == vibekit.RoleAssistant {
			t.Errorf("an unstarted turn persisted an assistant message %q; the divider is "+
				"the whole record", m.Content)
		}
	}
}

// TestAbandonInFlightTurn_CarriesTheCallersReason is the ordinary failed prompt,
// and it is the other inversion.
//
// This used to assert the divider stayed EMPTY here, because "a failed prompt
// already sends its reason as an error frame". The frame is real but ephemeral:
// it reaches one client surface, for the active chat, until the next reload. So
// the reason the server had all along never survived to the transcript, and the
// user was left with the generic "Turn interrupted" label.
func TestAbandonInFlightTurn_CarriesTheCallersReason(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx := t.Context()

	const reason = "The model is at capacity. (request req-9)"
	buf := h.bridge.assistantBufs.GetOrInit("c1")
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString("half an answer")

	h.AbandonInFlightTurn(ctx, "c1", reason)

	chat, ok := h.chatStore.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat record vanished")
	}
	divider := dividerIn(chat)
	if divider == nil {
		t.Fatal("no interrupted event message")
	}
	if divider.Content != reason {
		t.Errorf("divider content = %q, want %q — without it the transcript cannot say "+
			"why the turn stopped once the error frame is gone", divider.Content, reason)
	}
}

// TestAbandonInFlightTurn_StashedReasonBeatsTheCallers closes the loop on the
// tool-interruption fix, and pins the PRECEDENCE between the two writers.
//
// InterruptTurn stashes the cause when kiro-cli's tool-use security filter stops
// a turn. The prompt Call then fails as a consequence of that stop, so the
// caller's reason describes the fallout rather than the cause. The specific one
// has to win, or a security-filter stop reads as whatever RPC error it produced.
func TestAbandonInFlightTurn_StashedReasonBeatsTheCallers(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx := t.Context()

	const stashed = "Stopped by kiro-cli's tool-use security filter"
	const callers = "The turn was cancelled before the agent answered."

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

	h.coord.InterruptTurn("c1", stashed)

	// The prompt context must be dead, or the blocked Call never returns and the
	// chat answers 409 busy to every later Send.
	select {
	case <-pctx.Done():
	default:
		t.Error("InterruptTurn left the prompt context live")
	}

	h.AbandonInFlightTurn(ctx, "c1", callers)

	chat, ok := h.chatStore.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat record vanished")
	}
	divider := dividerIn(chat)
	if divider == nil {
		t.Fatal("no interrupted event message")
	}
	if divider.Content != stashed {
		t.Errorf("divider content = %q, want the stashed %q — the caller's %q describes "+
			"the RPC failure the filter caused, not the stop itself",
			divider.Content, stashed, callers)
	}
}
