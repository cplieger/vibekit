// WHERE THE RAIL PUTS THINGS, measured against the shipped stylesheet.
//
// `turn-rail.ts` publishes a position as a 0..1 fraction of the track and one CSS
// rule per element turns it into a `top`. The arithmetic is pure and tested in
// `rail-select.node.test.ts`; what only a real box can answer is whether the rule
// reproduces it — a percentage inside `calc()`, a registered custom property and a
// half-box centring term are all things a DOM emulator reports as 0.
//
// FIVE CLAIMS, and each of the last four has a control that makes it falsifiable:
//
//  - `--rail-at` lands a marker where `markerPosition` says, both ends inside.
//  - a session shorter than the track is spread from the TOP at the relaxed pitch,
//    and only one that cannot fit at it reaches the foot of the travel.
//  - the track's reserved foot clears the resume control by `--sp-2`, at both
//    pointer tiers, and the control is `--hit-floor` tall rather than `--btn-h`.
//  - two markers stay `pitchPx` apart at both tiers, the pitch read off the tier.
//  - a seam is a band BETWEEN two markers, and the caret is centred in the box the
//    marked turn's marker occupies.
//
// WHICH TURN the caret's subject is — the reader's pick while they hold one, the
// scroll-derived turn otherwise — is a property of the renderer rather than of the
// stylesheet, so it is driven against the real module in `turn-rail.test.ts`'s "the
// reader's position on a downsampled rail" block. This file sets the fractions
// itself, which is what keeps it measuring the rule.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  HERE_PX,
  MARKER_FALLBACK_PX,
  railAt,
  railMetrics,
  railSpan,
  relaxedPitch,
  markerPosition,
  selectMarkers,
} from "./rail-select.js";
import type { TurnSummary } from "./rail-merge.js";

/** Wide enough that the rail is shown and the resume control docks in its column:
 *  both turn on `@container chat-area (width >= 57.5rem)`. */
const CHAT_PX = 1120;
/** Sub-pixel slack. Every number here is a used value the engine rounds. */
const EPS = 0.5;

/** Two used values agree to the pixel.
 *
 *  `toBeCloseTo`'s second argument is a DIGIT COUNT rather than a tolerance, so a
 *  0.5 there asks for agreement to within 0.158 and reads as the opposite of what
 *  it says. */
function near(actual: number, expected: number, what: string): void {
  expect(
    Math.abs(actual - expected),
    `${what}: ${String(actual)} vs ${String(expected)}`,
  ).toBeLessThanOrEqual(EPS);
}

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(() => {
  style.remove();
});

interface Rail {
  /** The wrapper both the rail and the resume control position against. */
  outer: HTMLElement;
  rail: HTMLElement;
  resume: HTMLElement;
  /** The track's own height, which is what every fraction is a fraction of. Read
   *  per call, because `.turn-rail:empty` hides the rail and a height captured
   *  before its first marker is 0. */
  track: () => number;
  markerPx: number;
  pitchPx: number;
  /** The three tokens the reserved foot is made of, in px. */
  sp2: number;
  sp3: number;
  btnH: number;
}

let area: HTMLElement | undefined;

/** A token in PX. An unregistered custom property's computed value is its own token
 *  stream, so `getPropertyValue("--sp-2")` answers `0.5rem` and a `parseFloat`
 *  answers 0.5; assigning it to a real length property is what absolutizes it. */
function lengthOf(host: HTMLElement, token: string): number {
  const probe = document.createElement("div");
  probe.style.cssText = `position:absolute;visibility:hidden;block-size:var(${token})`;
  host.appendChild(probe);
  const px = parseFloat(getComputedStyle(probe).blockSize);
  probe.remove();
  return px;
}

/** The real nesting, because three separate rules depend on it: the container query
 *  needs `#chat-area` to BE the container, the rail's height comes from
 *  `#messages-wrap-outer`'s box through the view's flex chain, and the reserved foot
 *  is only meaningful against a resume control docked in the same column. */
