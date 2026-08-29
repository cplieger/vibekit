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
// TWO VARIANTS, and the difference is only how the card says "work is happening"
// (see SubagentActivity). A LEAF spins its identity glyph, because the card IS
// the work. A CONTAINER holds other delegate cards — a subagent-orchestration
// pipeline over its stages — and it keeps its glyph, shows activity DOTS while
// collapsed, and carries no tail. Its stages already spin; a ring here would be
// a second moving thing for one piece of work, at a different rate (0.6s against
// a tool card's 0.8s, so they beat against each other), and its tail would fold
// a whole stage card into one line of glued words.
//
// THIS CARD IS THE TRANSCRIPT'S AND ONLY THE TRANSCRIPT'S. A delegate also has a
// PAGE (`subagent-view.ts`), and that page does NOT render a variant of this card:
// it is the shared `exec-view/` surface every delegated execution uses, because a
// page wants a tree, a timeline and a detail pane that a card in a conversation has
// no room for. A `detail: "full"` flag was built here first and deleted — the run
// card's identical flag was retired for the same reason, that one component meaning
// two things was the wrong seam. What connects the two surfaces is the footer's
// `.subagent-open` link, which lives in the foot rather than the header because the
// header is `role="button"` (see buildOpenLink).
//
// The header glyph carries the outcome by tint plus a check/cross mark at its
// corner (no status word — same vocabulary as tool cards). The glyph defaults to
// the shared agent hexagon; the caller swaps a per-known-subagent icon via setIcon.
// While the delegate is active the tint stays accent, so the glyph reads as
// identity until there is an outcome to report.
//
// BOTH CHANNELS DEPEND ON THE `tool-icon` CLASS ON THAT SPAN, and it was absent
// until 2026-08-26. applyIcon builds its own `.tool-outcome-badge` rather than
// routing through tool-card.ts applyOutcome, and the copy dropped the contract
// that badge needs: `position: relative` for the corner (without it the mark
// resolved against `.subagent-block`, itself a containing block only because the
// card's entry animation ends on a transform, and painted about 190px away at the
// header's far edge) and the `.tool-icon.is-*` selectors for the tint (without
// it the three classes below matched no rule, so every settled delegate stayed
// accent). Measured both. A new outcome site should call applyOutcome instead of
// copying this block, because copying is what lost the contract.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import type { ToolStatus } from "../types.js";
import { isToolActive } from "../tool-schema.js";
import { iconEl } from "../icon-el.js";
import { chevronEl } from "../chevron.js";
import { ICON_TAB_AGENT, ICON_EXTERNAL } from "../icons.js";
import {
  buildTurnFooter,
  updateTurnFooter,
  hasTurnSummary,
  type TurnSummaryData,
} from "./turn-footer.js";

/** How many trailing lines the tail shows. Three is enough to tell moving
 *  from stuck; more re-creates the wall of text the collapse exists to stop. */
const TAIL_LINES = 3;

/** One block's text as lines. Element boundaries become spaces, runs of
 *  whitespace collapse, and any newlines the block's own text carries (a `<pre>`
 *  of command output) split it further. */
function blockLines(node: Node): string[] {
  const parts: string[] = [];
  const walk = (n: Node): void => {
    if (n.nodeType === Node.TEXT_NODE) {
      parts.push(n.nodeValue ?? "");
      return;
    }
    parts.push(" ");
    n.childNodes.forEach(walk);
    parts.push(" ");
  };
  walk(node);
  return parts
    .join("")
    .split("\n")
    .map((l) => l.replace(/\s+/gu, " ").trim())
    .filter((l) => l !== "");
}

/** The delegate's trailing progress lines, oldest first.
 *
 *  `body.textContent.split("\n")` cannot answer this, and that WAS the bug this
 *  function replaces. `textContent` concatenates a node tree with no separators,
 *  and the body holds rendered BLOCKS — bubbles, tool cards, reasoning — whose
 *  text carries no newline characters at all. So a real body collapsed to ONE
 *  line, `✓Grep Searchspaghetti✓File Search/workspace/The workspace is…`, which
 *  `.subagent-tail-line`'s nowrap + ellipsis then clipped at the card's width:
 *  the reader got the BEGINNING of the whole run, words glued together, instead
 *  of its last three lines. The unit test passed because it appended a raw text
 *  node holding literal `\n`s — a shape the block dispatcher never produces.
 *
 *  A line is a BLOCK, because that is where the reader's line breaks are.
 *
 *  Walks backwards from the last block and stops as soon as it has enough, so
 *  the cost is the tail rather than the whole transcript. That matters: this
 *  runs once per animation frame for as long as the delegate streams, and the
 *  old full-body read grew with everything the delegate had ever emitted.
 *
 *  Iterates `childNodes`, not `children`: a bare text node appended straight to
 *  the body is a block too, and skipping it would put the blind spot back one
 *  level down. */
