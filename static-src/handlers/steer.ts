// ---------------------------------------------------------------------------
// Mid-turn steering SSE handlers.
//
// Three events, three different facts, and keeping them apart is the point:
//
//   steer_queued    KAS's buffer has the message; the agent has NOT seen it
//   steer_injected  the model has read it; the redirection is in effect
//   steer_cleared   these were dropped at a turn boundary, unread
//
// steer_injected arrives TWICE for a steer the agent answers, and the second one
// is not a duplicate: the first is KAS's read frame, the second carries `ack` —
// the agent's own statement of what it did — off the assistant text stream once
// it has acted. Both merge onto the same chip by id.
//
// This is the ONLY writer of `session.steers`. The code that sends a steer
// records nothing locally (submit.ts), so the chip a user sees is always the
// server's account of what the agent knows — which is what makes the row correct
// on a second device and after a reconnect, not just on the tab that typed it.
//
// A steer's own text is not rendered as a message here. It reaches the transcript
// as an ordinary user turn when KAS persists it (`source: "steer"`), through the
// same message handlers as everything else.
//
// A FOURTH event lands in this file, `agent_notice`, because it arrives on the
// same KAS channel: a workflow step's or a subagent's progress line, which the
// server splits out before it reaches the client. It is not steering and it
// touches none of the state above. See the block at the bottom.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { recordSteerQueued, markSteerInjected, clearSteers } from "../store.js";
import { info, success, error } from "../toast.js";

onSSE("steer_queued", (chatID, p) => {
  recordSteerQueued(chatID, { id: p.steer_id, text: p.text });
});

onSSE("steer_injected", (chatID, p) => {
  markSteerInjected(chatID, p.steer_id, p.text, p.ack);
});

// Named ids only. KAS clears its buffer at EVERY turn boundary and the server
// suppresses the empty case, so an event that arrives here always names
// something — and clearing by id rather than wholesale is what lets an explicit
// discard of two steers coexist with a third that arrived in between.
onSSE("steer_cleared", (chatID, p) => {
  clearSteers(chatID, p.steer_ids);
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
