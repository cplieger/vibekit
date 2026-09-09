// ---------------------------------------------------------------------------
// `block-heights.ts`'s estimate table is a SHADOW of the stylesheet, and this is what
// holds it there.
//
// The module prices an unmounted block from literals, and it has to: the reserves are
// custom properties, and `getPropertyValue` on an unregistered one answers the
// substituted token stream rather than a used length (`--btn-h` is `2.25rem`,
// `--run-card-content` the whole `calc()`), so resolving either to px needs a probe
// element plus layout — which a pure pricing module cannot take. So the literals stay,
// and this file is the drift guard that makes them a shadow rather than a second
// source of truth.
//
// WHAT IS BEING ASSERTED IS THE RULE, NOT THE CONTENTS. A skipped element's box is
// `max(min-height, contain-intrinsic-size + padding-block + border-block)`, which is a
// function of the CSS alone — so the fixtures below need the class list their real
// builder gives them and nothing more, and every case names which term moved when it
// fails. The one entry with no reserve to shadow, `thinking`, is the exception and is
// measured as a REAL height off a real sealed trace.
//
// THE INSTRUMENT IS THE PER-ELEMENT RECT OF THE LAST INSTANCE, which is the one place
// that read is legitimate. `files-row-metrics.test.ts` records the instrument fact
// this appears to break — querying an element INSIDE a skipped subtree reports its
// real box — and the difference is what is being measured: here the element is the
// containment ROOT, whose own rect IS the rendered-skipped box, rather than something
// inside one. Both readings are checked against a container total: the four numbers
// this file pins for `subagentCard` and `runCard` are the ones
// `subagent-card-metrics.test.ts` and `run-card-metrics.test.ts` independently drive
// to a drift of exactly zero over a 200-box list.
//
// ONE CASE CANNOT USE THAT INSTRUMENT AND SAYS SO: the PIPELINE case, whose subject is
// what a stage card inside a collapsed container contributes. Neither per-element
// reading can see that — a SKIPPED container's own rect is its reserve whatever it
// holds, and a rect on the stage card inside reports that card's real box (measured at
// 71px for a card contributing 0) — so it reads the CONTAINER TOTAL three times
// instead, through `readTotals`.
//
// THE PREMISE, without which every reserve case reads a REAL box and passes against
// any literal at all: `checkVisibility({ contentVisibilityAuto: true })` must be FALSE
// on a child of the last instance and TRUE on a child of the first. `readTotals` states
// the same premise the metrics suites do, as an inflated estimate moving the total.
//
// RED CHECKS, each observed one at a time and restored before the next:
//   - shift ONE table entry by 1px      -> that entry's cases red, the rest green
//     (`fine.subagentCard` 71 -> 72: the fine reserve case AND the fine pipeline case,
//     2 failed / 14 passed, coarse untouched)
//   - restore `toolCard: 40`            -> the fine and coarse tool cases red at 2 and 6px
//   - force `content-visibility: visible` on the subjects -> every reserve case red on
//     the PREMISE rather than on the number, which is what proves the premise is load-bearing
//   - give the COLLAPSED body its padding back (undo `14-tools.css`'s
//     `.subagent-body[aria-hidden="true"] { padding-block: 0 }`) -> only the two
//     pipeline cases red, at 92px against 71 and 120 against 99 with a drift of
//     -2,142 / -2,184. The reserve cases stay GREEN, because the DECLARED reserve did
//     not move: that is what makes the pipeline case non-redundant with them.
//   - build the container OPEN -> the two pipeline cases red at 163px against 71 and
//     219 against 99, which is the opposite probe proving the reading can see a stage
//     card's contribution at all
//
// Follows `tool-box-height.test.ts` for reading box facts off the assembled stylesheet
// and `css-rules.ts` for assembling it through `?raw` rather than the gitignored bundle.
//
// 16 cases: 8 per tier, through one shared `tierCases(name, enter)` helper invoked from
// both tier describes, so neither tier can be pinned while the other is forgotten.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` throws.
import { page } from "vitest/browser";

// Two of the three real builders reach `scroll.ts`, a self-initialising singleton over
// a real `#messages`; the canonical mock is what every other suite reaching that graph
// uses. Nothing here folds anything, so the mock only has to exist.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { BLOCK_ESTIMATE_PX, ROW_GAP_PX, type BlockEstimates } from "./block-heights.js";
import { buildSubagentCard, buildSubagentContainer } from "./fundamentals/subagent-block.js";
import { buildRunCard } from "./fundamentals/run-card.js";
import { buildReasoning } from "./fundamentals/reasoning.js";

