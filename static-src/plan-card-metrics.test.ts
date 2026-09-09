// ---------------------------------------------------------------------------
// `.plan-message`'s intrinsic-size estimate states the CONTENT box, so the card's
// own block padding must NOT be in it.
//
// The rule's old comment said the fallback was "the header row plus this block
// padding", which is the mechanism exactly backwards: `contain-intrinsic-size` sizes
// the CONTENT box, and the box model then applies `padding: var(--sp-3) var(--sp-4)`
// and the 1px border on top -- to a skipped card and a rendered one alike. So a
// value that already claimed the padding counted it twice.
//
// Why this can only be a layout measurement. The card's real content is a
// `.plan-header` line box, its `margin-block-end`, and one `.plan-entries` row --
// and `.plan-entries` and `.plan-entry` carry no CSS rule at all, so the row's height
// is whatever the base line box resolves to for its font and its emoji status glyph.
// No source read can produce that number.
//
// MEASURED over this suite's own 200-card list, against the unfixed
// `contain-intrinsic-size: auto 4rem` and then against the fix (Chromium 151):
// a real card of 70px on all three tiers, a skipped one of 90px, and a list drift
// (`listSkipped - listRendered`) of +3,680px fine / +3,620px coarse falling to 0.
// 90px is `max(0, 64 + 24 + 2)` -- the declared 4rem plus the 24px of block padding
// it had already claimed, plus the 2px border -- against a real content box of 44px.
//
// ONE real height on all three tiers, which is the finding rather than a redundancy:
// nothing on this card reads a control-height token or a width query, so one value is
// exact everywhere.
//
// WHAT THE CASE ASSERTS IS NOT THAT DRIFT, and the numbers above are why it cannot be:
// the content box is two line boxes plus a spacing token, so it moves with the machine's
// font stack — a CI runner measures the same card at 64px, a 38px content box against
// this container's 44 — while the estimate is a literal, and the drift's own magnitude
// moves with the count of cards Chromium chose not to render (1080 then 1074 across two
// retries of one run). The durable term is the BOX MODEL: the estimate must state the
// content box, so it stays inside half the padding-and-border a double-count added.
//
// ONE ENTRY is the floor and the card's own comment is why: a plan always has at
// least one entry under its header. A plan with more entries under-states, which is
// the benign direction and is what `.msg-row` does for genuinely variable content.
//
// Follows `files-row-metrics.test.ts` for the three-reading shape and its two
// instrument facts, and `css-rules.ts` for reading the shipped stylesheet through
// `?raw` rather than the gitignored bundle.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` throws.
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildPlanRow } from "./messages-plan.js";

/** Long enough that a per-card error is a four-figure number in the list total, so
 *  a failure is unmistakable rather than a rounding argument. Also the divisor the
 *  real card height is reported through, so it has to match the card count. */
const CARDS = 200;

/** The scrollport's height. Every card past it is off-viewport and therefore
 *  skipped, which is what gives the first reading its estimate-driven total. */
const WRAP_H = 500;

/** Class that turns the skip off, so the same list can be read a second time with
 *  every card genuinely rendered. */
const FORCE = "plan-metrics-force-render";

/** Class that replaces the estimate with an obviously wrong one, so the harness
 *  can prove the estimate is what the first reading is made of. */
const PROBE = "plan-metrics-probe-estimate";

/** The probe's content height. Far from every real card height, so the shift it
 *  produces cannot be confused with a rounding difference. */
const PROBE_PX = 300;

/** Per-case timeout. A case's cost here is SIX rAF turns, and a loaded `npm test`
 *  prices a turn at hundreds of ms whatever the list holds -- measured on
 *  `files-row-metrics.test.ts`, whose cases run 86-280ms cold in isolation and
 *  ~4.1s inside a full run. Widened rather than restructured, because there is no
 *  timing dependence to remove: nothing polls, awaits a duration or samples a
 *  window, and the assertion is a deterministic comparison of two synchronous
 *  `scrollHeight` reads. */
const LOADED_BUDGET_MS = 30_000;

let style: HTMLStyleElement;
let force: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  // (0,2,0) against `.plan-message`'s own (0,1,0), so both win wherever they are
  // inserted.
  force = document.createElement("style");
  force.textContent = [
    `.${FORCE} .plan-message { content-visibility: visible }`,
    `.${PROBE} .plan-message { contain-intrinsic-size: auto ${String(PROBE_PX)}px }`,
  ].join("\n");
  document.head.appendChild(force);

  host = document.createElement("div");
  // Out of the way of anything else the page holds, and a fixed inline size so a
  // card's own width never depends on the body's flow.
  host.style.cssText = "position:fixed;top:0;left:0;inline-size:48rem;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  force.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

