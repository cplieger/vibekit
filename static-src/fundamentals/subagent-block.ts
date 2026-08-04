// ---------------------------------------------------------------------------
// Fundamental: the delegated-work card — a SUBAGENT's or a WORKFLOW STEP's box.
//
// A delegate's blocks (text / thinking / tool_use) carry its agent_subtask_id
// (a KAS subtask uuid, or `wf:<nodePath>` for a run step). The composition
// groups them and renders them into THIS card's `.body` using the SAME block
// dispatcher as the main transcript — real tool cards, diffs, reasoning — not
// a text preview. One card serves both delegate kinds, which is what lets a
// run render in the chat exactly as a subagent does.
//
// Four regions: header / tail / body / footer. The body is the disclosure; the
// other three live OUTSIDE it, which is the point —
//
//   - COLLAPSED BY DEFAULT, ALWAYS: while running and after settling. The old
//     policy (open while running, auto-close on settle) was exactly backwards:
//     it spent the expanded state on the moment when N delegates stream at
//     once — ten concurrent walls of text — and folded the box up right when
//     its result became worth reading. Not conditional on sibling count
//     either: a box that changed presentation because a sibling started would
//     rearrange the transcript for reasons the reader did not cause.
//   - THE TAIL answers the question collapsed-by-default creates: with ten in
//     flight, which are progressing and which are stuck? The last few output
//     lines, rolling — the CI-log convention, at a fraction of the height. It
//     lives outside the disclosure because a closed disclosure body is hidden
//     AND inert; shown only while running and collapsed (expanding shows the
//     real thing, so the tail would duplicate it), removed on settle.
//   - THE FOOTER is turn-footer.ts REUSED: a delegate has an outcome, a
//     duration, changed files and command/read counts — the exact facts the
//     turn footer already renders. Visible collapsed, because it IS the
//     result's summary and the collapsed card is the normal reading state.
//
// The header glyph carries the outcome by tint plus a composited check/cross
// (no status word — same vocabulary as tool cards). The glyph defaults to the
// shared agent hexagon; the caller swaps a per-known-subagent icon via setIcon.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import type { ToolStatus } from "../types.js";
import { isToolActive } from "../tool-schema.js";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_AGENT } from "../icons.js";
import {
  buildTurnFooter,
  updateTurnFooter,
  hasTurnSummary,
  type TurnSummaryData,
} from "./turn-footer.js";

/** How many trailing lines the tail shows. Three is enough to tell moving
 *  from stuck; more re-creates the wall of text the collapse exists to stop. */
const TAIL_LINES = 3;

/** A mounted delegated-work card plus its imperative handle. */
export interface SubagentView {
  /** The `.subagent-block` root to insert into the DOM. */
  readonly root: HTMLDivElement;
  /** The container the composition renders the delegate's child blocks into. */
  readonly body: HTMLElement;
  /** Update the header status glyph; on settle, drop the tail for good. */
  setStatus(status: ToolStatus): void;
  /** Update the delegate's display name. */
  setName(name: string): void;
  /** Swap the identity glyph (SVG string; roles.ts iconForSubagent). The
   *  spinner still owns the slot while the delegate is active. */
  setIcon(svg: string): void;
  /** Render the footer ledger (turn-footer, reused). No-op until the data has
   *  something worth a row — an empty footer is chrome claiming a result that
   *  is not there. */
  setSummary(d: TurnSummaryData): void;
}

