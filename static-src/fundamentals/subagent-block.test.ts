// ---------------------------------------------------------------------------
// Tests for fundamentals/subagent-block.ts — the delegated-work boxes.
//
// Five subjects. The header identity glyph: while active the slot shows the
// spinner, once settled the SVG icon — the shared agent hexagon by default, or
// the per-known-subagent glyph installed via setIcon (roles.ts iconForSubagent
// keys it off the invoke_sub_agent input name). The two SHAPES: a card discloses
// nothing, takes its tail from outside, and its head is the door to the delegate's
// page; a container discloses its stages. The card's HEAD AS A CONTROL: what
// activating it does, what a modified click does instead, and that nothing
// interactive sits inside it. The container's WITHDRAWAL: a body with nothing in it
// loses the control. And what a screen reader is told about any of them.
//
// The tail's CONTENT is not tested here any more, because this file no longer
// derives it: `subagent-tail.test.ts` owns the projection and this one owns the
// sink.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { loadCSS, mountAppCSS } from "../__test-helpers__/css-rules.js";

// Partial mock: the card now imports turn-footer (whose navigate → tabs chain
// reads the whole icon table), so only the identity glyph is overridden.
vi.mock("../icons.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, string>>()),
  ICON_TAB_AGENT: '<svg data-icon="agent-hexagon"></svg>',
}));

// scroll.ts is a self-initialising singleton over a real `#messages`; the canonical
// mock is what every other suite in this graph uses, and its compensation helpers run
// their mutation, so a fold still reaches the DOM. It is also what makes the
// compensation itself observable — see "the compensator wraps both height changes".
vi.mock("../scroll.js", () =>
  import("../__test-helpers__/scroll-mock.js").then((m) => m.scrollMock),
);

import {
  buildSubagentCard,
  buildSubagentContainer,
  type SubagentContainer,
} from "./subagent-block.js";
import { scrollMock } from "../__test-helpers__/scroll-mock.js";
import type { ToolStatus } from "../types.js";
import { outcomeIcon } from "../icons.js";
import { iconEl } from "../icon-el.js";

const iconSlot = (root: HTMLElement): HTMLElement =>
  root.querySelector(".subagent-icon") as HTMLElement;
const headerOf = (root: HTMLElement): HTMLElement =>
  root.querySelector(".subagent-header") as HTMLElement;

/** A task boundary, not a timeout. `syncDisclosure` runs off a construction
 *  microtask and off a MutationObserver, and both have been delivered by the next
 *  task — so this is a guarantee rather than a wait long enough to probably work. */
function nextTask(): Promise<void> {
  return new Promise<void>((resolve) => {
    setTimeout(resolve, 0);
  });
}

/** Put a stage in the container's body, which is what a real pipeline has. Every
 *  collapse-policy fixture needs it: a container whose body is empty WITHDRAWS its
 *  whole control, so a header click on one toggles nothing. */
async function populate(box: { body: HTMLElement }): Promise<void> {
  box.body.appendChild(document.createElement("div")).textContent = "a stage card";
  await nextTask();
}

const tailLines = (root: HTMLElement): (string | null)[] =>
  [...root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);

/** The href every opener below hands the head. */
const HREF = "/chat/c/subagent/u-1";

const opener = (open: () => void = () => undefined): { href: string; open: () => void } => ({
  href: HREF,
  open,
});

/** A click carrying modifiers or a non-primary button, which `HTMLElement.click()`
 *  cannot express. Returns whether the head cancelled it.
 *
 *  The trailing guard is what keeps a NOT-cancelled click from navigating the test
 *  page — an anchor follows its href even detached. Registered after the head's own
 *  listener, so it reads that listener's verdict before stopping the default. */
function clickWith(head: HTMLElement, init: MouseEventInit): boolean {
  let prevented = false;
  const guard = (e: Event): void => {
    prevented = e.defaultPrevented;
    e.preventDefault();
  };
  head.addEventListener("click", guard);
  head.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...init }));
  head.removeEventListener("click", guard);
  return prevented;
}

