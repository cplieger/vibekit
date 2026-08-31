// ---------------------------------------------------------------------------
// Fundamental: TurnHeader — the turn card's tinted top band.
//
// The turn's TRIGGER, not always a user message. Two kinds, one component:
// the user prompted (the request text), or the system did (a typed trigger
// line). Everything else about the turn — number, outcome, timestamp,
// permalink — is identical across both, so the trigger is the only branch.
//
// The request text is CLAMPED TO THREE LINES with a show-more. That is
// load-bearing rather than cosmetic: once old turns fold to their header
// (§3.4), the folded rows become the session's navigation surface, and one
// pasted stack trace in a prompt would render hundreds of lines as a
// "collapsed" turn and push every neighbouring row off-screen. The clamp is on
// the TEXT only — the action row and any future attachment/reference chips sit
// outside it, because they are how a reader identifies which request this was.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { linkifyPaths } from "../linkify.js";
import { iconEl } from "../icon-el.js";
import { ICON_COPY } from "../icons.js";
import { buildAttachmentPill, type AttachmentRef } from "../attachment-pill.js";
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

/** Human label per outcome, used for the dot's accessible name (the dot itself
 *  carries colour only — colour is never the sole channel). */
const OUTCOME_LABEL: Record<TurnOutcome, string> = {
  running: "Running",
  completed: "Completed",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
  refused: "Refused",
  unknown: "Unknown",
  failed: "Failed",
};

/** Hover text for the dot. Says what the state MEANS rather than repeating the
 *  one-word label, because the complaint the tooltip answers was "I have no
 *  idea what this represents". */
const OUTCOME_TOOLTIP: Record<TurnOutcome, string> = {
  running: "This turn is still running",
  completed: "This turn finished normally",
  cancelled: "You stopped this turn",
  interrupted: "This turn was interrupted before it finished",
  refused: "The model declined to continue",
  unknown: "This turn's end could not be read",
  failed: "This turn failed",
};

/** Above this many characters, assume the text overflows three lines when the
 *  environment cannot measure (a detached or unstyled host, where `scrollHeight` is
 *  0 there). Deliberately generous: a false positive shows an unnecessary
 *  show-more, a false negative would make a long prompt unreadable. */
const CLAMP_FALLBACK_CHARS = 220;

/** Copy handler, injected. The assistant side's Copy already goes through the
 *  actions framework with a per-button confirmation flash; this is that same
 *  behaviour reached from a pure `fundamentals/` view, which must not import
 *  `actions/`. Default is a no-op so a header built in a test renders without
 *  wiring. */
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
      },
      chevronEl(),
    ),
  );
  row.appendChild(el("span", { className: "turn-n" }, `#${String(d.n)}`));
  row.appendChild(el("span", { className: "turn-dot", role: "img" }));
  row.appendChild(el("time", { className: "turn-ts" }));
  // Match count, filled while a search is active. A fold should ADVERTISE what
  // is inside it rather than hiding it, which is the whole bargain that makes
  // collapse acceptable.
  row.appendChild(el("span", { className: "turn-hit-count" }));
  // Rewind is NOT here: it lives in the footer, where "go back to after this
  // turn" reads correctly. What the meta row does carry is Copy — the sent
  // prompt was merely selectable before, while the reply beside it has had
  // Copy as text and Copy as markdown all along.
  //
  // In the META row, not inside `.turn-req`: the clamp is scoped to
  // `.turn-req-text`, so a control here is outside it by construction and
  // cannot be hidden by a long prompt folding to three lines. Reads the text
  // from the DOM at CLICK time rather than closing over it, so a repaint that
  // changes the request cannot leave a button copying the previous one.
  row.appendChild(buildCopyButton(header));
  header.appendChild(row);

  const req = el("div", { className: "turn-req" });
  req.appendChild(el("div", { className: "turn-req-text" }));
  const more = el("button", {
    className: "turn-req-more",
    type: "button",
  }) as HTMLButtonElement;
  more.hidden = true;
  more.addEventListener("click", () => {
    setExpanded(header, !isExpanded(header));
  });
  req.append(more);
  // Inside `.turn-req` but a SIBLING of `.turn-req-text`, so the three-line
  // clamp cannot hide it: the attachments are part of how a reader identifies
  // which request this was, which is the same reason the action row sits outside
  // the clamp. Empty and hidden until there is something to draw.
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
  // Hidden with the `hidden` PROPERTY rather than a class: an agent-initiated
  // turn has no prompt to copy, and a `display: none` button is still a button
  // in the accessibility tree.
  btn.hidden = true;
  btn.addEventListener("click", () => {
    const text = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-text");
    _copy(btn, text?.textContent ?? "");
  });
  return btn;
}

