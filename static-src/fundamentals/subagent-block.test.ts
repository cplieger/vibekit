// ---------------------------------------------------------------------------
// Tests for fundamentals/subagent-block.ts — the collapsible subagent host.
// Focus: the header identity glyph. While active the slot shows the spinner;
// once settled it shows the SVG icon — the shared agent hexagon by default,
// or the per-known-subagent glyph installed via setIcon (roles.ts
// iconForSubagent keys it off the invoke_sub_agent input name).
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { loadCSS } from "../__test-helpers__/css-rules.js";

// Partial mock: the card now imports turn-footer (whose navigate → tabs chain
// reads the whole icon table), so only the identity glyph is overridden.
vi.mock("../icons.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, string>>()),
  ICON_TAB_AGENT: '<svg data-icon="agent-hexagon"></svg>',
}));

import { buildSubagentBlock, type SubagentView } from "./subagent-block.js";
import type { ToolStatus } from "../types.js";
import { outcomeIcon } from "../icons.js";
import { iconEl } from "../icon-el.js";
import { CHUNK_ENTER_ATTR } from "../smd-renderer.js";

// The real chrome producers the tail cases below build from reach `scroll.ts`,
// which resolves the transcript scroller at module load and throws on a missing id.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}
const { buildReasoning } = await import("./reasoning.js");
const { buildToolGroupShell, groupBody } = await import("../tool-group.js");
const { buildToolCard } = await import("../tool-card.js");
const { updateToolCall } = await import("../messages-tools.js");

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

/** Put something in the card's body, which is what a real box has. Every
 *  collapse-policy fixture needs it: a box whose body is empty WITHDRAWS its whole
 *  control, so a header click on one toggles nothing. */
async function populate(view: { body: HTMLElement }): Promise<void> {
  view.body.appendChild(document.createElement("div")).textContent = "a line of work";
  await nextTask();
}

describe("buildSubagentBlock icon", () => {
  it("shows the spinner while active, the default hexagon once settled", () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(iconSlot(sa.root).querySelector("svg")).toBeNull();

    sa.setStatus("completed");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(false);
    expect(iconSlot(sa.root).querySelector('svg[data-icon="agent-hexagon"]')).not.toBeNull();
  });

  it("setIcon swaps the settled glyph (distinct icon per known subagent)", () => {
    const sa = buildSubagentBlock("Introspect", "completed");
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
    const sa = buildSubagentBlock("Introspect", "in_progress");
    sa.setIcon('<svg data-icon="introspect"></svg>');
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(iconSlot(sa.root).querySelector("svg")).toBeNull();

    sa.setStatus("completed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();
  });
});

