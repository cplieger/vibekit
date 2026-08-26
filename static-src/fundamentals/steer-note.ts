// ---------------------------------------------------------------------------
// Fundamental: SteerNote — a mid-turn correction, rendered INSIDE the turn.
//
// A steer starts life as a row in the bottom dock, waiting. When the agent reads
// it the row leaves the dock and lands here, between the blocks the turn had
// already produced and the ones that arrive after — which is the only placement
// that is chronologically honest, since one turn is ONE assistant message and
// "where it was injected" can therefore only mean "between two of its blocks".
//
// It replaced a green checkmark on the dock row. The check said the agent had
// read the message and nothing else, so a reader watching the agent change
// course had a tick in the composer and no explanation in the transcript. This
// note is the explanation, in the place the change happened.
//
// WHY IT LOOKS THE WAY IT DOES. It reuses two vocabularies the reader already
// knows: the tint says USER INPUT (the same wash `.turn-header` uses for the
// request that started the turn) and the accent left edge says STEER (the dock
// row's own edge), so the note reads as the same object that just left the dock.
// The label is what makes it not merely a second prompt: a turn's header is the
// message that STARTED the work, and this one arrived in the middle of it. State
// is carried by the fill AND the label, never by colour alone (WCAG 1.4.1).
//
// TWO STATES:
//
//   read     the agent consumed it (`steer_injected`), optionally with the
//            agent's own account of what it did about it
//   dropped  a turn boundary cleared KAS's buffer before the agent got to it
//            (`steer_cleared`). The text is kept and one control is offered —
//            put it back in the message box — because "I sent this and it was
//            never read" is exactly the fact the transcript is for, and the
//            message is then one click from being re-sent as a prompt.
//
// `text-bubble.ts` is deliberately untouched: `buildUserBubble` was deleted from
// it on purpose, and this is a small annotated band rather than a markdown prose
// bubble.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

export interface SteerNoteData {
  text: string;
  /** What the agent said it did about the steer. Read state only. */
  ack?: string;
  /** The agent never read it. */
  dropped: boolean;
  /** Put the text back in the message box. Offered on the dropped state only —
   *  a message the agent already read cannot be unsent, so a control there would
   *  be a button that lies. Injected, so this stays a pure `fundamentals/` view
   *  with no reach into the composer. */
  onRestore?: () => void;
}

const LABEL_READ = "Steered mid-turn";
const LABEL_DROPPED = "Not delivered";

export function buildSteerNote(d: SteerNoteData): HTMLElement {
  const label = d.dropped ? LABEL_DROPPED : LABEL_READ;
  const body = oneLine(d.text);
  const ack = d.dropped ? "" : oneLine(d.ack ?? "");

  const root = el("div", {
    className: "steer-note",
    // Read by CSS for the two treatments, so they differ by more than a word
    // without a second class to keep in sync.
    "data-state": d.dropped ? "dropped" : "read",
    // The whole of both strings: the visible text clamps to fit the measure, and
    // an accessible name has no width. The label styling and the tint are
    // visual, so the state has to be in words here too.
    "aria-label": accessibleName(label, body, ack),
  });

  root.appendChild(el("span", { className: "steer-note-label" }, label));
  // No truncation in the DOM. The row clamps to two lines in CSS, which degrades
  // to the whole text rather than to an ellipsis nothing can open.
  root.appendChild(el("span", { className: "steer-note-text" }, body));
  if (ack !== "") {
    root.appendChild(el("span", { className: "steer-note-ack" }, ack));
  }
  if (d.dropped && d.onRestore !== undefined) {
    const restore = d.onRestore;
    const btn = el(
      "button",
      {
        className: "steer-note-restore",
        type: "button",
        "data-tooltip": "The agent never read it — send it again as a message",
      },
      "Put it back in the message box",
    );
    btn.addEventListener("click", (e: Event) => {
      // The note sits inside the turn card, whose header band is a fold toggle.
      // Restoring must not also fold the turn away.
      e.stopPropagation();
      restore();
    });
    root.appendChild(btn);
  }
  return root;
}

function accessibleName(label: string, text: string, ack: string): string {
  if (ack === "") {
    return `${label}: ${text}`;
  }
  return `${label}: ${text}. The agent did: ${ack}`;
}

/** Collapse whitespace without shortening. A steer is one message however the
 *  user typed it, and the note is a single clamped block. */
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}
