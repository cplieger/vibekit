// ---------------------------------------------------------------------------
// The steer stack: full-width rows in the bottom bar, above the message box,
// listing the mid-turn messages KAS is holding and which of them the model has
// read.
//
// WHERE IT SITS, AND WHY. A sibling of `.prompt-box` inside `#prompt-form`,
// under `#decision-dock`. These are messages already SENT, so they belong beside
// the box rather than inside it; the bar grows upward to expose them, exactly as
// it does for a permission ask. Below the dock because a permission ask BLOCKS
// the turn, so it outranks a record of what has already gone. Oldest at the top:
// `session.steers` is in arrival order and renders in it, so a new message
// appears at the bottom and pushes the older ones up.
//
// It replaced a horizontal chip row wedged between the textarea and the pill
// row, where a 60-character preview was all that fitted and the row read as
// composer furniture rather than as a record of sent messages.
//
// EVERY ROW IS THE USER'S OWN MESSAGE. That is what the stack says: this is what
// you told the agent, whether it has seen it, and what it did about it. An
// agent's own progress notice arrives on the same KAS buffer and used to land
// here as a steer carrying a severity, which put a line the agent wrote inside
// the message box styled as something typed into it. Those are `agent_notice`
// now and they toast.
//
// A pure projection of `activeSession.steers`, which is itself written only by
// the three steer SSE events (store.ts). This module sends nothing and records
// nothing; it renders server state and offers the controls the WIRE can honour.
//
// THE STACK HOLDS ONLY WHAT THE AGENT HAS NOT READ. That is the invariant, and
// it is what a person steering a live turn is watching: these messages are in
// KAS's buffer and the agent is still doing the thing you are trying to redirect.
// A steer LEAVES the stack the moment it is read (`steer_injected`) or dropped at
// a turn boundary (`steer_cleared`), and reappears INSIDE the turn transcript as
// a note at the block it landed on (fundamentals/steer-note.ts). So the count
// here falling to zero is the whole read signal; there is no settled row to
// distinguish, and there is no checkmark.
//
// That replaced a green check on the row plus the agent's own account of what it
// did (`[STEERING steer-<id>: ...]`, which vibekit strips from the transcript as
// machinery). Both were real information in the wrong place: a tick in the
// composer while the transcript showed the agent change course with nothing
// explaining why. The ack rides the transcript note now.
//
// TWO STATES, and both are "not read yet". `pending` is this device's own claim
// that a POST is in flight — drawn on submit, so the row appears on the keystroke
// rather than after a round trip, and un-drawn by the action's rollback if the
// POST fails. Once KAS's `steer_queued` frame confirms it, the row is SENT and
// gains its controls, because only then is there a server-side id a clear can
// address.
//
// WHAT THE CONTROLS CAN BE, measured against KAS 2.18.0's own source rather
// than assumed. There are exactly two steer verbs, `_session/steer` and
// `_session/steer/clear`, and `handleSessionSteerClear` reads ONLY `sessionId`:
// it drains the whole buffer through `clearSteeringAtTurnBoundary` and bumps the
// steering epoch. So there is no per-steer removal and no edit verb anywhere on
// the wire, and three rules follow:
//
//   - A `pending` row carries no controls at all. Its id is derived rather than
//     confirmed, so there is no server-side id to clear; a control there would
//     be a button that cannot act yet.
//   - Discard appears on the confirmed rows and always drops EVERY unread
//     message.
//     With one unread that is unambiguous, so it acts immediately. With more it
//     confirms first, naming the count, because a × beside one row looks like it
//     removes that row.
//   - Edit appears only when exactly ONE message is unread. It is discard plus
//     the text back in the composer, which is the only honest spelling of an
//     edit here. With two or more unread it is withheld, because taking one back
//     would silently drop the others.
//
// The rejected alternative, for the next person who reaches for it: clear the
// buffer and re-send the keepers to synthesize a per-row delete. It changes
// every survivor's id, and it has a real failure window — the turn can end
// between the clear and the re-sends, leaving messages that were shown as kept
// simply gone.
// ---------------------------------------------------------------------------