/** One `.plan-message`, built the way `messages-plan.ts` `planElement` builds one:
 *  a `.plan-header` over a `.plan-entries` list holding ONE entry.
 *
 *  The entry comes from that module's own `buildPlanRow`, not a hand-written span:
 *  a row's content is an emoji status glyph plus the entry text, and the emoji is
 *  what the line box resolves against, so a fixture that dropped it would measure a
 *  row production never renders. */
function planCard(i: number): HTMLElement {
  const card = document.createElement("div");
  card.className = "plan-message";

  const header = document.createElement("div");
  header.className = "plan-header";
  header.textContent = "Plan";

  const list = document.createElement("div");
  list.className = "plan-entries";
  list.appendChild(
    buildPlanRow({
      content: `Read the failing test and reproduce it (${String(i)})`,
      status: "in_progress",
      priority: "medium",
    }),
  );

  card.append(header, list);
  return card;
}

/** The production nesting: a fixed-height scroller holding the `.msg-wrap` block
 *  container a turn's cards live in. `.msg-wrap` is a flex column with a gap, and
 *  the gap is the same in all three readings, so it cancels out of the drift while
 *  staying faithful to what the transcript lays out. */
function mountList(): { wrap: HTMLElement; list: HTMLElement } {
  const wrap = document.createElement("div");
  wrap.style.cssText = `height:${String(WRAP_H)}px;overflow-y:auto;`;

  const list = document.createElement("div");
  list.className = "msg-wrap";
  for (let i = 0; i < CARDS; i++) {
    list.appendChild(planCard(i));
  }

  wrap.appendChild(list);
  host.replaceChildren(wrap);
  return { wrap, list };
}

/** Yield until the renderer has run, which is what updates
 *  `content-visibility: auto` relevance. */
