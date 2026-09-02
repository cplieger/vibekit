// ---------------------------------------------------------------------------
// Fundamental: the delegated-work card — a SUBAGENT's or a WORKFLOW STEP's box.
//
// A delegate's blocks (text / thinking / tool_use) carry its agent_subtask_id;
// composition groups them and renders into `.body` via the same block
// dispatcher as the main transcript, so one card serves both delegate kinds.
//
// Four regions: header / tail / body / footer, and only the body is disclosed.
//
//   - COLLAPSED BY DEFAULT, ALWAYS (running and settled). Expanding on settle
//     is what a reader wants to read; expanding while running wastes the state
//     on the moment N delegates all stream at once.
//   - THE TAIL (last few lines, rolling) answers "which are progressing" while
//     collapsed+running. Lives outside the disclosure so it survives folding;
//     removed on settle.
//   - THE FOOTER reuses turn-footer.ts: a delegate has an outcome, duration,
//     changed files and command/read counts, same as a turn.
//
// TWO VARIANTS (see SubagentActivity): a LEAF spins its identity glyph — the
// card IS the work. A CONTAINER (a pipeline over its stages) keeps its glyph,
// shows activity dots while collapsed, and carries no tail — its stages
// already spin, and a tail would fold a whole stage into one glued line.
//
// This card is transcript-only. A delegate also has its own PAGE
// (`subagent-view.ts`) built on the shared `exec-view/` surface; the footer's
// `.subagent-open` link is the door between them.
//
// The header glyph carries outcome by tint plus a check/cross at its corner
// (same vocabulary as tool cards, no status word). `applyIcon` depends on the
// `tool-icon` class for both the badge's `position: relative` anchor and the
// `.tool-icon.is-*` tint selectors — a new outcome site should call
// `applyOutcome` (tool-card.ts) rather than copy this block.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import type { ToolStatus } from "../types.js";
import { isToolActive } from "../tool-schema.js";
import { iconEl } from "../icon-el.js";
import { chevronEl } from "../chevron.js";
import { ICON_TAB_AGENT, ICON_EXTERNAL } from "../icons.js";
import { CHUNK_ENTER_ATTR } from "../smd-renderer.js";
import {
  buildTurnFooter,
  updateTurnFooter,
  hasTurnSummary,
  type TurnSummaryData,
} from "./turn-footer.js";

/** How many trailing lines the tail shows. */
const TAIL_LINES = 3;

/** One block's text as lines. Element boundaries become spaces; runs of
 *  whitespace collapse; any newlines the block's own text carries split it
 *  further.
 *
 *  A PER-CHUNK WRAPPER IS NOT A BOUNDARY. `smd-renderer.ts` wraps every streamed
 *  text emission in a `<span data-vk-chunk-enter>` so the per-chunk fade can
 *  animate it, so one streaming sentence is dozens of sibling spans — and the
 *  space this walk puts at every element boundary then lands INSIDE words, at
 *  positions that move with each frame's chunk size. That is the reported defect:
 *  a delegate writing `I am creating a workflow` showed
 *  `I am crea ting a workflow` in its tail and read correctly again after a tab
 *  switch, because the replay path renders with `animateText: false` and produces
 *  one text node per block. Skipping the marked span keeps the boundary rule for
 *  everything that IS a boundary — two blocks whose `textContent` carries no
 *  separator between them, which is why the space exists at all. */
