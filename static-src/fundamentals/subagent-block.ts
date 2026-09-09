// The delegated-work boxes. `buildSubagentCard` is a LEAF and renders NONE of its
// delegate's output — that lives on the delegate's own page — so it has no
// disclosure and its HEAD is the control that opens that page.
// `buildSubagentContainer` is a pipeline over its stages, and its body holds THEIR
// cards, so the container is the one that discloses.
//
// Two consequences of the leaf having no body: the tail is PUSHED IN through
// `setTail` (`subagent-tail.ts` derives it from the store), and the state word is an
// `.sr-only` span — but only on an INERT head, because a screen reader ignores
// `aria-label` on a plain div while an anchor head carries the name and the word in
// an `aria-label` of its own.

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import type { ToolStatus } from "../types.js";
import { isToolActive } from "../tool-schema.js";
import { iconEl } from "../icon-el.js";
import { chevronEl } from "../chevron.js";
import { ICON_TAB_AGENT, outcomeIcon } from "../icons.js";
import { preserveReadingPosition } from "../scroll.js";
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
  /** Whether another element has been posted after this box in the store. Pushed in
   *  because only the dispatcher holds the block index that answers it. True FOLDS
   *  the box, subject to the carve-outs; false never opens one, because the
   *  carve-outs are refusals to collapse rather than reasons to expand. */
  setSuperseded(superseded: boolean): void;
}

/** The way to this delegate's own page, injected because a `fundamentals/`
 *  view must not import the feature module that owns tabs. `href` makes the head a
 *  real anchor (middle-click, copy-link); `open` routes an ordinary click
 *  through the app instead of reloading. */
export interface SubagentOpener {
  href: string;
  open: () => void;
}

export interface SubagentCardOptions {
  /** What the card's HEAD opens. Absent = the head stays inert, which is what a
   *  DETACHED render (it IS the delegate's page) and a delegate with no chat to open
   *  it in get. */
  open?: SubagentOpener;
}

export interface SubagentContainerOptions {
  /** Fired when the READER flips the disclosure; composition keys its
   *  open-container bookkeeping on ids this view never learns. An auto collapse
   *  and the failure auto-open are silent here, or the registry would record the
   *  view's own decisions as the reader's. */
  onOpenChange?: (open: boolean) => void;
  /** Where the disclosure starts. Default TRUE, which is the policy's floor: a box
   *  nothing has been posted after renders expanded, and only composition can say
   *  otherwise — it resolves the reader's own recorded state first, then the
   *  newest-element verdict. */
  startOpen?: boolean;
  /** Whether the reader has ALREADY decided about this box in a previous mount, so
   *  the auto path is off for its whole life. Default false. */
  userDecided?: boolean;
}

/** The announced word for a status. `cancelled` rather than the wire's `aborted`,
 *  matching the enclosing turn card's own footer. */
function stateWord(s: ToolStatus): string {
  return s === "failed"
    ? "failed"
    : s === "aborted"
      ? "cancelled"
      : isToolActive(s)
        ? "running"
        : "succeeded";
}

/** The identity row, the foot and the four setters that write them; each builder
 *  adds its own middle region between them. */
