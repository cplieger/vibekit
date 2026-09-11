// ---------------------------------------------------------------------------
// `.run-card`'s intrinsic-size estimate has to cover the card's REAL floor, on
// every pointer tier.
//
// Why this can only be a layout measurement. With `content-visibility: auto` plus
// `contain-intrinsic-size: auto <len>`, a card that has never been rendered
// contributes `<len>` to layout while a card that HAS been rendered contributes its
// remembered real size. So a wrong `<len>` moves the transcript's `scrollHeight` by
// `(real - skipped) x (cards not yet rendered)`. No source read can see that: the
// height is a sum over four regions, three of which read a control-height or
// spacing token, and the questions are which of them BINDS and whether the estimate
// is the content box or the border box. Both are used-value questions.
//
// MEASURED over this suite's own 200-card list, against the unfixed
// `contain-intrinsic-size: auto 4rem` and then against the fix (Chromium 151).
// Drift is `listSkipped - listRendered`, so a NEGATIVE number means the estimate
// UNDER-states and the list GROWS as the reader scrolls -- the opposite sign from
// the file browser's:
//
//   tier              real card   a skipped card was   drift was   now
//   fine, collapsed   79px        66px                 -2,327px    0px
//   coarse, collapsed 107px       66px                 -3,696px    0px
//   fine, one step    129px       66px                -11,277px   -9,100px
//   coarse, one step  165px       66px                -13,904px  -10,498px
//
// THE TWO COARSE HEIGHTS MOVED, and the drift columns beside them did not. They read
// 87px and 145px until `.run-open` stopped declaring its own `1.5rem` and took the
// app-wide hit floor (2026-09-11, item 6): a coarse foot went 33px to 53px, so the
// reserve under-stated a coarse card by 20px until `--run-card-content`'s foot term
// read `var(--hit-floor)` too. That is what these two cases caught, and it is why
// the estimate is an expression over the same tokens the card's own rules read. The
// drift and residual figures are the ORIGINAL measurement against `auto 4rem`, taken
// when a coarse collapsed card was 87px; the assertions are derived from the rendered
// height, so they moved with the card while those historical numbers did not.
//
// The two coarse rows cover BOTH coarse cases -- narrow (768px) and wide (1024px)
// read identically, because nothing on this card keys on width; only `--btn-h` and
// `--hit-floor` move, and both move on the pointer tier.
//
// TWO ARMS, because a run card has two disclosure states and ONE estimate cannot be
// exact for both: a collapsed card is head + foot, an open one adds its step rows.
// The reserve is therefore the COLLAPSED height -- the card's floor, and the state
// every superseded card in a transcript is in -- so the collapsed arm asserts a
// drift of exactly 0 and the open arm asserts the SIGN: the reserve must never
// OVER-state, because an over-stating estimate makes the container shrink under a
// reader who is scrolling. The open residual is reported rather than pinned, since
// it is a function of how many steps a card holds.
//
// TWO RED CHECKS, because THE VALUE THE FIX REPLACED CANNOT REACH THE OPEN ARM.
// Restoring `auto 4rem` (66px rendered) sits below the floor in BOTH states, so it
// turns the three COLLAPSED cases red -- one per tier -- at the drifts tabled above,
// and it leaves the three OPEN cases GREEN, correctly: a value under an open card's
// real height cannot over-state one, and over-stating is the only thing those cases
// assert (`overStatesBy === 0`, not the residual the table reports). So the open arm
// needs the opposite probe, and it is a reachable regression rather than a
// hypothetical -- reserving the open height is exactly the option the fix weighed and
// rejected. RED-CHECKED against `--run-card-content: 200px` (202px rendered), above
// an open card's real height on every tier:
//
//   tier               real card   over-stated by
//   fine, one step     129px       +13,797px
//   coarse, one step   145px       +10,887px
//
// Both coarse viewports read identically there too. That probe reddens the three
// COLLAPSED cases as well (+23,616px fine, +21,965px coarse), because their arm pins
// a drift of exactly 0 and so fails in either direction: ONE over-stating value
// covers all six cases, where the under-stating restore reaches only three. Each
// drift is a per-card error times the cards Chromium had not rendered, so every
// figure is a function of the scrollport as well as the estimate -- which is why all
// of them are recorded rather than derived.
//
// Follows `files-row-metrics.test.ts` for the three-reading shape and its two
// instrument facts, and `css-rules.ts` for reading the shipped stylesheet through
// `?raw` rather than the gitignored bundle.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control. `--btn-h` moves on the POINTER tier, which the attribute
// alone reaches, so the two coarse cases exist to PROVE nothing here keys on width --
// `01-tokens.css` carries a width-keyed no-JS fallback for that token and `50-mobile.css`
// could grow a rule for this card, and only a real resize would catch either.
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` throws.
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { iconEl } from "./icon-el.js";
import { chevronEl } from "./chevron.js";
import { ICON_TAB_RUN, ICON_EXTERNAL } from "./icons.js";
import { paintStateMark } from "./exec-view/status.js";

/** Long enough that a per-card error is a four-figure number in the list total, so
 *  a failure is unmistakable rather than a rounding argument. Also the divisor the
 *  real card height is reported through, so it has to match the card count. */
const CARDS = 200;

/** The scrollport's height. Every card past it is off-viewport and therefore
 *  skipped, which is what gives the first reading its estimate-driven total. */
const WRAP_H = 500;

/** Class that turns the skip off, so the same list can be read a second time with
 *  every card genuinely rendered. */
const FORCE = "run-metrics-force-render";

/** Class that replaces the estimate with an obviously wrong one, so the harness
 *  can prove the estimate is what the first reading is made of. */
const PROBE = "run-metrics-probe-estimate";

/** The probe's content height. Far from every real card height, so the shift it
 *  produces cannot be confused with a rounding difference. */
const PROBE_PX = 400;

/** Per-case timeout. A case's cost here is SIX rAF turns, and a loaded `npm test`
 *  prices a turn at hundreds of ms whatever the list holds -- measured on
 *  `files-row-metrics.test.ts`, whose cases run 86-280ms cold in isolation and
 *  ~4.1s inside a full run. Widened rather than restructured, because there is no
 *  timing dependence to remove: nothing polls, awaits a duration or samples a
 *  window, and the assertion is a deterministic comparison of two synchronous
 *  `scrollHeight` reads. Follows `residency-anchor.test.ts`, which gives its
 *  scroll-walk cases 90s for the same reason. */
const LOADED_BUDGET_MS = 30_000;

let style: HTMLStyleElement;
let force: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  // (0,2,0) against `.run-card`'s own (0,1,0), so both win wherever they are
  // inserted.
  force = document.createElement("style");
  force.textContent = [
    `.${FORCE} .run-card { content-visibility: visible }`,
    `.${PROBE} .run-card { contain-intrinsic-size: auto ${String(PROBE_PX)}px }`,
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

/** One step row, with the children `fundamentals/run-card.ts` `stepRow` gives it:
 *  the state mark, the node's own name, its meta line and its duration, on an
 *  anchor (the row is a DOOR into the run tab, not a disclosure). */
function stepRow(i: number): HTMLElement {
  const root = document.createElement("div");
  root.className = "run-step";
  root.dataset["node"] = `build-${String(i)}`;
  root.dataset["status"] = "ok";
  root.dataset["nodeType"] = "step";

  const glyph = document.createElement("span");
  glyph.className = "run-step-glyph";
  glyph.setAttribute("aria-hidden", "true");
  paintStateMark(glyph, "ok");

  const name = document.createElement("span");
  name.className = "run-step-name";
  name.textContent = `build-${String(i)}`;

  const meta = document.createElement("span");
  meta.className = "run-step-meta";
  meta.textContent = "general-task-execution \u00b7 claude-sonnet-5";

  const dur = document.createElement("span");
  dur.className = "run-step-dur";
  dur.textContent = "12s";

  const head = document.createElement("a");
  head.className = "run-step-head";
  head.href = `/run/r-${String(i)}#node=build-${String(i)}`;
  head.append(glyph, name, meta, dur);

  root.appendChild(head);
  return root;
}