/** Build a delegated-work card. Collapsed by default, always. */
export function buildSubagentBlock(name: string, status: ToolStatus): SubagentView {
  const root = el("div", { className: "subagent-block collapsed" }) as HTMLDivElement;

  const icon = el("span", { className: "subagent-icon" });
  const nameEl = el("span", { className: "subagent-name" }, name);
  // The chevron is purely decorative: the HEADER is the disclosure trigger
  // (it carries aria-expanded + activation), so a nested focusable button
  // would be a redundant tab stop announcing a second control.
  const chevron = el("button", {
    className: "subagent-toggle",
    type: "button",
    "aria-hidden": "true",
    tabindex: "-1",
  });
  const header = el(
    "div",
    { className: "subagent-header", role: "button", tabindex: "0" },
    icon,
    nameEl,
    chevron,
  ) as HTMLDivElement;

  // The tail: rolling activity while running, outside the disclosure so it
  // survives the collapsed state. aria-hidden — it is a visual progress cue
  // whose content re-renders several times a second; a screen reader gets the
  // header's status instead.
  const tail = el("div", { className: "subagent-tail", "aria-hidden": "true" });

  const body = el("div", { className: "subagent-body" });
  root.append(header, tail, body);

  const ctl = createDisclosure(header, body, {
    open: false,
    onToggle: (open) => {
      root.classList.toggle("collapsed", !open);
    },
  });
  void ctl;

  // The tail mirrors the body's trailing text. A MutationObserver rather than
  // a data feed threaded through the dispatcher: the body already receives
  // every rendered form of progress (bubbles, tool cards, reasoning), so its
  // text IS the activity, and observing it keeps this card a pure view with no
  // second pipeline to drift. rAF-coalesced — bursts of streaming mutations
  // repaint once per frame.
  let tailScheduled = false;
  const observer = new MutationObserver(() => {
    if (tailScheduled) {
      return;
    }
    tailScheduled = true;
    requestAnimationFrame(() => {
      tailScheduled = false;
      const lines = body.textContent
        .split("\n")
        .map((l) => l.trim())
        .filter((l) => l !== "");
      tail.replaceChildren(
        ...lines.slice(-TAIL_LINES).map((l) => el("div", { className: "subagent-tail-line" }, l)),
      );
    });
  });
  observer.observe(body, { childList: true, characterData: true, subtree: true });

  // The footer: attached lazily on the first summary worth showing, AFTER the
  // body so it reads as the card's last word whether or not the box is open.
  let footer: HTMLDivElement | null = null;
  let lastSummary: TurnSummaryData = {};

  let iconSvg = ICON_TAB_AGENT;
  let lastStatus = status;
  const applyIcon = (s: ToolStatus): void => {
    const failed = s === "failed";
    icon.classList.toggle("is-fail", failed);
    icon.classList.toggle("is-ok", !failed && !isToolActive(s));
    icon.classList.toggle("is-running", isToolActive(s));
    root.classList.toggle("running", isToolActive(s));
    if (isToolActive(s)) {
      icon.classList.add("subagent-spinner");
      icon.replaceChildren();
    } else {
      icon.classList.remove("subagent-spinner");
      icon.replaceChildren(
        iconEl(iconSvg),
        el(
          "span",
          { className: "tool-outcome-badge", "aria-hidden": "true" },
          failed ? "\u2717" : "\u2713",
        ),
      );
    }
    header.setAttribute(
      "aria-label",
      `${nameEl.textContent}, ${failed ? "failed" : isToolActive(s) ? "running" : "succeeded"}`,
    );
  };
  applyIcon(status);

  return {
    root,
    body,
    setStatus(s: ToolStatus): void {
      lastStatus = s;
      applyIcon(s);
      // Settled: the tail's job is done and the footer takes over. Removed
      // rather than hidden — the observer would otherwise keep repainting a
      // region nothing shows.
      if (!isToolActive(s)) {
        observer.disconnect();
        tail.remove();
      }
    },
    setName(n: string): void {
      nameEl.textContent = n;
    },
    setIcon(svg: string): void {
      if (svg === iconSvg) {
        return;
      }
      iconSvg = svg;
      applyIcon(lastStatus);
    },
    setSummary(d: TurnSummaryData): void {
      lastSummary = d;
      if (!hasTurnSummary(d)) {
        return;
      }
      if (footer === null) {
        footer = buildTurnFooter(d);
        footer.classList.add("subagent-footer");
        root.appendChild(footer);
      }
      updateTurnFooter(footer, lastSummary);
    },
  };
}