describe("buildSubagentCard icon", () => {
  it("shows the spinner while active, the default hexagon once settled", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(iconSlot(sa.root).querySelector("svg")).toBeNull();

    sa.setStatus("completed");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(false);
    expect(iconSlot(sa.root).querySelector('svg[data-icon="agent-hexagon"]')).not.toBeNull();
  });

  it("setIcon swaps the settled glyph (distinct icon per known subagent)", () => {
    const sa = buildSubagentCard("Introspect", "completed");
    sa.setIcon('<svg data-icon="introspect"></svg>');
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();

    // A FAILURE replaces the installed glyph with the shared failure silhouette:
    // one mark per row, and its SHAPE is what changes for a non-success state.
    sa.setStatus("failed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).toBeNull();
    expect(iconSlot(sa.root).querySelector("svg")?.outerHTML).toBe(
      (iconEl(outcomeIcon("fail")) as HTMLElement).outerHTML,
    );
    expect(iconSlot(sa.root).querySelectorAll("svg")).toHaveLength(1);

    // Back to a success and the installed identity glyph returns, so the card
    // cannot be stranded on a mark it borrowed.
    sa.setStatus("completed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();
    expect(iconSlot(sa.root).querySelectorAll("svg")).toHaveLength(1);
  });

  it("setIcon while active defers the glyph until the subagent settles", () => {
    const sa = buildSubagentCard("Introspect", "in_progress");
    sa.setIcon('<svg data-icon="introspect"></svg>');
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(iconSlot(sa.root).querySelector("svg")).toBeNull();

    sa.setStatus("completed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();
  });

  // A delegate the reader STOPPED is a third terminal state, not a slow success and
  // not a failure to debug.
  it("leaves the running state for the warn mark when the turn is cancelled", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(sa.root.classList.contains("running")).toBe(true);

    sa.setStatus("aborted");

    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(false);
    expect(sa.root.classList.contains("running")).toBe(false);
    expect(sa.root.querySelector(".subagent-tail")).toBeNull();
    // Yellow, and its own SHAPE: `is-ok` would claim a result this delegate never
    // produced, `is-fail` would send the reader to debug work nothing broke in.
    expect(iconSlot(sa.root).classList.contains("is-warn")).toBe(true);
    expect(iconSlot(sa.root).classList.contains("is-ok")).toBe(false);
    expect(iconSlot(sa.root).classList.contains("is-fail")).toBe(false);
    expect(iconSlot(sa.root).querySelector("svg")?.outerHTML).toBe(
      (iconEl(outcomeIcon("warn")) as HTMLElement).outerHTML,
    );
    expect(iconSlot(sa.root).querySelectorAll("svg")).toHaveLength(1);
  });
});

// A CARD HAS NO BODY, which is the whole of this change: the transcript renders
// none of a delegate's output, so there is nothing here to disclose and a toggle
// that toggles nothing is worse than no toggle.
describe("a delegate's card is not a disclosure", () => {
  it("has no body, no disclosure toggle and no aria-expanded", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    expect(sa.root.querySelector(".subagent-body")).toBeNull();
    expect(sa.root.querySelector(".subagent-toggle")).toBeNull();
    const header = sa.root.querySelector<HTMLElement>(".subagent-header");
    expect(header?.getAttribute("role")).toBeNull();
    expect(header?.getAttribute("tabindex")).toBeNull();
    expect(header?.getAttribute("aria-expanded")).toBeNull();

    // A card WITH an opener is a plain anchor: nothing expands, so it takes none of
    // the disclosure's attributes and its `href` is the whole state it carries.
    const door = buildSubagentCard("Subagent", "in_progress", { open: opener() });
    const head = headerOf(door.root);
    expect(head.tagName).toBe("A");
    expect(head.getAttribute("href")).toBe(HREF);
    expect(head.getAttribute("role")).toBeNull();
    expect(head.getAttribute("aria-expanded")).toBeNull();
    expect(door.root.querySelector(".subagent-toggle")).toBeNull();
  });

  it("opens the delegate's page when its header is activated, and folds nothing", () => {
    const opened: number[] = [];
    const sa = buildSubagentCard("Subagent", "in_progress", {
      open: opener(() => opened.push(1)),
    });
    // Through `clickWith` rather than `.click()`, so a head that stops cancelling the
    // event fails the case below by name instead of navigating the test page.
    clickWith(headerOf(sa.root), {});
    expect(opened).toHaveLength(1);
    expect(sa.root.classList.contains("collapsed")).toBe(false);
    // Nor on a failure, which used to open the box to show the reason. The reason
    // is on the delegate's page now, and the head is the way to it.
    sa.setStatus("failed");
    expect(sa.root.classList.contains("collapsed")).toBe(false);
  });

  it("marks the root running for the tail's visibility gate", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    expect(sa.root.classList.contains("running")).toBe(true);
    sa.setStatus("completed");
    expect(sa.root.classList.contains("running")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// The card's HEAD is the door to the delegate's page. One `it` per claim, so a
// failure names one thing.
// ---------------------------------------------------------------------------
describe("the card's head is the door to the delegate's page", () => {
  /** A card with an opener, plus the calls its head made. */
  function door(): { head: HTMLElement; opened: number[]; root: HTMLElement } {
    const opened: number[] = [];
    const sa = buildSubagentCard("Subagent", "in_progress", {
      open: opener(() => opened.push(1)),
    });
    return { head: headerOf(sa.root), opened, root: sa.root };
  }

  it("routes a plain click through the app instead of the href", () => {
    const { head, opened } = door();
    expect(clickWith(head, { button: 0 })).toBe(true);
    expect(opened).toHaveLength(1);
  });

  it("lets a modified or non-primary click fall through to the browser", () => {
    // The deliberate escape from routing: a new tab or window is the reader asking
    // the platform rather than the app.
    for (const init of [
      { metaKey: true },
      { ctrlKey: true },
      { shiftKey: true },
      { altKey: true },
      { button: 1 },
    ]) {
      const { head, opened } = door();
      expect(clickWith(head, init), JSON.stringify(init)).toBe(false);
      expect(opened, JSON.stringify(init)).toHaveLength(0);
    }
  });

  it("is keyboard-reachable and natively activated", () => {
    // A real `a[href]` gets its tab stop and its Enter activation from the platform,
    // which is why the head carries no `role`, no `tabindex` and no keydown handler.
    const { head } = door();
    expect(head.tagName).toBe("A");
    expect(head.getAttribute("href")).toBe(HREF);
    expect(head.tabIndex).toBe(0);
    // Both lines are wanted: `tabIndex === 0` is the platform's answer and an explicit
    // `tabindex="0"` satisfies it too, so it cannot say the head takes its tab stop
    // from being an anchor.
    expect(head.hasAttribute("tabindex")).toBe(false);
  });

  it("holds no interactive descendant", () => {
    // The binding constraint: anything interactive inside the control is axe's
    // `nested-interactive`, which is why the foot is OUTSIDE it — turn-footer builds a
    // real button plus one per changed file.
    const { head } = door();
    expect(
      head.querySelector(
        "a, button, input, select, textarea, summary, [role='button'], [tabindex]",
      ),
    ).toBeNull();
  });

  it("carries the shared chevron, and a detached render carries none", () => {
    const { head } = door();
    expect(head.querySelector(".subagent-head-chevron > .disclosure-chevron")).not.toBeNull();

    const inert = buildSubagentCard("Subagent", "in_progress");
    expect(inert.root.querySelector(".subagent-head-chevron")).toBeNull();
    expect(inert.root.querySelector(".disclosure-chevron")).toBeNull();
    expect(headerOf(inert.root).tagName).toBe("DIV");
  });

  it("has no separate open link, in either shape or in the stylesheet", () => {
    // The source guard is what catches a rule left behind for an element nothing builds.
    const { root } = door();
    expect(root.querySelector(".subagent-open")).toBeNull();
    expect(buildSubagentCard("Subagent", "completed").root.querySelector(".subagent-open")).toBe(
      null,
    );
    expect(loadCSS("14-tools.css")).not.toContain("subagent-open");
  });
});

describe("the tail is a sink", () => {
  it("renders one line element per line, in the order given", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setTail(["three", "four", "five"]);
    expect(tailLines(sa.root)).toEqual(["three", "four", "five"]);
    // Replaced, never appended: the tail is the last N lines, not a log.
    sa.setTail(["four", "five", "six"]);
    expect(tailLines(sa.root)).toEqual(["four", "five", "six"]);
  });

  it("exists while running and is REMOVED on settle — the footer takes over", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setTail(["working"]);
    expect(sa.root.querySelector(".subagent-tail")).not.toBeNull();
    sa.setStatus("completed");
    expect(sa.root.querySelector(".subagent-tail")).toBeNull();
  });

  it("ignores a write that arrives after the settle", () => {
    // The binding is disposed on settle, but a delta already in flight would
    // otherwise re-attach nothing and paint into a detached node.
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setStatus("completed");
    sa.setTail(["late line"]);
    expect(sa.root.querySelector(".subagent-tail")).toBeNull();
    expect(tailLines(sa.root)).toEqual([]);
  });

  it("has no tail at all when built already settled", () => {
    const sa = buildSubagentCard("Subagent", "completed");
    expect(sa.root.querySelector(".subagent-tail")).toBeNull();
  });
});

describe("the pipeline container", () => {
  it("has a body, a chevron and a header that is a real control", async () => {
    const box = buildSubagentContainer("Subagent pipeline · 2 stages", "in_progress");
    await populate(box);
    expect(box.body.classList.contains("subagent-body")).toBe(true);
    expect(box.root.classList.contains("subagent-container")).toBe(true);
    expect(box.root.querySelector(".subagent-toggle")).not.toBeNull();
    const header = headerOf(box.root);
    expect(header.getAttribute("role")).toBe("button");
    // The newest box is expanded, and the control SAYS so — the aria state has to
    // track the fold in both directions or a screen-reader user is told the box is
    // open while the reader sees it shut.
    expect(header.getAttribute("aria-expanded")).toBe("true");

    box.setStatus("completed");
    box.setSuperseded(true);
    expect(header.getAttribute("aria-expanded")).toBe("false");
  });

  it("renders expanded while it is the newest box, and folds when it is superseded", async () => {
    // The rule, at box scope. The expanded state goes to the box the reader is
    // currently being shown; the next element posted after it is what earns the fold.
    // (What this replaced asserted the inverse — born collapsed, no auto-toggle in
    // either direction — and called the newest-is-open reading "exactly backwards".)
    const box = buildSubagentContainer("pipeline", "completed");
    await populate(box);
    expect(box.root.classList.contains("collapsed")).toBe(false);

    box.setSuperseded(true);
    expect(box.root.classList.contains("collapsed")).toBe(true);

    // And the reader outranks it for life: re-opening a folded box is not undone by a
    // later verdict.
    headerOf(box.root).click();
    expect(box.root.classList.contains("collapsed")).toBe(false);
    box.setSuperseded(true);
    expect(box.root.classList.contains("collapsed")).toBe(false);
  });

  it("a settle alone folds nothing: only being superseded does", async () => {
    // The "never a timer" half of the rule. Nothing about a pipeline finishing is
    // positional, so it cannot fold the box.
    const box = buildSubagentContainer("pipeline", "in_progress");
    await populate(box);
    expect(box.root.classList.contains("collapsed")).toBe(false);
    box.setStatus("completed");
    expect(box.root.classList.contains("collapsed")).toBe(false);
  });

  it("does not fold a container while a stage is still running", async () => {
    // A still-running member keeps it open, exactly as it does for a tool group —
    // and the later settle is what releases the refusal for a verdict already given.
    const box = buildSubagentContainer("pipeline", "in_progress");
    await populate(box);
    box.setSuperseded(true);
    expect(box.root.classList.contains("collapsed")).toBe(false);
    box.setStatus("completed");
    expect(box.root.classList.contains("collapsed")).toBe(true);
  });

  it("does not fold a container whose body is empty", async () => {
    // The INVERTED bare carve-out: an empty body withdraws the whole control, so the
    // disclosure is not the box's to drive — folding one would animate a box shut on
    // its way to having no chevron at all.
    //
    // Asserted BEFORE the withdrawal microtask, which is the window the guard is
    // reachable in and the one production takes: composition builds a box and runs
    // `syncContainerCollapse` in the same synchronous pass. After the microtask the
    // withdrawn controller is already closed, so a fold there is a no-op and the
    // assertion could not fail.
    const box = buildSubagentContainer("pipeline", "completed");
    box.setSuperseded(true);
    expect(box.root.classList.contains("collapsed")).toBe(false);

    await nextTask();
    expect(box.root.querySelector("[aria-expanded]")).toBeNull();
    expect(box.root.querySelector(".subagent-toggle")).toBeNull();
  });

  // FAILURE IS NOT NOISE: the header can only say THAT a stage failed, and which
  // one is the reader's next question — same rule as the tool group.
  it("pops open when the pipeline fails", async () => {
    const box = buildSubagentContainer("pipeline", "in_progress", { startOpen: false });
    await populate(box);
    expect(box.root.classList.contains("collapsed")).toBe(true);
    box.setStatus("failed");
    expect(box.root.classList.contains("collapsed")).toBe(false);
  });

  // AN ABORT IS NOT A FAILURE, so it takes neither half of the failure carve-out: a
  // pipeline the reader stopped has no error to investigate, so it folds like any
  // other settled box and never pops open demanding attention.
  it("folds a superseded pipeline the reader cancelled, and never pops it open", async () => {
    const box = buildSubagentContainer("pipeline", "in_progress");
    await populate(box);
    box.setSuperseded(true);
    expect(box.root.classList.contains("collapsed")).toBe(false);

    box.setStatus("aborted");

    expect(box.root.classList.contains("collapsed")).toBe(true);
    expect(box.root.classList.contains("running")).toBe(false);
    // It keeps its identity glyph rather than emptying the slot — a container's
    // stages carry the rings, so its own mark is the warn silhouette.
    expect(iconSlot(box.root).classList.contains("subagent-spinner")).toBe(false);
    expect(iconSlot(box.root).querySelectorAll("svg")).toHaveLength(1);
  });

  it("re-opens a box the verdict had already folded when a stage fails", async () => {
    // The other half of the same carve-out, and the one the fold makes reachable: a
    // failure acquired AFTER the fold is the one state the header cannot stand in for.
    const box = buildSubagentContainer("pipeline", "completed");
    await populate(box);
    box.setSuperseded(true);
    expect(box.root.classList.contains("collapsed")).toBe(true);
    box.setStatus("failed");
    expect(box.root.classList.contains("collapsed")).toBe(false);
  });

  it("routes the fold through the scroll compensator", async () => {
    // `scroll.ts` calls `preserveReadingPosition` THE ONE ENTRY POINT for a transcript
    // height change, and an auto fold removes height ABOVE the reader — this body is N
    // stage cards, taller than the tool-group case the helper was made mandatory for.
    // `autoCollapseGroup` wraps its own fold for exactly this reason.
    //
    // Withholding the wrapped mutation is what makes the wrapping falsifiable: a
    // `ctl.close()` sitting OUTSIDE the wrapper folds the box regardless, so the last
    // two assertions go red the moment the compensation is dropped.
    const box = buildSubagentContainer("pipeline", "completed");
    await populate(box);
    scrollMock.preserveReadingPosition.mockImplementation(() => undefined);

    box.setSuperseded(true);

    expect(scrollMock.preserveReadingPosition).toHaveBeenCalledTimes(1);
    expect(scrollMock.preserveReadingPosition.mock.calls[0]?.[1]).toBe("content-growth");
    expect(box.root.classList.contains("collapsed")).toBe(false);
    expect(headerOf(box.root).getAttribute("aria-expanded")).toBe("true");
  });

  it("routes the failure re-open through it as well", async () => {
    // The other direction, which `maybeCollapseGroup` also wraps: this ADDS the stage
    // cards' height back above the reader.
    const box = buildSubagentContainer("pipeline", "completed", { startOpen: false });
    await populate(box);
    expect(box.root.classList.contains("collapsed")).toBe(true);
    scrollMock.preserveReadingPosition.mockImplementation(() => undefined);

    box.setStatus("failed");

    expect(scrollMock.preserveReadingPosition).toHaveBeenCalledTimes(1);
    expect(scrollMock.preserveReadingPosition.mock.calls[0]?.[1]).toBe("content-growth");
    expect(box.root.classList.contains("collapsed")).toBe(true);
  });

  it("respects a reader who closed it: a later failure stays closed", async () => {
    const box = buildSubagentContainer("pipeline", "in_progress");
    await populate(box);
    headerOf(box.root).click(); // close — the reader has taken control
    box.setStatus("failed");
    expect(box.root.classList.contains("collapsed")).toBe(true);
  });

  it("respects a reader who decided in a PREVIOUS mount, in both directions", async () => {
    // The cross-mount layer: the registry entry IS the latch, so a box re-created for
    // a reader who had closed it refuses the verdict AND the failure auto-open.
    const box = buildSubagentContainer("pipeline", "in_progress", {
      startOpen: false,
      userDecided: true,
    });
    await populate(box);
    expect(box.root.classList.contains("collapsed")).toBe(true);
    box.setStatus("failed");
    expect(box.root.classList.contains("collapsed")).toBe(true);

    const open = buildSubagentContainer("pipeline", "completed", {
      startOpen: true,
      userDecided: true,
    });
    await populate(open);
    open.setSuperseded(true);
    expect(open.root.classList.contains("collapsed")).toBe(false);
  });

  it("writes the reader's choice and never its own", async () => {
    // The registry records a DECISION, so the view's own folds and its failure
    // auto-open must leave no entry — otherwise every auto-collapse would come back
    // as "the reader closed this".
    const seen: boolean[] = [];
    const box = buildSubagentContainer("pipeline", "completed", {
      onOpenChange: (open) => seen.push(open),
    });
    await populate(box);
    box.setSuperseded(true);
    box.setStatus("failed");
    expect(seen).toEqual([]);

    headerOf(box.root).click();
    expect(seen).toEqual([false]);
  });

  it("carries no tail: its stages each have their own", () => {
    const box = buildSubagentContainer("pipeline", "in_progress");
    expect(box.root.querySelector(".subagent-tail")).toBeNull();
    expect(box.root.querySelector(".subagent-busy")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// A container whose body holds nothing loses its disclosure.
//
// One shape reaches it, probed: a driver that SETTLED having dispatched no stage.
// That box is deliberately kept, because nothing else stands in for a failed
// dispatch — so the header stays on screen and what goes is the CONTROL, through
// the primitive's region-only mode, the third use of it here after
// `tool-group.ts`. (A CARD cannot reach this at all: it has no body and no
// disclosure to withdraw.)
//
// The container is BUILT with its control and withdraws on a construction
// microtask, so every case here awaits one: the pass that builds a box fills it in
// the same task or never will, and the microtask beats the paint. Defaulting the
// other way would pop the chevron in on every box in a transcript.
// ---------------------------------------------------------------------------

describe("a container with nothing in its body", () => {
  /** A built container whose withdrawal has landed. */
  async function bare(status: ToolStatus = "completed"): Promise<SubagentContainer> {
    const box = buildSubagentContainer("pipeline", status);
    await nextTask();
    return box;
  }

  it("exposes no aria-expanded", async () => {
    expect((await bare()).root.querySelector("[aria-expanded]")).toBeNull();
  });

  it("is not a tab stop", async () => {
    expect(headerOf((await bare()).root).hasAttribute("tabindex")).toBe(false);
  });

  it("shows no chevron", async () => {
    expect((await bare()).root.querySelector(".subagent-toggle")).toBeNull();
  });

  it("drops the header's pointer affordance class", async () => {
    // What 14-tools.css keys `cursor`, `user-select` and the hover wash on.
    expect((await bare()).root.classList.contains("has-disclosure")).toBe(false);
  });

  it("opens nothing when its header is clicked", async () => {
    const box = await bare();
    headerOf(box.root).click();
    expect(box.body.getAttribute("aria-hidden")).toBe("true");
  });

  it("keeps the header on screen, named", async () => {
    // The box IS the header when the body is empty, and that box is what makes a
    // failed dispatch visible. Its name and its state word are ordinary text, so it
    // needs no role to carry them: it ends up the plain div a card's header is.
    const box = buildSubagentContainer("Subagent pipeline · 0 stages", "failed");
    await nextTask();
    const header = headerOf(box.root);
    expect(header.getAttribute("role")).toBeNull();
    expect(header.querySelector(".subagent-name")?.textContent).toBe(
      "Subagent pipeline · 0 stages",
    );
    expect(header.querySelector(".sr-only")?.textContent).toBe("failed");
  });

  it("is not auto-opened by a failure", async () => {
    // The refusal `expandToolDetails` makes on a bare tool card, for the same
    // reason: there is no chevron to close the region again, so an open one is
    // stranded — and worse than a control over nothing, it is an OPEN region
    // containing nothing.
    const box = await bare("failed");
    expect(box.body.getAttribute("aria-hidden")).toBe("true");
    expect(box.root.classList.contains("collapsed")).toBe(true);
  });

  it("holds that failure's open until the body has something to show", async () => {
    const box = await bare("failed");
    await populate(box);
    expect(box.body.getAttribute("aria-hidden")).toBe("false");
  });

  it("still holds it when the failure arrives on a later frame", async () => {
    const box = await bare("in_progress");
    box.setStatus("failed");
    await populate(box);
    expect(box.body.getAttribute("aria-hidden")).toBe("false");
  });

  it("does not read a click on the withdrawn header as the reader taking over", async () => {
    // A header with no trigger toggles nothing, so counting that click as a user
    // toggle would suppress the auto-open the reader was reaching for.
    const box = await bare("failed");
    headerOf(box.root).click();
    await populate(box);
    expect(box.body.getAttribute("aria-hidden")).toBe("false");
  });

  it("gains a working disclosure once the body has a stage", async () => {
    const box = await bare();
    expect(box.root.querySelector(".subagent-toggle")).toBeNull();
    await populate(box);

    const header = headerOf(box.root);
    expect(header.getAttribute("role")).toBe("button");
    expect(header.getAttribute("tabindex")).toBe("0");
    expect(header.getAttribute("aria-controls")).toBe(box.body.id);
    expect(box.root.querySelector(".subagent-toggle")).not.toBeNull();
    expect(box.root.classList.contains("has-disclosure")).toBe(true);

    // Restored to the newest-element default rather than to a remembered state: the
    // box is the newest thing in its lane until something says otherwise.
    expect(header.getAttribute("aria-expanded")).toBe("true");
    header.click();
    expect(header.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps its control when the pass that built it DID fill the body", async () => {
    // The reason the default is the control rather than the withdrawal: this is the
    // ordinary box, and it must never flicker a chevron away and back.
    const box = buildSubagentContainer("pipeline", "completed");
    box.body.appendChild(document.createElement("div")).textContent = "a stage card";
    await nextTask();
    expect(box.root.querySelector(".subagent-toggle")).not.toBeNull();
    expect(headerOf(box.root).getAttribute("aria-expanded")).toBe("true");
  });
});

// ---------------------------------------------------------------------------
// What a screen reader is told. After the drop the delegate's output is reachable
// only through the foot's link, so the card has to name itself and name where that
// link goes — a transcript can hold a dozen of them.
// ---------------------------------------------------------------------------
describe("accessible names", () => {
  it("announces the state as a word, not as the glyph's colour", () => {
    const sa = buildSubagentCard("introspect", "in_progress");
    const state = (): string | null =>
      sa.root.querySelector<HTMLElement>(".subagent-header > .sr-only")?.textContent ?? null;
    expect(state()).toBe("running");
    sa.setStatus("failed");
    expect(state()).toBe("failed");
    sa.setStatus("completed");
    expect(state()).toBe("succeeded");
    // The DISPLAYED word is not the wire enum: the wire spells it `aborted` and the
    // reader is told `cancelled`, which is what the enclosing turn card's own footer
    // says about the same stop.
    sa.setStatus("aborted");
    expect(state()).toBe("cancelled");
  });

  it("names the head for its delegate and its state, and follows both", () => {
    const sa = buildSubagentCard("Subagent", "in_progress", { open: opener() });
    const head = headerOf(sa.root);
    // `<thing>, <state word>` and nothing else: the anchor already says it opens
    // something, so a second sentence would restate the role.
    expect(head.getAttribute("aria-label")).toBe("Subagent, running");
    sa.setStatus("failed");
    expect(head.getAttribute("aria-label")).toBe("Subagent, failed");
    sa.setName("context-gatherer");
    expect(head.getAttribute("aria-label")).toBe("context-gatherer, failed");
    // ONE owner: the `.sr-only` span is not built at all here, so the state word
    // cannot be announced twice.
    expect(head.querySelector(".sr-only")).toBeNull();
  });
});

describe("the footer", () => {
  it("does not exist until the summary has something worth a row", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setSummary({ commands: 0, reads: 0, changedFiles: {} });
    expect(sa.root.querySelector(".subagent-footer")).toBeNull();
  });

  it("is turn-footer reused, updated in place, and the card's last region", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setSummary({ commands: 3, reads: 2, changedFiles: {} });
    const footer = sa.root.querySelector<HTMLElement>(".subagent-footer");
    expect(footer).not.toBeNull();
    expect(footer?.classList.contains("turn-footer")).toBe(true);
    expect(sa.root.lastElementChild?.classList.contains("subagent-foot")).toBe(true);

    sa.setSummary({
      commands: 3,
      reads: 2,
      changedFiles: { "a.go": { lines_added: 4, lines_removed: 1 } },
      outcome: "completed",
      elapsedMs: 1200,
    });
    // Still ONE footer, updated rather than stacked.
    expect(sa.root.querySelectorAll(".subagent-footer").length).toBe(1);
    expect(footer?.dataset["outcome"]).toBe("completed");
  });
});

// ---------------------------------------------------------------------------
// The footer's INFO PANEL on a delegate card. This is the second consumer of
// `fundamentals/turn-footer.ts`, and the sections it can never fill are the point:
// nothing on the ACP wire carries credits, a model id or a stop reason PER
// delegate, so Cost, Model and Diagnostics withhold on every delegate card there
// will ever be. `messages-blocks.ts`'s `subagentSummary` is what shapes the data
// below — the fields it fills, and the three it deliberately leaves absent.
// ---------------------------------------------------------------------------

describe("the delegate footer's info panel", () => {
  /** A settled delegate as `subagentSummary` produces one: the member calls' counts
   *  and kinds, the tool time, a nested delegate, the invocation's own stamp, and the
   *  end DERIVED from that stamp plus this call's measured duration. No credits, no
   *  model, no stop reason. */
  const DELEGATE = {
    outcome: "completed",
    commands: 2,
    reads: 3,
    changedFiles: { "a.go": { lines_added: 4, lines_removed: 1 } },
    elapsedMs: 92000,
    toolMs: 30000,
    kindCounts: { read: 3, execute: 2, edit: 1 },
    delegateCount: 1,
    delegateMs: 9000,
    startedAt: Date.UTC(2026, 0, 2, 9, 5, 0),
    endedAt: Date.UTC(2026, 0, 2, 9, 5, 0) + 92000,
  } as const;

  function panelSections(root: HTMLElement): string[] {
    return [...root.querySelectorAll(".subagent-footer .turn-info-title")].map(
      (h) => h.textContent ?? "",
    );
  }

  function settled(): HTMLElement {
    const sa = buildSubagentCard("context-gatherer", "completed");
    sa.setSummary(DELEGATE);
    return sa.root;
  }

  it("renders the three sections a delegate can fill", () => {
    const root = settled();
    expect(panelSections(root)).toEqual(["Timings", "Work", "Delegates"]);
  });

  it("carries the delegate's own timings, its work and the delegates it dispatched", () => {
    const root = settled();
    const rows = [...root.querySelectorAll(".subagent-footer .turn-info-row")].map((r) => [
      r.querySelector(".turn-info-label")?.textContent ?? "",
      r.querySelector(".turn-info-value")?.textContent ?? "",
    ]);
    expect(rows).toEqual([
      ["Wall clock", "1m 32s"],
      ["Tool time", "30.0s"],
      ["Model time", "1m 2s"],
      // The stamp text is the reader's own locale, so the LABELS are what is pinned
      // here; `fundamentals/turn-footer.test.ts` owns the `datetime` pair.
      ["Started", rows[3]?.[1] ?? ""],
      ["Ended", rows[4]?.[1] ?? ""],
      ["reads", "3"],
      ["commands", "2"],
      // Singular at one, from the shared noun table rather than an `s` appended here.
      ["edit", "1"],
      ["Dispatched", "1"],
      ["Time", "9.0s"],
    ]);
    // The delegate's changed file still gets its row, inside Work.
    expect(root.querySelectorAll(".subagent-footer .turn-file-row")).toHaveLength(1);
  });

  it("withholds Cost, Model and Diagnostics, which the wire cannot carry per delegate", () => {
    // Asserted on an UNCLEAN delegate, which is the sharp case: `aborted` opens the
    // Diagnostics gate (the section is withheld only on a clean or running turn) and
    // the section is STILL absent, because no stop reason or truncation flag exists
    // for a delegate to put in it. So the withholding rests on the absent fields
    // rather than on the outcome, which is what makes it permanent.
    const sa = buildSubagentCard("context-gatherer", "aborted");
    sa.setSummary({ ...DELEGATE, outcome: "cancelled" });
    expect(panelSections(sa.root)).toEqual(["Timings", "Work", "Delegates"]);
    expect(sa.root.querySelector(".subagent-footer .turn-info-panel")).not.toBeNull();
  });

  it("opens the panel from the delegate card's own trigger", () => {
    // The trigger is never disabled now, so a delegate whose footer exists at all can
    // always be opened — including one whose only content is its tool time.
    const root = settled();
    const footer = root.querySelector<HTMLElement>(".subagent-footer");
    const trigger = root.querySelector<HTMLButtonElement>(".turn-ledger-summary");
    expect(trigger?.disabled).toBe(false);
    expect(trigger?.getAttribute("aria-expanded")).toBe("false");
    trigger?.click();
    expect(footer?.dataset["info"]).toBe("open");
    expect(trigger?.getAttribute("aria-expanded")).toBe("true");
  });
});

// ---------------------------------------------------------------------------
// The lazy rendering of a closed CONTAINER, measured rather than read off the
// source.
//
// `content-visibility` cannot be checked with a `toMatch` on the rule body: what
// matters is which of two rules WINS on the element in each state, and that is a
// cascade question only a layout engine answers. The browser project is a real
// headless Chromium, so the shipped sheet is injected and the computed value read
// — the pattern reasoning-live-cue.test.ts uses for the count's alignment.
//
// `01-tokens.css` rides along because the body's transition reads duration and
// easing tokens; without it the transition shorthand is invalid and the whole
// rule could be dropped.
// ---------------------------------------------------------------------------
describe("lazy rendering of a closed container, computed", () => {
  let style: HTMLStyleElement;
  let host: HTMLElement;

  beforeEach(() => {
    style = document.createElement("style");
    style.textContent = [loadCSS("01-tokens.css"), loadCSS("14-tools.css")].join("\n");
    document.head.appendChild(style);
    host = document.createElement("div");
    document.body.appendChild(host);
  });

  afterEach(() => {
    style.remove();
    host.remove();
  });

  /** A mounted pipeline container plus its body, in the styled host, born CLOSED —
   *  which is what composition passes for a box the store already holds something
   *  after, and the state every case below is about.
   *
   *  Born closed rather than folded by `setSuperseded`, deliberately: a fold ANIMATES,
   *  and `content-visibility` is a discrete property under `allow-discrete`, so it
   *  still reads its from-value for the length of the transition (the last two cases
   *  are about exactly that). `createDisclosure` does not animate at construction, so
   *  this is the resting closed state with no transition in the question.
   *
   *  Populated, because the disclosure these cases read is withdrawn while the body is
   *  empty. */
  async function container(): Promise<{ root: HTMLElement; body: HTMLElement }> {
    const box = buildSubagentContainer("pipeline", "completed", { startOpen: false });
    host.appendChild(box.root);
    await populate(box);
    return { root: box.root, body: box.body };
  }

  it("takes a closed container's body out of layout entirely", async () => {
    // The win. `height: 0` + `overflow: hidden` clips paint but leaves every
    // descendant in flow, so twenty collapsed pipelines were still laid out on
    // every reflow — and a reflow happens per streamed delta.
    const { root, body } = await container();
    expect(root.classList.contains("collapsed")).toBe(true);
    expect(getComputedStyle(body).contentVisibility).toBe("hidden");
  });

  it("renders it again the moment the reader opens the box", async () => {
    const { root, body } = await container();
    // Transitions off for this element first, and the reason is the subject of the
    // last two cases: `content-visibility` is DISCRETE, so with `allow-discrete`
    // the value is still the from-value while the transition runs and a read in the
    // click's own tick reports `hidden` however the cascade resolved. What this
    // case is about is the CASCADE — which of the two rules wins once
    // `.collapsed` is gone — so the animation is taken out of the question here and
    // asserted on its own below.
    body.style.transition = "none";
    headerOf(root).click();
    expect(root.classList.contains("collapsed")).toBe(false);
    expect(getComputedStyle(body).contentVisibility).not.toBe("hidden");
  });

  it("is keyed on the ROOT's collapsed class, not the body's aria-hidden", async () => {
    // The load-bearing half, and the one a source read cannot express.
    // `createDisclosure`'s `set` writes aria-hidden (reflectAria) BEFORE it starts
    // the height animation (applyHeight), and a collapse begins by reading
    // `region.scrollHeight` for a concrete start height. An aria-keyed rule would
    // already be in effect for that read, making it 0, so the box would snap shut
    // instead of animating. Asserted by putting the element in the state that
    // separates the two rules: aria-hidden set, `.collapsed` absent.
    const { root, body } = await container();
    body.style.transition = "none";
    headerOf(root).click();
    expect(root.classList.contains("collapsed")).toBe(false);
    body.setAttribute("aria-hidden", "true");
    expect(getComputedStyle(body).contentVisibility).not.toBe("hidden");
  });

  it("defers the flip to the end of the collapse, so content animates away first", async () => {
    // `content-visibility` is a discrete property: without `allow-discrete` the
    // flip is immediate and the box animates shut already empty. Read off the
    // computed transition rather than the source for the same reason as above —
    // the shorthand has to survive the cascade and token resolution.
    const { body } = await container();
    const t = getComputedStyle(body).transition;
    expect(t).toContain("content-visibility");
    expect(t).toContain("allow-discrete");
    // The height transition is still there beside it; the point is both, not one.
    expect(t).toContain("height");
  });

  it("needs no intrinsic-size estimate, because the closed height is already 0", async () => {
    // What `content-visibility: auto` on an unbounded container would have forced
    // us to guess. The controller pins the closed height inline, so a skipped
    // subtree has nothing to estimate.
    const { body } = await container();
    expect(body.style.height).toBe("0px");
    expect(getComputedStyle(body).containIntrinsicSize).toBe("none");
  });

  it("advertises the header as a control only while there is one", async () => {
    // The three `.subagent-header` affordance declarations were UNGATED, so a
    // container whose control had been withdrawn still answered the pointer like
    // one — and so did a card with no opener, which controls nothing at all.
    const bareBox = buildSubagentContainer("pipeline", "completed");
    host.appendChild(bareBox.root);
    const card = buildSubagentCard("Subagent", "completed");
    host.appendChild(card.root);
    const door = buildSubagentCard("Subagent", "completed", { open: opener() });
    host.appendChild(door.root);
    const { root: full } = await container();
    expect(getComputedStyle(headerOf(bareBox.root)).cursor).toBe("auto");
    expect(getComputedStyle(headerOf(card.root)).cursor).toBe("auto");
    expect(getComputedStyle(headerOf(door.root)).cursor).toBe("pointer");
    expect(getComputedStyle(headerOf(full)).cursor).toBe("pointer");
  });

  it("keeps an INERT header's own text selectable, and a control's not", async () => {
    // The other half of the same gate: `user-select: none` exists so a drag across a
    // live header activates instead of selecting its label, and a header that does
    // nothing has no reason to take the selection away.
    //
    // The accepted loss: a card whose head IS the control matches the tool card, so
    // the delegate's NAME can no longer be dragged out of it. The tail's lines, which
    // carry the delegate's own output, sit outside the control and stay selectable.
    const bareBox = buildSubagentContainer("pipeline", "completed");
    host.appendChild(bareBox.root);
    const door = buildSubagentCard("Subagent", "completed", { open: opener() });
    host.appendChild(door.root);
    const { root: full } = await container();
    expect(getComputedStyle(headerOf(bareBox.root)).userSelect).toBe("auto");
    expect(getComputedStyle(headerOf(door.root)).userSelect).toBe("none");
    expect(getComputedStyle(headerOf(full)).userSelect).toBe("none");
  });
});

// ---------------------------------------------------------------------------
// The leaf's chevron points RIGHTWARD, permanently.
//
// The closed angle lives in 10-shell-app.css and the container's open rule in
// 14-tools.css, so only the ASSEMBLED bundle answers the cascade question — hence
// `mountAppCSS()` rather than this file's two-sheet host.
// ---------------------------------------------------------------------------
describe("the leaf's navigation chevron never turns", () => {
  let style: HTMLStyleElement;
  let host: HTMLElement;

  beforeEach(() => {
    style = mountAppCSS();
    host = document.createElement("div");
    document.body.appendChild(host);
  });

  afterEach(() => {
    style.remove();
    host.remove();
  });

  it("resolves the closed angle and keeps it through every settle", () => {
    const sa = buildSubagentCard("Subagent", "in_progress", { open: opener() });
    host.appendChild(sa.root);
    const chev = sa.root.querySelector<HTMLElement>(".subagent-head-chevron > .disclosure-chevron");
    if (chev === null) {
      throw new Error("the card's head has no chevron");
    }
    const turn = (): string => getComputedStyle(chev).getPropertyValue("--chev-turn").trim();
    expect(turn()).toBe("-90deg");
    sa.setStatus("completed");
    expect(turn()).toBe("-90deg");
    sa.setStatus("failed");
    expect(turn()).toBe("-90deg");
  });
});
