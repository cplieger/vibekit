// ---------------------------------------------------------------------------
// Pending steers: the row of chips between the textarea and the pill row
// showing mid-turn messages KAS is holding, and which of them the model has
// read.
//
// A pure projection of `activeSession.steers`, which is itself written only by
// the three steer SSE events (store.ts). This module sends nothing and records
// nothing; it renders server state and offers one control.
//
// TWO STATES, and telling them apart is the row's whole job. A steer that has
// reached KAS's buffer is WAITING — the agent has not seen it and is still doing
// whatever you are trying to redirect. Once `steer_injected` arrives it has been
// READ, and the redirection is in effect. Collapsing those into one "pending"
// look would hide the only fact a person steering a live turn is watching for.
//
// A read chip also carries WHAT THE AGENT DID, when the agent said so. KAS asks
// it to close its response with `[STEERING steer-<id>: what I did about it]`;
// vibekit hides that marker from the transcript as machinery, and the sentence
// inside it lands here instead (`steer.ack`). It is strictly better information
// than a check glyph — "read" becomes "read: rebased onto main instead" — and it
// is the agent's own account rather than an inference, which is why the chip
// shows it verbatim and adds no interpretation of its own.
// Neither is a sent user message: a steer becomes transcript when the turn's
// messages arrive, exactly like a prompt waiting on `message_appended`
// (vibekit.md #2/#6).
//
// ONE CONTROL, AND IT IS ALL-OR-NOTHING. `_session/steer/clear` takes a session
// and nothing else — there is no per-steer removal on the wire — so Discard
// drops every waiting steer at once and says so. A per-chip × would be a lie
// about what the click does. It is offered only while something is still
// waiting, because a steer the model has read cannot be unsent, and it does not
// cancel the turn: changing your mind about a message is not changing your mind
// about the work.
//
// What this replaced: a chip row over a client-side FIFO, with a per-chip × that
// removed one entry and a per-chip "send now" that CANCELLED the running turn to
// jump the queue. Neither has a job now — a steer is delivered as fast as the
// next node boundary, so there is no queue to jump and no wait to skip.
// ---------------------------------------------------------------------------

import { el, computed, effect } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { $ } from "./dom.js";
import { activeSession, getActiveId } from "./store.js";
import { clearSteers } from "./actions/chat.js";
import { ICON_HOURGLASS, ICON_CHECK } from "./icons.js";
import { iconEl } from "./icon-el.js";
import type { PendingSteer } from "./types.js";

const PREVIEW_MAX = 60;

let bound = false;
let prevWaiting = 0;
let prevId = "";

/** Wire the reactive render. Idempotent. Called once from app.ts. */
export function initPendingSteers(): void {
  if (bound) {
    return;
  }
  bound = true;
  const row = $.queuedRow;
  // Re-render only when the active chat, the steer texts, their READ state or
  // their acknowledgement changes. The computed returns a string so it dedups by
  // value — an unrelated session write (usage, thinking, a streaming chunk) must
  // not re-render the row, and `injected` and `ack` each have to be in the key or
  // a steer being read, or answered, would repaint nothing.
  const sig = computed(() => {
    const s = activeSession.value;
    const steers = s?.steers ?? [];
    return (
      (s?.id ?? "") +
      "\u0001" +
      steers
        .map((e) => (e.injected ? "1" : "0") + ":" + (e.ack ?? "") + "\u0002" + e.text)
        .join("\u0000")
    );
  });
  effect(() => {
    void sig.value;
    render(row);
  });
}

function render(row: HTMLUListElement): void {
  const s = activeSession.peek();
  const steers = s?.steers ?? [];
  const id = s?.id ?? "";
  const waiting = steers.filter((e) => !e.injected).length;

  row.replaceChildren();
  if (steers.length === 0) {
    row.classList.add("hidden");
    // Reset the announce baseline so arriving at a chat that already has steers
    // reads them out fresh, while the empty case stays silent.
    prevWaiting = 0;
    prevId = id;
    return;
  }
  row.classList.remove("hidden");

  for (const steer of steers) {
    row.appendChild(buildChip(steer));
  }
  if (waiting > 0) {
    row.appendChild(buildDiscard(waiting));
  }

  // Announce only on the same chat, and only the WAITING count — the number the
  // user is waiting to see fall. A pure chat switch is not news.
  if (id === prevId && waiting !== prevWaiting) {
    announce(
      waiting === 0
        ? "Steering message delivered to the agent"
        : waiting === 1
          ? "1 steering message waiting for the agent"
          : `${String(waiting)} steering messages waiting for the agent`,
    );
  }
  prevWaiting = waiting;
  prevId = id;
}

