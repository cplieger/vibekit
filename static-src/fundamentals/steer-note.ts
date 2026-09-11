// Fundamental: SteerNote — a message delivered into a turn already running,
// rendered inside that turn at the block boundary where the agent read it.
//
// A CARD on the tool-card box vocabulary, not a left rail: `#vibekit-ui` reserves
// a leading rail for work this agent did not do itself. The LABEL and the GLYPH
// are what separate the two origins — see LABELS below for why there are two.
//
// IT CARRIES NO CONTROL, in either state; the boundary resend replaced the
// dropped state's "Put it back in the message box".
//
// THE DROPPED LABEL SAYS WHAT HAPPENED NEXT, and the origins diverge because only
// a USER row is carried into a new turn (`vibekit-client.md`).

import { el } from "@cplieger/reactive";
import { attachClamp } from "../clamp-text.js";
import { iconEl } from "../icon-el.js";
import { ICON_SEND, ICON_TAB_RUN } from "../icons.js";
import type { SteerOrigin } from "../types.js";

export interface SteerNoteData {
  text: string;
  /** Whose words these are. Decides the label and the glyph. */
  origin: SteerOrigin;
  /** What the agent said it did about the steer. Read state only. */
  ack?: string;
  /** The agent never read it. */
  dropped: boolean;
}

/** The four labels, TOTAL over the origin so a third value cannot compile without
 *  wording of its own.
 *
 *  KAS's steering buffer is the only inbound channel into a live turn, so a
 *  workflow's report arrives on it beside the reader's own corrections — and with
 *  one label the report read as something they had typed. */
const LABELS: Record<SteerOrigin, { read: string; dropped: string }> = {
  user: { read: "Mid-turn message", dropped: "Not read — sent as a new turn" },
  agent: { read: "Workflow result", dropped: "Workflow result not delivered" },
};

/** The second channel, never a hue (WCAG 1.4.1 would forbid one as the only
 *  channel anyway): the composer's send arrow, or the run tab's own glyph. */
const GLYPHS: Record<SteerOrigin, string> = {
  user: ICON_SEND,
  agent: ICON_TAB_RUN,
};

/** Lines the body shows before the opener appears. Four rather than the turn
 *  header's three: this is the message itself, not a navigation row. */
const CLAMP_LINES = 4;

export function buildSteerNote(d: SteerNoteData): HTMLElement {
  const label = d.dropped ? LABELS[d.origin].dropped : LABELS[d.origin].read;
  const ack = d.dropped ? "" : oneLine(d.ack ?? "");

  const root = el("div", {
    className: "steer-note",
    "data-state": d.dropped ? "dropped" : "read",
    // Read by CSS, so no class has to be kept in step with the label.
    "data-origin": d.origin,
    // The full text: an accessible name has no width, and neither the glyph nor
    // the clamp carries the state on its own.
    "aria-label": accessibleName(label, oneLine(d.text), ack),
  });

  root.appendChild(
    el(
      "div",
      { className: "steer-note-head" },
      el("span", { className: "tool-icon", "aria-hidden": "true" }, iconEl(GLYPHS[d.origin])),
      el("span", { className: "steer-note-label" }, label),
    ),
  );

  const body = el("div", { className: "steer-note-body" });
  // NOT collapsed to one line: the clamp is an opener rather than a cut, so the
  // message keeps the shape it was typed in (`white-space: pre-wrap`).
  const text = el("div", { className: "steer-note-text" }, d.text);
  body.appendChild(text);
  const more = el("button", {
    className: "steer-note-more",
    type: "button",
  }) as HTMLButtonElement;
  // OUTSIDE the clamped element, or the clamp hides its own opener.
  body.appendChild(more);
  if (ack !== "") {
    body.appendChild(el("div", { className: "steer-note-ack" }, ack));
  }
  root.appendChild(body);

  attachClamp(text, more, { lines: CLAMP_LINES });
  return root;
}

function accessibleName(label: string, text: string, ack: string): string {
  if (ack === "") {
    return `${label}: ${text}`;
  }
  return `${label}: ${text}. The agent did: ${ack}`;
}

/** Collapse whitespace without shortening, for the single-string surfaces only.
 *  The VISIBLE text keeps its newlines — see buildSteerNote. */
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}
