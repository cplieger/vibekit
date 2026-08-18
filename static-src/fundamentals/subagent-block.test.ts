// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for fundamentals/subagent-block.ts — the collapsible subagent host.
// Focus: the header identity glyph. While active the slot shows the spinner;
// once settled it shows the SVG icon — the shared agent hexagon by default,
// or the per-known-subagent glyph installed via setIcon (roles.ts
// iconForSubagent keys it off the invoke_sub_agent input name).
// ---------------------------------------------------------------------------

import { vi, describe, it, expect } from "vitest";

// Partial mock: the card now imports turn-footer (whose navigate → tabs chain
// reads the whole icon table), so only the identity glyph is overridden.
vi.mock("../icons.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, string>>()),
  ICON_TAB_AGENT: '<svg data-icon="agent-hexagon"></svg>',
}));

import { buildSubagentBlock } from "./subagent-block.js";

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

    // A later status change keeps the installed glyph.
    sa.setStatus("failed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();
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
  // ONE line of glued words (`✓ Grep Search spaghetti ✓ File Search …`), which the
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
      const icon = document.createElement("span");
      icon.textContent = "\u2713";
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
    expect(lines).toEqual([
      "\u2713 Grep Search .",
      "I've counted 47 Go modules.",
      "\u2713 Send Message report",
    ]);
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
