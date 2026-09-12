// The turn header dot exists only where the TAB STRIP cannot be reached.
//
// The dot and the tab dot answer the same question — how is this turn going /
// how did it end — and the tab dot answers it better: it has states the outcome
// vocabulary cannot express, it survives a reload, and it is the only surface
// that speaks for a chat nobody is looking at. So above the drawer breakpoint the
// header dot is a second rendering of a fact the strip already carries, and below
// it the strip is off-canvas at `translateX(-100%)` (50-mobile.css) and the
// transcript is all there is.
//
// The gate is therefore two claims, and they need two kinds of test. That the
// hide APPLIES against the real assembled cascade is a computed fact, and a
// source read cannot answer it — the rule is one of eight selecting `.turn-dot`
// and it has to win. That the breakpoint MATCHES the drawer's is a source fact,
// and computed style cannot answer it: nothing couples the two numbers except
// their being equal, since the drawer is a pure media query with no JS behind it
// (`device-view.ts` holds no viewport width and there is no device class), so if
// one moves the dot renders at a width where the strip is visible, or vanishes at
// one where it is not.
//
// The viewport is pinned at 1280x720 (vitest.config.ts) and nothing resizes it,
// so the mobile side is measured in an IFRAME, which carries a viewport of its
// own that `@media (width <= 48rem)` evaluates against.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { severityOf } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";

const BREAKPOINT = "width <= 48rem";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

/** A running turn's header dot, mounted in `doc` under the ancestry the real
 *  card gives it — the selectors are descendant-scoped, so a bare span measures
 *  nothing.
 *
 *  BOTH attributes, because `updateTurnHeader` writes both: hue and the
 *  `display` gate key on `data-severity` since the four outcome surfaces adopted
 *  the shared table, and `data-outcome` carries the words plus the one `unknown`
 *  exception. A header missing either is not the DOM production builds. */
function mountDot(doc: Document, outcome: TurnOutcome): HTMLElement {
  const header = doc.createElement("div");
  header.className = "turn-header";
  header.dataset["outcome"] = outcome;
  header.dataset["severity"] = severityOf(outcome);
  const row = doc.createElement("div");
  row.className = "turn-head-row";
  const dot = doc.createElement("span");
  dot.className = "turn-dot";
  row.appendChild(dot);
  header.appendChild(row);
  doc.body.replaceChildren(header);
  return dot;
}

describe("the turn header dot at desktop width", () => {
  it("does not render, at the real viewport against the real cascade", () => {
    const dot = mountDot(document, "running");
    expect(window.innerWidth).toBeGreaterThan(768);
    expect(getComputedStyle(dot).display).toBe("none");
  });

  it("does not render for any outcome, running included", () => {
    const outcomes: TurnOutcome[] = ["running", "completed", "interrupted", "failed", "unknown"];
    for (const outcome of outcomes) {
      const dot = mountDot(document, outcome);
      expect(getComputedStyle(dot).display, outcome).toBe("none");
    }
  });
});

describe("the turn header dot below the drawer breakpoint", () => {
  let frame: HTMLIFrameElement;
  let doc: Document;

  beforeAll(() => {
    frame = document.createElement("iframe");
    // 700px is inside 48rem (768px at the 16px root size the app never changes),
    // and the height is irrelevant to a width query.
    frame.width = "700";
    frame.height = "400";
    document.body.appendChild(frame);
    const inner = frame.contentDocument;
    if (inner === null) {
      throw new Error("iframe has no contentDocument");
    }
    doc = inner;
    const sheet = doc.createElement("style");
    sheet.textContent = style.textContent;
    doc.head.appendChild(sheet);
  });

  afterAll(() => {
    frame.remove();
  });

  it("renders for a running turn", () => {
    expect(doc.defaultView?.innerWidth).toBeLessThanOrEqual(768);
    const dot = mountDot(doc, "running");
    expect(getComputedStyle(dot).display).not.toBe("none");
  });

  it("still hides on a clean outcome", () => {
    // A specificity claim, and the one the reveal could quietly break:
    // `.turn-header[data-outcome="completed"] .turn-dot` is (0,3,0) against the
    // media rule's (0,1,0), so it wins inside the query too. "It worked" is the
    // expected case and a marker on every row communicates nothing.
    const dot = mountDot(doc, "completed");
    expect(getComputedStyle(dot).display).toBe("none");
  });
});

describe("the gate's breakpoint", () => {
  it("is the same width the sidebar drawer uses", () => {
    // The assertion that stops the two drifting apart. Read as PRELUDES rather
    // than as one shared token, because a media query cannot read a custom
    // property — so the coupling is the number, and two files stating it is the
    // most either side can do.
    const turns = ruleContaining(loadCSS("29-turns.css"), ".turn-dot", BREAKPOINT);
    expect(turns.body).toMatch(/display:\s*block/u);

    const drawer = ruleContaining(loadCSS("50-mobile.css"), '[id="sidebar"]', BREAKPOINT);
    expect(drawer.body).toContain("translateX(-100%)");
  });

  it("hides the dot at top level, so the media block is the whole reveal", () => {
    // The scope argument is mandatory: `.turn-dot` now names a rule in both
    // places, and the helper requires exactly one match.
    const base = ruleContaining(loadCSS("29-turns.css"), ".turn-dot", "top");
    expect(base.body).toMatch(/display:\s*none/u);
  });
});
