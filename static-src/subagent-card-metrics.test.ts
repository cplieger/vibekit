// `.subagent-block`'s intrinsic-size estimate has to cover the RESTING shape both
// delegate boxes share, on every pointer tier, and only a layout measurement can check
// it: with `content-visibility: auto` plus `contain-intrinsic-size: auto <len>`, a box
// that has never rendered contributes `<len>` PLUS its own padding, border and
// `min-height`, while one that HAS contributes its remembered real size -- so a wrong
// `<len>` moves the transcript's `scrollHeight` by `(real - skipped) x (boxes not yet
// rendered)`. No source read answers which of the three regions BINDS, or whether the
// value states the content box or the border box; both are used-value questions.

import { vi, describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control. Both terms of the reserve move on the POINTER tier, which the
// attribute alone reaches, so the two coarse cases exist to PROVE nothing here keys on
// width -- `01-tokens.css` carries a width-keyed no-JS fallback for both tokens, and
// only a real resize would catch a rule that read it.
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` throws.
import { page } from "vitest/browser";

// The delegate boxes compensate their own fold and failure re-open through scroll.ts,
// which is a self-initialising singleton over a real `#messages`; the canonical mock is
// what every other suite reaching that graph uses. Nothing here folds anything -- the
// container arm is built collapsed -- so the mock only has to exist.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildSubagentCard, buildSubagentContainer } from "./fundamentals/subagent-block.js";

/** Long enough that a per-card error is a four-figure number in the list total, so
 *  a failure is unmistakable rather than a rounding argument. Also the divisor the
 *  real box height is reported through, so it has to match the card count. */
const CARDS = 200;

/** The scrollport's height. Every box past it is off-viewport and therefore
 *  skipped, which is what gives the first reading its estimate-driven total. */
const WRAP_H = 500;

/** Class that turns the skip off, so the same list can be read a second time with
 *  every box genuinely rendered. */
const FORCE = "subagent-metrics-force-render";

/** Class that replaces the estimate with an obviously wrong one, so the harness
 *  can prove the estimate is what the first reading is made of. */
const PROBE = "subagent-metrics-probe-estimate";

/** The probe's content height. Far from every real box height, so the shift it
 *  produces cannot be confused with a rounding difference. */
const PROBE_PX = 400;

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
  // (0,2,0) against `.subagent-block`'s own (0,1,0), so both win wherever they are
  // inserted.
  force = document.createElement("style");
  force.textContent = [
    `.${FORCE} .subagent-block { content-visibility: visible }`,
    `.${PROBE} .subagent-block { contain-intrinsic-size: auto ${String(PROBE_PX)}px }`,
  ].join("\n");
  document.head.appendChild(force);

  host = document.createElement("div");
  // Out of the way of anything else the page holds, and a fixed inline size so a
  // box's own width never depends on the body's flow.
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

/** The ledger the settled shapes rest with. `elapsedMs` is what earns the footer here:
 *  an aggregate `commands` with no `kindCounts` behind it earns nothing, and nothing
 *  renders it either. The row's tallest child is the `.turn-ledger-summary` BUTTON,
 *  the term `--hit-floor` is in the expression for -- and the reserve does NOT move with
 *  that trigger's info `i` or its `.sr-only` name: measured, the button is 24px with
 *  those two children and 24px with both removed, and the footer 33px either way,
 *  because an `--icon-ui` glyph is shorter than the `--hit-floor` the button is already
 *  floored at and a visually-hidden span has no box. */
const LEDGER = { elapsedMs: 3_000 } as const;

/** What the transcript builds for a LEAF delegate: an identity row that is itself
 *  the anchor to the delegate's own page, and nothing under it. A settled card's tail
 *  is removed by the builder, so the resting shape is header + foot. */
function settledCard(i: number): HTMLElement {
  const sa = buildSubagentCard(`review-${String(i)}`, "completed", {
    open: { href: `/chat/c-1/subagent/sub-${String(i)}`, open: () => undefined },
  });
  sa.setSummary(LEDGER);
  return sa.root;
}

/** The same card mid-flight: the tail is present and EMPTY (a card is built before
 *  its delegate has said anything) and the foot is withheld by `:empty`, so this is
 *  the identity row alone. */
function runningCard(i: number): HTMLElement {
  const sa = buildSubagentCard(`review-${String(i)}`, "in_progress", {
    open: { href: `/chat/c-1/subagent/sub-${String(i)}`, open: () => undefined },
  });
  return sa.root;
}

/** A COLLAPSED pipeline container over two stage cards: the second resting shape
 *  the ONE reserve has to serve. Its body is driven to zero by the disclosure, so
 *  what is left is the identity row and the foot -- the settled card's shape. */
function collapsedContainer(i: number): HTMLElement {
  const c = buildSubagentContainer(`Subagent pipeline \u00b7 2 stages`, "completed", {
    startOpen: false,
  });
  c.setSummary(LEDGER);
  c.body.append(settledCard(i * 2), settledCard(i * 2 + 1));
  return c.root;
}

type Build = (i: number) => HTMLElement;

/** The production nesting: a fixed-height scroller holding the `.msg-wrap` block
 *  container a turn's boxes live in. `.msg-wrap` is a flex column with a gap, and
 *  the gap is the same in all three readings, so it cancels out of the drift while
 *  staying faithful to what the transcript lays out. */
function mountList(build: Build): { wrap: HTMLElement; list: HTMLElement } {
  const wrap = document.createElement("div");
  wrap.style.cssText = `height:${String(WRAP_H)}px;overflow-y:auto;`;

  const list = document.createElement("div");
  list.className = "msg-wrap";
  for (let i = 0; i < CARDS; i++) {
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
 *  Required, and NOT cosmetic: `.subagent-block`'s `vk-slide-up` starts at
 *  `translateY(6px)`, and a transformed descendant extends its container's
 *  SCROLLABLE OVERFLOW, so the container's `scrollHeight` reads 6px high for the
 *  0.25s the animation runs. Finishing rather than awaiting, so the reading does
 *  not depend on a duration token.
 *
 *  AN INFINITE ANIMATION IS SKIPPED, and this file is the first of the metrics
 *  suites to need that: a RUNNING card's `.subagent-spinner` is `vk-spin` on an
 *  infinite iteration count, and `Animation.finish()` throws `InvalidStateError`
 *  on one ("Cannot finish Animation with an infinite target effect end"), which
 *  takes the whole reading down before any assertion runs. It is also not one of
 *  the animations this exists for: the spin is a `rotate` on a glyph inside the
 *  card's own `overflow: hidden`, so it cannot reach the list's scrollable
 *  overflow, where the card's own `translateY` can. */
function finishAnimations(root: Element): void {
  for (const anim of root.getAnimations({ subtree: true })) {
    if (Number.isFinite(anim.effect?.getComputedTiming().endTime ?? Infinity)) {
      anim.finish();
    }
  }
}

interface Metrics {
  /** The container's height with every off-screen box on the estimate. */
  readonly listSkipped: number;
  /** The same, with the estimate replaced by `PROBE_PX`. The harness's own
   *  sensitivity check -- see `expectPremise`. */
  readonly listProbe: number;
  /** Its height with every box genuinely rendered. */
  readonly listRendered: number;
}

/** Read the same 200-box list three times: on the shipped estimate, on a
 *  deliberately wrong one, and with the skip turned off.
 *
 *  The list TOTAL is the only honest instrument here. A per-box
 *  `getBoundingClientRect()` is not: measured in Chromium 151, querying an element
 *  inside a skipped subtree reports its REAL box, so a per-box assertion would
 *  compare a rendered box against a rendered box and pass against the defect. */
async function measure(build: Build): Promise<Metrics> {
  const { wrap, list } = mountList(build);
  await frame();
  finishAnimations(wrap);
  await frame();
  const listSkipped = list.scrollHeight;

  // Still skipped, so this reads the fallback rather than a remembered size. Done
  // BEFORE the force-render, because a box that has been rendered once remembers
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
 *  not skipping any box, the first and third readings would agree for the trivial
 *  reason that both measured rendered boxes, and the case would pass against any
 *  estimate at all. Inflating the estimate has to move the total, and by a lot --
 *  most of the list is off the scrollport, so the shift is tens of thousands of px. */
function expectPremise(m: Metrics): void {
  expect(
    m.listProbe - m.listSkipped,
    "boxes are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(CARDS * 10);
}

/** THE PROPERTY for a RESTING box -- a settled card or a collapsed container: the
 *  reserve IS that shape, so the container reports one height whether its boxes are
 *  skipped or rendered. The two resting shapes measure identically, so both arms assert
 *  a drift of exactly 0. Stated as the DRIFT so a failure names the px rather than two
 *  five-figure totals. The per-box height rides the MESSAGE rather than the compared
 *  object: it says which tier failed, and asserting it would pin a font-dependent
 *  number (Chromium 152 measures 82.94px fine and 102.94px coarse). */
function expectNoDrift(m: Metrics): void {
  expectPremise(m);
  expect(
    m.listSkipped - m.listRendered,
    `drift at ${String(m.listRendered / CARDS)}px per box`,
  ).toBe(0);
}

/** THE PROPERTY for a RUNNING card (49.94px fine, 57.94px coarse): one estimate cannot be
 *  exact for both lifecycle states, and this is the one the reserve deliberately
 *  over-states, because the POPULATION is settled. Census over the chat files on one
 *  live volume, applying `earnsTurnFooter`'s conditions to each invocation: of 527 leaf
 *  cards, 521 are settled with a footer, 1 settled without one and 5 running, and all
 *  298 pipeline drivers are settled. The fallback is consulted only before a box has
 *  rendered once, and a running card is the newest work at the live edge. The SIGN is
 *  what is pinned, and it is what fails if the running shape grows into the reserve;
 *  the magnitude rides the message, for `expectNoDrift`'s reason. */
function expectOverStates(m: Metrics): void {
  expectPremise(m);
  const drift = m.listSkipped - m.listRendered;
  expect(
    drift > 0,
    `the reserve is the SETTLED shape, so a running card is over-stated: ${String(Math.round(drift / CARDS))}px per card at ${String(m.listRendered / CARDS)}px per box`,
  ).toBe(true);
}

// RED CHECK, observed before any of this was trusted: restoring `contain-intrinsic-size:
// auto 4rem` (66px rendered) turns the five SETTLED and CONTAINER cases red and leaves
// the two RUNNING cases green -- correctly, since a value below a running card's real
// height cannot stop over-stating one. The running arm needs the opposite probe, and
// `--subagent-content: 30px` (32px rendered, under the running card's 38px) reddens it.
describe("the fine-pointer tier", () => {
  it(
    "reports one height whether its settled cards are skipped or rendered",
    async () => {
      tier("fine");
      expectNoDrift(await measure(settledCard));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "reports one height for a COLLAPSED pipeline container too, on the same reserve",
    async () => {
      tier("fine");
      expectNoDrift(await measure(collapsedContainer));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "over-states a RUNNING card, which is the accepted half of the trade",
    async () => {
      tier("fine");
      expectOverStates(await measure(runningCard));
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
    "holds for a settled card on a NARROW viewport",
    async () => {
      // 48rem is the fallback's own boundary and `<=` includes it, so this is the
      // widest viewport that still takes the coarse control heights with no
      // attribute set.
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(settledCard));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a settled card on a WIDE viewport",
    async () => {
      await page.viewport(1024, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        1024, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(settledCard));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "holds for a COLLAPSED pipeline container",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectNoDrift(await measure(collapsedContainer));
    },
    LOADED_BUDGET_MS,
  );

  it(
    "still over-states a RUNNING card",
    async () => {
      await page.viewport(768, 900);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        768, 900,
      ]);
      tier("coarse");
      expectOverStates(await measure(runningCard));
    },
    LOADED_BUDGET_MS,
  );
});
