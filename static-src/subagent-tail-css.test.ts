// A DELEGATE CARD RESERVES NOTHING FOR OUTPUT IT HAS NOT RECEIVED, and the box
// it grows into is animated rather than snapped.
//
// The card is built the moment the delegate is dispatched, which is before the
// delegate has said anything, so its rolling tail (`subagent-tail.ts` derives it
// from the store) starts with no lines in it. A padded empty box therefore paints
// a band nothing fills — reported as an empty line inside the box that then GREW
// when the first line landed, because 12px of block padding is not a line of
// 11px mono text. Both halves are one CSS pair in 14-tools.css: the resting state
// is `:empty` at zero size, the shown state is `:not(:empty)` at `auto`.
//
// Measured against the assembled stylesheet on real boxes, because neither half
// is visible in the markup: the empty band is padding on an element that is
// present either way, and the growth is a transition whose absence looks
// identical in a DOM dump.
import { vi, describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

// The delegate boxes compensate their own fold and failure re-open through scroll.ts,
// which is a self-initialising singleton over a real `#messages`; the canonical mock is
// what every other suite reaching that graph uses. Nothing here folds anything — this
// file's subject is the tail's own box — so the mock only has to exist.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

import { buildSubagentCard, type SubagentCard } from "./fundamentals/subagent-block.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { ToolStatus } from "./types.js";

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  // The transcript's own block container, at a transcript width: a card takes
  // both from it, and the tail's line height is inherited.
  stage = document.createElement("div");
  stage.className = "msg-wrap";
  stage.style.inlineSize = "760px";
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

afterEach(() => {
  stage.replaceChildren();
});

/** A card as the transcript builds one, mounted. */
function card(status: ToolStatus): SubagentCard {
  const sa = buildSubagentCard("wf-workflow-creator", status);
  stage.appendChild(sa.root);
  return sa;
}

function tailOf(sa: SubagentCard): HTMLElement {
  const tail = sa.root.querySelector<HTMLElement>(".subagent-tail");
  if (tail === null) {
    throw new Error("the card has no tail element");
  }
  return tail;
}

const height = (el: Element): number => el.getBoundingClientRect().height;

/** Let the resting style be committed. Without it there is no before-change value
 *  for the growth to transition FROM, so the box would jump and every assertion
 *  below would pass for the wrong reason. */
const frame = (): Promise<void> =>
  new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });

describe("a delegate's tail before its first line", () => {
  it("occupies nothing on a card that has just been dispatched", () => {
    const sa = card("in_progress");
    expect(height(tailOf(sa))).toBe(0);
  });

  // `bindSubagentTail` paints as soon as it binds, so an empty projection is the
  // FIRST thing most cards are handed rather than an edge case.
  it("occupies nothing when the projection paints no lines", () => {
    const sa = card("in_progress");
    sa.setTail([]);
    expect(height(tailOf(sa))).toBe(0);
  });

  // The card's own height is the claim a reader actually sees, and an empty tail
  // is the one region between the identity row and the foot — so if it costs the
  // card anything, that cost IS the band. Measured after the frames
  // `content-visibility: auto` needs, because a card renders at its
  // `contain-intrinsic-size` reserve for its first two — 71px here, which is the
  // rule's 69px of CONTENT plus its own 2px border, measured in Chromium 151 — so
  // measured there the box IS the reserve rather than the laid-out row, and both
  // comparisons below fail whatever the tail does.
  it("leaves the card at its identity row, with nothing reserved under it", async () => {
    const sa = card("in_progress");
    await frame();
    await frame();
    const box = height(sa.root);
    const cs = getComputedStyle(sa.root);
    const border =
      Number.parseFloat(cs.borderBlockStartWidth) + Number.parseFloat(cs.borderBlockEndWidth);
    expect(border, "the card is bordered, so the relation below has to carry it").toBeGreaterThan(
      0,
    );
    // `auto <length>`, so the keyword comes off before the number does.
    const reserve = Number.parseFloat(cs.containIntrinsicBlockSize.replace(/^auto\s+/u, ""));
    expect(
      Number.isNaN(reserve),
      `containIntrinsicBlockSize was ${cs.containIntrinsicBlockSize}`,
    ).toBe(false);
    expect(box, "the card is laid out rather than rendering at its reserve").toBeLessThan(reserve);
    const header = sa.root.querySelector(".subagent-header");
    if (header === null) {
      throw new Error("the card has no identity row");
    }
    expect(box, "header plus borders, and nothing else").toBeCloseTo(height(header) + border, 1);
  });
});

