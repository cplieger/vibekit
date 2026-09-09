// ---------------------------------------------------------------------------
// `.fb-row`'s intrinsic-size estimate has to equal the row's REAL height, on
// every pointer/width tier.
//
// Why this can only be a layout measurement. With `content-visibility: auto` plus
// `contain-intrinsic-size: auto <len>`, a row that has never been rendered
// contributes `<len>` to layout while a row that HAS been rendered contributes its
// remembered real size. So a wrong `<len>` moves `#fb-list`'s `scrollHeight` by
// `(real - skipped) x (rows not yet rendered)` and keeps moving it as the reader
// scrolls — and `.fb-list-wrap` leaves `overflow-anchor` at its default, so
// Chromium applies a scroll-anchoring correction to `scrollTop` on every frame the
// height moves. No source read can see that: the numbers come from three token
// layers (`--sp-*`, `--btn-h`, and the 2px `70-selection.css` reserves on all four
// sides), and the questions are which of them BINDS and whether the estimate is
// the content box or the border box. Both are used-value questions.
//
// MEASURED over this suite's own 600-row list, against the unfixed
// `contain-intrinsic-size: auto 2.3125rem` and then against the fix
// (Chromium 151). Drift is `listSkipped - listRendered`, so a POSITIVE number
// means the estimate over-states and the list SHRINKS as the reader scrolls:
//
//   tier              real row   a skipped row was   drift was   now
//   fine pointer      38px       55px                +9,707px    0px
//   coarse + narrow   46px       63px                +9,690px    0px
//   coarse + wide     44px       55px                +6,226px    0px
//
// The sign is the finding, and the direction is the opposite of the one a reader
// expects. `contain-intrinsic-size` states the CONTENT box, so a skipped row
// resolved to `max(min-height, 37px + padding + border)` — 55px on the fine tier
// against a real 38 — while the row's real content is the 1.25rem `.fb-icon` box,
// 20px rather than 37. Padding, border and `min-height` are applied on top by the
// box model, to a skipped row and a rendered one alike, which is why one value
// covers all three tiers with no `max()` and no arithmetic.
//
// RED-CHECKED three times, because a three-tier suite is only worth three cases if
// each can fail alone and none of them can pass vacuously:
//
//   * `auto 2.3125rem` (the shipped literal): all three fail, with the three
//     distinct drifts above.
//   * `auto 0px`: the two padding-bound tiers fail (-1,112px and -1,114px) and
//     WIDE-COARSE PASSES — `min-height` binds there, so it absorbs any
//     under-estimate. So the wide tier is a genuinely separate arm rather than a
//     third spelling of the same one, and the value is pinned by the other two.
//   * `content-visibility: visible`: all three fail on the PREMISE, at 0px of
//     probe sensitivity, which is what stops the whole comparison passing for the
//     trivial reason that nothing was ever skipped.
//
// THE SAME VALUE IS EXACT FOR A SEARCH HIT, and `.fb-search-hit`'s own
// `auto 2.75rem` was pure over-statement. `files-search.ts` `hitRow` builds that
// row as `class="fb-row fb-search-hit"`, so ONE element takes `.fb-row`'s
// `content-visibility: auto`, its 16px block padding, its 36px `min-height` and
// the 2px 70-selection.css reserve — and then resolved them on top of 44px
// instead of 20px. Its real content height is `--fb-row-content` exactly: the
// extra spans (`.fb-search-lineno`, `.fb-search-excerpt`) are `white-space:
// nowrap` single-line children sharing the row's flex line, so the tallest child
// is still the `.fb-icon` box. MEASURED over this suite's own 600-hit list:
//
//   tier              real hit   a skipped hit was   per row   drift was    now
//   fine pointer      38px       62px                +24px     +13,536px    0px
//   coarse + narrow   46px       70px                +24px     +13,752px    0px
//   coarse + wide     44px       62px                +18px     +10,260px    0px
//
// The wide tier is the smaller error for `min-height`'s sake, the same way it is
// for a listing row: 44 + 16 + 2 = 62 exceeds the 44px floor there, so the floor
// stops absorbing and the whole over-statement lands.
//
// Same three real heights as a listing row, which is the finding: the rule's old
// comment claimed a hit row is "taller and more variable than a listing row",
// and it is neither. RED-CHECKED by restoring `auto 2.75rem`: all three fail with
// exactly those drifts, and the three listing cases stay green, so the two
// selectors are separate arms. The premise reading discriminates too — the PROBE
// override is `.<probe> .fb-row`, (0,2,0), so it outranked `.fb-search-hit`'s
// (0,1,0) even while the override was there.
//
// A DIRTY FILE'S ROW IS THE THIRD ARM, and until it existed this file certified a
// geometry no shipped listing has: `row()` builds no git letter, while
// `files.ts` `statusBadge` gives every dirty FILE one carrying `role="button"` —
// 133 of this workspace's own 596 `static-src` rows. That role matches the
// app-wide hit floor (61-mcp-tools.css), which set `min-width`/`min-height` on
// the letter and so made it, not `.fb-icon`, the row's tallest flex child.
// MEASURED over the same 600-row list against the unfixed `.fb-git-clickable`:
//
//   tier              reserve   real lettered row   per row   600-row drift
//   fine pointer      38px      42px                -4px      -2,232px
//   coarse + narrow   46px      70px                -24px     -13,416px
//   coarse + wide     44px      62px                -18px     -10,026px
//
// NEGATIVE, so the estimate UNDER-states and the list GREW as the reader
// scrolled, the opposite direction from the two arms above. The aggregate is
// smaller than `600 x per-row` because the ~42 rows inside the scrollport are
// rendered and contribute no drift. The fine and coarse-wide per-row figures
// reconcile against the live instance independently: 133 x 4 = 532px and
// 133 x 18 = 2,394px, both of which were observed there as scrollbar travel.
//
// The fix keeps `role="button"` (the letter IS a control — it opens that file's
// diff, and `files-decoration.test.ts` drives it) and gives its TARGET the floor
// through an `::after` expander instead of its BOX, so a lettered row returns to
// 38/46/44 and `--fb-row-content: 1.25rem` is exact for both arms again.
//
// RED-CHECKED twice, because the geometry and the target are pinned SEPARATELY:
//
//   * delete `min-width: 0; min-height: 0` from `.fb-git-clickable`: the three
//     lettered cases fail at exactly the three drifts above, and the six other
//     cases stay green. The scroll walk below fails with them, reporting
//     22,968px climbing to 25,200px over 49 distinct heights — the same
//     -2,232px, arrived at through the reader's own gesture instead of through
//     the estimate.
//   * delete the `::after` expander instead and keep the `min-*` pair: all TEN
//     cases stay GREEN. So this file says nothing about whether the letter is
//     still REACHABLE by a finger, and deleting the expander as unused CSS would
//     take a 24/44px touch target with it silently. That half is
//     `touch-policy.test.ts`'s, which measures the expander's used size.
//
// An earlier harness scrolled the whole list a screenful at a time and read the
// same three drifts. It was replaced by the three readings below, which need six
// frames rather than sixty: at 600 rows the traversal exceeded the 5s per-test
// budget under a loaded `npm test` (the cold-cache-plus-full-suite class), and
// widening the timeout would have left the assertion just as load-sensitive.
//
// Follows `turn-elapsed-css.test.ts` for the measure-real-layout shape and
// `css-rules.ts` (as `run-page-layout.test.ts` does) for reading the shipped
// stylesheet through `?raw` rather than the gitignored bundle.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control: the padding override is a `@media (width <= 48rem)` rule,
// so the narrow tier can only be reached by resizing the real frame.
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` throws.
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** Long enough that a per-row error is a four-figure number in the list total, so
 *  a failure is unmistakable rather than a rounding argument. Also the divisor the
 *  real row height is reported through, so it has to match the row count exactly. */
const ROWS = 600;

/** The scrollport's height. Every row past it is off-viewport and therefore
 *  skipped, which is what gives the first reading its estimate-driven total. */
const WRAP_H = 500;

/** Class that turns the skip off, so the same list can be read a second time with
 *  every row genuinely rendered. */
const FORCE = "fb-metrics-force-render";

/** Class that replaces the estimate with an obviously wrong one, so the harness
 *  can prove the estimate is what the first reading is made of. */
const PROBE = "fb-metrics-probe-estimate";

/** The probe's content height. Far from every real row height, so the shift it
 *  produces cannot be confused with a rounding difference. */
const PROBE_PX = 100;

/** Per-case timeout, because a case's cost here is SIX rAF turns and a loaded
 *  `npm test` prices a turn at hundreds of ms whatever the list holds.
 *
 *  MEASURED inside a full 368-file run: every case in this file took 4.1-4.8s
 *  against the project's 5s default, while the same cases cold in isolation take
 *  86-280ms — so the ~4.1s is a fixed six-frame cost, not the 600 rows (a 150-row
 *  variant of the hit cases measured the same 4.1s and was reverted for claiming
 *  otherwise). The three listing cases had been sitting within 200ms of the cap
 *  since they shipped; adding three more was what pushed one over it, which makes
 *  this a budget the file always needed rather than a new dependence.
 *
 *  Widened rather than restructured, because there is no timing dependence to
 *  remove: nothing here polls, awaits a duration or samples a window, and the
 *  assertion is a deterministic comparison of two synchronous `scrollHeight`
 *  reads. Only its COMPLETION is load-sensitive, so a wider budget costs
 *  precision nothing. Follows `residency-anchor.test.ts`, which gives its
 *  scroll-walk cases 90s for the same reason. */
const LOADED_BUDGET_MS = 30_000;

let style: HTMLStyleElement;
let force: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  // (0,2,0) against `.fb-row`'s own (0,1,0), so both win wherever they are
  // inserted.
  force = document.createElement("style");
  force.textContent = [
    `.${FORCE} .fb-row { content-visibility: visible }`,
    `.${PROBE} .fb-row { contain-intrinsic-size: auto ${String(PROBE_PX)}px }`,
  ].join("\n");
  document.head.appendChild(force);

  host = document.createElement("div");
  // Out of the way of anything else the page holds, and a fixed inline size so a
  // row's own width never depends on the body's flow.
  host.style.cssText = "position:fixed;top:0;left:0;inline-size:40rem;";
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

/** One `.fb-row`, with the children `files.ts` `entryRow` gives it: the checkbox,
 *  the icon box (the tallest flex child at 1.25rem), the name and the metadata.
 *  The estimate names the icon's height, so a fixture without it would measure a
 *  row production never renders. */
function row(i: number): HTMLElement {
  const el = document.createElement("div");
  el.className = "fb-row";
  el.setAttribute("role", "listitem");

  const check = document.createElement("input");
  check.type = "checkbox";
  check.className = "fb-check";

  const icon = document.createElement("span");
  icon.className = "fb-icon";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  icon.appendChild(svg);

  const name = document.createElement("span");
  name.className = "fb-name fb-name-link";
  name.textContent = `entry-${String(i)}.ts`;

  const meta = document.createElement("span");
  meta.className = "fb-meta";
  meta.textContent = "1.2 KB   ·   2026-09-01   ·   -rw-r--r--";

  el.append(check, icon, name, meta);
  return el;
}

/** A listing row for a DIRTY file: `row()` plus the git letter `statusBadge`
 *  inserts between the name and the metadata, in production's own order
 *  (`files.ts` `entryRow`). 133 of this workspace's 596 `static-src` rows carry
 *  one, so this is the shape most of a real listing has and the clean `row()`
 *  above is the control.
 *
 *  The three attributes are the ones that matter to layout: the class pair, and
 *  `role="button"` — which is what makes the app-wide hit floor
 *  (61-mcp-tools.css) match this element AT ALL. Without the role the fixture
 *  measures a letter no floor applies to, which is the clean row again under
 *  another name and cannot fail on the defect this arm exists for.
 *
 *  A directory-rollup builder is deliberately absent: a rollup letter gets
 *  neither the class nor the role (`files.ts` `statusBadge` adds both only in its
 *  `!isDir` branch), so it matches no floor rule and the clean arm covers it. */
function letteredRow(i: number): HTMLElement {
  const el = row(i);

  const badge = document.createElement("span");
  badge.className = "fb-git-letter git-st-m fb-git-clickable";
  badge.setAttribute("role", "button");
  badge.setAttribute("aria-label", "Git status: modified");
  badge.textContent = "M";

  el.insertBefore(badge, el.querySelector(".fb-meta"));
  return el;
}

/** One search hit, with the children `files-search.ts` `hitRow` gives it: the icon
 *  box, the name link, the line number and the excerpt. It carries BOTH classes,
 *  which is the whole point — the element takes `.fb-row`'s
 *  `content-visibility: auto`, its padding and its `min-height`, and
 *  `.fb-search-hit` used to override the estimate those are applied on top of.
 *  No checkbox: a hit row is not selectable. */
function hitRow(i: number): HTMLElement {
  const el = document.createElement("div");
  el.className = "fb-row fb-search-hit";
  el.setAttribute("role", "listitem");
  el.setAttribute("tabindex", "0");

  const icon = document.createElement("span");
  icon.className = "fb-icon";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  icon.appendChild(svg);

  const name = document.createElement("span");
  name.className = "fb-name fb-name-link";
  name.textContent = `src/entry-${String(i)}.ts`;

  const lineno = document.createElement("span");
  lineno.className = "fb-search-lineno";
  lineno.textContent = `:${String(i + 1)}`;

  const excerpt = document.createElement("span");
  excerpt.className = "fb-search-excerpt";
  excerpt.textContent = "const answer = compute(input, options, fallback);";

  el.append(icon, name, lineno, excerpt);
  return el;
}

/** A row builder: `row` for a listing row, `hitRow` for a search hit. */
type RowBuilder = (i: number) => HTMLElement;

/** The production nesting: a fixed-height `.fb-list-wrap` scroller holding the
 *  `#fb-list` the rows live in. `.fb-list-wrap` declares `flex: 1 1 0`, which is
 *  inert outside a flex container, so an inline height is what sizes it here. */