function buildChip(steer: PendingSteer): HTMLElement {
  const preview = truncate(steer.text);
  const read = steer.injected;
  // Only on a read chip. An ack cannot arrive before the read frame, but the
  // states are independent on the wire and a "waiting" chip claiming an outcome
  // would be the worst thing this row could say.
  const ack = read ? (steer.ack ?? "") : "";
  const icon = el(
    "span",
    { className: "queued-icon", "aria-hidden": "true" },
    iconEl(read ? ICON_CHECK : ICON_HOURGLASS),
  );
  const label = el("span", { className: "queued-text" }, preview);
  const li = el(
    "li",
    {
      className: read ? "queued-prompt steer-read" : "queued-prompt",
      // Both halves in full, because the visible text of each is truncated and
      // the agent's answer is the part worth reading whole.
      title: ack === "" ? steer.text : steer.text + "\n\n" + ack,
      // The state is carried by the glyph, so it has to be in the accessible
      // name too — a shape-only signal is invisible to a screen reader. The ack
      // is a second visual channel and rides the same rule.
      //
      // The RAW strings, not the truncated ones: truncation is a layout answer
      // to a fixed-width chip, and the accessible name has no width. Passing
      // the preview here left a screen-reader user with only the 60-character
      // summary while the full text existed solely in a hover tooltip, which is
      // not an accessible alternative to anything.
      "aria-label": accessibleName(steer.text, read, ack),
    },
    icon,
    label,
  );
  if (ack !== "") {
    li.appendChild(el("span", { className: "queued-ack" }, truncate(ack)));
  }
  // A notification did not come from the user (vibekit refuses to send one), so
  // it is marked as somebody else's voice rather than styled like their words.
  if (steer.severity !== undefined && steer.severity !== "") {
    li.classList.add("steer-notice");
    li.dataset["severity"] = steer.severity;
  }
  return li;
}

// accessibleName spells out the chip's state in words, because the glyph, the
// border treatment and the ack span are all visual.
//
// Nothing is shortened here. Both strings are collapsed to one line, because an
// accessible name is announced as a single string and stray newlines buy
// nothing, but the whole of each is present: the visible chip truncates to fit
// a fixed-width row, and a reader who cannot see the row is not subject to that
// constraint. The tooltip carries the same two strings for a mouse.
function accessibleName(text: string, read: boolean, ack: string): string {
  const steerText = oneLine(text);
  if (!read) {
    return `Waiting for the agent: ${steerText}`;
  }
  if (ack === "") {
    return `Read by the agent: ${steerText}`;
  }
  return `Read by the agent: ${steerText}. The agent did: ${oneLine(ack)}`;
}

function buildDiscard(waiting: number): HTMLElement {
  const label =
    waiting === 1 ? "Discard the waiting message" : `Discard ${String(waiting)} waiting messages`;
  const btn = el(
    "button",
    {
      type: "button",
      className: "queued-cancel",
      "aria-label": label,
      // Naming the all-or-nothing behaviour in the tooltip, because the wire has
      // no per-message clear and a user would reasonably assume otherwise.
      "data-tooltip": "Discard every message the agent hasn't read yet",
    },
    "\u00d7",
  );
  btn.addEventListener("click", onDiscard);
  return el("li", { className: "queued-discard" }, btn);
}

function onDiscard(): void {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  // Fire and forget: the row repaints from KAS's own `steer_cleared` frame, not
  // from this reply, so every device agrees and a reconnect cannot leave a chip
  // behind for a message that is gone.
  void clearSteers.dispatch({ chatID });
  announce("Discarding messages the agent hasn't read");
}

// oneLine collapses whitespace without shortening. Split out of truncate
// because the accessible name needs the normalisation and not the cut.
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

function truncate(text: string): string {
  const flat = oneLine(text);
  return flat.length > PREVIEW_MAX ? flat.slice(0, PREVIEW_MAX - 1) + "\u2026" : flat;
}
