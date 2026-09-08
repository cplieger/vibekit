// The delegated-work boxes. `buildSubagentCard` is a LEAF and renders NONE of its
// delegate's output — that lives on the delegate's own page, reached through the
// foot's link — so the card has no disclosure at all. `buildSubagentContainer` is a
// pipeline over its stages, and its body holds THEIR cards.
//
// Two consequences of the leaf having no body: the tail is PUSHED IN through
// `setTail` (`subagent-tail.ts` derives it from the store), and the state word is an
// `.sr-only` span, because a screen reader ignores `aria-label` on a plain div.

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import type { ToolStatus } from "../types.js";
import { isToolActive } from "../tool-schema.js";
import { iconEl } from "../icon-el.js";
import { chevronEl } from "../chevron.js";
import { ICON_TAB_AGENT, ICON_EXTERNAL, outcomeIcon } from "../icons.js";
import { CHROME_ATTR } from "../chrome-attr.js";
import {
  buildTurnFooter,
  updateTurnFooter,
  hasTurnSummary,
  type TurnSummaryData,
} from "./turn-footer.js";

/** What both delegated-work boxes answer to. */
export interface SubagentBox {
  /** The `.subagent-block` root to insert into the DOM. */
  readonly root: HTMLDivElement;
  /** Update the header status glyph and the announced state word. */
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

/** One delegate's card. No body: its output is on its own page. */
export interface SubagentCard extends SubagentBox {
  /** Replace the rolling tail, oldest line first, capped by its producer
   *  (`subagent-tail.ts`). A no-op once the delegate has settled and the tail is
   *  gone. */
  setTail(lines: readonly string[]): void;
}

/** A PIPELINE's container. Its `body` hosts its stages' own cards. */
export interface SubagentContainer extends SubagentBox {
  /** The container the composition renders this pipeline's stage cards into. */
  readonly body: HTMLElement;
}

/** The way to this delegate's own page, injected because a `fundamentals/`
 *  view must not import the feature module that owns tabs. `href` makes it a
 *  real anchor (middle-click, copy-link); `open` routes an ordinary click
 *  through the app instead of reloading. */
export interface SubagentOpener {
  href: string;
  open: () => void;
}

export interface SubagentCardOptions {
  /** The foot's link to this delegate's page. Absent = no link, which is what a
   *  delegate with no chat to open it in gets. */
  open?: SubagentOpener;
}

export interface SubagentContainerOptions {
  /** Fired when the disclosure flips; composition keys its open-container
   *  bookkeeping on ids this view never learns. */
  onOpenChange?: (open: boolean) => void;
  /** Where the disclosure starts. Default false — a box the READER opened is the
   *  only reason to pass true, and only composition knows that. */
  startOpen?: boolean;
}

/** The foot's link to this delegate's own page. A real anchor with a click
 *  handler over it, mirroring `run-card.ts`'s `.run-open`. */
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

/** The identity row, the foot and the four setters that write them; each builder
 *  adds its own middle region between them. */
interface Shell {
  root: HTMLDivElement;
  header: HTMLDivElement;
  foot: HTMLDivElement;
  box: SubagentBox;
}

function buildShell(
  name: string,
  status: ToolStatus,
  isContainer: boolean,
  opener?: SubagentOpener,
): Shell {
  const root = el("div", { className: "subagent-block" }) as HTMLDivElement;
  // `tool-icon` is what the `.tool-icon.is-*` tint selectors match against:
  // without it a settled delegate keeps the running accent forever.
  const icon = el("span", { className: "subagent-icon tool-icon" });
  const nameEl = el("span", { className: "subagent-name" }, name);
  const stateEl = el("span", { className: "sr-only" });
  const header = el(
    "div",
    { className: "subagent-header", [CHROME_ATTR]: "" },
    icon,
    nameEl,
    stateEl,
  ) as HTMLDivElement;

  // Ledger + this delegate's page link (right-aligned), created eagerly since
  // `setSummary` withholds the footer until there is something to show.
  const openLink = opener === undefined ? null : buildOpenLink(opener);
  const foot = el(
    "div",
    { className: "subagent-foot", [CHROME_ATTR]: "" },
    ...(openLink === null ? [] : [openLink]),
  ) as HTMLDivElement;
  root.append(header, foot);

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
    // A CARD empties the slot while active so CSS can spin it as a ring; a CONTAINER
    // keeps its identity glyph, because its stages carry the rings.
    const ring = active && !isContainer;
    icon.classList.toggle("subagent-spinner", ring);
    icon.replaceChildren(
      ...(ring ? [] : [iconEl(failed && !active ? outcomeIcon("fail") : iconSvg)]),
    );
    stateEl.textContent = failed ? "failed" : active ? "running" : "succeeded";
  };

  const refreshLinkName = (): void => {
    openLink?.setAttribute("aria-label", `Open ${nameEl.textContent}`);
  };
  applyIcon(status);
  refreshLinkName();

