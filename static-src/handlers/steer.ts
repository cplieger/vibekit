// ---------------------------------------------------------------------------
// Mid-turn steering SSE handlers.
//
// Three events, three different facts, and keeping them apart is the point:
//
//   steer_queued    KAS's buffer has the message; the agent has NOT seen it
//   steer_injected  the model has read it; the redirection is in effect
//   steer_cleared   these were dropped at a turn boundary, unread
//
// The first confirms a dock row, the other two REMOVE one — a steer leaves the
// dock the moment it is resolved either way, and lands in the turn transcript as
// a note (`steer_marks`, rendered by fundamentals/steer-note.ts): read, or "not
// delivered".
//
// steer_injected arrives TWICE for a steer the agent answers, and the second one
// is not a duplicate: the first is KAS's read frame, the second carries `ack` —
// the agent's own statement of what it did — off the assistant text stream once
// it has acted. Both merge onto the same note by id.
//
// THIS IS NO LONGER THE ONLY WRITER of `session.steers`, and the second writer is
// deliberate: `chat.steer`'s `optimistic` draws the row on submit and its
// `rollback` un-draws it on a refusal. The division is intent versus fact — the
// client says "I sent this", the server says "it is in the buffer / the agent
// read it / it was dropped" — and the reconcile is by the derivable id, so the
// optimistic row and the confirmed one are never two rows. Without that half the
// user watched the composer clear and saw nothing for a whole POST, an awaited
// JSON-RPC round trip to the kiro-cli subprocess and an SSE fan-out.
//
// A steer's own text does NOT reach the transcript through the message handlers
// during the live turn. `user_message_chunk` has no live handler
// (`internal/agent/translate.go:345`) and is decoded only by the `session/load`
// replay projection (`internal/translate/projection.go:187-200`), so a steer is a
// transcript message only after a RELOAD. That is why the client promotes it into
// the running turn itself: otherwise the transcript shows the agent changing
// course with nothing explaining why.
//
// A FOURTH event lands in this file, `agent_notice`, because it arrives on the
// same KAS channel: a workflow step's or a subagent's progress line, which the
// server splits out before it reaches the client. It is not steering and it
// touches none of the state above. See the block at the bottom.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { recordSteerQueued, promoteSteer, dropSteers } from "../store.js";
import { info, success, error } from "../toast.js";

onSSE("steer_queued", (chatID, p) => {
  recordSteerQueued(chatID, { id: p.steer_id, text: p.text });
});

onSSE("steer_injected", (chatID, p) => {
  promoteSteer(chatID, p.steer_id, p.text, p.ack);
});

// Named ids only. KAS clears its buffer at EVERY turn boundary and the server
// suppresses the empty case, so an event that arrives here always names
// something — and dropping by id rather than wholesale is what lets an explicit
// discard of two steers coexist with a third that arrived in between.
//
// An id the agent already READ is routine on this frame (the boundary clears
// everything) and `dropSteers` ignores it: it already has a note, and a second
// one claiming it was missed would be false.
onSSE("steer_cleared", (chatID, p) => {
  dropSteers(chatID, p.steer_ids);
});

// ---------------------------------------------------------------------------
// The agent's own notices.
//
// KAS delivers a workflow step's or a subagent's progress line through the same
// steering buffer, because that buffer is the only inbound path into a live
// turn. It arrives here as its own event, so this handler never has to work out
// whose words it is holding.
//
// A TOAST, deliberately, and not the chip row it used to land on: nobody is
// waiting on it, nobody can discard it, and it has no later state to update.
// Not a persistent banner either — a banner is keyed for replacement and asks to
// be dismissed, which is the wrong shape for a line that is one of several a
// long run emits. And not a transcript row: the step's own output already lands
// in its delegated-work block, so a second copy would double-report it.
//
// Chat-scoped in principle and global in practice: the notice names no chat in
// its text, so a toast fired for a background chat would be unattributable. The
// severity is what the level comes from, matching how a finished run's toast
// picks its own.
// ---------------------------------------------------------------------------
onSSE("agent_notice", (_chatID, p) => {
  const text = p.text.trim();
  if (text === "") {
    return;
  }
  switch (p.severity) {
    case "success":
      success(text);
      return;
    case "warning":
    case "error":
      // Both take the error face. The toast vocabulary has three levels and KAS
      // has four; a warning is closer to an error than to an aside, and the
      // alternative was inventing a fourth level for a channel that has never
      // been seen to emit one.
      error(text);
      return;
    default:
      // `info`, and anything a later KAS adds. An unrecognised severity is
      // still a notice worth showing, so it is shown rather than dropped.
      info(text);
  }
});