interface Shell {
  root: HTMLDivElement;
  header: HTMLElement;
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
  // ONE owner for the state word, chosen by which head was built: an anchor names
  // itself, so building both would announce the word twice.
  const stateEl = opener === undefined ? el("span", { className: "sr-only" }) : null;
  // The leaf's NAVIGATION glyph, in the slot the container's disclosure toggle
  // occupies. A span rather than a button: the head itself is the control, so
  // anything interactive inside it is axe's `nested-interactive`.
  const chevron =
    opener === undefined
      ? null
      : el("span", { className: "subagent-head-chevron", "aria-hidden": "true" }, chevronEl());
  const header = el(
    opener === undefined ? "div" : "a",
    {
      className: "subagent-header",
      [CHROME_ATTR]: "",
      ...(opener === undefined ? {} : { href: opener.href }),
    },
    icon,
    nameEl,
    stateEl,
    chevron,
  );
  let headLink: HTMLAnchorElement | null = null;
  if (opener !== undefined) {
    headLink = header as HTMLAnchorElement;
    headLink.addEventListener("click", (e) => {
      // A modified click (new tab/window) is a deliberate escape from routing.
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (e as MouseEvent).button !== 0) {
        return;
      }
      e.preventDefault();
      opener.open();
    });
  }

  // Created eagerly since `setSummary` withholds the footer until there is
  // something to show.
  const foot = el("div", { className: "subagent-foot", [CHROME_ATTR]: "" }) as HTMLDivElement;
  root.append(header, foot);

  let footer: HTMLDivElement | null = null;
  let lastSummary: TurnSummaryData = {};
  let iconSvg = ICON_TAB_AGENT;
  let lastStatus = status;
  let displayName = name;

  /** Write the name and its state word into whichever channel this head has. */
  const refreshName = (s: ToolStatus): void => {
    if (headLink === null) {
      if (stateEl !== null) {
        stateEl.textContent = stateWord(s);
      }
      return;
    }
    // `<thing>, <state word>` and nothing else: the role already says the head
    // opens something (run-card.ts's `.run-step-head` records the same call).
    headLink.setAttribute("aria-label", `${displayName}, ${stateWord(s)}`);
  };

  const applyIcon = (s: ToolStatus): void => {
    const failed = s === "failed";
    // A delegate the reader STOPPED is neither a success nor a failure of the work, so it
    // takes the yellow `warn` mark rather than the red one or the identity glyph.
    const aborted = s === "aborted";
    const active = isToolActive(s);
    icon.classList.toggle("is-fail", failed);
    icon.classList.toggle("is-warn", aborted);
    icon.classList.toggle("is-ok", !failed && !aborted && !active);
    icon.classList.toggle("is-running", active);
    root.classList.toggle("running", active);
    // A CARD empties the slot while active so CSS can spin it as a ring; a CONTAINER
    // keeps its identity glyph, because its stages carry the rings.
    const ring = active && !isContainer;
    icon.classList.toggle("subagent-spinner", ring);
    const mark = failed ? outcomeIcon("fail") : aborted ? outcomeIcon("warn") : iconSvg;
    icon.replaceChildren(...(ring ? [] : [iconEl(mark)]));
    refreshName(s);
  };

  applyIcon(status);

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
        displayName = n;
        nameEl.textContent = n;
        refreshName(lastStatus);
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
          foot.appendChild(footer);
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
 *  body for its stages, foot.
 *
 *  IT RENDERS EXPANDED WHILE IT IS THE NEWEST TOP-LEVEL ELEMENT, and folds when the
 *  next element is posted after it — the same rule and the same carve-outs as
 *  `tool-group.ts`, driven by the dispatcher through `setSuperseded` because only it
 *  knows where this box sits in the store. The expanded state goes to the box the
 *  reader is currently being shown; being superseded is what earns the fold.
 *
 *  Its carve-outs, all refusals to COLLAPSE: a still-running stage, a failure (which
 *  also RE-OPENS a box that folded before it), a reader who has decided, and an EMPTY
 *  body — the inverted form of the group's bare carve-out, since an empty body has
 *  already withdrawn the whole control and there would be no chevron to close it
 *  again.
 *
 *  BOTH height changes — the fold and the failure re-open — go through `scroll.ts`'s
 *  `preserveReadingPosition`, the transcript's one entry point for a layout change,
 *  exactly as `tool-group.ts` wraps its own two. */
