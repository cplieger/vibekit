// The transcript's LAYOUT declarations: what is contained, what is not part of
// the transcript's layout at all, and what the live-edge marker costs.
//
// The containment cases are source facts about the shipped stylesheets, so they
// are read out of the CSS rather than off a computed style — `content-visibility`
// and `contain` resolve to themselves whether or not the property does anything
// observable in one page. The marker's cost is the opposite kind of claim, so it
// is measured against real layout.
//
// The regression each one guards is a measured one. `.msg-row` (13-messages.css)
// has carried this pair for a long time while the four cards below — one open
// turn measured 353 tool cards — carried none, so the containment was declared on
// one row per prose bubble and inert for everything that costs anything. And the
// composer is a flex sibling of the transcript's scroller with
// `field-sizing: content` on its textarea, so without `contain: layout` a
// keystroke shares a layout pass with whatever the transcript is doing.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";

/** The selectors that carry the bulk, and the file each is authored in. */
const BULK: readonly { selector: string; file: string }[] = [
  { selector: ".msg-row", file: "13-messages.css" },
  { selector: ".tool-call", file: "14-tools.css" },
  { selector: ".subagent-block", file: "14-tools.css" },
  { selector: ".plan-message", file: "14-tools.css" },
  { selector: ".run-card", file: "27-run-card.css" },
];

describe("transcript containment", () => {
  for (const { selector, file } of BULK) {
    it(`${selector} skips its subtree off-screen and reserves a size`, () => {
      const body = ruleBody(loadCSS(file), selector);
      expect(body, `${selector} declares content-visibility: auto`).toMatch(
        /content-visibility:\s*auto\s*;/u,
      );
      // `auto` as the first value is what makes the fallback a pre-first-render
      // estimate rather than a permanent lie about the card's height.
      expect(body, `${selector} declares contain-intrinsic-size: auto <size>`).toMatch(
        /contain-intrinsic-size:\s*auto\s+[\d.]+rem\s*;/u,
      );
    });
  }

  it("leaves the collapsed subagent body on `hidden`, keyed on its own state", () => {
    // The block's `auto` is keyed on viewport relevancy; the BODY's is keyed on
    // the disclosure, and only `hidden` applies in one state. Two elements, two
    // keyings — a sweep that "aligned" them would resurrect the case
    // 14-tools.css documents at length.
    const body = ruleBody(loadCSS("14-tools.css"), ".subagent-block.collapsed > .subagent-body");
    expect(body).toMatch(/content-visibility:\s*hidden\s*;/u);
  });
});

describe("composer layout independence", () => {
  const form = (): string => ruleBody(loadCSS("15-input.css"), '[id="prompt-form"]');

  it("declares LAYOUT containment, and only that", () => {
    // Not `paint`, `content` or `strict`: the pill cards open UPWARD out of this
    // bar (`bottom: 100%` against their slot, up to `min(26rem, 55dvh)` tall), and
    // paint containment clips a subtree at the padding box — every one of them
    // would lose its top. Not `size` either: the bar's height IS its content's.
    expect(form()).toMatch(/contain:\s*layout\s*;/u);
    expect(form()).not.toMatch(/contain:[^;]*\b(?:paint|content|strict|size)\b/u);
  });

  it("carries the z-index its own stacking context makes necessary", () => {
    // Layout containment makes the bar a stacking context, which would otherwise
    // paint the pill cards that open out of it below `.chat-toolbar`.
    const body = form();
    expect(body).toMatch(/position:\s*relative\s*;/u);
    expect(body).toMatch(/z-index:\s*10\s*;/u);
    expect(ruleBody(loadCSS("12-chat.css"), ".chat-toolbar")).toMatch(/z-index:\s*10\s*;/u);
  });

  it("does not put the containment on the bottom bar every view shares", () => {
    // `.bottom-bar` is also the editor, files and run toolbars; none of them
    // sits beside a live transcript, and containing them would be a claim about
    // three surfaces this finding never measured.
    expect(ruleBody(loadCSS("19-files.css"), ".bottom-bar")).not.toMatch(/contain:/u);
  });
});

// Real layout, because both of these are claims about pixels rather than about
// declarations.
describe("against real layout", () => {
  let sheet: HTMLStyleElement;
  let stage: HTMLElement;

  beforeAll(() => {
    sheet = mountAppCSS();
    stage = document.createElement("div");
    document.body.appendChild(stage);
  });

  afterAll(() => {
    sheet.remove();
    stage.remove();
  });

  /** A transcript view holding two 100px turns, optionally with the live-edge
   *  marker scroll.ts appends — placed FIRST, which is where reconcile leaves
   *  unkeyed furniture, so the flex order is what has to put it last. */
  function view(withEdge: boolean): HTMLElement {
    const v = document.createElement("div");
    v.className = "transcript-view is-active";
    if (withEdge) {
      const edge = document.createElement("div");
      edge.className = "transcript-edge";
      v.appendChild(edge);
    }
    for (let i = 0; i < 2; i++) {
      const turn = document.createElement("div");
      turn.className = "turn";
      turn.style.blockSize = "100px";
      v.appendChild(turn);
    }
    stage.appendChild(v);
    return v;
  }

  it("the live-edge marker costs the transcript no height", () => {
    // A zero-height flex item still earns the column's `gap`, so without the
    // negative margin the marker would add `--sp-3` under the last turn — and
    // with `justify-content: flex-end` that lifts the whole transcript.
    const without = view(false).getBoundingClientRect().height;
    const withEdge = view(true).getBoundingClientRect().height;
    expect(withEdge).toBe(without);
  });

  it("the live-edge marker lands after the last turn whatever its DOM position", () => {
    const v = view(true);
    const edge = v.querySelector<HTMLElement>(".transcript-edge");
    const turns = v.querySelectorAll<HTMLElement>(".turn");
    const last = turns[turns.length - 1];
    // `order: 1` is the reason: in the DOM this marker is the FIRST child.
    expect(edge?.previousElementSibling).toBeNull();
    expect(edge!.getBoundingClientRect().top).toBeGreaterThanOrEqual(
      last!.getBoundingClientRect().bottom,
    );
  });

  // There is no rect-based case for the composer's containment CLIPPING a pill
  // card, and the absence is deliberate: paint containment clips what is PAINTED
  // and leaves every geometry read unchanged, so such a case passes identically
  // under `contain: paint` (measured). The choice is pinned as a declaration
  // above, where it is falsifiable.
});
