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

  /** A transcript view holding `n` 100px turns, optionally with the live-edge
   *  marker scroll.ts appends — placed FIRST, which is where reconcile leaves
   *  unkeyed furniture, so the flex order is what has to put it last. Parented by
   *  the caller. */
  function view(withEdge: boolean, n = 2): HTMLElement {
    const v = document.createElement("div");
    v.className = "transcript-view is-active";
    if (withEdge) {
      const edge = document.createElement("div");
      edge.className = "transcript-edge";
      v.appendChild(edge);
    }
    for (let i = 0; i < n; i++) {
      const turn = document.createElement("div");
      turn.className = "turn";
      turn.style.blockSize = "100px";
      v.appendChild(turn);
    }
    return v;
  }

  /** The multiplexer inside the REAL ancestor chain index.html declares, sized so
   *  the transcript overflows: `#messages-wrap` is the element scroll.ts scrolls,
   *  measures and roots the observer on, so every box between it and the marker is
   *  part of the geometry. `parked` adds the second view a chat switch leaves
   *  behind. `#messages-wrap-outer` takes an explicit height because production
   *  gets its own from `flex: 1` in the chat view. */
  function scroller(n: number, parked = 0): { scrollEl: HTMLElement; edge: HTMLElement } {
    const outer = document.createElement("div");
    outer.id = "messages-wrap-outer";
    outer.style.blockSize = "200px";
    const wrap = document.createElement("div");
    wrap.id = "messages-wrap";
    const messages = document.createElement("div");
    messages.id = "messages";
    const v = view(true, n);
    messages.appendChild(v);
    for (let i = 0; i < parked; i++) {
      const p = view(true, n);
      p.classList.remove("is-active");
      messages.appendChild(p);
    }
    wrap.appendChild(messages);
    outer.appendChild(wrap);
    stage.appendChild(outer);
    const edge = v.querySelector<HTMLElement>(".transcript-edge");
    expect(edge, "the fixture mounted the marker").not.toBeNull();
    expect(wrap.scrollHeight, "the fixture overflows its scroller").toBeGreaterThan(
      wrap.clientHeight,
    );
    return { scrollEl: wrap, edge: edge as HTMLElement };
  }

  /** How far the marker's bottom sits above the scroller's content bottom, which is
   *  the figure `isAtBottom()` and the edge observer have to agree on. */
  function offsetFromContentBottom(scrollEl: HTMLElement, edge: HTMLElement): number {
    const markerBottom =
      edge.getBoundingClientRect().bottom -
      scrollEl.getBoundingClientRect().top +
      scrollEl.scrollTop;
    return scrollEl.scrollHeight - markerBottom;
  }

  it("the live-edge marker sits ON the SCROLLER's content bottom", () => {
    // THE WHOLE POINT OF THE MARKER'S TOP MARGIN, against the box that decides it:
    // scroll.ts roots the observer on `#messages-wrap` with a `rootMargin` of
    // BOTTOM_TOLERANCE_PX and `isAtBottom` reads that element's `scrollHeight`, so
    // the two agree only at zero offset from ITS content bottom. What reopens the
    // 16px is `.transcript-view`'s own `padding-block-end` and, measured one ancestor
    // at a time in this Chromium, nothing else: padding on `#messages`, on the
    // scroller or on `#messages-wrap-outer` each moves the marker and the content
    // bottom together. The scroller-relative form earns its place on the case below.
    const { scrollEl, edge } = scroller(3);
    expect(offsetFromContentBottom(scrollEl, edge)).toBe(0);
  });

  it("a parked view adds nothing below the marker", () => {
    // The other half of the same geometry, and it is only visible from the
    // scroller: the views share one scroll box, so a parked one contributing
    // height stacks it UNDER the active view's marker. `content-visibility: hidden`
    // skips only the contents, so the zeroed `min-height` and `padding-block` are
    // what keep the marker on the content bottom — without them the observer
    // answers about a position the reader can never reach.
    const { scrollEl, edge } = scroller(3, 1);
    expect(offsetFromContentBottom(scrollEl, edge)).toBe(0);
  });

  it("the marker's margin carries the block-end air and adds nothing of its own", () => {
    // A zero-height flex item still earns the column's `gap`, so the margin is
    // `--sp-4 - --sp-3` — enough to draw the air the view's `padding-block-end` used
    // to, and no more. Get it wrong in either direction and the transcript MOVES:
    // with `justify-content: flex-end`, extra height lifts the whole column.
    //
    // Absolute numbers rather than a comparison against a marker-less view, which
    // measures nothing production has (scroll.ts appends the marker to every view it
    // attaches): 244 = two 100px turns, two 12px gaps, 4px of margin, 16px of air
    // above the first turn. That total is what it was while the view carried
    // symmetric padding and the margin was negative.
    const v = view(true);
    stage.appendChild(v);
    const edge = v.querySelector<HTMLElement>(".transcript-edge");
    const turns = v.querySelectorAll<HTMLElement>(".turn");
    const last = turns[turns.length - 1];
    expect(v.getBoundingClientRect().height).toBe(244);
    expect(v.getBoundingClientRect().bottom - last!.getBoundingClientRect().bottom).toBe(16);
    // `order: 1` is why it lands last: in the DOM this marker is the FIRST child.
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