describe("the delegated-work card's collapse policy", () => {
  it("is collapsed by default, ALWAYS — running and settled", async () => {
    // The old policy (open while running, auto-close on settle) was exactly
    // backwards: it spent the expanded state on the moment N delegates stream
    // at once, and folded the box right when its result became worth reading.
    const sa = buildSubagentBlock("Subagent", "in_progress");
    await populate(sa);
    expect(sa.root.classList.contains("collapsed")).toBe(true);
    sa.setStatus("completed");
    expect(sa.root.classList.contains("collapsed")).toBe(true);
  });

  it("stays open once the user opens it, across a settle", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    await populate(sa);
    headerOf(sa.root).click();
    expect(sa.root.classList.contains("collapsed")).toBe(false);
    // Settling must not fold the box the user opened — there is no auto-toggle
    // in either direction any more.
    sa.setStatus("completed");
    expect(sa.root.classList.contains("collapsed")).toBe(false);
  });

  it("marks the root running for the tail's visibility gate", () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    expect(sa.root.classList.contains("running")).toBe(true);
    sa.setStatus("completed");
    expect(sa.root.classList.contains("running")).toBe(false);
  });

  // FAILURE IS NOT NOISE: the header can only say THAT it failed, and the
  // reason is the reader's next question — same rule as the tool group.
  it("pops open when the delegate fails", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    await populate(sa);
    expect(sa.root.classList.contains("collapsed")).toBe(true);
    sa.setStatus("failed");
    expect(sa.root.classList.contains("collapsed")).toBe(false);
  });

  it("mounts open when built already failed", async () => {
    const sa = buildSubagentBlock("Subagent", "failed");
    await populate(sa);
    expect(sa.root.classList.contains("collapsed")).toBe(false);
  });

  it("respects a reader who closed it: a later failure stays closed", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    await populate(sa);
    headerOf(sa.root).click(); // open
    headerOf(sa.root).click(); // close — the reader has taken control
    sa.setStatus("failed");
    expect(sa.root.classList.contains("collapsed")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// A card whose body holds nothing loses its disclosure.
//
// Two shapes reach it, both probed. A LEAF whose only block is its own
// invocation: composition builds the box before it routes the block, and the
// invocation is consumed as the header rather than appended. A CONTAINER for a
// driver that SETTLED having dispatched no stage — deliberately kept, because
// that box is what makes a failed dispatch visible.
//
// The header stays on screen either way: it IS the card's visible content, so
// hiding it or detaching it would delete the box. What goes is the CONTROL —
// the primitive's region-only mode, the third use of it here after
// `tool-group.ts`.
//
// The card is BUILT with its control and withdraws on a construction microtask,
// so every case here awaits one: the pass that builds a box fills it in the same
// task or never will, and the microtask beats the paint. Defaulting the other way
// would pop the chevron in on every box in a transcript.
// ---------------------------------------------------------------------------

describe("a card with nothing in its body", () => {
  /** A built card whose withdrawal has landed. */
  async function bare(status: ToolStatus = "completed"): Promise<SubagentView> {
    const sa = buildSubagentBlock("Subagent", status);
    await nextTask();
    return sa;
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
    const sa = await bare();
    headerOf(sa.root).click();
    expect(sa.body.getAttribute("aria-hidden")).toBe("true");
  });

  it("keeps the header on screen, named", async () => {
    // The box IS the header when the body is empty, and that box is what makes a
    // failed dispatch visible. `role="group"` rather than no role: `applyIcon`
    // writes the outcome into `aria-label`, which a roleless div does not carry.
    const sa = buildSubagentBlock("gatherer", "failed");
    await nextTask();
    expect(headerOf(sa.root).getAttribute("role")).toBe("group");
    expect(headerOf(sa.root).getAttribute("aria-label")).toBe("gatherer, failed");
  });

  it("is not auto-opened by a failure", async () => {
    // The refusal `expandToolDetails` makes on a bare tool card, for the same
    // reason: there is no chevron to close the region again, so an open one is
    // stranded — and worse than a control over nothing, it is an OPEN region
    // containing nothing.
    const sa = await bare("failed");
    expect(sa.body.getAttribute("aria-hidden")).toBe("true");
    expect(sa.root.classList.contains("collapsed")).toBe(true);
  });

  it("holds that failure's open until the body has something to show", async () => {
    const sa = await bare("failed");
    await populate(sa);
    expect(sa.body.getAttribute("aria-hidden")).toBe("false");
  });

  it("still holds it when the failure arrives on a later frame", async () => {
    const sa = await bare("in_progress");
    sa.setStatus("failed");
    await populate(sa);
    expect(sa.body.getAttribute("aria-hidden")).toBe("false");
  });

  it("does not read a click on the withdrawn header as the reader taking over", async () => {
    // A header with no trigger toggles nothing, so counting that click as a user
    // toggle would suppress the auto-open the reader was reaching for.
    const sa = await bare("failed");
    headerOf(sa.root).click();
    await populate(sa);
    expect(sa.body.getAttribute("aria-hidden")).toBe("false");
  });

  it("gains a working disclosure once the body has a child", async () => {
    const sa = await bare();
    expect(sa.root.querySelector(".subagent-toggle")).toBeNull();
    await populate(sa);

    const header = headerOf(sa.root);
    expect(header.getAttribute("role")).toBe("button");
    expect(header.getAttribute("tabindex")).toBe("0");
    expect(header.getAttribute("aria-controls")).toBe(sa.body.id);
    expect(sa.root.querySelector(".subagent-toggle")).not.toBeNull();
    expect(sa.root.classList.contains("has-disclosure")).toBe(true);

    expect(header.getAttribute("aria-expanded")).toBe("false");
    header.click();
    expect(header.getAttribute("aria-expanded")).toBe("true");
  });

  it("applies the same withdrawal to a zero-stage pipeline container", async () => {
    // The container variant, kept on purpose at a count of zero: nothing stands in
    // for a driver that dispatched nothing, so the box must stay visible while its
    // control goes.
    const box = buildSubagentBlock("Subagent pipeline \u00b7 0 stages", "completed", {
      activity: "container",
    });
    await nextTask();
    expect(box.root.querySelector("[aria-expanded]")).toBeNull();
    expect(box.root.querySelector(".subagent-toggle")).toBeNull();
    expect(box.root.querySelector(".subagent-name")?.textContent).toBe(
      "Subagent pipeline \u00b7 0 stages",
    );
  });

  it("keeps its control when the pass that built it DID fill the body", async () => {
    // The reason the default is the control rather than the withdrawal: this is the
    // ordinary box, and it must never flicker a chevron away and back.
    const sa = buildSubagentBlock("Subagent", "completed");
    sa.body.appendChild(document.createElement("div")).textContent = "delegate words";
    await nextTask();
    expect(sa.root.querySelector(".subagent-toggle")).not.toBeNull();
    expect(headerOf(sa.root).getAttribute("aria-expanded")).toBe("false");
  });
});

describe("the tail", () => {
  it("exists while running and is REMOVED on settle — the footer takes over", () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    expect(sa.root.querySelector(".subagent-tail")).not.toBeNull();
    sa.setStatus("completed");
    expect(sa.root.querySelector(".subagent-tail")).toBeNull();
  });

  it("mirrors the body's trailing lines, capped", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);
    sa.body.appendChild(document.createTextNode("one\ntwo\nthree\nfour\nfive"));
    // The observer coalesces via rAF.
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));
    const lines = [...sa.root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);
    expect(lines).toEqual(["three", "four", "five"]);
    sa.root.remove();
  });

  // A LINE IS A BLOCK. This is the shape the block dispatcher actually appends —
  // elements, whose text carries no newline characters — and it is the shape the
  // test above cannot produce. Reading `body.textContent.split("\n")` here yields
  // ONE line of glued words (`Grep Search spaghetti File Search …`), which the
  // nowrap + ellipsis then clips at the card width: the beginning of the whole
  // run instead of its last three lines.
  it("takes one line per BLOCK, not per newline in concatenated text", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);

    const bubble = (t: string): HTMLElement => {
      const d = document.createElement("div");
      d.className = "message assistant";
      const p = document.createElement("p");
      p.textContent = t;
      d.appendChild(p);
      return d;
    };
    const card = (title: string, sub: string): HTMLElement => {
      const d = document.createElement("div");
      d.className = "tool-call";
      // A real settled card's mark is an SVG, so the icon slot contributes NO
      // text to the tail — which is why the expected lines below carry no glyph.
      const icon = document.createElement("span");
      icon.className = "tool-icon";
      icon.appendChild(iconEl(outcomeIcon("ok")));
      const name = document.createElement("span");
      name.textContent = title;
      const subtitle = document.createElement("div");
      subtitle.textContent = sub;
      d.append(icon, name, subtitle);
      return d;
    };

    sa.body.append(
      card("Grep Search", "spaghetti"),
      bubble("The workspace is a multi-repo tree."),
      card("Grep Search", "."),
      bubble("I've counted 47 Go modules."),
      card("Send Message", "report"),
    );
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));

    const lines = [...sa.root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);
    expect(lines).toEqual(["Grep Search .", "I've counted 47 Go modules.", "Send Message report"]);
    sa.root.remove();
  });

  // A block carrying real newlines (a <pre> of command output) still splits, so
  // the last lines of a long output are the tail rather than its first line.
  it("splits a block that does carry newlines, and takes its LAST lines", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);
    const pre = document.createElement("pre");
    pre.textContent = "line one\nline two\nline three\nline four";
    sa.body.appendChild(pre);
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));
    const lines = [...sa.root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);
    expect(lines).toEqual(["line two", "line three", "line four"]);
    sa.root.remove();
  });

  // THE STREAMING SHAPE, which no case above can produce: `smd-renderer.ts` wraps
  // every text emission in a `<span data-vk-chunk-enter>`, so one sentence is a
  // run of sibling spans whose boundaries fall wherever a frame's chunk ended.
  // The element-boundary space this walk adds then lands inside words, and it
  // moves every frame — the reported "random gaps" that fix themselves on a tab
  // switch, because the replay path renders `animateText: false` and produces one
  // text node per block. Built with the real attribute name rather than a literal,
  // so a rename of the marker fails here instead of silently un-fixing this.
  it("does not separate the per-chunk spans a streaming delta arrives in", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);

    const p = document.createElement("p");
    for (const chunk of ["I am", " crea", "ting a", " workflow"]) {
      const span = document.createElement("span");
      span.setAttribute(CHUNK_ENTER_ATTR, "");
      span.appendChild(document.createTextNode(chunk));
      p.appendChild(span);
    }
    const bubble = document.createElement("div");
    bubble.className = "message assistant streaming";
    bubble.appendChild(p);
    sa.body.appendChild(bubble);

    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));
    const lines = [...sa.root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);
    expect(lines).toEqual(["I am creating a workflow"]);
    sa.root.remove();
  });

  // Built from the REAL producers rather than hand-rolled markup: a hand-rolled
  // fixture would pass against a filter keyed on the wrong element.
  it("takes a reasoning trace's text and neither its label nor its word count", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);
    sa.body.appendChild(buildReasoning("I need to check the build first.", true).root);
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));
    const lines = [...sa.root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);
    expect(lines).toEqual(["I need to check the build first."]);
    sa.root.remove();
  });

  it("takes a nested delegate's text and none of its card chrome", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);
    const inner = buildSubagentBlock("context-gatherer", "in_progress", {
      open: { href: "/chat/c/subagent/u", open: () => undefined },
    });
    inner.setSummary({ commands: 2, reads: 1, changedFiles: {}, elapsedMs: 4000 });
    inner.body.appendChild(document.createTextNode("scanning the tree"));
    sa.body.appendChild(inner.root);
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));
    // `:scope >` because the nested card has a tail of its own, and this case is
    // about what the OUTER one harvested.
    const lines = [
      ...sa.root.querySelectorAll(":scope > .subagent-tail > .subagent-tail-line"),
    ].map((n) => n.textContent);
    expect(lines).toEqual(["scanning the tree"]);
    sa.root.remove();
  });

  it("takes a tool group's cards and not the header sentence it computed", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);
    const group = buildToolGroupShell();
    const count = group.querySelector(".tool-group-count");
    if (count !== null) {
      count.textContent = "Ran 12 commands · 1 failed";
    }
    groupBody(group).appendChild(document.createTextNode("go build ./..."));
    sa.body.appendChild(group);
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));
    const lines = [...sa.root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);
    expect(lines).toEqual(["go build ./..."]);
    sa.root.remove();
  });

  // The other half of the rule: this tail keeps the delegate's output and its
  // tools' claim lines, minus UI text about the UI. A shell card's claim line
  // carries the command, because the tool-input <pre> holding it is marked chrome.
  it("takes a running shell card's command and none of the JSON that also holds it", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);
    sa.body.appendChild(
      buildToolCard({
        id: "t-cmd",
        title: "Run Command",
        kind: "execute",
        status: "in_progress",
        input: { command: "go build ./..." },
        live: true,
      }),
    );
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));
    const text = [...sa.root.querySelectorAll(".subagent-tail-line")]
      .map((n) => n.textContent)
      .join("\n");
    expect(text).toContain("go build ./...");
    for (const json of ["{", "}", '"command":']) {
      expect(text, `the tool-input JSON stays excluded (${json})`).not.toContain(json);
    }
    sa.root.remove();
  });

  it("takes a failed card's output and not the Explain-this-error button under it", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);
    const card = buildToolCard({
      id: "t-explain",
      title: "Execute Bash",
      kind: "execute",
      status: "in_progress",
      live: true,
    });
    sa.body.appendChild(card);
    const tc = { id: "t-explain", title: "Execute Bash", kind: "execute" as const, ts: 0 };
    // ONE frame carrying the failure and its output, the shape a terminal
    // `tool_call_update` actually has. `applyToolCallUpdate` applies status last so
    // the Explain gate reads a painted region (`messages-tools-status.test.ts`).
    const out = "build failed\nexit status 2";
    updateToolCall(card, { ...tc, status: "failed", output: out }, "c-tail");
    expect(card.querySelector(".tool-explain-btn")).not.toBeNull();
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));
    const lines = [...sa.root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);
    expect(lines.at(-1)).toBe("exit status 2");
    sa.root.remove();
  });

  // The other half of the same rule: an element that IS a boundary keeps its
  // separator, or two blocks glue into one word. Pinned beside the case above so
  // a fix to one cannot be a regression in the other.
  it("still separates two blocks whose text carries no newline between them", async () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    document.body.appendChild(sa.root);
    const para = (t: string): HTMLElement => {
      const el2 = document.createElement("p");
      el2.textContent = t;
      return el2;
    };
    const wrap = document.createElement("div");
    wrap.append(para("first"), para("second"));
    sa.body.appendChild(wrap);
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await new Promise((r) => setTimeout(r, 20));
    const lines = [...sa.root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);
    expect(lines).toEqual(["first second"]);
    sa.root.remove();
  });
});

