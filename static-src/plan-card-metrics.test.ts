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
// `contain-intrinsic-size: auto 4rem` and then against the fix (Chromium 151).
// Drift is `listSkipped - listRendered`, so a POSITIVE number means the estimate
// OVER-states and the list SHRINKS as the reader scrolls:
//
//   tier              real card   a skipped card was   drift was   now
//   fine pointer      70px        90px                 +3,680px    0px
//   coarse + narrow   70px        90px                 +3,620px    0px
//   coarse + wide     70px        90px                 +3,620px    0px
//
// ONE real height on all three tiers, which is the finding rather than a redundancy:
// nothing on this card reads a control-height token or a width query, so one value is
// exact everywhere. 90px is `max(0, 64 + 24 + 2)` -- the declared 4rem plus the 24px
// of block padding it had already claimed, plus the 2px border -- against a real
// content box of 44px. The 20px per-card error times the ~181-184 cards Chromium had
// not rendered is the drift, so the total is a function of the scrollport as well as
// the estimate, which is why it is recorded rather than derived (the coarse rows
// differ from the fine one only because those cases measure at a 900px viewport).
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
   *  sensitivity check -- see `expectNoDrift`. */
  readonly listProbe: number;
  /** Its height with every card genuinely rendered. */
  readonly listRendered: number;
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

  return { listSkipped, listProbe, listRendered: list.scrollHeight };
}

/** Set the pointer tier the way `pointer-tier.ts` does. */
function tier(name: "fine" | "coarse"): void {
  document.documentElement.dataset["pointer"] = name;
}

/** Assert the property, plus the premise that makes the reading mean anything. */
function expectNoDrift(m: Metrics): void {
  // THE PREMISE. Without it the whole comparison is vacuous: if Chromium were not
  // skipping any card, the first and third readings would agree for the trivial
  // reason that both measured rendered cards, and the case would pass against any
  // estimate at all. Inflating the estimate has to move the total, and by a lot --
  // most of the list is off the scrollport, so the shift is tens of thousands of px.
  expect(
    m.listProbe - m.listSkipped,
    "cards are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(CARDS * 10);

  // THE PROPERTY, over the whole list -- this is the `scrollHeight` the scrollbar
  // and the scroll anchor read. Stated as the DRIFT so a failure names the px rather
  // than two five-figure totals, with the real card height beside it so the message
  // says which tier it was measuring.
  expect({
    drift: m.listSkipped - m.listRendered,
    realCardHeight: m.listRendered / CARDS,
  }).toEqual({ drift: 0, realCardHeight: m.listRendered / CARDS });
}

describe("the fine-pointer tier", () => {
  it(
    "reports one list height whether its plan cards are skipped or rendered",
    async () => {
      tier("fine");
      expectNoDrift(await measure());
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
      expectNoDrift(await measure());
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
      expectNoDrift(await measure());
    },
    LOADED_BUDGET_MS,
  );
});