describe("a delegate's first line", () => {
  it("grows the box over --dur-enter instead of snapping it open", async () => {
    const sa = card("in_progress");
    const tail = tailOf(sa);
    await frame();
    expect(height(tail), "resting at zero before the line lands").toBe(0);

    sa.setTail(["go build ./..."]);
    // getAnimations flushes style, so the transitions have started by the read
    // below — which is what makes that read the animation's own first value.
    const running = tail.getAnimations();
    expect(running.length, "the growth is transitioned").toBeGreaterThan(0);
    expect(height(tail), "still at the resting height on the frame it lands").toBeLessThan(2);

    await Promise.all(running.map((a) => a.finished));
    const line = tail.firstElementChild;
    if (line === null) {
      throw new Error("the tail painted no line");
    }
    const cs = getComputedStyle(tail);
    const pad = Number.parseFloat(cs.paddingBlockStart) + Number.parseFloat(cs.paddingBlockEnd);
    expect(pad, "the shown state carries block padding").toBeGreaterThan(0);
    expect(height(tail), "settles at its one line plus that padding").toBeCloseTo(
      height(line) + pad,
      1,
    );
  });

  // The anti-thrash property, and the reason a height transition is safe on a box
  // whose text is rewritten per streamed delta: `auto` is the computed value in
  // every shown state, so a rewrite that keeps the line count transitions nothing.
  // 13-messages.css records the defect this forbids — a 200ms height animation
  // retriggered per delta, with deltas landing faster than that.
  it("does not re-run the growth for a delta that rewrites its last line", async () => {
    const sa = card("in_progress");
    const tail = tailOf(sa);
    await frame();
    sa.setTail(["go build ./..."]);
    await Promise.all(tail.getAnimations().map((a) => a.finished));
    const settled = height(tail);

    // Long enough to overflow the card several times over, which is the other half
    // of the same property: the lines are `nowrap`, so length cannot become height.
    sa.setTail([`go build ./... ${"and then some more output ".repeat(12)}`]);
    expect(tail.getAnimations(), "a rewrite starts no transition").toEqual([]);
    expect(height(tail), "and moves the box not at all").toBeCloseTo(settled, 1);
  });

  // Stated as a RELATION rather than a length: what this catches is a resting
  // state that keeps the block padding, which is the defect, whatever the tokens
  // are worth.
  it("is what the block padding arrives with", async () => {
    const sa = card("in_progress");
    const tail = tailOf(sa);
    await frame();
    const resting = getComputedStyle(tail);
    expect(Number.parseFloat(resting.paddingBlockStart)).toBe(0);
    expect(Number.parseFloat(resting.paddingBlockEnd)).toBe(0);
    // Inline padding is constant: the tail's lines align with the header's name
    // from the first frame, so only the block axis is part of the growth.
    expect(Number.parseFloat(resting.paddingInlineStart)).toBeGreaterThan(0);

    sa.setTail(["one"]);
    await Promise.all(tail.getAnimations().map((a) => a.finished));
    const shown = getComputedStyle(tail);
    expect(Number.parseFloat(shown.paddingBlockStart)).toBeGreaterThan(0);
    expect(Number.parseFloat(shown.paddingBlockEnd)).toBeGreaterThan(0);
    expect(shown.paddingInlineStart).toBe(resting.paddingInlineStart);
  });
});