function buildRail(tier: "fine" | "coarse"): Rail {
  if (tier === "coarse") {
    document.documentElement.dataset["pointer"] = "coarse";
  }
  area = document.createElement("div");
  area.id = "chat-area";
  area.style.cssText = `position:fixed;top:0;left:0;width:${String(CHAT_PX)}px;height:600px;`;
  const view = document.createElement("div");
  view.id = "chat-view";
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  const scroller = document.createElement("div");
  scroller.id = "messages-wrap";
  const messages = document.createElement("div");
  messages.id = "messages";
  scroller.appendChild(messages);
  outer.appendChild(scroller);

  const rail = document.createElement("nav");
  rail.className = "turn-rail";
  outer.appendChild(rail);

  const resume = document.createElement("button");
  resume.id = "scroll-bottom";
  resume.type = "button";
  resume.textContent = "Latest";
  outer.appendChild(resume);

  view.appendChild(outer);
  area.appendChild(view);
  document.body.appendChild(area);

  const { markerPx, pitchPx } = railMetrics(rail);
  return {
    outer,
    rail,
    resume,
    track: () => rail.clientHeight,
    markerPx,
    pitchPx,
    sp2: lengthOf(outer, "--sp-2"),
    sp3: lengthOf(outer, "--sp-3"),
    btnH: lengthOf(outer, "--btn-h"),
  };
}

afterEach(() => {
  area?.remove();
  area = undefined;
  delete document.documentElement.dataset["pointer"];
});

/** One marker, positioned the way `render()` positions it: the turn's own fraction of
 *  the span the session's size resolves to, never a bare 0..1 ramp. */
function marker(r: Rail, n: number, total: number): HTMLElement {
  const btn = document.createElement("button");
  btn.className = "rail-marker";
  btn.type = "button";
  btn.textContent = String(n);
  btn.style.setProperty("--rail-at", String(railAt(n, total, spanFor(r, total))));
  r.rail.appendChild(btn);
  return btn;
}

/** The track's height, measured through a temporary marker when the rail is still
 *  empty: `.turn-rail:empty` hides the element, so a height read before its first
 *  marker is 0 — and a span resolved against 0 is the stretched one, which is the
 *  layout these cases exist to tell apart. */
function trackOf(r: Rail): number {
  if (r.rail.childElementCount > 0) {
    return r.track();
  }
  const probe = document.createElement("button");
  probe.className = "rail-marker";
  r.rail.appendChild(probe);
  const px = r.track();
  probe.remove();
  return px;
}

/** The span `render()` would resolve for a session of `total` turns on this track. */
function spanFor(r: Rail, total: number): number {
  return railSpan(total, trackOf(r), r.markerPx);
}

/** A turn count too large to fit at the relaxed pitch, so the set takes the whole
 *  travel. DERIVED from the real track, because the crossover moves with the tier and
 *  with the reserved foot, and a hard-coded count would sit on the wrong side of it
 *  after a retune of either. */
function stretchedTotal(r: Rail): number {
  return Math.ceil(trackOf(r) / relaxedPitch(r.markerPx)) + 2;
}

/** A marker's rendered top, in the track's own frame. */
function topIn(r: Rail, el: HTMLElement): number {
  return el.getBoundingClientRect().top - r.rail.getBoundingClientRect().top;
}

function turn(n: number): TurnSummary {
  return { id: `m${String(n)}`, n, outcome: "completed", ts: n * 60_000 };
}