async function frame(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/** Jump every entry animation to its end.
 *
 *  Required, and NOT cosmetic: `.plan-message`'s `vk-slide-up` starts at
 *  `translateY(6px)`, and a transformed descendant extends its container's
 *  SCROLLABLE OVERFLOW, so the container's `scrollHeight` reads 6px high for the
 *  0.25s the animation runs. Finishing rather than awaiting, so the reading does
 *  not depend on a duration token. */
function finishAnimations(root: Element): void {
  for (const anim of root.getAnimations({ subtree: true })) {
    anim.finish();
  }
}

interface Metrics {
  /** The container's height with every off-screen card on the estimate. */
  readonly listSkipped: number;
  /** The same, with the estimate replaced by `PROBE_PX`. The harness's own
   *  sensitivity check -- see `expectContentBoxEstimate`. */
  readonly listProbe: number;
  /** Its height with every card genuinely rendered. */
  readonly listRendered: number;
  /** The declared fallback in px: the CONTENT height a skipped card resolves to. */
  readonly estimatePx: number;
  /** A rendered card's block padding plus its border -- exactly the term the
   *  estimate must not claim, and the scale the assertion is stated against. */
  readonly boxModelPx: number;
  /** `.msg-wrap`'s own `row-gap`. It cancels out of the drift, which is why the
   *  three readings can ignore it, and it does NOT cancel out of a per-card height:
   *  199 gaps over 200 cards is ~12px a card, which is most of the box-model term
   *  the assertion is measured against. */
  readonly gapPx: number;
}

/** The px half of `contain-intrinsic-block-size`, whose computed value keeps the
 *  `auto` keyword beside the length (`auto 44px`), so `parseFloat` reads NaN. */
function estimatePx(cs: CSSStyleDeclaration): number {
  return Number.parseFloat(/(-?[\d.]+)px/.exec(cs.containIntrinsicBlockSize)?.[1] ?? "NaN");
}

/** Read the same 200-card list three times: on the shipped estimate, on a
 *  deliberately wrong one, and with the skip turned off.
 *
 *  The list TOTAL is the only honest instrument here. A per-card
 *  `getBoundingClientRect()` is not: measured in Chromium 151, querying an element
 *  inside a skipped subtree reports its REAL box, so a per-card assertion would
 *  compare a rendered card against a rendered card and pass against the defect. */
async function measure(): Promise<Metrics> {
  const { wrap, list } = mountList();
  await frame();
  finishAnimations(wrap);
  await frame();
  const listSkipped = list.scrollHeight;

  // Still skipped, so this reads the fallback rather than a remembered size. Done
  // BEFORE the force-render, because a card that has been rendered once remembers
  // its real size and stops consulting the fallback at all.
  list.classList.add(PROBE);
  await frame();
  const listProbe = list.scrollHeight;
  list.classList.remove(PROBE);

  list.classList.add(FORCE);
  await frame();
  finishAnimations(wrap);
  await frame();

  const cs = getComputedStyle(list.firstElementChild as HTMLElement);
  return {
    listSkipped,
    listProbe,
    listRendered: list.scrollHeight,
    estimatePx: estimatePx(cs),
    gapPx: Number.parseFloat(getComputedStyle(list).rowGap),
    boxModelPx:
      Number.parseFloat(cs.paddingBlockStart) +
      Number.parseFloat(cs.paddingBlockEnd) +
      Number.parseFloat(cs.borderBlockStartWidth) +
      Number.parseFloat(cs.borderBlockEndWidth),
  };
}

/** Set the pointer tier the way `pointer-tier.ts` does. */
function tier(name: "fine" | "coarse"): void {
  document.documentElement.dataset["pointer"] = name;
}

/** Assert the property, plus the premise that makes the reading mean anything. */
function expectContentBoxEstimate(m: Metrics): void {
  // THE PREMISE. Without it the whole comparison is vacuous: if Chromium were not
  // skipping any card, the first and third readings would agree for the trivial
  // reason that both measured rendered cards, and the case would pass against any
  // estimate at all. Inflating the estimate has to move the total, and by a lot --
  // most of the list is off the scrollport, so the shift is tens of thousands of px.
  expect(
    m.listProbe - m.listSkipped,
    "cards are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(CARDS * 10);

  // THE PROPERTY: the estimate states the CONTENT box. It cannot be stated as a
  // drift of zero, which is what this case asserted until a CI runner measured the
  // same card at 75.94px against this container's 70 — the height is two line boxes
  // plus a spacing token, so the real content box moves with the font stack while
  // the estimate is a literal, and even the drift's own magnitude moved between two
  // retries of one run (1080 then 1074, the skipped count varying). What does not
  // move is the box-model term: a value that claimed the padding and the border as
  // well resolved a skipped card to 90px against a real 70, so HALF that term is the
  // width of the disagreement a font may produce and the double-count may not hide in.
  // 200 cards carry 199 gaps, so the gap comes out BEFORE the division — leaving it
  // in charges each card ~12px of `.msg-wrap` and swallows most of the box-model term.
  const realCardHeight = (m.listRendered - (CARDS - 1) * m.gapPx) / CARDS;
  const realContent = realCardHeight - m.boxModelPx;
  expect(
    Math.abs(m.estimatePx - realContent),
    `the estimate (${String(m.estimatePx)}px) states the content box, measured ` +
      `${realContent.toFixed(2)}px on a ${realCardHeight.toFixed(2)}px card; a value ` +
      `claiming its ${String(m.boxModelPx)}px of padding and border would read ` +
      `${(realContent + m.boxModelPx).toFixed(2)}px. Drift over the list: ` +
      `${String(m.listSkipped - m.listRendered)}px.`,
  ).toBeLessThan(m.boxModelPx / 2);
}

describe("the fine-pointer tier", () => {
  it(
    "prices a skipped plan card at its CONTENT box, not its border box",
    async () => {
      tier("fine");
      expectContentBoxEstimate(await measure());
    },
    LOADED_BUDGET_MS,
  );
});

describe("the coarse-pointer tiers, measured at real viewport sizes", () => {
  // Nothing on this card reads `--btn-h` or a width query, so both coarse cases are
  // expected to measure exactly what the fine tier does -- which is the finding, not
  // a redundancy: it is what makes ONE value exact everywhere. The block sits LAST in
  // the file and restores the size it found, because `page.viewport` has no getter
  // and a hand-copied pair would silently leave every later file measuring at the
  // wrong size.
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
  });

  afterAll(async () => {
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  it(
    "holds on a NARROW viewport",
    async () => {
      // 48rem is the no-JS coarse fallback's own boundary in `01-tokens.css` and
      // `<=` includes it, so this is the widest viewport that still takes the coarse
      // control heights.
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectContentBoxEstimate(await measure());
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectContentBoxEstimate(await measure());
    },
    LOADED_BUDGET_MS,
  );
});