/** Recompute the header from turn data. Idempotent, and preserves a
 *  user-expanded clamp (a reader who opened a long prompt does not want the
 *  next repaint to fold it back). */
export function updateTurnHeader(header: HTMLElement, d: TurnHeaderData): void {
  header.dataset["outcome"] = d.outcome;

  const num = header.querySelector<HTMLElement>(":scope > .turn-head-row > .turn-n");
  if (num !== null) {
    num.textContent = `#${String(d.n)}`;
  }

  const dot = header.querySelector<HTMLElement>(":scope > .turn-head-row > .turn-dot");
  if (dot !== null) {
    dot.setAttribute("aria-label", OUTCOME_LABEL[d.outcome]);
    // A visible dot with no explanation is a puzzle, so it carries its meaning
    // on hover like every other status affordance in the app. The CSS hides it
    // outright on a completed turn — see 29-turns.css — because "it worked" is
    // the expected case and a marker on every row communicates nothing.
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
    // Nothing the user sent, nothing to copy.
    copy.hidden = d.request === undefined;
  }

  // Ahead of the request-text branch, which returns early for an
  // agent-initiated turn: the row has to be reachable on every path, or a
  // repaint could leave pills describing a request that is no longer here.
  syncAttachments(header, d.attachments);

  const text = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-text");
  const more = header.querySelector<HTMLButtonElement>(":scope > .turn-req > .turn-req-more");
  if (text === null || more === null) {
    return;
  }

  if (d.request === undefined) {
    // A turn the user did not ask for. Naming the trigger is the honest
    // rendering; fabricating a user message would put words in their mouth.
    header.dataset["trigger"] = "system";
    text.textContent = "Agent-initiated turn";
    text.removeAttribute("data-clamped");
    more.hidden = true;
    return;
  }

  header.dataset["trigger"] = "user";
  const body = d.request.trim();
  if (text.textContent !== body) {
    text.textContent = body;
    // A file path the user typed is the fastest route to the file they meant,
    // so the header linkifies exactly as the old user bubble did. Runs after
    // the textContent write, which is what wipes the previous pass's anchors.
    linkifyPaths(text);
    // New text: re-apply the clamp. An expansion belonged to the old content.
    setExpanded(header, false);
  }
  syncClampAffordance(text, more, body);
}

/** Draw the request's attachment pills, rebuilding only when the LIST changed.
 *
 *  The guard is correctness rather than economy: `updateTurnHeader` runs on every
 *  repaint, including each streaming chunk, and a blind `replaceChildren` would
 *  destroy a pill the user is tabbed onto several times a second. Compared
 *  against the DOM directly — each pill carries its path as its `title` — so
 *  there is no second copy of the list to keep in sync. */
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
  // No `onRemove`: a sent attachment cannot be un-sent, so the pill has no `×`.
  row.replaceChildren(...atts.map((att) => buildAttachmentPill(att)));
}

function isExpanded(header: HTMLElement): boolean {
  return header.dataset["expanded"] === "";
}

function setExpanded(header: HTMLElement, on: boolean): void {
  const text = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-text");
  const more = header.querySelector<HTMLButtonElement>(":scope > .turn-req > .turn-req-more");
  if (on) {
    header.dataset["expanded"] = "";
    text?.removeAttribute("data-clamped");
  } else {
    delete header.dataset["expanded"];
    text?.setAttribute("data-clamped", "");
  }
  if (more !== null) {
    more.textContent = on ? "Show less" : "Show more";
    more.setAttribute("aria-expanded", on ? "true" : "false");
  }
}

/** Decide whether the show-more is needed, and keep the clamp attribute in
 *  sync. Measurement is the truth when layout is available; the character
 *  fallback covers the no-layout case so a long prompt is never clamped with
 *  no way to open it. */
function syncClampAffordance(text: HTMLElement, more: HTMLButtonElement, body: string): void {
  const header = text.closest<HTMLElement>(".turn-header");
  if (header !== null && isExpanded(header)) {
    more.hidden = false;
    return;
  }
  text.setAttribute("data-clamped", "");
  more.textContent = "Show more";
  more.setAttribute("aria-expanded", "false");

  const measured = text.scrollHeight;
  const visible = text.clientHeight;
  const overflows =
    measured > 0 && visible > 0
      ? measured - visible > 1
      : body.length > CLAMP_FALLBACK_CHARS || countLines(body) > 3;
  more.hidden = !overflows;
}

function countLines(s: string): number {
  let n = 1;
  for (const ch of s) {
    if (ch === "\n") {
      n++;
    }
  }
  return n;
}