function tailLines(body: HTMLElement, want: number): string[] {
  const out: string[] = [];
  const kids = body.childNodes;
  for (let i = kids.length - 1; i >= 0 && out.length < want; i--) {
    const child = kids[i];
    if (child === undefined) {
      continue;
    }
    const lines = blockLines(child);
    out.unshift(...lines.slice(Math.max(0, lines.length - (want - out.length))));
  }
  return out;
}

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

/** How a card reports that work is happening.
 *
 *  `spinner` is the leaf: this card IS the work, so its identity glyph becomes a
 *  ring for the duration. `container` is a card whose body holds other delegate
 *  cards that each carry their own ring — a pipeline over its stages. It keeps
 *  its glyph and shows ACTIVITY DOTS instead, and only while collapsed, for two
 *  reasons that both come out of one screen: a second ring beside its children's
 *  is a fourth moving thing claiming to be a fifth piece of work, and the two
 *  ran at different rates (the tool card's 0.8s against this card's 0.6s), so
 *  they visibly beat against each other. Expanded, the children's rings are on
 *  screen and the container needs to say nothing.
 *
 *  Same rule the run card already follows: its head carries a status word and a
 *  step counter, and the spinner lives on the step glyph. */
export type SubagentActivity = "spinner" | "container";

/** The way to this delegate's own page, injected because a `fundamentals/` view
 *  must not import the feature module that owns tabs.
 *
 *  Both halves are needed and neither substitutes for the other. `href` is what
 *  makes it a real anchor — middle-click, copy-link and open-in-a-browser-tab all
 *  work — and `open` is what an ordinary click calls instead, so the app routes
 *  itself rather than reloading. */
export interface SubagentOpener {
  href: string;
  open: () => void;
}

export interface SubagentOptions {
  /** Default `spinner`. See SubagentActivity. */
  activity?: SubagentActivity;
  /** The footer's link to this delegate's page. Absent means no link, which is the
   *  right answer for a pipeline CONTAINER: opening a stage's page shows the whole
   *  pipeline as a tree, so the driver needs no second door of its own. */
  open?: SubagentOpener;
  /** Fired when the disclosure flips. The composition layer keys its
   *  open-container bookkeeping on ids this view never learns, so the state
   *  change is reported rather than read back off the DOM. */
  onOpenChange?: (open: boolean) => void;
}

/** The footer's link to this delegate's own page.
 *
 *  A real anchor, so middle-click and copy-link do what the browser would, with a
 *  click handler over it so an ordinary click routes the app instead of reloading
 *  it. Copied from `run-card.ts`'s `.run-open` deliberately: the two are the same
 *  affordance on the same kind of card, and a reader learns one control.
 *
 *  IT IS NOT IN THE HEADER, and that is the one place it cannot go. The header is
 *  `role="button"` (it carries the disclosure's activation and `aria-expanded`), so
 *  an `<a href>` or a `<button>` inside it is axe's `nested-interactive` (serious)
 *  — and `aria-hidden` plus `tabindex="-1"` does NOT clear it, because a
 *  `tabindex="-1"` element is still focusable by click and by script. That is the
 *  same finding the chevron beside it records, and it is why the run card put its
 *  own link in the foot. Measured on the run card before it was fixed: 18 offending
 *  nodes on a four-card page. */
function buildOpenLink(opener: SubagentOpener): HTMLAnchorElement {
  const link = el(
    "a",
    { className: "subagent-open", href: opener.href },
    "Open",
    el("span", { className: "subagent-open-icon", "aria-hidden": "true" }, iconEl(ICON_EXTERNAL)),
  ) as HTMLAnchorElement;
  link.addEventListener("click", (e) => {
    // Let a modified click do what the browser would: a new tab or window is a
    // deliberate escape from the app's own routing.
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (e as MouseEvent).button !== 0) {
      return;
    }
    e.preventDefault();
    opener.open();
  });
  return link;
}