describe("the two pixel numbers come off the pointer tier", () => {
  // MEASURED AGAINST A REAL TRACK, and that is the whole point of the cases being
  // here. `--hit-floor` is 1.5rem / 2.75rem in the stylesheet, so a reader that takes
  // the custom property's own computed value gets the token stream back and a
  // `parseFloat` answers 1.5 — a 5.5px pitch, no downsampling at any track height and
  // markers inside each other. A stubbed `getComputedStyle` answering "24px" cannot
  // see that; only a real cascade can.
  for (const [tier, floor] of [
    ["fine", 24],
    ["coarse", 44],
  ] as const) {
    it(`reads a ${tier} pointer's floor as px`, () => {
      const r = buildRail(tier);
      expect(railMetrics(r.rail)).toEqual({ markerPx: floor, pitchPx: floor + 4 });
    });
  }

  it("falls back to the fine tier's own floor for a track the token does not reach", () => {
    // `initial` on a custom property is the guaranteed-invalid value, so every
    // `var(--hit-floor)` under it is invalid at computed-value time — the pre-layout
    // state, where the fallback is the fine floor rather than a third number.
    const r = buildRail("coarse");
    r.outer.style.setProperty("--hit-floor", "initial");

    expect(railMetrics(r.rail)).toEqual({
      markerPx: MARKER_FALLBACK_PX,
      pitchPx: MARKER_FALLBACK_PX + 4,
    });
  });
});

describe("a marker's position is its turn's own number", () => {
  it("turns --rail-at into the top the pure arithmetic computes", () => {
    const r = buildRail("fine");
    const els = [1, 2, 3].map((n) => marker(r, n, 3));

    els.forEach((el, i) => {
      const n = i + 1;
      near(topIn(r, el), markerPosition(n, 3, r.track(), r.markerPx), `marker ${String(n)}`);
    });
  });

  it("keeps both ends fully inside the track once the set takes the whole travel", () => {
    // The travel span is the track minus one marker box, which is what stops the
    // last marker hanging half out of the column.
    const r = buildRail("fine");
    const total = stretchedTotal(r);
    // The premise: this many turns cannot fit at the relaxed pitch, so the last
    // marker really is meant to reach the foot of the track.
    expect(spanFor(r, total)).toBe(1);
    const first = marker(r, 1, total);
    const last = marker(r, total, total);

    near(topIn(r, first), 0, "first marker's top");
    near(topIn(r, last) + last.getBoundingClientRect().height, r.track(), "last marker's bottom");
  });

  it("spreads a young session from the top at the relaxed pitch instead", () => {
    // THE REGRESSION, over real layout: the second of two turns used to render at the
    // foot of the track, one marker box above the resume control, with the whole axis
    // empty between the two markers.
    const r = buildRail("fine");
    const first = marker(r, 1, 2);
    const second = marker(r, 2, 2);

    near(topIn(r, first), 0, "first marker's top");
    near(topIn(r, second), relaxedPitch(r.markerPx), "second marker's top");
    // Stated as a relation rather than a number so the case survives a taller track:
    // what it denies is the marker reaching the end of the travel.
    expect(topIn(r, second)).toBeLessThan(r.track() / 2);
  });
});

describe("the track's reserved foot clears the resume control", () => {
  // RED against a foot of `--sp-3 + --hit-floor` alone: the track's bottom edge
  // would land flush on the control's top edge and the `--sp-2` clearance below
  // would read 0. Reserved UNCONDITIONALLY, so a marker does not move when the
  // control appears.
  for (const tier of ["fine", "coarse"] as const) {
    it(`leaves --sp-2 between the track and the control on a ${tier} pointer`, () => {
      const r = buildRail(tier);
      // One marker, because `.turn-rail:empty` hides the rail and a hidden track
      // has no bottom edge to measure the clearance from.
      marker(r, 1, 1);

      const railBottom = r.rail.getBoundingClientRect().bottom;
      const controlTop = r.resume.getBoundingClientRect().top;
      near(controlTop - railBottom, r.sp2, "clearance");
    });

    it(`sizes that foot from --hit-floor rather than --btn-h on a ${tier} pointer`, () => {
      // The premise the reservation is derived from: `#scroll-bottom`'s own box in
      // the rail's column is the tier's hit floor, so a `--btn-h`-based foot
      // over-reserves by 12px on a fine pointer.
      const r = buildRail(tier);
      marker(r, 1, 1);

      near(r.resume.getBoundingClientRect().height, r.markerPx, "control height");
      near(
        r.outer.getBoundingClientRect().bottom - r.rail.getBoundingClientRect().bottom,
        r.sp3 + r.markerPx + r.sp2,
        "reserved foot",
      );
      // Stated as a relation rather than a number, so the case survives a retune of
      // either token and still fails if the two are conflated.
      expect(tier === "fine" ? r.btnH !== r.markerPx : r.btnH === r.markerPx).toBe(true);
    });
  }
});