/** Enough instances that the last one is far past the scrollport whatever the box
 *  resolves to: the smallest reserve here is 38px, so 120 of them is 4,560px against a
 *  400px scroller. */
const INSTANCES = 120;

/** The scrollport's height. Every instance past it is off-viewport and therefore
 *  skipped, which is what makes the LAST one's rect the rendered-skipped box. */
const WRAP_H = 400;

/** Per-case timeout, matching the metrics suites: a case's cost is four rAF turns, and
 *  a loaded `npm test` prices a turn at hundreds of ms. */
const LOADED_BUDGET_MS = 30_000;

/** Class that turns the skip off, so the same list reads a second time with every box
 *  genuinely rendered. Only the PIPELINE case needs it — see `readTotals`. */
const FORCE = "block-heights-force-render";

let style: HTMLStyleElement;
let force: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  // (0,2,0) against `.subagent-block`'s own (0,1,0), so it wins wherever it is inserted.
  force = document.createElement("style");
  force.textContent = `.${FORCE} .subagent-block { content-visibility: visible }`;
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

/** A child for the premise probe. Its shape is irrelevant to the box being measured
 *  (see the header) and its only job is to be something
 *  `checkVisibility({ contentVisibilityAuto: true })` can be asked about. */
function marker(): HTMLElement {
  const s = document.createElement("span");
  s.textContent = "x";
  return s;
}

/** `.msg-row`, as `messages.ts` `makeRow` builds one: `el("div", { className: "msg-row" })`
 *  and nothing else, so the class list here is the whole of it. Hand-built because that
 *  builder is private to a module this file must not import — the transcript's own
 *  entry point, whose graph is the whole feature layer. */
function msgRow(): HTMLElement {
  const row = document.createElement("div");
  row.className = "msg-row";
  row.appendChild(marker());
  return row;
}

/** The empty PAD a `padBlocks` slot mounts: the same element plus `is-empty`, which
 *  `13-messages.css` takes out of flow entirely. */
function emptyRow(): HTMLElement {
  const row = msgRow();
  row.classList.add("is-empty");
  return row;
}

/** A claim-only `.tool-call`, in the class list `tool-card.ts` gives one: the card
 *  class plus its depth-1 tier (`tool-card.ts` `buildToolCard`, `tool-call
 *  tool-depth1-${depth1}`, which is `none` for a `read`). Hand-built for `msgRow`'s
 *  reason — that builder's graph reaches the editor and the tab store. */
function toolCard(): HTMLElement {
  const card = document.createElement("div");
  card.className = "tool-call tool-depth1-none";
  card.appendChild(marker());
  return card;
}

/** A settled delegate card, from its real builder. */
function subagentCard(i: number): HTMLElement {
  const sa = buildSubagentCard(`review-${String(i)}`, "completed", {
    open: { href: `/chat/c-1/subagent/sub-${String(i)}`, open: () => undefined },
  });
  sa.setSummary({ commands: 2, elapsedMs: 3_000 });
  return sa.root;
}

/** A collapsed run card, from its real builder. The disclosure state does not reach
 *  the box being measured — the reserve does — but it is what the table's number
 *  MEANS, so the fixture states it. */
function runCard(i: number): HTMLElement {
  const view = buildRunCard(`wf-${String(i)}`, `recipe-${String(i)}`, () => undefined, {
    wasOpen: () => undefined,
    defaultOpen: false,
    onOpenChange: () => undefined,
  });
  return view.root;
}

/** A COLLAPSED pipeline container over `stages` settled stage cards, from its real
 *  builder — the box a pipeline's DRIVER block mounts. Its stage cards are the subject:
 *  `subagentCard` is the price of a WHOLE pipeline, so what has to hold is that the
 *  cards inside contribute nothing to the box around them. */
function pipelineBox(stages: number): (i: number) => HTMLElement {
  return (i: number): HTMLElement => {
    const c = buildSubagentContainer(
      `Subagent pipeline \u00b7 ${String(stages)} stages`,
      "completed",
      {
        startOpen: false,
      },
    );
    c.setSummary({ commands: 2, elapsedMs: 3_000 });
    for (let s = 0; s < stages; s++) {
      c.body.append(subagentCard(i * 100 + s));
    }
    return c.root;
  };
}

