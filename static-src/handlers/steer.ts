// ---------------------------------------------------------------------------
// Mid-turn steering SSE handlers.
//
// Three events: `steer_queued` (KAS has the message, agent has not read it),
// `steer_injected` (the model has read it), `steer_cleared` (dropped unread
// at a turn boundary). The first confirms a dock row; the other two remove
// one and land a note in the transcript (`steer_marks`,
// fundamentals/steer-note.ts).
//
// `steer_injected` arrives twice for a steer the agent answers: the read
// frame, then an `ack` off the assistant text stream once it has acted.
// Both merge onto the same note by id.
//
// This is not the only writer of `session.steers` — `chat.steer`'s
// `optimistic` draws the row on submit and `rollback` un-draws it on
// refusal, reconciled by the derivable id so the optimistic and confirmed
// rows never duplicate.
//
// A steer's own text does not reach the transcript through the message
// handlers during the live turn (`user_message_chunk` has no live handler,
// only the session/load replay projection), so the client promotes it into
// the running turn itself.
//
// A fourth event, `agent_notice`, lands here because it arrives on the same
// KAS channel: a workflow step's or subagent's progress line, split out by
// the server before it reaches the client.
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

// Named ids only. KAS clears its buffer at every turn boundary and the
// server suppresses the empty case. An id the agent already read is
// routine here and `dropSteers` ignores it, since it already has a note.
onSSE("steer_cleared", (chatID, p) => {
  dropSteers(chatID, p.steer_ids);
});

// ---------------------------------------------------------------------------
// The agent's own notices: a workflow step's or subagent's progress line,
// delivered through the same steering buffer and split into its own event.
//
// A toast, deliberately: nobody is waiting on it or can discard it, and it
// has no later state to update. Not a transcript row either — the step's
// output already lands in its delegated-work block.
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
      // Both take the error face — warning is closer to error than aside.
      error(text);
      return;
    default:
      info(text);
  }
});