/** One `.run-card`, with the four regions `fundamentals/run-card.ts` builds: head,
 *  alert (hidden until the run wants a person), the disclosure body holding the step
 *  rows, and the foot. Only the BODY is inside the disclosure, which is the whole
 *  point of the measurement: the reserve has to cover the regions that are present
 *  whatever the disclosure says.
 *
 *  `open` reproduces what `createDisclosure` leaves on the region in each state --
 *  `aria-hidden`, `inert` and an inline `height: 0px` when closed -- rather than
 *  wiring the primitive, so the fixture needs no import from the feature graph. */
function runCard(i: number, open: boolean): HTMLElement {
  const root = document.createElement("div");
  root.className = open ? "run-card" : "run-card collapsed";
  root.dataset["run"] = `r-${String(i)}`;
  root.dataset["status"] = "completed";

  const icon = document.createElement("span");
  icon.className = "run-icon";
  icon.setAttribute("aria-hidden", "true");
  icon.appendChild(iconEl(ICON_TAB_RUN));

  const name = document.createElement("span");
  name.className = "run-name";
  name.textContent = `code-review-${String(i)}`;

  const state = document.createElement("span");
  state.className = "run-state";
  state.textContent = "completed";
  const count = document.createElement("span");
  count.className = "run-count";
  count.textContent = "1 step";
  const clock = document.createElement("span");
  clock.className = "run-clock";
  clock.textContent = "1m 4s";
  const meta = document.createElement("span");
  meta.className = "run-head-meta";
  meta.append(state, count, clock);

  const toggle = document.createElement("span");
  toggle.className = "run-toggle";
  toggle.setAttribute("aria-hidden", "true");
  toggle.appendChild(chevronEl());

  const head = document.createElement("div");
  head.className = "run-head";
  head.setAttribute("role", "button");
  head.setAttribute("tabindex", "0");
  head.setAttribute("aria-expanded", open ? "true" : "false");
  head.append(icon, name, meta, toggle);

  const alert = document.createElement("div");
  alert.className = "run-alert hidden";
  alert.setAttribute("role", "status");
  alert.setAttribute("aria-live", "polite");

  const steps = document.createElement("div");
  steps.className = "run-steps";
  steps.appendChild(stepRow(i));

  const outputs = document.createElement("dl");
  outputs.className = "run-outputs hidden";

  const body = document.createElement("div");
  body.className = "run-body uip-disclosure-region";
  body.setAttribute("aria-hidden", open ? "false" : "true");
  body.inert = !open;
  if (!open) {
    body.style.height = "0px";
  }
  body.append(steps, outputs);

  const ledger = document.createElement("span");
  ledger.className = "run-ledger";
  ledger.textContent = "1 step \u00b7 1m 4s";

  const openLink = document.createElement("a");
  openLink.className = "run-open";
  openLink.href = `/run/r-${String(i)}`;
  openLink.append("Open run");
  const openIcon = document.createElement("span");
  openIcon.className = "run-open-icon";
  openIcon.setAttribute("aria-hidden", "true");
  openIcon.appendChild(iconEl(ICON_EXTERNAL));
  openLink.appendChild(openIcon);

  const foot = document.createElement("div");
  foot.className = "run-foot";
  foot.append(ledger, openLink);

  root.append(head, alert, body, foot);
  return root;
}

