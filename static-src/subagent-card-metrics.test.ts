// ---------------------------------------------------------------------------
// `.subagent-block`'s intrinsic-size estimate has to cover the RESTING shape both
// delegate boxes share, on every pointer tier.
//
// Why this can only be a layout measurement. With `content-visibility: auto` plus
// `contain-intrinsic-size: auto <len>`, a box that has never been rendered
// contributes `<len>` PLUS its own padding, border and `min-height` to layout, while
// one that HAS been rendered contributes its remembered real size. So a wrong
// `<len>` moves the transcript's `scrollHeight` by `(real - skipped) x (boxes not yet
// rendered)`. No source read can see that: the resting height is a sum over three
// regions, two of which resolve a control-height token, and the questions are which
// of them BINDS and whether the value states the content box or the border box.
// Both are used-value questions.
//
// MEASURED over this suite's own 200-box list, against the unfixed
// `contain-intrinsic-size: auto 4rem` and then against the fix (Chromium 151). The
// PER-BOX figures are the durable ones, since a list total is also a function of how
// many boxes the scrollport had already rendered:
//
//   tier / shape        real box   a skipped box was   is now
//   fine, settled       71px       66px                71px
//   fine, container     71px       66px                71px
//   fine, running       38px       66px                71px
//   coarse, settled     99px       66px                99px
//   coarse, container   99px       66px                99px
//   coarse, running     46px       66px                99px
//
// So `auto 4rem` UNDER-stated the resting shape by 5px on the fine tier and 33px on
// the coarse one, and the corrected value over-states a RUNNING card by 33 / 53px --
// the trade below. The list DRIFTS the FIVE resting cases failed at, exactly, are
// -895px (fine) and -5,808px (coarse), against 0 now; the two running cases moved
// from +24 to +29 px per card (fine) and stayed positive on the coarse tier. Five
// cases over four tier-and-shape combinations: the two coarse SETTLED viewports are
// one combination measured twice, which is the width-independence control below.
//
// The two coarse viewports read identically -- narrow (768px) and wide (1024px) --
// which is the finding rather than a redundancy: nothing on this box keys on width,
// only on the pointer tier, so ONE expression is exact on both.
//
// THREE ARMS, and the arms are swapped relative to `run-card-metrics.test.ts`. A
// delegate box has two RESTING shapes -- a settled card with its ledger foot, and a
// COLLAPSED pipeline container, which measure identically because
// `.subagent-block.collapsed > .subagent-body` is driven to zero -- so the reserve is
// exact for both and those two arms assert a drift of exactly 0. The RUNNING card is
// the shape one reserve cannot also be exact for, and its arm asserts the SIGN with
// the per-card figure in the message: the reserve OVER-states a running card, which
// is the inverse of the trade `--run-card-content` makes.
//
// THE TRADE IS DELIBERATE AND THE POPULATION IS WHY. A run card's two states are
// DISCLOSURE states, both of which persist throughout history in unknowable
// proportions, so that reserve takes the floor and never over-states. A delegate's
// two states are LIFECYCLE states and only one of them persists: census over the 107
// chat files on one live volume, applying `earnsTurnFooter`'s own conditions to each
// invocation's persisted `duration_ms` and member tool calls, found 372 settled cards
// with a footer (96.6%), 8 settled without one (2.1%) and 5 running (1.3%), plus 195
// pipeline drivers, all settled. The fallback is consulted only before a box has
// rendered once (`auto` remembers the real size afterwards), so its population is the
// boxes the reader has not reached -- history, which is settled -- while a RUNNING
// card is by construction the newest delegated work, at the live edge where the
// reader already is, so it renders on arrival and its fallback is never consulted at
// all. The reachable residual is a delegate still running in a chat the reader has
// scrolled away from within the same view: one card, 33px, for the length of its turn.
//
// THE RUNNING ARM IS NOT A LOOSER ASSERTION, it is the guard that catches the running
// shape itself changing: a tail that gained resting padding, or a foot that stopped
// hiding while `:empty`, would take that drift to zero or below and fail here.
//
// RED CHECK, observed before any of this was trusted: restoring `auto 4rem` (66px
// rendered) turns the five SETTLED and CONTAINER cases red at the drifts tabled above
// and leaves the two RUNNING cases green -- correctly, since a value below a running
// card's real height cannot stop over-stating one. So the running arm needs the
// opposite probe, and `--subagent-content: 30px` (32px rendered, under the running
// card's 38px) is what reddens it.
//
// Follows `run-card-metrics.test.ts` for the harness and `files-row-metrics.test.ts`
// for the three-reading shape and its two instrument facts, and `css-rules.ts` for
// reading the shipped stylesheet through `?raw` rather than the gitignored bundle.
// ---------------------------------------------------------------------------

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

/** The ledger the settled shapes rest with. `commands` and `elapsedMs` are what
 *  `earnsTurnFooter` admits, and the row's tallest child is the
 *  `.turn-ledger-summary` BUTTON -- which is the term `--hit-floor` is in the
 *  expression for.
 *
 *  THE RESERVE DID NOT MOVE when the trigger gained the info `i` and its `.sr-only`
 *  name, and that is measured rather than inferred from a green run: the button is
 *  24px with those two children and 24px with both removed, and the footer 33px
 *  either way, because an `--icon-ui` glyph is shorter than the `--hit-floor` the
 *  button is already floored at and a visually-hidden span has no box. */
const LEDGER = { commands: 2, elapsedMs: 3_000 } as const;

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
 *  skipped or rendered. Stated as the DRIFT so a failure names the px rather than two
 *  five-figure totals, with the real box height beside it so the message says which
 *  tier it was measuring. */
function expectNoDrift(m: Metrics): void {
  expectPremise(m);
  expect({
    drift: m.listSkipped - m.listRendered,
    realBoxHeight: m.listRendered / CARDS,
  }).toEqual({ drift: 0, realBoxHeight: m.listRendered / CARDS });
}

/** THE PROPERTY for a RUNNING card: one estimate cannot be exact for both lifecycle
 *  states, and this is the state the reserve deliberately over-states -- see the trade
 *  in the header. Pinned as the SIGN plus the per-card figure, so the case fails if
 *  the running shape itself grows into the reserve. */
function expectOverStates(m: Metrics): void {
  expectPremise(m);
  const drift = m.listSkipped - m.listRendered;
  expect(
    {
      overStates: drift > 0,
      perCard: Math.round(drift / CARDS),
      realBoxHeight: m.listRendered / CARDS,
    },
    "the reserve is the SETTLED shape, so a running card is over-stated",
  ).toEqual({
    overStates: true,
    perCard: Math.round(drift / CARDS),
    realBoxHeight: m.listRendered / CARDS,
  });
}

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