describe("two markers never overlap at the tier's own floor", () => {
  // THE COARSE CASE IS THE CONTROL, and the separation is measured against the
  // MARKER'S RENDERED BOX rather than against `railMetrics`' answer: comparing a
  // selection made at one pitch against that same pitch is a tautology, and it
  // stays green against a hard-coded 28 (measured). The box is CSS-driven, so on a
  // coarse pointer it is 44px and a 28px pitch puts targets 16px inside each other.
  for (const tier of ["fine", "coarse"] as const) {
    it(`on a ${tier} pointer, at the density the track allows`, () => {
      const r = buildRail(tier);
      const all = Array.from({ length: 60 }, (_, i) => turn(i + 1));
      // The track is measured through one marker, for `:empty`'s reason above.
      const probe = marker(r, 1, 60);
      const shown = selectMarkers(all, r.track(), r.pitchPx, new Set<number>());
      const box = probe.getBoundingClientRect().height;
      probe.remove();

      expect(shown.length).toBeGreaterThan(2);
      expect(shown.length).toBeLessThan(all.length);
      // Resolved from the token independently of the module, so a marker sized off
      // anything but the tier fails here rather than downstream.
      near(box, lengthOf(r.outer, "--hit-floor"), "marker box");

      const tops = shown.map((s) => topIn(r, marker(r, s.n, 60)));
      for (let i = 1; i < tops.length; i++) {
        // STRICTLY greater: two conforming targets need a clear between them, not
        // merely edges that touch.
        expect((tops[i] ?? 0) - (tops[i - 1] ?? 0)).toBeGreaterThan(box);
      }
    });
  }
});

describe("a seam is a band between two markers", () => {
  it("starts at the earlier marker's bottom and ends at the later's top", () => {
    const r = buildRail("fine");
    const from = marker(r, 1, 8);
    const to = marker(r, 8, 8);
    const seam = document.createElement("div");
    seam.className = "rail-seam";
    seam.setAttribute("role", "separator");
    seam.style.setProperty("--rail-from", String(railAt(1, 8, spanFor(r, 8))));
    seam.style.setProperty("--rail-to", String(railAt(8, 8, spanFor(r, 8))));
    r.rail.appendChild(seam);

    const band = seam.getBoundingClientRect();
    expect(band.height).toBeGreaterThan(0);
    near(band.top, from.getBoundingClientRect().bottom, "band top");
    near(band.bottom, to.getBoundingClientRect().top, "band bottom");
  });
});

describe("the reader's caret sits on the marked turn's own line", () => {
  /** The caret, positioned the way `hereNode` positions it. */
  function caret(r: Rail, n: number, total: number): HTMLElement {
    const node = document.createElement("div");
    node.className = "rail-here";
    node.setAttribute("aria-hidden", "true");
    node.style.setProperty("--rail-at", String(railAt(n, total, spanFor(r, total))));
    r.rail.appendChild(node);
    return node;
  }

  it("centres it inside the box that turn's marker occupies", () => {
    const r = buildRail("fine");
    const m = marker(r, 17, 40);
    const here = caret(r, 17, 40);

    const mid = (b: DOMRect): number => b.top + b.height / 2;
    near(mid(here.getBoundingClientRect()), mid(m.getBoundingClientRect()), "caret centre");
  });

  for (const tier of ["fine", "coarse"] as const) {
    it(`takes no hit-target box on a ${tier} pointer, so it competes for no slot`, () => {
      // It carries no accessible name and is not a button, so WCAG 2.5.8 does not
      // reach it — and the number is the one `rail-select.ts` centres against, so a
      // retune of `--dot-size` that left HERE_PX behind fails here.
      const r = buildRail(tier);
      const box = caret(r, 3, 9).getBoundingClientRect();

      near(box.height, HERE_PX, "caret box");
      expect(box.height).toBeLessThan(r.markerPx);
    });
  }
});