export function buildSubagentContainer(
  name: string,
  status: ToolStatus,
  opts: SubagentContainerOptions = {},
): SubagentContainer {
  const shell = buildShell(name, status, true);
  const startOpen = opts.startOpen ?? true;
  // Built in its FULL form; `syncDisclosure` below takes the control away when there
  // is nothing to reveal.
  shell.root.classList.add("subagent-container", "has-disclosure");
  shell.root.classList.toggle("collapsed", !startOpen);
  // A span, not a button: the header is `role="button"` and carries the
  // disclosure's activation, so a `<button>` chevron inside it is axe's
  // `nested-interactive` (aria-hidden + tabindex="-1" does not clear it).
  const chevron = el("span", { className: "subagent-toggle", "aria-hidden": "true" }, chevronEl());
  // The chevron LEADS, because it DISCLOSES the stage cards below it, and the LEAF's
  // navigation chevron trails (`buildShell`). That difference is the whole point:
  // both boxes are `.subagent-header` with the same glyph, so before this a
  // collapsed container and a leaf card were pixel-identical while one expands in
  // place and the other opens a page. See chevron.ts for the rule.
  shell.header.prepend(chevron);
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
  );
  shell.header.setAttribute("role", "button");
  shell.header.setAttribute("tabindex", "0");
  const body = el("div", { className: "subagent-body" });
  shell.root.insertBefore(body, shell.foot);
  // ONE flag, one meaning, two writers: the creation seed below and the reader's own
  // toggle. A user toggle outranks every automatic path, in both directions.
  //
  // It is read off the toggle's own `source` rather than inferred from a click, which
  // is what retired the header's click/keydown listeners: a withdrawn (region-only)
  // disclosure has no trigger, so it emits no user toggle at all and a click on such
  // a header cannot be mistaken for the reader taking over.
  let userToggled = opts.userDecided ?? false;
  let lastStatus = status;
  let superseded = false;
  const onToggle = (open: boolean, source: "user" | "api"): void => {
    shell.root.classList.toggle("collapsed", !open);
    if (source !== "user") {
      return;
    }
    userToggled = true;
    opts.onOpenChange?.(open);
  };
  let ctl = createDisclosure(shell.header, body, { open: startOpen, onToggle });
  let wired = true;
  let pendingAutoOpen = false;

  /** The newest-element fold, and the only place the carve-outs are spelled.
   *
   *  COLLAPSE-ONLY, which is what makes it trivially idempotent: the verdict is
   *  monotone (once something is posted after this box in the store it stays posted,
   *  and a rewind rebuilds the transcript), so "apply the verdict every pass" and
   *  "collapse when superseded" are the same function. */
  const applyAutoCollapse = (): void => {
    if (
      !superseded ||
      userToggled ||
      isToolActive(lastStatus) ||
      lastStatus === "failed" ||
      body.firstElementChild === null ||
      // Gated on the state actually moving: this runs on every pass, and the
      // compensator measures the scroller on each call.
      !ctl.isOpen
    ) {
      return;
    }
    // An AUTO collapse removes height ABOVE the reader, so it is compensated —
    // `scroll.ts` is THE ONE ENTRY POINT for a transcript height change, and this box's
    // body is N stage cards. Wrapped HERE and not at the dispatcher's
    // `syncContainerCollapse` arm: the helper adjusts scrollTop by a delta it measures
    // itself, so a nested pair would compensate the same delta twice.
    preserveReadingPosition(() => {
      ctl.close();
    }, "content-growth");
  };

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
        //
        // From `startOpen` and NOT from `ctl.isOpen`, because a body only ever goes
        // empty→populated: this box is withdrawn exactly once, on the construction
        // microtask before its first stage arrives, and the withdrawn controller is
        // built `open: false`, so reading the current state would born-collapse every
        // box the stage path fills a task late — including one the verdict says is the
        // newest element. The reverse transition cannot reach here: once the box is in
        // `st.pipelines`, `stageHostFor` returns its body for every one of its stages,
        // so nothing moves a stage OUT, and the one path that removes the last one
        // (`pruneOrphanedCards`) is followed synchronously by `pruneEmptyContainers`,
        // which releases the box and removes its root in the same drop pass. So a
        // populated body cannot empty and refill, and there is no current state for
        // the seed to overwrite. `userToggled` is a closure variable either way, so a
        // reader who has decided keeps their auto path off across the swap.
        ctl = createDisclosure(shell.header, body, { open: startOpen, onToggle });
        // PREPEND, matching where the build put it: a disclosure chevron leads.
        // Appending here would restore it to the trailing edge, where it would read
        // as the leaf card's navigation glyph.
        shell.header.prepend(chevron);
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
      // Through `openBody` rather than a bare `ctl.open()`, so the held ask lands
      // through the one enforcement point and takes its scroll compensation with it.
      openBody();
    }
    // A supersede that arrived while the body was empty was refused then; the body
    // gaining its first stage is when it becomes answerable.
    applyAutoCollapse();
  };

  /** The failure auto-open's one enforcement point. An empty body has no chevron to
   *  close it again, so the ask is HELD until the body gains its first stage.
   *
   *  Compensated for the same reason the fold is: this adds the stage cards' height
   *  back ABOVE the reader, which is the direction `maybeCollapseGroup` wraps too. */
  const openBody = (): void => {
    if (body.firstElementChild === null) {
      pendingAutoOpen = true;
      return;
    }
    if (ctl.isOpen) {
      return;
    }
    preserveReadingPosition(() => {
      ctl.open();
    }, "content-growth");
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
      lastStatus = s;
      shell.box.setStatus(s);
      if (s === "failed" && !userToggled) {
        // Both directions of the failure carve-out: it BLOCKS a fold (through
        // `applyAutoCollapse`) and RE-OPENS a box that folded before the failing
        // stage settled — the one state the header cannot substitute for.
        openBody();
        return;
      }
      // A settle is not another element being posted, so it folds nothing by itself;
      // what it can do is release the still-running refusal for a supersede that
      // already arrived.
      applyAutoCollapse();
    },
    setSuperseded(next: boolean): void {
      superseded = next;
      applyAutoCollapse();
    },
  };
}
