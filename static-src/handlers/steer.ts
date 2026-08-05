// ---------------------------------------------------------------------------
// Mid-turn steering SSE handlers.
//
// Three events, three different facts, and keeping them apart is the point:
//
//   steer_queued    KAS's buffer has the message; the agent has NOT seen it
//   steer_injected  the model has read it; the redirection is in effect
//   steer_cleared   these were dropped at a turn boundary, unread
//
// This is the ONLY writer of `session.steers`. The code that sends a steer
// records nothing locally (submit.ts), so the chip a user sees is always the
// server's account of what the agent knows — which is what makes the row correct
// on a second device and after a reconnect, not just on the tab that typed it.
//
// A steer's own text is not rendered as a message here. It reaches the transcript
// as an ordinary user turn when KAS persists it (`source: "steer"`), through the
// same message handlers as everything else.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { recordSteerQueued, markSteerInjected, clearSteers } from "../store.js";

onSSE("steer_queued", (chatID, p) => {
  recordSteerQueued(chatID, {
    id: p.steer_id,
    text: p.text,
    ...(p.severity !== undefined ? { severity: p.severity } : {}),
  });
});

onSSE("steer_injected", (chatID, p) => {
  markSteerInjected(chatID, p.steer_id, p.text);
});

// Named ids only. KAS clears its buffer at EVERY turn boundary and the server
// suppresses the empty case, so an event that arrives here always names
// something — and clearing by id rather than wholesale is what lets an explicit
// discard of two steers coexist with a third that arrived in between.
onSSE("steer_cleared", (chatID, p) => {
  clearSteers(chatID, p.steer_ids);
});
