// ---------------------------------------------------------------------------
// The one way to write the composer's value programmatically.
//
// A `.value` assignment fires no `input` event, and the per-chat draft layer
// (composer-state.ts) listens for exactly that: it holds the AUTHORITATIVE copy
// of what the user has typed, and a write it never sees is text the server never
// learns about. Two bugs came from that silence, in both directions — the
// autosave kept a sent message on the chat record, and a prompt opened from the
// PWA share target was lost on the next reload because nothing scheduled a save
// for it.
//
// So every programmatic write goes through here and announces itself. The event
// bubbles, like the real thing a keystroke produces: a synthetic non-bubbling
// `input` is invisible to any listener that is not on the element itself, which
// is a difference nobody writing a listener would expect.
//
// This is a module of its own rather than a helper inside prompt-input.ts
// because the writers are PEERS that must not import each other: send-state
// imports prompt-input to push the send button's state and transport imports
// send-state, so a static import from prompt-input to the draft layer (whose
// action reaches the transport) would close a cycle. Announcing on the element
// is what keeps the three of them apart, and a shared leaf is where the rule
// they share belongs.
//
// NOT for the draft layer's own restore/seed writes: those put the mapped value
// on screen, so announcing it would only feed the map its own copy back and
// schedule a save the server does not need.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";

/** Write the composer's value and announce it, so every listener on the box
 *  sees the change whoever made it. */
export function setComposerValue(v: string): void {
  const input = $.promptInput;
  input.value = v;
  input.dispatchEvent(new Event("input", { bubbles: true }));
}
