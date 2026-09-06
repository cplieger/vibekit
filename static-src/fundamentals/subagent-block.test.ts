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

import { buildSubagentBlock } from "./subagent-block.js";
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
  it("is collapsed by default, ALWAYS — running and settled", () => {
    // The old policy (open while running, auto-close on settle) was exactly
    // backwards: it spent the expanded state on the moment N delegates stream
    // at once, and folded the box right when its result became worth reading.
    const sa = buildSubagentBlock("Subagent", "in_progress");
    expect(sa.root.classList.contains("collapsed")).toBe(true);
    sa.setStatus("completed");
    expect(sa.root.classList.contains("collapsed")).toBe(true);
  });

  it("stays open once the user opens it, across a settle", () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    const header = sa.root.querySelector<HTMLElement>(".subagent-header");
    header?.click();
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
  it("pops open when the delegate fails", () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    expect(sa.root.classList.contains("collapsed")).toBe(true);
    sa.setStatus("failed");
    expect(sa.root.classList.contains("collapsed")).toBe(false);
  });

  it("mounts open when built already failed", () => {
    const sa = buildSubagentBlock("Subagent", "failed");
    expect(sa.root.classList.contains("collapsed")).toBe(false);
  });

  it("respects a reader who closed it: a later failure stays closed", () => {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    const header = sa.root.querySelector<HTMLElement>(".subagent-header");
    header?.click(); // open
    header?.click(); // close — the reader has taken control
    sa.setStatus("failed");
    expect(sa.root.classList.contains("collapsed")).toBe(true);
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
    // The output has to land BEFORE the failed frame: `applyStatusUpdate` reads
    // `.tool-output` to decide, so one frame carrying both builds no button.
    const out = "build failed\nexit status 2";
    updateToolCall(card, { ...tc, status: "in_progress", output: out }, "c-tail");
    updateToolCall(card, { ...tc, status: "failed" }, "c-tail");
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

  /** A mounted card plus its body, in the styled host. */
  function card(): { root: HTMLElement; body: HTMLElement } {
    const sa = buildSubagentBlock("Subagent", "in_progress");
    host.appendChild(sa.root);
    const body = sa.root.querySelector<HTMLElement>(".subagent-body");
    expect(body).not.toBeNull();
    return { root: sa.root, body: body as HTMLElement };
  }

  it("takes a closed card's body out of layout entirely", () => {
    // The win. `height: 0` + `overflow: hidden` clips paint but leaves every
    // descendant in flow, so twenty collapsed delegates were still laid out on
    // every reflow — and a reflow happens per streamed delta.
    const { root, body } = card();
    expect(root.classList.contains("collapsed")).toBe(true);
    expect(getComputedStyle(body).contentVisibility).toBe("hidden");
  });

  it("renders it again the moment the reader opens the card", () => {
    const { root, body } = card();
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

  it("is keyed on the ROOT's collapsed class, not the body's aria-hidden", () => {
    // The load-bearing half, and the one a source read cannot express.
    // `createDisclosure`'s `set` writes aria-hidden (reflectAria) BEFORE it starts
    // the height animation (applyHeight), and a collapse begins by reading
    // `region.scrollHeight` for a concrete start height. An aria-keyed rule would
    // already be in effect for that read, making it 0, so the card would snap shut
    // instead of animating. Asserted by putting the element in the state that
    // separates the two rules: aria-hidden set, `.collapsed` absent.
    const { root, body } = card();
    body.style.transition = "none";
    root.querySelector<HTMLElement>(".subagent-header")?.click();
    expect(root.classList.contains("collapsed")).toBe(false);
    body.setAttribute("aria-hidden", "true");
    expect(getComputedStyle(body).contentVisibility).not.toBe("hidden");
  });

  it("defers the flip to the end of the collapse, so content animates away first", () => {
    // `content-visibility` is a discrete property: without `allow-discrete` the
    // flip is immediate and the box animates shut already empty. Read off the
    // computed transition rather than the source for the same reason as above —
    // the shorthand has to survive the cascade and token resolution.
    const { body } = card();
    const t = getComputedStyle(body).transition;
    expect(t).toContain("content-visibility");
    expect(t).toContain("allow-discrete");
    // The height transition is still there beside it; the point is both, not one.
    expect(t).toContain("height");
  });

  it("needs no intrinsic-size estimate, because the closed height is already 0", () => {
    // What `content-visibility: auto` on an unbounded container would have forced
    // us to guess. The controller pins the closed height inline, so a skipped
    // subtree has nothing to estimate.
    const { body } = card();
    expect(body.style.height).toBe("0px");
    expect(getComputedStyle(body).containIntrinsicSize).toBe("none");
  });
});
