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
// TWO STATES, and telling them apart is the stack's whole job. A steer that has
// reached KAS's buffer is SENT and unread — the agent has not seen it and is
// still doing whatever you are trying to redirect. Once `steer_injected` arrives
// it has been READ, and the redirection is in effect. Collapsing those into one
// look would hide the only fact a person steering a live turn is watching for.
//
// A read row also carries WHAT THE AGENT DID, when the agent said so. KAS asks
// it to close its response with `[STEERING steer-<id>: what I did about it]`;
// vibekit hides that marker from the transcript as machinery, and the sentence
// inside it lands here instead (`steer.ack`). It is strictly better information
// than a check glyph — "read" becomes "read: rebased onto main instead" — and it
// is the agent's own account rather than an inference, which is why the row
// shows it verbatim and adds no interpretation of its own.
//
// Neither state is a sent user message in the transcript: a steer becomes
// transcript when the turn's messages arrive, exactly like a prompt waiting on
// `message_appended` (vibekit.md #2/#6).
//
// WHAT THE CONTROLS CAN BE, measured against KAS 2.18.0's own source rather
// than assumed. There are exactly two steer verbs, `_session/steer` and
// `_session/steer/clear`, and `handleSessionSteerClear` reads ONLY `sessionId`:
// it drains the whole buffer through `clearSteeringAtTurnBoundary` and bumps the
// steering epoch. So there is no per-steer removal and no edit verb anywhere on
// the wire, and three rules follow:
//
//   - A READ row carries no controls at all. It cannot be unsent and it cannot
//     be changed; offering either would be a button that lies.
//   - Discard appears on the unread rows and always drops EVERY unread message.
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
import { announce } from "@cplieger/ui-primitives/announce";
import { $ } from "./dom.js";
import { activeSession, getActiveId } from "./store.js";
import { clearSteers } from "./actions/chat.js";
import { setComposerValue } from "./composer-value.js";
import { confirm } from "./confirm.js";
import { ICON_HOURGLASS, ICON_CHECK, ICON_EDIT, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import type { PendingSteer } from "./types.js";

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
  // Re-render only when the active chat, the steer texts, their READ state or
  // their acknowledgement changes. The computed returns a string so it dedups by
  // value — an unrelated session write (usage, thinking, a streaming chunk) must
  // not re-render the stack, and `injected` and `ack` each have to be in the key
  // or a steer being read, or answered, would repaint nothing.
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
    render(stack);
  });
}

function render(stack: HTMLUListElement): void {
  const s = activeSession.peek();
  const steers = s?.steers ?? [];
  const id = s?.id ?? "";
  const waiting = steers.filter((e) => !e.injected).length;

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

/** One full-width row. `waiting` is the stack-wide unread count, which is what
 *  decides whether Edit is offerable — see the header. */
function buildRow(steer: PendingSteer, waiting: number): HTMLElement {
  const read = steer.injected;
  // Only on a read row. An ack cannot arrive before the read frame, but the
  // states are independent on the wire and a "sent" row claiming an outcome
  // would be the worst thing this stack could say.
  const ack = read ? (steer.ack ?? "") : "";

  const state = el(
    "span",
    { className: "steer-state" },
    el(
      "span",
      { className: "steer-state-icon", "aria-hidden": "true" },
      iconEl(read ? ICON_CHECK : ICON_HOURGLASS),
    ),
    // The word, not only the glyph. "Sent" is the fact the user asked this stack
    // to state: the message has left, it is not a draft, and it is waiting.
    el("span", { className: "steer-state-label" }, read ? "Read" : "Sent"),
  );

  // No truncation here. The row is full width and the text clamps to two lines
  // in CSS, which fits several times what the old 60-character chip did, and the
  // clamp degrades to the whole text rather than to an ellipsis nothing can open.
  const body = el(
    "span",
    { className: "steer-body" },
    el("span", { className: "steer-text" }, oneLine(steer.text)),
  );
  if (ack !== "") {
    body.appendChild(el("span", { className: "steer-ack" }, oneLine(ack)));
  }

  const row = el(
    "li",
    {
      className: "steer-row",
      // Read by CSS for the state treatment, so the two states differ by more
      // than a glyph without a second class to keep in sync.
      "data-state": read ? "read" : "sent",
      // Both halves in full: the visible text is clamped by layout and the
      // agent's answer is the part worth reading whole.
      title: ack === "" ? steer.text : steer.text + "\n\n" + ack,
      // The state is carried by the glyph AND the label, and the ack is a third
      // visual channel, so all of it has to be in the accessible name too.
      //
      // The RAW strings, not the clamped ones: clamping is a layout answer to a
      // fixed-width row, and an accessible name has no width.
      "aria-label": accessibleName(steer.text, read, ack),
    },
    state,
    body,
  );

  const actions = buildActions(steer, read, waiting);
  if (actions !== null) {
    row.appendChild(actions);
  }
  return row;
}

/** The right-hand controls, or null for a row that honestly has none.
 *
 *  A read steer cannot be unsent or changed, so it gets nothing rather than a
 *  disabled control implying the operation exists. */
function buildActions(steer: PendingSteer, read: boolean, waiting: number): HTMLElement | null {
  if (read) {
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

// accessibleName spells out the row's state in words, because the glyph, the
// label's styling and the ack span are all visual.
//
// Nothing is shortened here. Both strings are collapsed to one line, because an
// accessible name is announced as a single string and stray newlines buy
// nothing, but the whole of each is present: the visible row clamps to fit, and
// a reader who cannot see it is not subject to that constraint. The title
// attribute carries the same two strings for a mouse.
function accessibleName(text: string, read: boolean, ack: string): string {
  const steerText = oneLine(text);
  if (!read) {
    return `Sent, waiting for the agent: ${steerText}`;
  }
  if (ack === "") {
    return `Read by the agent: ${steerText}`;
  }
  return `Read by the agent: ${steerText}. The agent did: ${oneLine(ack)}`;
}

// oneLine collapses whitespace without shortening. A steer is one message
// however the user typed it, and the row is a single clamped block.
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}
