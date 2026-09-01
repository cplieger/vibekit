// Fundamental: SteerNote — a mid-turn correction, rendered inside the turn at
// the block boundary where the agent read it (one turn is one assistant
// message, so that boundary is the only chronologically honest placement).
//
// Tint = user input (matches `.turn-header`), left edge = steer (matches the
// dock row); state is carried by fill + label, not colour alone (WCAG 1.4.1).
//
// Two states: `read` (steer_injected, optional agent account of what it did)
// and `dropped` (steer_cleared — a turn boundary cleared it before the agent
// saw it; offers "put it back in the message box" since it was never sent).

import { el } from "@cplieger/reactive";

export interface SteerNoteData {
  text: string;
  /** What the agent said it did about the steer. Read state only. */
  ack?: string;
  /** The agent never read it. */
  dropped: boolean;
  /** Put the text back in the message box. Dropped state only — a read message
   *  cannot be unsent. Injected to keep this a pure `fundamentals/` view. */
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
    "data-state": d.dropped ? "dropped" : "read",
    // The full text, since an accessible name has no width and the visual
    // clamp/tint carry no state on their own.
    "aria-label": accessibleName(label, body, ack),
  });

  root.appendChild(el("span", { className: "steer-note-label" }, label));
  // No truncation in the DOM; CSS clamps to two lines, degrading to the whole
  // text rather than an ellipsis nothing can open.
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
      // Must not also fold the turn card away (its header is a fold toggle).
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

/** Collapse whitespace without shortening — a steer is one message. */
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}
