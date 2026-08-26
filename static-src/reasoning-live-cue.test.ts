// ---------------------------------------------------------------------------
// A live thinking trace carries NO pulsing dot, and this suite is what keeps it
// that way.
//
// The dot was one CSS rule (`.msg-reasoning.streaming > .reasoning-summary::before`,
// a breathing accent disc) plus the `streaming` class that switched it on. Both
// are gone: a streaming trace is already legible as live from its open
// disclosure, its growing body, its "Thinking…" label and the turn header's own
// running dot, so a fourth animated marker inside the same card was motion
// carrying no information.
//
// Two halves, because the removal has two halves. The DOM half runs the real
// builder; the SOURCE half reads the shipped stylesheet, since the test page
// links no app stylesheet (see __test-helpers__/css-rules.ts).
//
// The word count on the summary row is covered here too, as the one cue that IS
// wanted in that row: it is a sibling of the label rather than part of it, so
// every assertion above on the label's exact text has to keep holding.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect } from "vitest";
import { loadCSS } from "./__test-helpers__/css-rules.js";

vi.mock("./scroll.js", () => ({
  setUserScrolledUp: vi.fn(),
  preserveReadingPosition: (fn: () => void) => {
    fn();
  },
}));

import { buildReasoning } from "./fundamentals/reasoning.js";

function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//gu, "");
}

describe("a live reasoning block's cues", () => {
  it("mounts open and labelled, and sets no streaming class", () => {
    const r = buildReasoning("weighing the options", true);
    // The two cues that replaced the dot: the trace is visible, and it says so.
    expect(r.root.open).toBe(true);
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Thinking…");
    // The count rides BESIDE the label, never inside it: the label is the state
    // and stays exactly the state (see "the summary's word count" below).
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("3 words");
    // A state class with no CSS or TS reader is a false signal, so there is none.
    expect(r.root.classList.contains("streaming")).toBe(false);
  });

  it("keeps it off after sealing too", () => {
    const r = buildReasoning("weighing the options", true);
    r.seal();
    expect(r.root.classList.contains("streaming")).toBe(false);
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Thinking completed");
  });
});

describe("the summary's word count", () => {
  function countOf(r: { root: HTMLElement }): string | undefined {
    return r.root.querySelector(".reasoning-count")?.textContent ?? undefined;
  }

  it("grows with the trace, and is a sibling of the label rather than part of it", () => {
    const r = buildReasoning("", true);
    // Zero words renders NOTHING. "0 words" on a trace that has not started is
    // a measurement of nothing dressed up as information.
    expect(countOf(r)).toBe("");
    r.append("weighing");
    expect(countOf(r)).toBe("1 word");
    // This boundary falls INSIDE a word, and the next chunk finishes it. Adding
    // each delta's own count would reach 1 + 2 + 2 = 5 by the end; recounting
    // the whole accumulated string reaches the 4 that are actually there.
    r.append(" the opt");
    expect(countOf(r)).toBe("3 words");
    r.append("ions carefully");
    expect(countOf(r)).toBe("4 words");
    // Sealing rewrites the label and leaves the number alone.
    r.seal();
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Thinking completed");
    expect(countOf(r)).toBe("4 words");
  });

  it("recounts on the replace-to-full path, which is the one streaming uses", () => {
    // messages-blocks.ts drives a live block through setText from a signal, not
    // through append, so the count has to track that path too.
    const r = buildReasoning("one", true);
    expect(countOf(r)).toBe("1 word");
    r.setText("one two three");
    expect(countOf(r)).toBe("3 words");
    // setText early-returns on a value no longer than what is already rendered,
    // so the count holds instead of following the shorter string down.
    r.setText("one two");
    expect(countOf(r)).toBe("3 words");
  });

  it("counts a replayed trace at mount and groups the thousands", () => {
    const words = Array.from({ length: 1204 }, (_, i) => `w${String(i)}`).join(" ");
    const r = buildReasoning(words, false);
    // Computed rather than a "1,204" literal: the separator belongs to the
    // platform formatter, and what this pins is that one is used at all.
    expect(countOf(r)).toBe(`${(1204).toLocaleString()} words`);
  });

  it("renders nothing for a trace that is only whitespace", () => {
    // Reachable on replay: the mount guard only skips an EMPTY settled block.
    expect(countOf(buildReasoning("  \n  ", false))).toBe("");
  });
});

describe("13-messages.css styles no pulse onto a thinking trace", () => {
  const css = stripComments(loadCSS("13-messages.css"));

  it("has no rule selecting .msg-reasoning at all", () => {
    // The class survives as IDENTITY (it is what separates a real thinking trace
    // from the compaction-summary disclosure, which shares `.reasoning-block`),
    // so its one CSS consumer being the pulse means zero consumers now.
    expect(css).not.toMatch(/\.msg-reasoning/u);
  });

  it("grows no pseudo-element disc on the summary row", () => {
    expect(css).not.toMatch(/\.reasoning-summary::(?:before|after)/u);
  });

  it("declares no animation on any reasoning selector", () => {
    // Guards the re-add by another door: a breath moved onto `.reasoning-summary`
    // or `.reasoning-block` would pass the two checks above.
    const offenders: string[] = [];
    for (const m of css.matchAll(/([^{}]*reasoning[^{}]*)\{([^{}]*)\}/gu)) {
      const [, selector = "", body = ""] = m;
      if (/animation(?:-name)?\s*:/u.test(body)) {
        offenders.push(selector.trim());
      }
    }
    expect(offenders).toEqual([]);
  });
});

describe("the shared breathe keyframe survives the removal", () => {
  it("still exists in 03-base.css for its other consumers", () => {
    // Ten other rules animate with it (the tab dot, the run pip, the streaming
    // code fence, …). Only the reasoning rule's `animation` line was deleted.
    expect(loadCSS("03-base.css")).toContain("@keyframes vk-breathe");
  });
});