  return {
    root,
    header,
    foot,
    box: {
      root,
      setStatus(s: ToolStatus): void {
        lastStatus = s;
        applyIcon(s);
      },
      setName(n: string): void {
        nameEl.textContent = n;
        refreshLinkName();
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
    },
  };
}

/** Build one delegate's card: identity row, rolling tail, foot. */
export function buildSubagentCard(
  name: string,
  status: ToolStatus,
  opts: SubagentCardOptions = {},
): SubagentCard {
  const shell = buildShell(name, status, false, opts.open);
  // Between the header and the foot, and outside nothing: this card has no
  // disclosure for the tail to sit outside of.
  const tail = el("div", { className: "subagent-tail", "aria-hidden": "true", [CHROME_ATTR]: "" });
  shell.root.insertBefore(tail, shell.foot);
  let live = isToolActive(status);
  if (!live) {
    tail.remove();
  }
  return {
    ...shell.box,
    setStatus(s: ToolStatus): void {
      shell.box.setStatus(s);
      // Settled: the tail's job is done and the footer takes over. Removed rather
      // than hidden, and `live` is what stops a late tail write putting it back.
      if (live && !isToolActive(s)) {
        live = false;
        tail.remove();
      }
    },
    setTail(lines: readonly string[]): void {
      if (!live) {
        return;
      }
      tail.replaceChildren(...lines.map((l) => el("div", { className: "subagent-tail-line" }, l)));
    },
  };
}

/** Build a pipeline's container: identity row with activity dots, a disclosed
 *  body for its stages, foot. Collapsed unless `startOpen` says otherwise. */
export function buildSubagentContainer(
  name: string,
  status: ToolStatus,
  opts: SubagentContainerOptions = {},
): SubagentContainer {
  const shell = buildShell(name, status, true);
  const startOpen = opts.startOpen ?? false;
  // Built in its FULL form; `syncDisclosure` below takes the control away when there
  // is nothing to reveal.
  shell.root.classList.add("subagent-container", "has-disclosure");
  shell.root.classList.toggle("collapsed", !startOpen);
  // A span, not a button: the header is `role="button"` and carries the
  // disclosure's activation, so a `<button>` chevron inside it is axe's
  // `nested-interactive` (aria-hidden + tabindex="-1" does not clear it).
  const chevron = el("span", { className: "subagent-toggle", "aria-hidden": "true" }, chevronEl());
  shell.header.append(
    // Shown only while collapsed+running (14-tools.css): open, the stages' own
    // rings are on screen.
    el(
      "span",
      { className: "subagent-busy activity-dots", "aria-hidden": "true" },
      el("span", { className: "activity-dot" }),
      el("span", { className: "activity-dot" }),
      el("span", { className: "activity-dot" }),
    ),
    chevron,
  );
  shell.header.setAttribute("role", "button");
  shell.header.setAttribute("tabindex", "0");
  const body = el("div", { className: "subagent-body" });
  shell.root.insertBefore(body, shell.foot);
  const onToggle = (open: boolean): void => {
    shell.root.classList.toggle("collapsed", !open);
    opts.onOpenChange?.(open);
  };
  let ctl = createDisclosure(shell.header, body, { open: startOpen, onToggle });
  let wired = true;
  let pendingAutoOpen = false;
  // A failed pipeline opens on its own: the header can only say THAT a stage
  // failed, and which one is the reader's next question. A user toggle outranks it.
  let userToggled = false;
  const markToggled = (e: Event): void => {
    if (e instanceof KeyboardEvent && e.key !== "Enter" && e.key !== " ") {
      return;
    }
    // A header whose trigger is withdrawn toggles nothing, so a click on it is not
    // the reader taking over — reading it as one would suppress the auto-open the
    // body's first stage is about to earn.
    if (!wired) {
      return;
    }
    userToggled = true;
  };
  shell.header.addEventListener("click", markToggled);
  shell.header.addEventListener("keydown", markToggled);

  /** The disclosure's ONE writer. An EMPTY body gets the primitive's region-only mode,
   *  so the header keeps its glyph, name and state word and loses the control: no
   *  `aria-expanded` over an empty region, no tab stop, no chevron. */
  const syncDisclosure = (): void => {
    const populated = body.firstElementChild !== null;
    if (populated !== wired) {
      wired = populated;
      ctl.dispose();
      if (populated) {
        // Re-created rather than re-wired; the primitive re-installs `role` and
        // `tabindex` because the withdrawal took both away. `aria-controls` survives
        // the swap because the primitive assigns `region.id` only when it is empty.
        ctl = createDisclosure(shell.header, body, { open: startOpen, onToggle });
        shell.header.appendChild(chevron);
        shell.root.classList.add("has-disclosure");
        shell.root.classList.toggle("collapsed", !startOpen);
      } else {
        shell.header.removeAttribute("aria-expanded");
        shell.header.removeAttribute("aria-controls");
        shell.header.removeAttribute("tabindex");
        shell.header.removeAttribute("role");
        chevron.remove();
        shell.root.classList.add("collapsed");
        shell.root.classList.remove("has-disclosure");
        ctl = createDisclosure(null, body, { open: false });
      }
    }
    if (populated && pendingAutoOpen && !userToggled) {
      pendingAutoOpen = false;
      ctl.open();
    }
  };

  /** The failure auto-open's one enforcement point. An empty body has no chevron to
   *  close it again, so the ask is HELD until the body gains its first stage. */
  const openBody = (): void => {
    if (body.firstElementChild === null) {
      pendingAutoOpen = true;
      return;
    }
    ctl.open();
  };

  if (status === "failed") {
    openBody();
  }
  // A MICROTASK for the box built empty: the pass that builds it fills it
  // synchronously or never will, and a microtask lands before the frame is painted,
  // so the withdrawal is invisible where a task-late wiring would pop the chevron in.
  queueMicrotask(syncDisclosure);
  // The observer covers the rest of the container's life: a stage can arrive after the
  // driver's status frame, and `pipelineBoxFor` re-parents a promoted stage in.
  new MutationObserver(syncDisclosure).observe(body, { childList: true });

  return {
    ...shell.box,
    body,
    setStatus(s: ToolStatus): void {
      shell.box.setStatus(s);
      if (s === "failed" && !userToggled) {
        openBody();
      }
    },
  };
}