/** Build a delegated-work card. Collapsed by default, always. */
export function buildSubagentBlock(
  name: string,
  status: ToolStatus,
  opts: SubagentOptions = {},
): SubagentView {
  const isContainer = opts.activity === "container";
  const root = el("div", { className: "subagent-block collapsed" }) as HTMLDivElement;
  if (isContainer) {
    root.classList.add("subagent-container");
  }

  // `tool-icon` is not decoration here, it is the outcome contract this card
  // needs and had been missing: it carries the `position: relative` that keeps the
  // `.tool-outcome-badge` applyIcon appends at the glyph's corner, and it is what
  // the `.tool-icon.is-ok` / `.is-fail` tint rules select on. Without it the badge
  // resolved against `.subagent-block` and painted ~190px away at the header's
  // edge, and the three state classes below matched no rule at all, so a delegate
  // reported its outcome through neither channel. See css/14-tools.css.
  const icon = el("span", { className: "subagent-icon tool-icon" });
  const nameEl = el("span", { className: "subagent-name" }, name);
  // The chevron is purely decorative: the HEADER is the disclosure trigger (it
  // carries aria-expanded + activation), so this glyph has never had a handler.
  //
  // A SPAN, not a button, and the change is a real fix rather than tidiness. The
  // header is `role="button"`, so a `<button>` inside it is axe's
  // `nested-interactive` (serious) — and `aria-hidden` plus `tabindex="-1"` does
  // NOT clear it, because a `tabindex="-1"` element is still focusable by click
  // and by script. `tabs.ts`'s `createTabEl` documents the same finding for the
  // tab row's close affordance; this card had the same shape and the run card
  // copied it before an axe pass over the run card caught all three.
  const chevron = el("span", { className: "subagent-toggle", "aria-hidden": "true" }, chevronEl());
  // The container variant's busy indicator: three dots rather than a ring, and
  // CSS shows it only while this card is collapsed and running (14-tools.css,
  // the same gate the tail uses). aria-hidden for the same reason the tail is —
  // it is a visual cue, and the header's own aria-label carries the state.
  const busy = isContainer
    ? el(
        "span",
        { className: "subagent-busy activity-dots", "aria-hidden": "true" },
        el("span", { className: "activity-dot" }),
        el("span", { className: "activity-dot" }),
        el("span", { className: "activity-dot" }),
      )
    : null;
  const header = el(
    "div",
    { className: "subagent-header", role: "button", tabindex: "0" },
    icon,
    nameEl,
    ...(busy === null ? [] : [busy]),
    chevron,
  ) as HTMLDivElement;

  // The tail: rolling activity while running, outside the disclosure so it
  // survives the collapsed state. aria-hidden — it is a visual progress cue
  // whose content re-renders several times a second; a screen reader gets the
  // header's status instead.
  //
  // A CONTAINER gets none, and that is not a saving but a correctness call. Its
  // body holds delegate CARDS, and `blockLines` splits a child on the newlines
  // its text carries; a card's DOM carries none, so the whole stage — header
  // name, glyph, its collapsed body — folds into ONE line of glued words, which
  // is exactly the defect `tailLines`' own comment records for the old
  // whole-body read. The dots say busy; opening the card shows the stage rows,
  // each with a real tail of its own.
  const tail = isContainer
    ? null
    : el("div", { className: "subagent-tail", "aria-hidden": "true" });

  const body = el("div", { className: "subagent-body" });

  // --- foot ------------------------------------------------------------------
  // One row holding the ledger and the way to this delegate's page, in the same
  // composition `.run-foot` uses: ledger first, the link right-aligned. Created
  // EAGERLY, because the ledger is not: `setSummary` withholds a footer until
  // there is something worth a row, and a link that appeared halfway through a
  // run would be an affordance the reader had already looked for and not found.
  // Empty is invisible (`.subagent-foot:empty`), so a card with neither costs
  // nothing.
  const openLink = opts.open === undefined ? null : buildOpenLink(opts.open);
  const foot = el(
    "div",
    { className: "subagent-foot" },
    ...(openLink === null ? [] : [openLink]),
  ) as HTMLDivElement;

  root.append(header, ...(tail === null ? [] : [tail]), body, foot);

  const ctl = createDisclosure(header, body, {
    open: false,
    onToggle: (open) => {
      root.classList.toggle("collapsed", !open);
      opts.onOpenChange?.(open);
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
  const observer =
    tail === null
      ? null
      : new MutationObserver(() => {
          if (tailScheduled) {
            return;
          }
          tailScheduled = true;
          requestAnimationFrame(() => {
            tailScheduled = false;
            tail.replaceChildren(
              ...tailLines(body, TAIL_LINES).map((l) =>
                el("div", { className: "subagent-tail-line" }, l),
              ),
            );
          });
        });
  observer?.observe(body, { childList: true, characterData: true, subtree: true });

  // The footer: attached lazily on the first summary worth showing, AFTER the
  // body so it reads as the card's last word whether or not the box is open.
  let footer: HTMLDivElement | null = null;
  let lastSummary: TurnSummaryData = {};

  let iconSvg = ICON_TAB_AGENT;
  let lastStatus = status;
  const applyIcon = (s: ToolStatus): void => {
    const failed = s === "failed";
    const active = isToolActive(s);
    icon.classList.toggle("is-fail", failed);
    icon.classList.toggle("is-ok", !failed && !active);
    icon.classList.toggle("is-running", active);
    root.classList.toggle("running", active);
    // A CONTAINER never gives its glyph up to a spinner: its stages carry the
    // rings, and this slot stays identity for the whole run. It still withholds
    // the outcome badge while active, because there is no outcome yet.
    if (active && !isContainer) {
      icon.classList.add("subagent-spinner");
      icon.replaceChildren();
    } else if (active) {
      icon.classList.remove("subagent-spinner");
      icon.replaceChildren(iconEl(iconSvg));
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
      `${nameEl.textContent}, ${failed ? "failed" : active ? "running" : "succeeded"}`,
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
        observer?.disconnect();
        tail?.remove();
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
        // PREPENDED into the foot row, so the ledger leads and the open link
        // stays right-aligned beside it. The row is the card's last word either
        // way; what this buys over a second bordered strip is that a reader gets
        // one seam rather than two.
        foot.prepend(footer);
      }
      updateTurnFooter(footer, lastSummary);
    },
  };
}
