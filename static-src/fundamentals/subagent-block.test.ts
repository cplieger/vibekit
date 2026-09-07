// ---------------------------------------------------------------------------
// Tests for fundamentals/subagent-block.ts — the delegated-work boxes.
//
// Four subjects. The header identity glyph: while active the slot shows the
// spinner, once settled the SVG icon — the shared agent hexagon by default, or
// the per-known-subagent glyph installed via setIcon (roles.ts iconForSubagent
// keys it off the invoke_sub_agent input name). The two SHAPES: a card discloses
// nothing and takes its tail from outside, a container discloses its stages. The
// container's WITHDRAWAL: a body with nothing in it loses the control. And what a
// screen reader is told about any of them.
//
// The tail's CONTENT is not tested here any more, because this file no longer
// derives it: `subagent-tail.test.ts` owns the projection and this one owns the
// sink.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { loadCSS } from "../__test-helpers__/css-rules.js";

// Partial mock: the card now imports turn-footer (whose navigate → tabs chain
// reads the whole icon table), so only the identity glyph is overridden.
vi.mock("../icons.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, string>>()),
  ICON_TAB_AGENT: '<svg data-icon="agent-hexagon"></svg>',
}));

import {
  buildSubagentCard,
  buildSubagentContainer,
  type SubagentContainer,
} from "./subagent-block.js";
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
});

// A CARD HAS NO BODY, which is the whole of this change: the transcript renders
// none of a delegate's output, so there is nothing here to disclose and a toggle
// that toggles nothing is worse than no toggle.
describe("a delegate's card is not a disclosure", () => {
  it("has no body, no chevron and no button role", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    expect(sa.root.querySelector(".subagent-body")).toBeNull();
    expect(sa.root.querySelector(".subagent-toggle")).toBeNull();
    const header = sa.root.querySelector<HTMLElement>(".subagent-header");
    expect(header?.getAttribute("role")).toBeNull();
    expect(header?.getAttribute("tabindex")).toBeNull();
    expect(header?.getAttribute("aria-expanded")).toBeNull();
  });

  it("does not fold when its header is activated", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.root.querySelector<HTMLElement>(".subagent-header")?.click();
    expect(sa.root.classList.contains("collapsed")).toBe(false);
    // Nor on a failure, which used to open the box to show the reason. The reason
    // is on the delegate's page now, and the foot's link is the way to it.
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
    expect(header.getAttribute("aria-expanded")).toBe("false");
  });

  it("is collapsed by default and opens on the reader's click", async () => {
    // The old policy (open while running, auto-close on settle) was exactly
    // backwards: it spent the expanded state on the moment N stages start at
    // once, and folded the box right when its result became worth reading.
    const box = buildSubagentContainer("pipeline", "in_progress");
    await populate(box);
    expect(box.root.classList.contains("collapsed")).toBe(true);
    headerOf(box.root).click();
    expect(box.root.classList.contains("collapsed")).toBe(false);
    // Settling must not fold the box the user opened — there is no auto-toggle in
    // either direction.
    box.setStatus("completed");
    expect(box.root.classList.contains("collapsed")).toBe(false);
  });

  it("stays collapsed across a settle when the reader has not opened it", async () => {
    // The other direction of the same rule: a settle opens nothing either.
    const box = buildSubagentContainer("pipeline", "in_progress");
    await populate(box);
    expect(box.root.classList.contains("collapsed")).toBe(true);
    box.setStatus("completed");
    expect(box.root.classList.contains("collapsed")).toBe(true);
  });

  // FAILURE IS NOT NOISE: the header can only say THAT a stage failed, and which
  // one is the reader's next question — same rule as the tool group.
  it("pops open when the pipeline fails", async () => {
    const box = buildSubagentContainer("pipeline", "in_progress");
    await populate(box);
    expect(box.root.classList.contains("collapsed")).toBe(true);
    box.setStatus("failed");
    expect(box.root.classList.contains("collapsed")).toBe(false);
  });

  it("respects a reader who closed it: a later failure stays closed", async () => {
    const box = buildSubagentContainer("pipeline", "in_progress");
    await populate(box);
    const header = headerOf(box.root);
    header.click(); // open
    header.click(); // close — the reader has taken control
    box.setStatus("failed");
    expect(box.root.classList.contains("collapsed")).toBe(true);
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

    expect(header.getAttribute("aria-expanded")).toBe("false");
    header.click();
    expect(header.getAttribute("aria-expanded")).toBe("true");
  });

  it("keeps its control when the pass that built it DID fill the body", async () => {
    // The reason the default is the control rather than the withdrawal: this is the
    // ordinary box, and it must never flicker a chevron away and back.
    const box = buildSubagentContainer("pipeline", "completed");
    box.body.appendChild(document.createElement("div")).textContent = "a stage card";
    await nextTask();
    expect(box.root.querySelector(".subagent-toggle")).not.toBeNull();
    expect(headerOf(box.root).getAttribute("aria-expanded")).toBe("false");
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
  });

  it("names the link for its delegate, and follows a rename", () => {
    const sa = buildSubagentCard("Subagent", "in_progress", {
      open: { href: "/chat/c/subagent/u-1", open: () => undefined },
    });
    const link = sa.root.querySelector<HTMLAnchorElement>("a.subagent-open");
    // The visible word stays "Open"; the accessible name is what disambiguates a
    // list of them.
    expect(link?.textContent).toContain("Open");
    expect(link?.getAttribute("aria-label")).toBe("Open Subagent");
    sa.setName("context-gatherer");
    expect(link?.getAttribute("aria-label")).toBe("Open context-gatherer");
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

  /** A mounted pipeline container plus its body, in the styled host. Populated,
   *  because the disclosure these cases read is withdrawn while the body is empty. */
  async function container(): Promise<{ root: HTMLElement; body: HTMLElement }> {
    const box = buildSubagentContainer("pipeline", "in_progress");
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
    // one — and so did a card, which never had a control at all.
    const bareBox = buildSubagentContainer("pipeline", "completed");
    host.appendChild(bareBox.root);
    const card = buildSubagentCard("Subagent", "completed");
    host.appendChild(card.root);
    const { root: full } = await container();
    expect(getComputedStyle(headerOf(bareBox.root)).cursor).toBe("auto");
    expect(getComputedStyle(headerOf(card.root)).cursor).toBe("auto");
    expect(getComputedStyle(headerOf(full)).cursor).toBe("pointer");
  });

  it("keeps that header's own text selectable", async () => {
    // The other half of the same gate: `user-select: none` exists so a drag across
    // a live header toggles instead of selecting its label, and a header that
    // toggles nothing has no reason to take the selection away.
    const bareBox = buildSubagentContainer("pipeline", "completed");
    host.appendChild(bareBox.root);
    const { root: full } = await container();
    expect(getComputedStyle(headerOf(bareBox.root)).userSelect).toBe("auto");
    expect(getComputedStyle(headerOf(full)).userSelect).toBe("none");
  });
});