function mountList(build: RowBuilder = row): { wrap: HTMLElement; list: HTMLElement } {
  const wrap = document.createElement("div");
  wrap.className = "fb-list-wrap";
  wrap.style.cssText = `height:${String(WRAP_H)}px;`;

  const list = document.createElement("div");
  list.id = "fb-list";
  list.setAttribute("role", "list");
  for (let i = 0; i < ROWS; i++) {
    list.appendChild(build(i));
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
 *  Required, and NOT cosmetic: `.fb-row`'s `vk-slide-up` starts at
 *  `translateY(6px)`, and a transformed descendant extends its container's
 *  SCROLLABLE OVERFLOW, so `#fb-list`'s `scrollHeight` reads 6px high for the
 *  0.25s the animation runs. Measured: it put a floor of -6px on the drift with a
 *  perfectly correct estimate. Finishing rather than awaiting, so the reading does
 *  not depend on a duration token. */
function finishAnimations(root: Element): void {
  for (const anim of root.getAnimations({ subtree: true })) {
    anim.finish();
  }
}

interface Metrics {
  /** `#fb-list`'s height with every off-screen row on the estimate. */
  readonly listSkipped: number;
  /** The same, with the estimate replaced by `PROBE_PX`. The harness's own
   *  sensitivity check — see `expectNoDrift`. */
  readonly listProbe: number;
  /** Its height with every row genuinely rendered. */
  readonly listRendered: number;
}

/** Read the same 600-row list three times: on the shipped estimate, on a
 *  deliberately wrong one, and with the skip turned off.
 *
 *  The list TOTAL is the only honest instrument here. A per-row
 *  `getBoundingClientRect()` is not: measured in Chromium 151, querying a row
 *  inside a skipped subtree reports its REAL box (20px for the icon of a row that
 *  had never been rendered), so a per-row assertion would have compared a rendered
 *  row against a rendered row and passed against the defect. */
async function measure(build: RowBuilder = row): Promise<Metrics> {
  const { wrap, list } = mountList(build);
  await frame();
  finishAnimations(wrap);
  await frame();
  const listSkipped = list.scrollHeight;

  // Still skipped, so this reads the fallback rather than a remembered size. Done
  // BEFORE the force-render, because a row that has been rendered once remembers
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
  // skipping any row, the first and third readings would agree for the trivial
  // reason that both measured rendered rows, and the case would pass against any
  // estimate at all. Inflating the estimate has to move the total, and by a lot —
  // ~570 rows are off the scrollport, so the shift is tens of thousands of px.
  expect(
    m.listProbe - m.listSkipped,
    "rows are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(ROWS * 10);

  // THE PROPERTY, over the whole list — this is the `scrollHeight` the scrollbar
  // and the scroll anchor read. Stated as the DRIFT so a failure names the px
  // rather than two five-figure totals, with the real row height beside it so the
  // message says which tier it was measuring.
  expect({
    drift: m.listSkipped - m.listRendered,
    realRowHeight: m.listRendered / ROWS,
  }).toEqual({ drift: 0, realRowHeight: m.listRendered / ROWS });
}

describe("the fine-pointer tier", () => {
  it(
    "reports one list height whether its rows are skipped or rendered",
    async () => {
      tier("fine");
      expectNoDrift(await measure());
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a search hit, which takes .fb-row's estimate on the same element",
    async () => {
      tier("fine");
      expectNoDrift(await measure(hitRow));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a DIRTY file's row, which carries the git letter production emits",
    async () => {
      tier("fine");
      expectNoDrift(await measure(letteredRow));
    },
    LOADED_BUDGET_MS,
  );
});

/** Budget for the scroll walk, which is a different cost from the three-reading
 *  cases above: ~60 steps of two rAF turns each rather than six turns total, and
 *  a loaded `npm test` prices a turn at hundreds of ms. Follows
 *  `residency-anchor.test.ts`, whose own scroll walks carry 90s for this reason.
 *
 *  The walk is being RE-ADDED deliberately. FEAT-001 replaced an earlier
 *  traversal harness with the three-reading form because it exceeded a 5s cap;
 *  that cap is now 30s and this case carries its own, and what it adds over the
 *  arithmetic is the MONOTONIC CLIMB — the thing a reader sees as the scrollbar
 *  thumb shrinking under their finger — plus the shape a live sidecar pass has to
 *  use, since a literal top/bottom `scrollHeight` pair reads EQUAL there even
 *  against this defect (a frozen rAF never lays out the bands already scrolled
 *  past, so neither reading ever leaves the estimate). Equality of two reads is
 *  not an instrument; a per-step animation finish plus a frame kick is. */
const WALK_BUDGET_MS = 90_000;

/** Bound on the walk, so a list that somehow never reaches its end fails on the
 *  premise below rather than hanging the case. 600 rows in `WRAP_H` steps needs
 *  about 60, and the worst measured drift adds ~27 more. */
const MAX_WALK_STEPS = 200;

interface Walk {
  /** `#fb-list`'s `scrollHeight` at the top, then once per step. */
  readonly heights: readonly number[];
  /** Steps actually taken. */
  readonly steps: number;
  /** Whether the last step landed at the scroller's own end. */
  readonly reachedEnd: boolean;
}

/** Scroll the whole list a screenful at a time, reading `#fb-list`'s
 *  `scrollHeight` after every step.
 *
 *  Both per-step calls are load-bearing. `finishAnimations` because `vk-slide-up`
 *  holds each newly-relevant row at `translateY(6px)`, which extends the
 *  container's scrollable overflow for the animation's duration; `frame()`
 *  because `content-visibility: auto` relevance — and therefore whether a band is
 *  laid out at its remembered real size or at the estimate — is only updated by
 *  the renderer. */
async function walkScrollHeights(build: RowBuilder): Promise<Walk> {
  const { wrap, list } = mountList(build);
  await frame();
  finishAnimations(wrap);
  await frame();

  const heights: number[] = [list.scrollHeight];
  let steps = 0;
  let reachedEnd = false;
  while (steps < MAX_WALK_STEPS) {
    // Re-read per step: the whole point is that this maximum can MOVE as bands
    // are rendered, so a bound captured at the top would end the walk early.
    const max = wrap.scrollHeight - wrap.clientHeight;
    if (max - wrap.scrollTop < 1) {
      reachedEnd = true;
      break;
    }
    wrap.scrollTop = Math.min(wrap.scrollTop + WRAP_H, max);
    steps++;
    finishAnimations(wrap);
    await frame();
    heights.push(list.scrollHeight);
  }
  return { heights, steps, reachedEnd };
}

describe("scrolling a lettered listing end to end", () => {
  it(
    "never moves #fb-list's scrollHeight",
    async () => {
      tier("fine");
      const walk = await walkScrollHeights(letteredRow);

      // THE PREMISE, both halves. A walk that took one step measured nothing, and
      // one that never reached the end left most bands on the estimate, so either
      // would let the property pass for a reason that is not the property.
      expect(walk.steps, "the walk actually traversed the list").toBeGreaterThan(1);
      expect(walk.reachedEnd, "the walk reached the scroller's end").toBe(true);

      // THE PROPERTY. Reported as [min, max, distinct] rather than as one number,
      // so a regression names the CLIMB — the reader's own symptom — instead of
      // one arbitrary sample from it.
      const distinct = [...new Set(walk.heights)];
      expect({
        min: Math.min(...walk.heights),
        max: Math.max(...walk.heights),
        distinctHeights: distinct.length,
      }).toEqual({
        min: walk.heights[0],
        max: walk.heights[0],
        distinctHeights: 1,
      });
    },
    WALK_BUDGET_MS,
  );
});

describe("the coarse-pointer tiers, measured at real viewport sizes", () => {
  // The padding override is keyed on WIDTH (`50-mobile.css`) while `--btn-h` moves
  // on the POINTER tier (`01-tokens.css`), so a coarse pointer has TWO cases and
  // only one of them is reachable at this project's own viewport. The block sits
  // LAST in the file and restores the size it found, because `page.viewport` has no
  // getter and a hand-copied pair would silently leave every later file measuring
  // at the wrong size.
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
    "holds on a NARROW viewport, where the padding override binds",
    async () => {
      // 48rem is the override's own boundary and `<=` includes it, so this is the
      // widest viewport that still takes the larger padding.
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
    "holds on a WIDE viewport, where min-height binds instead",
    async () => {
      // The case a per-tier literal cannot cover: the padding is the desktop value
      // while `--btn-h` is the coarse one, so the row is `min-height` tall and its
      // padding+content box is SHORTER than that. It is also the tier that absorbs
      // an UNDER-estimate, which is why the other two are what pin the value.
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure());
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a search hit on a NARROW viewport",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(hitRow));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a search hit on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(hitRow));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a DIRTY file's row on a NARROW viewport",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(letteredRow));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a DIRTY file's row on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(letteredRow));
    },
    LOADED_BUDGET_MS,
  );
});