describe("the footer", () => {
  it("does not exist until the summary has something worth a row", () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    sa.setSummary({ commands: 0, reads: 0, changedFiles: {} });
    expect(sa.root.querySelector(".subagent-footer")).toBeNull();
  });

  it("is turn-footer reused, updated in place, outside the disclosure", () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    sa.setSummary({ commands: 3, reads: 2, changedFiles: {} });
    const footer = sa.root.querySelector<HTMLElement>(".subagent-footer");
    expect(footer).not.toBeNull();
    expect(footer?.classList.contains("turn-footer")).toBe(true);
    // Outside the body: a collapsed card still states its result.
    expect(footer?.closest(".subagent-body")).toBeNull();

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
// The lazy rendering of a closed card, measured rather than read off the source.
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
describe("lazy rendering of a closed card, computed", () => {
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

  /** A mounted card with content in its body, in the styled host. Populated
   *  because the disclosure these cases read is withdrawn while the body is empty. */
  async function card(): Promise<{ root: HTMLElement; body: HTMLElement }> {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    host.appendChild(sa.root);
    await populate(sa);
    return { root: sa.root, body: sa.body };
  }

  it("takes a closed card's body out of layout entirely", async () => {
    // The win. `height: 0` + `overflow: hidden` clips paint but leaves every
    // descendant in flow, so twenty collapsed delegates were still laid out on
    // every reflow — and a reflow happens per streamed delta.
    const { root, body } = await card();
    expect(root.classList.contains("collapsed")).toBe(true);
    expect(getComputedStyle(body).contentVisibility).toBe("hidden");
  });

  it("renders it again the moment the reader opens the card", async () => {
    const { root, body } = await card();
    // Transitions off for this element first, and the reason is the subject of the
    // last two cases: `content-visibility` is DISCRETE, so with `allow-discrete`
    // the value is still the from-value while the transition runs and a read in the
    // click's own tick reports `hidden` however the cascade resolved. What this
    // case is about is the CASCADE — which of the two rules wins once
    // `.collapsed` is gone — so the animation is taken out of the question here and
    // asserted on its own below.
    body.style.transition = "none";
    root.querySelector<HTMLElement>(".subagent-header")?.click();
    expect(root.classList.contains("collapsed")).toBe(false);
    expect(getComputedStyle(body).contentVisibility).not.toBe("hidden");
  });

  it("is keyed on the ROOT's collapsed class, not the body's aria-hidden", async () => {
    // The load-bearing half, and the one a source read cannot express.
    // `createDisclosure`'s `set` writes aria-hidden (reflectAria) BEFORE it starts
    // the height animation (applyHeight), and a collapse begins by reading
    // `region.scrollHeight` for a concrete start height. An aria-keyed rule would
    // already be in effect for that read, making it 0, so the card would snap shut
    // instead of animating. Asserted by putting the element in the state that
    // separates the two rules: aria-hidden set, `.collapsed` absent.
    const { root, body } = await card();
    body.style.transition = "none";
    root.querySelector<HTMLElement>(".subagent-header")?.click();
    expect(root.classList.contains("collapsed")).toBe(false);
    body.setAttribute("aria-hidden", "true");
    expect(getComputedStyle(body).contentVisibility).not.toBe("hidden");
  });

  it("defers the flip to the end of the collapse, so content animates away first", async () => {
    // `content-visibility` is a discrete property: without `allow-discrete` the
    // flip is immediate and the box animates shut already empty. Read off the
    // computed transition rather than the source for the same reason as above —
    // the shorthand has to survive the cascade and token resolution.
    const { body } = await card();
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
    const { body } = await card();
    expect(body.style.height).toBe("0px");
    expect(getComputedStyle(body).containIntrinsicSize).toBe("none");
  });

  it("advertises the header as a control only while there is one", async () => {
    // The three `.subagent-header` affordance declarations were UNGATED, so a box
    // whose control had been withdrawn still answered the pointer like one.
    const bare = buildSubagentBlock("Subagent", "completed");
    host.appendChild(bare.root);
    const { root: full } = await card();
    expect(getComputedStyle(headerOf(bare.root)).cursor).toBe("auto");
    expect(getComputedStyle(headerOf(full)).cursor).toBe("pointer");
  });

  it("keeps that header's own text selectable", async () => {
    // The other half of the same gate: `user-select: none` exists so a drag across
    // a live header toggles instead of selecting its label, and a header that
    // toggles nothing has no reason to take the selection away.
    const bare = buildSubagentBlock("Subagent", "completed");
    host.appendChild(bare.root);
    const { root: full } = await card();
    expect(getComputedStyle(headerOf(bare.root)).userSelect).toBe("auto");
    expect(getComputedStyle(headerOf(full)).userSelect).toBe("none");
  });
});
