// ---------------------------------------------------------------------------
// Fundamental: TurnHeader — the turn card's tinted top band.
//
// The turn's TRIGGER, not always a user message: either the user prompted
// (the request text) or the system did (a typed trigger line) — everything
// else (number, outcome, timestamp, permalink) is identical across both.
//
// Two rows: a meta row carrying the fold toggle, the turn number, the outcome
// dot, the timestamp, the search-hit count and Copy, then the request itself
// under it.
//
// An OPEN turn's request is not clamped: full text, however long. A FOLDED
// turn's clamps to four lines, in CSS, for the reason a clamp is here at all —
// folded rows are the session's navigation surface, and one pasted stack trace
// would render hundreds of lines as a "collapsed" turn and push every
// neighbouring row off screen. The clamp is on the TEXT only, so the
// attachment chips stay outside it: they are how a reader identifies the
// request.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { linkifyPaths } from "../linkify.js";
import { iconEl } from "../icon-el.js";
import { ICON_COPY } from "../icons.js";
import { buildAttachmentPill, type AttachmentRef } from "../attachment-pill.js";
// The dot's words, shared with the timeline rail's marker — see turn-severity.ts.
// Colour is never the sole channel (WCAG 1.4.1), and these are that channel.
import { OUTCOME_LABEL, OUTCOME_TOOLTIP, severityOf } from "../turn-severity.js";
import type { TurnOutcome } from "../turns.js";

export interface TurnHeaderData {
  /** 1-based turn ordinal as rendered. */
  n: number;
  outcome: TurnOutcome;
  /** Turn start, ms epoch. */
  ts: number;
  /** The user's request. Undefined for a turn the user did not ask for. */
  request: string | undefined;
  /** Files the user attached to this request. Drawn as the composer's own pill,
   *  read-only (a sent attachment cannot be un-sent), and OUTSIDE the clamp. */
  attachments: readonly AttachmentRef[];
}

/** Copy handler, injected — the assistant side's Copy already routes through
 *  the actions framework, and this reaches it from a pure `fundamentals/`
 *  view that must not import `actions/`. */
let _copy: (btn: HTMLButtonElement, text: string) => void = () => {
  /* not wired */
};

export function initTurnHeaderCallbacks(cbs: {
  copy: (btn: HTMLButtonElement, text: string) => void;
}): void {
  _copy = cbs.copy;
}

export function buildTurnHeader(d: TurnHeaderData): HTMLElement {
  const header = el("div", { className: "turn-header" });

  const row = el("div", { className: "turn-head-row" });
  // The fold toggle leads the row, so the affordance sits where the eye starts
  // and is in the same place whether the turn is open or folded.
  row.appendChild(
    el(
      "button",
      {
        className: "turn-fold-toggle",
        type: "button",
        "aria-label": "Expand or collapse this turn",
        // `.turn-header` is a plain div — `aria-expanded` there is an axe
        // `aria-allowed-attr` violation, so the state lives on this button.
        "aria-expanded": "true",
      },
      chevronEl(),
    ),
  );
  row.appendChild(el("span", { className: "turn-n" }, `#${String(d.n)}`));
  row.appendChild(el("span", { className: "turn-dot", role: "img" }));
  row.appendChild(el("time", { className: "turn-ts" }));
  // Filled while a search is active.
  row.appendChild(el("span", { className: "turn-hit-count" }));
  // Rewind lives in the footer instead. Reads text from the DOM at click
  // time, not closure-captured, so a repaint mid-flight can't copy stale text.
  row.appendChild(buildCopyButton(header));
  header.appendChild(row);

  const req = el("div", { className: "turn-req" });
  req.appendChild(el("div", { className: "turn-req-text" }));
  // Sibling of `.turn-req-text`, so the clamp cannot hide the attachments.
  req.appendChild(el("ul", { className: "turn-req-attachments attachment-row hidden" }));
  header.appendChild(req);

  updateTurnHeader(header, d);
  return header;
}

function buildCopyButton(header: HTMLElement): HTMLButtonElement {
  const btn = el(
    "button",
    {
      className: "turn-action-btn turn-copy-req",
      type: "button",
      "aria-label": "Copy this prompt",
      "data-tooltip": "Copy this prompt",
    },
    iconEl(ICON_COPY),
  ) as HTMLButtonElement;
  // `hidden` property, not a class: `display: none` is still in the a11y tree.
  btn.hidden = true;
  btn.addEventListener("click", () => {
    const text = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-text");
    _copy(btn, text?.textContent ?? "");
  });
  return btn;
}

/** Recompute the header from turn data. Idempotent. */
export function updateTurnHeader(header: HTMLElement, d: TurnHeaderData): void {
  header.dataset["outcome"] = d.outcome;
  // Hue comes off the shared severity table; `data-outcome` keeps the words and
  // the one stated exception. See turn-footer.ts's own write for the split.
  header.dataset["severity"] = severityOf(d.outcome);

  const num = header.querySelector<HTMLElement>(":scope > .turn-head-row > .turn-n");
  if (num !== null) {
    num.textContent = `#${String(d.n)}`;
  }

  const dot = header.querySelector<HTMLElement>(":scope > .turn-head-row > .turn-dot");
  if (dot !== null) {
    dot.setAttribute("aria-label", OUTCOME_LABEL[d.outcome]);
    dot.setAttribute("data-tooltip", OUTCOME_TOOLTIP[d.outcome]);
  }

  const time = header.querySelector<HTMLTimeElement>(":scope > .turn-head-row > .turn-ts");
  if (time !== null && d.ts > 0) {
    const when = new Date(d.ts);
    time.dateTime = when.toISOString();
    time.textContent = when.toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
    });
  }

  const copy = header.querySelector<HTMLButtonElement>(":scope > .turn-head-row > .turn-copy-req");
  if (copy !== null) {
    copy.hidden = d.request === undefined;
  }

  // Ahead of the early return below, so a repaint cannot leave a pill row
  // describing a request that is no longer here.
  syncAttachments(header, d.attachments);

  const text = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-text");
  if (text === null) {
    return;
  }

  if (d.request === undefined) {
    // No user message: naming the trigger is honest, fabricating one is not.
    header.dataset["trigger"] = "system";
    text.textContent = "Agent-initiated turn";
    return;
  }

  header.dataset["trigger"] = "user";
  const body = d.request.trim();
  if (text.textContent !== body) {
    text.textContent = body;
    linkifyPaths(text);
  }
}

/** Draw the request's attachment pills, rebuilding only when the list changed
 *  (a blind `replaceChildren` on every repaint would destroy a pill the user
 *  is tabbed onto). Compared against the DOM directly via each pill's
 *  `title`. */
function syncAttachments(header: HTMLElement, atts: readonly AttachmentRef[]): void {
  const row = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-attachments");
  if (row === null) {
    return;
  }
  row.classList.toggle("hidden", atts.length === 0);
  if (row.children.length === atts.length) {
    let same = true;
    for (const [i, att] of atts.entries()) {
      if (row.children[i]?.getAttribute("title") !== att.path) {
        same = false;
        break;
      }
    }
    if (same) {
      return;
    }
  }
  row.replaceChildren(...atts.map((att) => buildAttachmentPill(att)));
}