/** A SEALED reasoning trace, from its real builder: the one fixture whose contents
 *  matter, because there is no reserve here and the value is the element's real
 *  collapsed height. */
function sealedTrace(): HTMLElement {
  const r = buildReasoning("a few sentences of a settled trace", false, false);
  r.settle();
  return r.root;
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

/** Jump every finite entry animation to its end. An INFINITE one is skipped, because
 *  `Animation.finish()` throws on one; none of the fixtures here carries a spinner,
 *  but the guard costs nothing and a fixture that grew one would otherwise take the
 *  reading down rather than fail an assertion. */
function finishAnimations(root: Element): void {
  for (const anim of root.getAnimations({ subtree: true })) {
    if (Number.isFinite(anim.effect?.getComputedTiming().endTime ?? Infinity)) {
      anim.finish();
    }
  }
}

interface Reading {
  /** The rendered-skipped box: the LAST instance's own rect. */
  readonly box: number;
  /** The FIRST instance's rect, which is real. Reported so a case that fails on the
   *  premise says what it was reading instead. */
  readonly realBox: number;
  /** The premise. Both halves, because "nothing is skipped" and "everything is
   *  skipped" are different failures. */
  readonly premise: { readonly firstRendered: boolean; readonly lastSkipped: boolean };
  /** Every term the box is made of, so a red case names which one moved. */
  readonly terms: {
    readonly contentVisibility: string;
    readonly containIntrinsicBlockSize: string;
    readonly paddingBlock: number;
    readonly borderBlock: number;
    readonly minBlockSize: string;
  };
}

async function read(build: (i: number) => HTMLElement): Promise<Reading> {
  const wrap = document.createElement("div");
  wrap.style.cssText = `height:${String(WRAP_H)}px;overflow-y:auto;`;
  const list = document.createElement("div");
  list.className = "msg-wrap";
  for (let i = 0; i < INSTANCES; i++) {
    list.appendChild(build(i));
  }
  wrap.appendChild(list);
  host.replaceChildren(wrap);

  await frame();
  finishAnimations(wrap);
  await frame();

  const first = list.firstElementChild as HTMLElement;
  const last = list.lastElementChild as HTMLElement;
  const cs = getComputedStyle(last);
  const probe = (el: Element | null): boolean =>
    el !== null && el.checkVisibility({ contentVisibilityAuto: true });

  return {
    box: Number(last.getBoundingClientRect().height.toFixed(2)),
    realBox: Number(first.getBoundingClientRect().height.toFixed(2)),
    premise: {
      firstRendered: probe(first.firstElementChild),
      lastSkipped: !probe(last.firstElementChild),
    },
    terms: {
      contentVisibility: cs.contentVisibility,
      containIntrinsicBlockSize: cs.containIntrinsicBlockSize,
      paddingBlock: Number.parseFloat(cs.paddingBlockStart) + Number.parseFloat(cs.paddingBlockEnd),
      borderBlock:
        Number.parseFloat(cs.borderBlockStartWidth) + Number.parseFloat(cs.borderBlockEndWidth),
      minBlockSize: cs.minBlockSize,
    },
  };
}

interface Totals {
  /** The box each instance resolves to when RENDERED, the list's row gaps taken back
   *  out. This is the number a stage card can move. */
  readonly perBox: number;
  /** Skipped total minus rendered total: 0 when the estimate IS the resting shape. */
  readonly drift: number;
  /** The collapsed body's own terms, echoed so a red case names what moved. */
  readonly bodyTerms: { readonly contentVisibility: string; readonly blockSize: string };
}

/** How far an inflated estimate has to move the total before the readings above can be
 *  believed. Most of the list is off the scrollport, so the real shift is tens of
 *  thousands of px; this is a floor, not an expectation. */
const PREMISE_FLOOR = INSTANCES * 10;

/** The estimate the premise probe substitutes. Far from every real box height here, so
 *  the shift it produces cannot be a rounding difference. */
const PROBE_PX = 400;

/** The CONTAINER TOTAL over one list, read three times: on the estimate, on a
 *  deliberately wrong one, and with the skip forced off.
 *
 *  THE ONLY INSTRUMENT THAT CAN SEE WHAT A STAGE CARD CONTRIBUTES, which is why the
 *  pipeline case does not use `read` above. Neither per-element reading can: a SKIPPED
 *  container's own rect is its reserve whatever it holds, and a rect taken on the stage
 *  card INSIDE the collapsed body reports that card's REAL box — measured at 71px for a
 *  card contributing 0 — because Chromium answers with the real box for an element
 *  inside a skipped or hidden subtree. Only the container the boxes sit in can tell a
 *  stage that costs nothing from one that costs a card.
 *
 *  The probe reading is taken BEFORE the force-render, because a box that has rendered
 *  once remembers its real size and stops consulting the fallback. */
async function readTotals(build: (i: number) => HTMLElement): Promise<Totals> {
  const wrap = document.createElement("div");
  wrap.style.cssText = `height:${String(WRAP_H)}px;overflow-y:auto;`;
  const list = document.createElement("div");
  list.className = "msg-wrap";
  for (let i = 0; i < INSTANCES; i++) {
    list.appendChild(build(i));
  }
  wrap.appendChild(list);
  host.replaceChildren(wrap);

  await frame();
  finishAnimations(wrap);
  await frame();
  const skipped = list.scrollHeight;
  const body = list.firstElementChild?.querySelector(".subagent-body") ?? null;
  const bodyCS = body === null ? null : getComputedStyle(body);

  const probeStyle = document.createElement("style");
  probeStyle.textContent = `.msg-wrap .subagent-block { contain-intrinsic-size: auto ${String(PROBE_PX)}px }`;
  document.head.appendChild(probeStyle);
  await frame();
  const probed = list.scrollHeight;
  probeStyle.remove();

  list.classList.add(FORCE);
  await frame();
  finishAnimations(wrap);
  await frame();
  const rendered = list.scrollHeight;
  list.classList.remove(FORCE);

  expect(
    probed - skipped,
    "boxes are being skipped, and the estimate is what their height is made of",
  ).toBeGreaterThan(PREMISE_FLOOR);

  return {
    // `.msg-wrap` puts one gap BETWEEN instances, so N boxes carry N-1 of them.
    perBox: (rendered - (INSTANCES - 1) * ROW_GAP_PX) / INSTANCES,
    drift: skipped - rendered,
    bodyTerms: {
      contentVisibility: bodyCS?.contentVisibility ?? "(no body)",
      blockSize: bodyCS?.blockSize ?? "(no body)",
    },
  };
}

/** Assert one entry, premise first. The received `terms` are echoed into the expected
 *  object so a failure prints them beside the number without asserting them. */
function expectShadows(r: Reading, want: number, label: string): void {
  expect(r.premise, `${label}: the last instance's contents are being skipped`).toEqual({
    firstRendered: true,
    lastSkipped: true,
  });
  expect({ box: r.box, ...r.terms }, `${label}: the rendered-skipped box`).toEqual({
    box: want,
    ...r.terms,
  });
}

/** Set the pointer tier the way `pointer-tier.ts` does. */
function tier(name: "fine" | "coarse"): void {
  document.documentElement.dataset["pointer"] = name;
}

/** The cases every tier runs, so neither tier can be pinned and the other forgotten. */
function tierCases(name: "fine" | "coarse", enter: () => Promise<void>): void {
  const est = (): BlockEstimates => BLOCK_ESTIMATE_PX[name];

  it(
    "prices `text` and `row` at what .msg-row reserves",
    async () => {
      await enter();
      const r = await read(msgRow);
      expectShadows(r, est().text, "text");
      // A blockless message mounts ONE row, so the two entries answer for the same
      // element and cannot legitimately differ.
      expectShadows(r, est().row, "row");
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `emptyText` at nothing, because the pad is out of flow",
    async () => {
      await enter();
      const r = await read(emptyRow);
      // The one entry with NO premise to state: `display: none` generates no box at
      // all, so nothing is being skipped and `checkVisibility` answers false for the
      // first instance as well as the last. The rule itself is the assertion.
      expect(getComputedStyle(host.querySelector(".msg-row.is-empty")!).display).toBe("none");
      expect({ box: r.box, realBox: r.realBox }).toEqual({ box: 0, realBox: 0 });
      expect(est().emptyText).toBe(0);
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `toolCard` at .tool-call's reserve plus its border",
    async () => {
      await enter();
      expectShadows(await read(toolCard), est().toolCard, "toolCard");
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `runCard` at .run-card's reserve plus its border",
    async () => {
      await enter();
      expectShadows(await read(runCard), est().runCard, "runCard");
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `subagentCard` at .subagent-block's reserve plus its border",
    async () => {
      await enter();
      expectShadows(await read(subagentCard), est().subagentCard, "subagentCard");
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices a whole PIPELINE at `subagentCard`, whatever its stage count",
    async () => {
      await enter();
      // The entry `block-heights.ts` charges at a pipeline's DRIVER block, where the
      // container stands. What makes ONE card the price of a whole pipeline is that a
      // stage card inside the collapsed body contributes nothing to the box around it —
      // `.subagent-block.collapsed > .subagent-body` is `content-visibility: hidden` at
      // the disclosure controller's inline height 0 — so the box does not grow with the
      // count. Read at ONE stage and at THREE: two extra cards would show up as roughly
      // +158px per box.
      const one = await readTotals(pipelineBox(1));
      const three = await readTotals(pipelineBox(3));
      expect(
        {
          oneStage: one.perBox,
          threeStages: three.perBox,
          oneStageDrift: one.drift,
          threeStagesDrift: three.drift,
          ...three.bodyTerms,
        },
        "a pipeline is one card, and its stages are out of the box's layout",
      ).toEqual({
        oneStage: est().subagentCard,
        threeStages: est().subagentCard,
        oneStageDrift: 0,
        threeStagesDrift: 0,
        ...three.bodyTerms,
      });
    },
    LOADED_BUDGET_MS,
  );

  it(
    "prices `thinking` at a sealed trace's REAL collapsed height, there being no reserve",
    async () => {
      await enter();
      const r = await read(sealedTrace);
      // The premise here is the OPPOSITE one, and it is what makes a real per-element
      // rect legitimate: `.reasoning-block` declares no `content-visibility`, so
      // nothing is skipped, there is no reserve to shadow, and both instances report
      // their real box. A rule that ever gave this element `auto` would fail here.
      expect(
        {
          contentVisibility: r.terms.contentVisibility,
          reserve: r.terms.containIntrinsicBlockSize,
          bothReal: r.box === r.realBox,
        },
        "a sealed trace is never skipped, so its price is a measured height",
      ).toEqual({ contentVisibility: "visible", reserve: "none", bothReal: true });
      expect({ height: r.box, ...r.terms }, "thinking: the collapsed summary row").toEqual({
        height: est().thinking,
        ...r.terms,
      });
    },
    LOADED_BUDGET_MS,
  );

  it(
    "puts ROW_GAP_PX on both levels the module claims it serves",
    async () => {
      await enter();
      // The module's own comment says "one value for both levels", which is itself an
      // assertion nothing held. Read off a mounted instance of each, because `--sp-3`
      // reads back as a token rather than a length.
      const wrap = document.createElement("div");
      wrap.className = "msg-wrap";
      wrap.appendChild(marker());
      const body = document.createElement("div");
      body.className = "turn-body";
      body.appendChild(marker());
      host.replaceChildren(wrap, body);
      await frame();
      expect({
        msgWrap: Number.parseFloat(getComputedStyle(wrap).rowGap),
        turnBody: Number.parseFloat(getComputedStyle(body).rowGap),
      }).toEqual({ msgWrap: ROW_GAP_PX, turnBody: ROW_GAP_PX });
    },
    LOADED_BUDGET_MS,
  );
}

describe("the fine-pointer tier", () => {
  tierCases("fine", async () => {
    tier("fine");
    await Promise.resolve();
  });
});

describe("the coarse-pointer tier, measured at a real viewport size", () => {
  // The block sits LAST in the file and restores the size it found, because
  // `page.viewport` has no getter and a hand-copied pair would silently leave every
  // later file measuring at the wrong size. 768px is the widest viewport that also
  // engages `01-tokens.css`'s width-keyed no-JS fallback, so the attribute and the
  // fallback agree here rather than one masking the other.
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
  });

  afterAll(async () => {
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  tierCases("coarse", async () => {
    await page.viewport(768, 900);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      768, 900,
    ]);
    tier("coarse");
  });
});