import { el, computed, effect } from "@cplieger/reactive";
import { attachClamp } from "./clamp-text.js";
import { announce } from "@cplieger/ui-primitives/announce";
import { $ } from "./dom.js";
import { activeSession, getActiveId } from "./store.js";
import { clearSteers } from "./actions/chat.js";
import { setComposerValue } from "./composer-value.js";
import { confirm } from "./confirm.js";
import { ICON_HOURGLASS, ICON_EDIT, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import type { PendingSteer } from "./types.js";

/** Lines a dock row shows before its opener appears. Four, matching the
 *  transcript note: the bar grows upward into the transcript, so an unbounded row
 *  here costs the reader the conversation, and four plus an opener is the
 *  compromise — the row already carries the whole text in its `title` and in its
 *  accessible name. */
const DOCK_CLAMP_LINES = 4;

let bound = false;
let prevWaiting = 0;
let prevId = "";

/** Wire the reactive render. Idempotent. Called once from app.ts. */
export function initPendingSteers(): void {
  if (bound) {
    return;
  }
  bound = true;
  const stack = $.steerStack;
  // Re-render only when the active chat, the steer texts or their SENDING state
  // change. The computed returns a string so it dedups by value — an unrelated
  // session write (usage, thinking, a streaming chunk) must not re-render the
  // stack — and `pending` has to be in the key or a row gaining its controls when
  // `steer_queued` confirms it would repaint nothing.
  const sig = computed(() => {
    const s = activeSession.value;
    const steers = s?.steers ?? [];
    return (
      (s?.id ?? "") +
      "\u0001" +
      steers.map((e) => (e.pending === true ? "1" : "0") + "\u0002" + e.text).join("\u0000")
    );
  });
  effect(() => {
    void sig.value;
    render(stack);
  });
}

function render(stack: HTMLUListElement): void {
  const s = activeSession.peek();
  const steers = s?.steers ?? [];
  const id = s?.id ?? "";
  // Every row in the stack is waiting; a confirmed one is what a clear can
  // actually address, which is what decides whether Edit is offerable.
  const waiting = steers.filter((e) => e.pending !== true).length;

  stack.replaceChildren();
  if (steers.length === 0) {
    stack.classList.add("hidden");
    // Reset the announce baseline so arriving at a chat that already has steers
    // reads them out fresh, while the empty case stays silent.
    prevWaiting = 0;
    prevId = id;
    return;
  }
  stack.classList.remove("hidden");

  // Arrival order, so the newest sits at the bottom nearest the box it was typed
  // into and every earlier one moves up.
  for (const steer of steers) {
    stack.appendChild(buildRow(steer, waiting));
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

/** One full-width row. `waiting` is the stack-wide confirmed count, which is what
 *  decides whether Edit is offerable — see the header. */
function buildRow(steer: PendingSteer, waiting: number): HTMLElement {
  const sending = steer.pending === true;

  const state = el(
    "span",
    { className: "steer-state" },
    el("span", { className: "steer-state-icon", "aria-hidden": "true" }, iconEl(ICON_HOURGLASS)),
    // The word, not only the glyph. "Sent" is the fact the user asked this stack
    // to state: the message has left, it is not a draft, and it is waiting.
    el("span", { className: "steer-state-label" }, sending ? "Sending" : "Sent"),
  );

  // No truncation here: the text clamps to four lines in CSS and the button
  // below OPENS it, so the whole message is in the DOM and reachable rather than
  // cut at an ellipsis. Still collapsed to one line, unlike the transcript note:
  // this is composer furniture and the bar grows upward into the transcript, so
  // an unbounded row here costs the reader the conversation.
  const text = el("span", { className: "steer-text" }, oneLine(steer.text));
  const more = el("button", {
    className: "steer-more",
    type: "button",
  }) as HTMLButtonElement;
  // A SIBLING of the clamped element, or the clamp would hide its own opener.
  const body = el("span", { className: "steer-body" }, text, more);

  const row = el(
    "li",
    {
      className: "steer-row",
      // Read by CSS, so the in-flight row differs by more than a word without a
      // second class to keep in sync.
      "data-state": sending ? "sending" : "sent",
      // The RAW text, not the clamped one: the visible row is clamped by layout.
      title: steer.text,
      // The state is carried by the glyph AND the label, both visual, so it has
      // to be in the accessible name too.
      "aria-label": accessibleName(steer.text, sending),
    },
    state,
    body,
  );

  const actions = buildActions(steer, sending, waiting);
  if (actions !== null) {
    row.appendChild(actions);
  }
  attachClamp(text, more, { lines: DOCK_CLAMP_LINES });
  return row;
}

/** The right-hand controls, or null for a row that honestly has none.
 *
 *  A row still SENDING gets nothing: its id is derived rather than confirmed, so
 *  `_session/steer/clear` has nothing to address yet and a control would be one
 *  that cannot act. It gains them when `steer_queued` lands. */
function buildActions(steer: PendingSteer, sending: boolean, waiting: number): HTMLElement | null {
  if (sending) {
    return null;
  }
  const actions = el("span", { className: "steer-actions" });
  // Edit is discard-plus-retype, so it is only offered when discarding cannot
  // take anything else with it.
  if (waiting === 1) {
    actions.appendChild(
      actionButton(
        ICON_EDIT,
        "Edit this message",
        "Take it back and put it in the message box",
        () => {
          void editSteer(steer.text);
        },
      ),
    );
  }
  actions.appendChild(
    actionButton(
      ICON_TRASH,
      waiting === 1 ? "Discard this message" : `Discard all ${String(waiting)} unread messages`,
      // Naming the all-or-nothing behaviour, because the wire has no per-message
      // clear and a reader would reasonably assume otherwise.
      waiting === 1
        ? "The agent has not read it yet"
        : "The agent's buffer clears all at once; there is no per-message removal",
      () => {
        void discardSteers(waiting);
      },
      "steer-act-danger",
    ),
  );
  return actions;
}

function actionButton(
  icon: string,
  label: string,
  tooltip: string,
  onClick: () => void,
  extraClass = "",
): HTMLElement {
  const btn = el(
    "button",
    {
      type: "button",
      className: extraClass === "" ? "steer-act" : `steer-act ${extraClass}`,
      "aria-label": label,
      "data-tooltip": tooltip,
    },
    iconEl(icon),
  );
  btn.addEventListener("click", (e: Event) => {
    // The row carries a title tooltip and is not itself interactive, but stop
    // here anyway so a future row-level affordance cannot fire off a button.
    e.stopPropagation();
    onClick();
  });
  return btn;
}

/** Take the only unread steer back and put its text in the composer.
 *
 *  The order matters: the box is filled BEFORE the clear is dispatched, so a
 *  failed clear leaves the user holding the text rather than losing it. The
 *  stack repaints from KAS's own `steer_cleared` frame, not from this call. */
async function editSteer(text: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  setComposerValue(text);
  $.promptInput.focus();
  announce("Message taken back for editing");
  await clearSteers.dispatch({ chatID });
}

/** Drop every steer the agent has not read.
 *
 *  Confirms only when more than one would go: with a single unread message the
 *  button's label and its effect already agree, and a dialog for the common case
 *  is a click that teaches nothing. */
async function discardSteers(waiting: number): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  if (waiting > 1) {
    const ok = await confirm(
      `Discard all ${String(waiting)} messages the agent has not read yet? They clear together — the wire has no way to remove just one.`,
      "Discard all",
      "destructive",
    );
    if (!ok) {
      return;
    }
  }
  announce("Discarding messages the agent hasn't read");
  // Fire and forget: the stack repaints from KAS's own `steer_cleared` frame, not
  // from this reply, so every device agrees and a reconnect cannot leave a row
  // behind for a message that is gone.
  await clearSteers.dispatch({ chatID });
}

// accessibleName spells out the row's state in words, because the glyph and the
// label's styling are both visual.
//
// Nothing is shortened here. The text is collapsed to one line, because an
// accessible name is announced as a single string and stray newlines buy
// nothing, but the whole of it is present: the visible row clamps to fit, and a
// reader who cannot see it is not subject to that constraint. The title
// attribute carries the same string for a mouse.
function accessibleName(text: string, sending: boolean): string {
  const steerText = oneLine(text);
  return sending
    ? `Sending, not in the agent's buffer yet: ${steerText}`
    : `Sent, waiting for the agent: ${steerText}`;
}

// oneLine collapses whitespace without shortening. A steer is one message
// however the user typed it, and the row is a single clamped block.
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}