function blockLines(node: Node): string[] {
  const parts: string[] = [];
  const walk = (n: Node): void => {
    if (n.nodeType === Node.TEXT_NODE) {
      parts.push(n.nodeValue ?? "");
      return;
    }
    if (n.nodeType === Node.ELEMENT_NODE && (n as Element).hasAttribute(CHUNK_ENTER_ATTR)) {
      n.childNodes.forEach(walk);
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
 *  A line is a BLOCK, not a text node: `body.textContent.split("\n")` cannot
 *  answer this, because the body holds rendered blocks whose `textContent`
 *  carries no separators between them — the old whole-body read collapsed to
 *  one glued line and clipped to the beginning instead of the tail.
 *
 *  Walks backwards from the last block and stops once it has enough, so cost
 *  is the tail rather than the whole transcript — this runs on every
 *  animation frame while the delegate streams. */
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
 *  `spinner`: the leaf, the card IS the work, so its identity glyph becomes a
 *  ring. `container`: a card whose body holds other delegate cards that each
 *  carry their own ring — a pipeline over its stages. It keeps its glyph and
 *  shows activity dots instead, only while collapsed: a second ring beside
 *  the children's is a duplicate signal at a different rate (0.6s vs the tool
 *  card's 0.8s), and expanded the children's rings are already on screen. */
export type SubagentActivity = "spinner" | "container";

/** The way to this delegate's own page, injected because a `fundamentals/`
 *  view must not import the feature module that owns tabs. `href` makes it a
 *  real anchor (middle-click, copy-link); `open` routes an ordinary click
 *  through the app instead of reloading. */
export interface SubagentOpener {
  href: string;
  open: () => void;
}

export interface SubagentOptions {
  /** Default `spinner`. See SubagentActivity. */
  activity?: SubagentActivity;
  /** The footer's link to this delegate's page. Absent = no link — the right
   *  answer for a pipeline CONTAINER, since opening a stage's page already
   *  shows the whole pipeline as a tree. */
  open?: SubagentOpener;
  /** Fired when the disclosure flips; composition keys its open-container
   *  bookkeeping on ids this view never learns. */
  onOpenChange?: (open: boolean) => void;
}

/** The footer's link to this delegate's own page. A real anchor with a click
 *  handler over it, mirroring `run-card.ts`'s `.run-open`.
 *
 *  NOT in the header: it is `role="button"` (carries the disclosure's
 *  activation + `aria-expanded`), so an `<a href>` inside it is axe's
 *  `nested-interactive` — `aria-hidden` + `tabindex="-1"` does not clear it,
 *  since such an element is still focusable by click and script. */
function buildOpenLink(opener: SubagentOpener): HTMLAnchorElement {
  const link = el(
    "a",
    { className: "subagent-open", href: opener.href },
    "Open",
    el("span", { className: "subagent-open-icon", "aria-hidden": "true" }, iconEl(ICON_EXTERNAL)),
  ) as HTMLAnchorElement;
  link.addEventListener("click", (e) => {
    // A modified click (new tab/window) is a deliberate escape from routing.
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

  // `tool-icon` carries `position: relative` for the outcome badge's corner
  // anchor, and the `.tool-icon.is-*` tint selectors match against it —
  // without it the badge paints ~190px off (against `.subagent-block`'s
  // transform-derived containing block) and the tint classes select nothing.
  const icon = el("span", { className: "subagent-icon tool-icon" });
  const nameEl = el("span", { className: "subagent-name" }, name);
  // A span, not a button: the header is `role="button"` and carries the
  // disclosure's activation, so a `<button>` chevron inside it is axe's
  // `nested-interactive` (aria-hidden + tabindex="-1" does not clear it).
  const chevron = el("span", { className: "subagent-toggle", "aria-hidden": "true" }, chevronEl());
  // Container's busy indicator: three dots shown only while collapsed+running
  // (14-tools.css, same gate as the tail).
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

  // Rolling activity tail while running, outside the disclosure so it
  // survives the collapsed state. A CONTAINER gets none: its children's DOM
  // carries no newlines for `blockLines` to split on, so a stage would fold
  // into one glued line — the dots already say busy.
  const tail = isContainer
    ? null
    : el("div", { className: "subagent-tail", "aria-hidden": "true" });

  const body = el("div", { className: "subagent-body" });

  // --- foot ------------------------------------------------------------------
  // Ledger + this delegate's page link (right-aligned), created eagerly since
  // `setSummary` withholds the footer until there is something to show.
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
  // A failed delegate opens on its own: the header can only say THAT it
  // failed, and the reason is the reader's next question. A user toggle
  // outranks it.
  let userToggled = false;
  const markToggled = (e: Event): void => {
    if (e instanceof KeyboardEvent && e.key !== "Enter" && e.key !== " ") {
      return;
    }
    userToggled = true;
  };
  header.addEventListener("click", markToggled);
  header.addEventListener("keydown", markToggled);
  if (status === "failed") {
    ctl.open();
  }

  // Mirrors the body's trailing text via MutationObserver rather than a
  // second data feed — the body already receives every progress form.
  // rAF-coalesced so a burst of streaming mutations repaints once per frame.
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

  // Footer attached lazily on the first summary worth showing, after the
  // body so it reads as the card's last word regardless of open state.
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
    // A CONTAINER keeps its identity glyph for the whole run — its stages
    // carry the rings — and withholds the outcome badge while active.
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
      if (s === "failed" && !userToggled) {
        ctl.open();
      }
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
        // Prepended so the ledger leads and the open link stays right-aligned.
        foot.prepend(footer);
      }
      updateTurnFooter(footer, lastSummary);
    },
  };
}