/** The production nesting: a fixed-height scroller holding the `.msg-wrap` block
 *  container a turn's cards live in. `.msg-wrap` is a flex column with a gap, and
 *  the gap is the same in all three readings, so it cancels out of the drift while
 *  staying faithful to what the transcript lays out. */
function mountList(open: boolean): { wrap: HTMLElement; list: HTMLElement } {
  const wrap = document.createElement("div");
  wrap.style.cssText = `height:${String(WRAP_H)}px;overflow-y:auto;`;

  const list = document.createElement("div");
  list.className = "msg-wrap";
  for (let i = 0; i < CARDS; i++) {
    list.appendChild(runCard(i, open));
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
 *  Required, and NOT cosmetic: `.run-card`'s `vk-slide-up` starts at
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
   *  sensitivity check -- see `expectPremise`. */
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
async function measure(open: boolean): Promise<Metrics> {
  const { wrap, list } = mountList(open);
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

/** THE PREMISE, without which every assertion below is vacuous: if Chromium were
 *  not skipping any card, the first and third readings would agree for the trivial
 *  reason that both measured rendered cards, and the case would pass against any
 *  estimate at all. Inflating the estimate has to move the total, and by a lot --
 *  most of the list is off the scrollport, so the shift is tens of thousands of px. */
function expectPremise(m: Metrics): void {
  expect(
    m.listProbe - m.listSkipped,
    "cards are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(CARDS * 10);
}

/** THE PROPERTY for a COLLAPSED card: the reserve IS the floor, so the container
 *  reports one height whether its cards are skipped or rendered. Stated as the DRIFT
 *  so a failure names the px rather than two five-figure totals, with the real card
 *  height beside it so the message says which tier it was measuring. */
function expectNoDrift(m: Metrics): void {
  expectPremise(m);
  expect({
    drift: m.listSkipped - m.listRendered,
    realCardHeight: m.listRendered / CARDS,
  }).toEqual({ drift: 0, realCardHeight: m.listRendered / CARDS });
}

/** THE PROPERTY for an OPEN card: one estimate cannot be exact for both disclosure
 *  states, so what is pinned here is the SIGN. The reserve may under-state an open
 *  card -- the list grows as the reader scrolls, which is the benign direction -- and
 *  may never over-state one, because an over-stating estimate makes the container
 *  SHRINK under a reader who is scrolling through it. */
function expectNeverOverStates(m: Metrics): void {
  expectPremise(m);
  const drift = m.listSkipped - m.listRendered;
  expect(
    { overStatesBy: Math.max(drift, 0), realCardHeight: m.listRendered / CARDS },
    "the reserve must not exceed an open card's real height",
  ).toEqual({ overStatesBy: 0, realCardHeight: m.listRendered / CARDS });
}

describe("the fine-pointer tier", () => {
  it(
    "reports one height whether its collapsed cards are skipped or rendered",
    async () => {
      tier("fine");
      expectNoDrift(await measure(false));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "never over-states a card open with one step",
    async () => {
      tier("fine");
      expectNeverOverStates(await measure(true));
    },
    LOADED_BUDGET_MS,
  );
});

describe("the coarse-pointer tiers, measured at real viewport sizes", () => {
  // A coarse pointer has TWO cases -- narrow, where `01-tokens.css`'s width-keyed
  // fallback also binds, and wide, where only the attribute does -- and they measure
  // identically here, which is the finding rather than a redundancy: it is what makes
  // one expression exact on all three tiers. The block sits LAST in the file and
  // restores the size it found, because `page.viewport` has no getter and a
  // hand-copied pair would silently leave every later file measuring at the wrong
  // size.
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
    "holds for a collapsed card on a NARROW viewport",
    async () => {
      // 48rem is the fallback's own boundary and `<=` includes it, so this is the
      // widest viewport that still takes the coarse control heights with no
      // attribute set.
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(false));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a collapsed card on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(false));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "never over-states an open card on a NARROW viewport",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNeverOverStates(await measure(true));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "never over-states an open card on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNeverOverStates(await measure(true));
    },
    LOADED_BUDGET_MS,
  );
});
