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
// WHAT THE CONTROLS CAN BE, measured against KAS's own source rather than
// assumed (re-measured on 2.21.2). There are exactly two steer verbs,
// `_session/steer` and `_session/steer/clear`, and `handleSessionSteerClear`
// reads ONLY `sessionId`: it drains the whole buffer through
// `clearSteeringAtTurnBoundary` and bumps the steering epoch. Nothing on the wire
// injects. Four rules follow:
//
//   - A `pending` row carries no controls at all. Its id is derived rather than
//     confirmed, so there is no server-side id to clear; a control there would
//     be a button that cannot act yet.
//   - Send now stops the TURN and sends the message as a new one, the only
//     reading the wire can honour. It leads the column; `steer-resend.ts` owns
//     the mechanism.
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

import { el, computed, effect, touch } from "@cplieger/reactive";
import { attachClamp, releaseClampsIn } from "./clamp-text.js";
import { announce } from "@cplieger/ui-primitives/announce";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { $ } from "./dom.js";
import { activeSession, getActiveId, pendingSteerCarry } from "./store.js";
import { cancelTurn, clearSteers } from "./actions/chat.js";
import { forgetSteerPreference, preferSteerFirst } from "./steer-resend.js";
import { setComposerValue } from "./composer-value.js";
import { confirm } from "./confirm.js";
import { ICON_ARROW_UP, ICON_HOURGLASS, ICON_EDIT, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import type { PendingSteer } from "./types.js";

/** Lines a dock row shows before its opener appears. Four, matching the
 *  transcript note: the bar grows upward into the transcript, so an unbounded row
 *  here costs the reader the conversation, and four plus an opener is the
 *  compromise — the row already carries the whole text in its tooltip and in its
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
    touch(sig);
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

  if (steers.length === 0) {
    reconcile(stack, [], rowSpec(waiting));
    stack.classList.add("hidden");
    // Reset the announce baseline so arriving at a chat that already has steers
    // reads them out fresh, while the empty case stays silent.
    prevWaiting = 0;
    prevId = id;
    return;
  }
  // Un-hidden BEFORE the rows land, because `.hidden` is `display: none` and an
  // element inserted into a subtree with no box has its first style resolution
  // there — which is the one the entry transition below reads.
  stack.classList.remove("hidden");
  // Arrival order, so the newest sits at the bottom nearest the box it was typed
  // into and every earlier one moves up.
  reconcile(stack, steers, rowSpec(waiting));

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

/** KEYED BY THE STEER'S OWN ID, so a row survives its own confirmation.
 *
 *  The node is what has to be kept. `.steer-row` enters through `@starting-style`
 *  (26-dock.css), which supplies a before-change style to every element being
 *  rendered for the FIRST time — so a freshly inserted node always fades in from
 *  `opacity: 0` and `translateY(4px)`, and one Send produces two renders a round
 *  trip apart (`recordSteerSent`, then `recordSteerQueued` off the POST's own
 *  reply). Rebuilding the row on the second one therefore replayed that entry
 *  fade over a row already on screen, interrupting the first fade mid-flight:
 *  the row appeared, dropped back to invisible and appeared again. Keeping the
 *  node makes the second render an attribute write, which `@starting-style`
 *  cannot re-fire.
 *
 *  Two things ride along. The clamp keys its state to the text element
 *  (`clamp-text.ts`), so a message the reader opened stays open through the
 *  confirmation instead of re-collapsing, and its measured verdict is not thrown
 *  away and re-guessed on a detached node.
 *
 *  `waiting` is stack-wide rather than per-entry, so the spec is built per render
 *  rather than held as a module constant. */
function rowSpec(waiting: number): ReconcileSpec<PendingSteer> {
  return {
    key: (steer) => steer.id,
    mount: (steer) => buildRow(steer, waiting),
    update: (row, steer) => {
      updateRow(row, steer, waiting);
    },
    // The row's message clamp goes with the row. A dock row leaves on a promote
    // or a clear, and this is the whole of that teardown — the release has to be
    // explicit, because the observer's own zero-size callback may never arrive
    // (`clamp-text.ts` `releaseClamp`).
    onRemove: (row) => {
      releaseClampsIn(row);
    },
  };
}

/** Bring an existing row up to date in place. Everything a render can change is
 *  written here: the state (attribute, word and accessible name), the message, and
 *  the controls. `sending` -> `sent` is the transition every steer makes. */
function updateRow(row: HTMLElement, steer: PendingSteer, waiting: number): void {
  const sending = steer.pending === true;
  row.dataset["state"] = sending ? "sending" : "sent";
  row.dataset["tooltip"] = steer.text;
  row.setAttribute("aria-label", accessibleName(steer.text, sending));

  const label = row.querySelector(".steer-state-label");
  if (label !== null) {
    label.textContent = sending ? "Sending" : "Sent";
  }

  syncText(row, steer.text);
  syncActions(row, steer, sending, waiting);
}

/** No path in the store rewrites the text of an id it already holds, so this is
 *  the update being TOTAL over the item rather than a live case. It goes through
 *  the clamp's own handle because new content invalidates both an expansion and
 *  the opener's verdict. */
function syncText(row: HTMLElement, text: string): void {
  const textEl = row.querySelector<HTMLElement>(".steer-text");
  const more = row.querySelector<HTMLButtonElement>(".steer-more");
  if (textEl === null || more === null) {
    return;
  }
  const body = oneLine(text);
  if (textEl.textContent === body) {
    return;
  }
  textEl.textContent = body;
  attachClamp(textEl, more, { lines: DOCK_CLAMP_LINES }).collapse();
}

/** The signature the row's controls were last built for. */
const actionSig = new WeakMap<HTMLElement, string>();

/** Build, replace or remove the controls, and leave them alone when neither input
 *  moved. Leaving them alone is the point: replacing a button takes focus off one
 *  a keyboard reader is on, and a second message arriving is a render where every
 *  earlier row's controls are unchanged. */
function syncActions(
  row: HTMLElement,
  steer: PendingSteer,
  sending: boolean,
  waiting: number,
): void {
  const sig = `${sending ? "1" : "0"}\u0001${String(waiting)}`;
  if (actionSig.get(row) === sig) {
    return;
  }
  actionSig.set(row, sig);
  const column = row.querySelector<HTMLElement>(".steer-actions");
  if (column !== null) {
    fillActions(column, steer, sending, waiting);
  }
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

  // Built empty and kept for the row's life: it is what reserves the height a
  // control needs, so the confirmation reveals buttons into a box that was always
  // there rather than growing the row under the reader. See `fillActions`.
  const actions = el("span", { className: "steer-actions" });

  const row = el(
    "li",
    {
      className: "steer-row",
      // Read by CSS, so the in-flight row differs by more than a word without a
      // second class to keep in sync.
      "data-state": sending ? "sending" : "sent",
      // The RAW text, not the clamped one: the visible row is clamped by layout.
      "data-tooltip": steer.text,
      // The state is carried by the glyph AND the label, both visual, so it has
      // to be in the accessible name too.
      "aria-label": accessibleName(steer.text, sending),
    },
    state,
    body,
    actions,
  );

  // Through the same helper the update path uses, so the signature it compares
  // against is recorded for the row's first paint too.
  syncActions(row, steer, sending, waiting);
  attachClamp(text, more, { lines: DOCK_CLAMP_LINES });
  return row;
}

/** Put the right-hand controls into the column, or empty it.
 *
 *  A row still SENDING gets NONE: its id is derived rather than confirmed, so
 *  `_session/steer/clear` has nothing to address yet and a control would be one
 *  that cannot act. It gains them when `steer_queued` lands.
 *
 *  The COLUMN is there in both states, and that is what stops the confirmation
 *  moving anything: a control is floored to the hit-target size, so a column
 *  arriving with its buttons grew the row — 8px on a mouse, 28px on a phone — and
 *  the bar grows UPWARD, so the transcript moved by the same amount in the same
 *  frame the row was still fading in. `.steer-actions` reserves that height while
 *  the column is empty (26-dock.css), which is the reserved-box rule the turn
 *  footer's elapsed slot already follows. */
function fillActions(
  column: HTMLElement,
  steer: PendingSteer,
  sending: boolean,
  waiting: number,
): void {
  if (sending) {
    column.replaceChildren();
    return;
  }
  const controls: HTMLElement[] = [];
  // Send now LEADS, and the destructive control stays last. Only on a row the resend
  // would carry: nothing carries an agent's own notice, so offering it there would be
  // a button that cannot act.
  if (steer.origin === "user") {
    controls.push(
      actionButton(
        ICON_ARROW_UP,
        "Send this message now",
        // Names what it does AND what it costs: no wire verb injects mid-turn.
        waiting === 1
          ? "Stops the turn and sends this message as a new one"
          : "Stops the turn and sends this message first, then the others",
        () => {
          void sendSteerNow(steer.id);
        },
      ),
    );
  }
  // Edit is discard-plus-retype, so it is only offered when discarding cannot
  // take anything else with it.
  if (waiting === 1) {
    controls.push(
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
  controls.push(
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
  column.replaceChildren(...controls);
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
    // The row carries a tooltip and is not itself interactive, but stop here
    // anyway so a future row-level affordance cannot fire off a button.
    e.stopPropagation();
    onClick();
  });
  return btn;
}

/** Stop the running turn and send this message as a new one.
 *
 *  THE PRESSED ROW LEADS, then every other carried row in arrival order. Sending only
 *  the pressed one was rejected: the same cancel drains KAS's whole buffer, so the
 *  others would be lost — the very loss the boundary resend exists to remove, on a
 *  different button.
 *
 *  It records an ORDER and cancels; the boundary reads the text (`steer-resend.ts`). */
async function sendSteerNow(steerID: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  // Click-time read for the announcement and the no-op guard only, never the payload.
  const carried = pendingSteerCarry(chatID);
  if (!carried.some((e) => e.id === steerID)) {
    return;
  }
  preferSteerFirst(chatID, steerID);
  announce(
    carried.length === 1
      ? "Stopping the turn and sending this message"
      : `Stopping the turn and sending ${String(carried.length)} messages`,
  );
  const outcome = await cancelTurn.dispatch(chatID).outcome;
  if (outcome.status !== "success") {
    forgetSteerPreference(chatID);
  }
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
// reader who cannot see it is not subject to that constraint. The row's
// data-tooltip carries the same string for a mouse.
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
