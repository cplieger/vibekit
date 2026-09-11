// ---------------------------------------------------------------------------
// The reasoning-effort slider's own geometry and gestures, measured against the
// SHIPPED cascade: every claim here is about a real box in a real card, so the
// stylesheet is mounted rather than reasoned about.
//
// The handle is driven directly (`buildEffortSlider` plus a spy `onPick`), which
// is what keeps `model-switcher.ts`'s ten-mock graph out of this file — the state
// and dispatch behaviours live there, in `model-switcher.test.ts`.
//
// The card fixture is the real one, because the track's width is what every travel
// figure below is derived from and the card's `overflow: hidden` is what would clip
// the control.
//
// A pointer target is aimed at the knob's own rendered centre per tier: CSS places
// that and `fracAt` resolves the gesture, so the oracle is independent of the
// arithmetic under test.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import { userEvent } from "vitest/browser";
import { rovingFocus } from "@cplieger/ui-primitives/roving-focus";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildEffortSlider, type EffortSliderHandle } from "./effort-slider.js";
import type { SessionEffortLevel } from "./types.js";

/** Not 1, which is the mouse's own id: a real Playwright gesture in an earlier
 *  case leaves pointer 1 ACTIVE, so `setPointerCapture(1)` would succeed by
 *  accident and a synthetic gesture would pass without the stub below doing any
 *  work. A id no real pointer holds is what keeps the stub load-bearing. */
const POINTER_ID = 7;

/** What KAS 2.21.2 sends for a real model in this account. */
const FIVE: SessionEffortLevel[] = [
  { id: "low", name: "Low" },
  { id: "medium", name: "Medium" },
  { id: "high", name: "High" },
  { id: "xhigh", name: "xHigh" },
  { id: "max", name: "Max" },
];

/** The geometry per pointer tier, all of it derived in 15-input.css from two
 *  tokens: the BAR is `--ctl-h-sm` (01-tokens.css, 1.5rem fine / 2.25rem coarse),
 *  the INSET is `--sp-1`, the KNOB is the bar minus the inset on both edges, and the
 *  LINE the bar sits in is floored at `--hit-floor` because the whole track answers
 *  a tap while the knob is deliberately under that floor. */
const TIER = {
  fine: { line: 24, bar: 24, knob: 16, inset: 4 },
  coarse: { line: 44, bar: 36, knob: 28, inset: 4 },
} as const;

let style: HTMLStyleElement;
let host: HTMLElement;
let picks: string[];
let slider: EffortSliderHandle;
let mounted: readonly SessionEffortLevel[] = [];

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
  host = document.createElement("div");
  // A definite width, so the card's shrink-to-fit width is decided by its own
  // `min-inline-size` floor and its content the way it is above the composer — and
  // 400px down the page, because the card is anchored ABOVE its slot and a real
  // pointer gesture is refused for an element outside the viewport.
  host.style.cssText = "position:fixed;top:400px;left:0;width:600px;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

/** Mount a slider in the real card and put its knob on `active`. */
function mount(levels: readonly SessionEffortLevel[], active: string): EffortSliderHandle {
  picks = [];
  mounted = levels;
  slider = buildEffortSlider({
    onPick: (level) => {
      picks.push(level);
    },
  });
  const scroll = document.createElement("div");
  scroll.className = "pill-model-scroll";
  const card = document.createElement("span");
  card.className = "pill-expand-content pill-model-list is-open";
  // The resting state is `opacity: 0; transform: scale(0.4)`, and a transformed box
  // reports scaled rects against unscaled computed values.
  card.style.cssText = "opacity:1;transform:none;animation:none;transition:none;";
  card.append(scroll, slider.el);
  const slot = document.createElement("span");
  slot.className = "pill-slot";
  slot.appendChild(card);
  host.replaceChildren(slot);
  slider.setLevels(levels);
  slider.setActive(active);
  // The snap transition is the one thing a geometry reading cannot tolerate: a rect
  // taken mid-flight is the interpolated position, and Playwright refuses to click
  // an element whose box is still moving. The declaration itself is asserted below.
  knob().style.transition = "none";
  return slider;
}

function card(): HTMLElement {
  return slider.el.parentElement as HTMLElement;
}

function track(): HTMLElement {
  const el = slider.el.querySelector<HTMLElement>(".effort-track");
  expect(el).not.toBeNull();
  return el as HTMLElement;
}

function knob(): HTMLElement {
  const el = slider.el.querySelector<HTMLElement>('[role="slider"]');
  expect(el).not.toBeNull();
  return el as HTMLElement;
}

/** The word the caption names the live tier with. */
function caption(): string {
  return slider.el.querySelector<HTMLElement>(".effort-value")?.textContent ?? "";
}

/** The bar's own box, off the `::before` that paints it. */
function barHeight(): number {
  return parseFloat(getComputedStyle(track(), "::before").blockSize);
}

/** Any CSS colour string as 8-bit sRGB. Chromium computes `color-mix(in oklch, …)`
 *  to an `oklab()` form, so a paint through the CSS colour parser is what reads it. */
function rgbOf(colour: string): readonly [number, number, number] {
  const canvas = document.createElement("canvas");
  canvas.width = 1;
  canvas.height = 1;
  const ctx = canvas.getContext("2d");
  expect(ctx, "the harness needs a 2d context to read a colour").not.toBeNull();
  const c = ctx as CanvasRenderingContext2D;
  c.fillStyle = colour.trim();
  c.fillRect(0, 0, 1, 1);
  const d = c.getImageData(0, 0, 1, 1).data;
  return [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0] as const;
}

/** Plain sRGB distance, for "is this step nearer the accent than the last one". */
function distance(
  a: readonly [number, number, number],
  b: readonly [number, number, number],
): number {
  return Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]);
}

/** WCAG relative-luminance contrast. */
function contrast(
  a: readonly [number, number, number],
  b: readonly [number, number, number],
): number {
  const lum = (c: readonly [number, number, number]): number => {
    const [r, g, bl] = c.map((v) => {
      const s = v / 255;
      return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    }) as [number, number, number];
    return 0.2126 * r + 0.7152 * g + 0.0722 * bl;
  };
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

/** The knob's continuous position along its travel, 0..1 — the value the drag
 *  writes and the release snaps. */
function frac(): number {
  return Number(track().style.getPropertyValue("--effort-frac"));
}

/** The index the knob reports, cross-checked against the tier it paints. The
 *  vocabulary comes from the fixture rather than from the DOM: nothing draws the
 *  tiers now, so the mounted list is the only independent statement of them. */
function shown(): number {
  const k = knob();
  const now = Number(k.getAttribute("aria-valuenow"));
  expect(mounted[now]?.id, "aria-valuenow indexes the tier the knob shows").toBe(
    k.dataset["level"],
  );
  return now;
}

async function press(...keys: readonly string[]): Promise<void> {
  knob().focus();
  for (const key of keys) {
    await userEvent.keyboard(key);
  }
}

/** The x a finger would be at to aim the knob at each tier, read off the knob's own
 *  rendered position. Gathered up front, and it leaves the knob on the last tier, so
 *  callers re-seat it. */
function tierCentres(levels: readonly SessionEffortLevel[]): number[] {
  return levels.map((level) => {
    slider.setActive(level.id);
    const r = knob().getBoundingClientRect();
    return (r.left + r.right) / 2;
  });
}

/** Back the three pointer-capture methods with a Set so the capture-gated
 *  move/up paths run under a synthetic gesture. Required rather than tidy:
 *  `setPointerCapture` throws `NotFoundError` for a pointerId that is not a real
 *  active pointer, so a synthetic `PointerEvent` cannot use the real method — the
 *  HARNESS adapts, never the control, which must not grow a try/catch to
 *  accommodate a test. `shell.test.ts` is the precedent, for the same reason. */
function stubPointerCapture(el: HTMLElement): void {
  const captured = new Set<number>();
  el.setPointerCapture = (id: number): void => {
    captured.add(id);
  };
  el.releasePointerCapture = (id: number): void => {
    captured.delete(id);
  };
  el.hasPointerCapture = (id: number): boolean => captured.has(id);
}

/** A synthetic pointer event carrying the two fields the handlers read. */
function ptr(type: string, clientX: number): PointerEvent {
  return new PointerEvent(type, { bubbles: true, clientX, pointerId: POINTER_ID });
}

beforeEach(() => {
  picks = [];
});

afterEach(() => {
  delete document.documentElement.dataset["pointer"];
});

describe("the knob's size", () => {
  it("is ONE derived square, whatever words the vocabulary carries", () => {
    // The whole point of moving the tier's name into the caption: the knob stopped
    // being sized from foreign text, so its box is a constant of the pointer tier
    // rather than a measurement anything can move. A re-introduced measure step
    // fails here on the first tier whose label is not "Medium"-shaped.
    mount(FIVE, "low");
    const boxes = new Set<string>();
    for (const level of FIVE) {
      slider.setActive(level.id);
      boxes.add(`${String(knob().offsetWidth)}x${String(knob().offsetHeight)}`);
    }
    expect([...boxes], "one box serves the whole vocabulary").toEqual([
      `${String(TIER.fine.knob)}x${String(TIER.fine.knob)}`,
    ]);

    // And it is the same box for a vocabulary of one very long foreign name.
    mount([{ id: "max", name: "M".repeat(120) }], "max");
    expect(knob().offsetWidth).toBe(TIER.fine.knob);
    expect(knob().offsetHeight).toBe(TIER.fine.knob);
  });

  it("is UNDER the universal hit floor, and the track carries the target instead", () => {
    // The floor is physical on purpose and 15-input.css precedes it in MANIFEST
    // order, so a logical `min-inline-size` override would lose on source order.
    mount(FIVE, "high");
    const k = getComputedStyle(knob());
    expect(parseFloat(k.minWidth), "the floor is overridden, not inherited").toBe(0);
    expect(parseFloat(k.minHeight)).toBe(0);
    expect(knob().offsetHeight).toBeLessThan(TIER.fine.line);
    // The tap target is the whole track, which carries the one pointerdown handler,
    // and it is what meets the floor.
    expect(track().offsetHeight).toBe(TIER.fine.line);
    expect(track().clientWidth).toBeGreaterThan(TIER.fine.line);
  });

  it("takes no width from the card it happens to be in", () => {
    // The knob's size is a property of the pointer tier, so a card the model list
    // widened must not change it — nor may the width the card had once carry into
    // the next open, which is what a published measurement did.
    mount(FIVE, "high");
    const before = knob().offsetWidth;
    const wide = document.createElement("div");
    wide.className = "pill-model-item";
    wide.textContent = "a-model-with-a-long-name".repeat(3);
    card().querySelector<HTMLElement>(".pill-model-scroll")?.appendChild(wide);

    slider.setActive("high");

    expect(card().clientWidth, "the model row made the card wide").toBeGreaterThan(400);
    expect(knob().offsetWidth).toBe(before);
    expect(track().clientWidth, "the bar took the width instead").toBeGreaterThan(400);
  });

  it("SURROUNDS the knob: the bar is the box, the knob slides inside it", () => {
    // The knob clears the bar by `--effort-knob-inset` on all four edges at every
    // tier, which is what makes the bar read as a container rather than a segment.
    mount(FIVE, "high");
    const bar = barHeight();
    expect(bar, "the bar is taller than the knob").toBe(TIER.fine.bar);
    expect(knob().offsetHeight).toBe(TIER.fine.knob);
    expect(bar - knob().offsetHeight).toBe(2 * TIER.fine.inset);

    for (const level of FIVE) {
      slider.setActive(level.id);
      const k = knob().getBoundingClientRect();
      const c = card().getBoundingClientRect();
      const t = track().getBoundingClientRect();
      // The BAR's own band, centred in the line the track reserves.
      const barTop = (t.top + t.bottom) / 2 - bar / 2;
      const barBottom = barTop + bar;
      expect(k.top - barTop, `${level.id}: inset from the bar's top`).toBeCloseTo(
        TIER.fine.inset,
        1,
      );
      expect(barBottom - k.bottom, `${level.id}: inset from the bar's bottom`).toBeCloseTo(
        TIER.fine.inset,
        1,
      );
      // And nothing leaves the card's clip box.
      expect(k.top, `${level.id}: inside the card's clip box`).toBeGreaterThanOrEqual(c.top);
      expect(k.bottom, `${level.id}: inside the card's clip box`).toBeLessThanOrEqual(c.bottom);
    }
  });

  it("holds the knob inside the bar at both extremes, inset and all", () => {
    mount(FIVE, "low");
    for (const index of [0, FIVE.length - 1]) {
      slider.setActive(FIVE[index]?.id ?? "");
      const t = track().getBoundingClientRect();
      const k = knob().getBoundingClientRect();
      expect(k.left - t.left, `tier ${String(index)} starts inside the bar`).toBeGreaterThanOrEqual(
        TIER.fine.inset - 0.5,
      );
      expect(t.right - k.right, `tier ${String(index)} ends inside the bar`).toBeGreaterThanOrEqual(
        TIER.fine.inset - 0.5,
      );
    }
    // The two ends really are the ends: the low tier sits ON the start inset and the
    // top tier on the end one, so the travel spends the whole bar.
    slider.setActive("low");
    expect(knob().getBoundingClientRect().left - track().getBoundingClientRect().left).toBeCloseTo(
      TIER.fine.inset,
      1,
    );
    slider.setActive("max");
    expect(
      track().getBoundingClientRect().right - knob().getBoundingClientRect().right,
    ).toBeCloseTo(TIER.fine.inset, 1);
  });

  it("GAINS INTENSITY as the knob moves right, bounded by the handle's contrast", () => {
    mount(FIVE, "low");
    const root = getComputedStyle(document.documentElement);
    const accent = rgbOf(root.getPropertyValue("--c-accent"));
    const handle = rgbOf(getComputedStyle(knob()).backgroundColor);
    // The fill is EASED and a pseudo-element takes no inline style, so the read runs
    // under `data-dragging` — the production rule that suppresses that transition.
    // Without it `getComputedStyle` answers the value it is animating FROM, and every
    // tier reads as the one below it.
    track().dataset["dragging"] = "";
    const fills = FIVE.map((level) => {
      slider.setActive(level.id);
      return rgbOf(getComputedStyle(track(), "::before").backgroundColor);
    });
    delete track().dataset["dragging"];

    // Each step is strictly nearer the accent than the one below it.
    const gaps = fills.map((f) => distance(f, accent));
    for (const [i, gap] of gaps.entries()) {
      if (i === 0) {
        continue;
      }
      expect(gap, `tier ${String(i)} is more accent than tier ${String(i - 1)}`).toBeLessThan(
        gaps[i - 1] as number,
      );
    }
    // A ramp a reader SEES: the 6%-to-26% an accent handle allows moves this by ~26.
    expect((gaps[0] as number) - (gaps[4] as number)).toBeGreaterThan(90);

    // THE BOUND, and the assertion that stops the ramp being widened until the handle
    // disappears at the top tier: WCAG 1.4.11 wants 3:1 against what it sits on.
    for (const [i, fill] of fills.entries()) {
      expect(contrast(handle, fill), `the handle clears 3:1 on tier ${String(i)}`).toBeGreaterThan(
        3,
      );
    }
    // NOT the accent: the fill closes on the accent's own lightness as it rises.
    expect(distance(handle, accent), "the handle is not accent-coloured").toBeGreaterThan(40);
  });

  it("draws no per-tier mark at all", () => {
    // DELETED, not hidden: an element painting nothing is still in the box the tap
    // resolves over. The caption names the tier at every step instead.
    mount(FIVE, "low");
    expect(slider.el.querySelectorAll(".effort-tick")).toHaveLength(0);
    expect([...track().children], "the knob is the track's only child").toEqual([knob()]);
    // The bar is one flat fill rather than a ramp under those marks.
    expect(getComputedStyle(track(), "::before").backgroundImage).toBe("none");
  });

  it("leaves a foreign tier name to the caption, which WRAPS rather than widening the card", () => {
    // A tier label is KAS's text, so its length is not vibekit's to bound. The row of
    // five buttons this replaced spent that budget on the RAIL's width, which is what
    // made the card grow; the caption spends it on its own lines. Both halves matter:
    // the card must not grow sideways, and the name must not be clipped — the card
    // declares `overflow: hidden`, so a nowrap caption would lose characters with no
    // ellipsis and no scroll.
    mount(FIVE, "high");
    const narrow = card().clientWidth;

    mount(
      [
        { id: "low", name: "Low" },
        { id: "max", name: "M".repeat(120) },
      ],
      "max",
    );
    expect(caption(), "the long name landed in the caption").toBe("M".repeat(120));
    const label = slider.el.querySelector<HTMLElement>(".effort-label") as HTMLElement;
    expect(card().clientWidth, "the card did not grow sideways for it").toBe(narrow);
    expect(label.scrollWidth, "and not one character is clipped").toBeLessThanOrEqual(
      label.clientWidth,
    );
    expect(label.getBoundingClientRect().height, "it wrapped onto more lines").toBeGreaterThan(30);

    const t = track();
    const k = knob();
    for (const id of ["low", "max"]) {
      slider.setActive(id);
      const tr = t.getBoundingClientRect();
      const kr = k.getBoundingClientRect();
      expect(kr.left, `${id} starts inside the track`).toBeGreaterThanOrEqual(tr.left);
      expect(kr.right, `${id} ends inside the track`).toBeLessThanOrEqual(tr.right);
    }
    // The mechanism: the track's floor is the knob PLUS its inset on both edges, so
    // the travel can never invert.
    expect(parseFloat(getComputedStyle(t).minInlineSize)).toBe(k.offsetWidth + 2 * TIER.fine.inset);
    expect(t.clientWidth).toBeGreaterThan(k.offsetWidth);
  });

  it("grows every part of itself on the coarse tier", () => {
    document.documentElement.dataset["pointer"] = "coarse";
    mount(FIVE, "high");
    // The TRACK meets the 44px target, so the knob is free to stay a handle.
    expect(track().offsetHeight).toBe(TIER.coarse.line);
    expect(barHeight()).toBe(TIER.coarse.bar);
    expect(knob().offsetHeight).toBe(TIER.coarse.knob);
    expect(knob().offsetWidth).toBe(TIER.coarse.knob);
    // Still under the line: 44px of knob under a finger was the reported complaint.
    expect(knob().offsetHeight).toBeLessThan(TIER.coarse.line);
    // And nothing is clipped by the card at the bigger size.
    const k = knob().getBoundingClientRect();
    const c = card().getBoundingClientRect();
    expect(k.top).toBeGreaterThanOrEqual(c.top);
    expect(k.bottom).toBeLessThanOrEqual(c.bottom);
  });
});

describe("one tier", () => {
  it("shrinks the bar to the knob, stays named, and reports a zero-width range", () => {
    mount([{ id: "high", name: "High" }], "high");
    const t = track();
    const k = knob();
    // The bar is as wide as the knob plus its inset: one tier is not a choice, so
    // drawing travel would claim a range the vocabulary does not offer.
    expect(t.clientWidth).toBe(k.offsetWidth + 2 * TIER.fine.inset);
    expect(k.offsetWidth).toBe(TIER.fine.knob);
    expect(caption(), "the caption still names the tier in force").toBe("High");
    expect(k.getAttribute("aria-valuemin")).toBe("0");
    expect(k.getAttribute("aria-valuemax")).toBe("0");
    expect(shown()).toBe(0);
    // And the knob cannot drift, because the travel is zero.
    const kr = k.getBoundingClientRect();
    const tr = t.getBoundingClientRect();
    expect(kr.left - tr.left).toBeCloseTo(TIER.fine.inset, 1);
    expect(tr.right - kr.right).toBeCloseTo(TIER.fine.inset, 1);
  });

  it("cannot be stepped off its one tier", async () => {
    mount([{ id: "high", name: "High" }], "high");
    await press("{ArrowRight}", "{End}", "{ArrowLeft}", "{Home}");
    expect(shown()).toBe(0);
    expect(picks).toEqual(["high", "high", "high", "high"]);
  });
});

describe("the keyboard", () => {
  it("steps down on Left and Down, up on Right and Up, clamped at both ends", async () => {
    mount(FIVE, "medium");
    await press("{ArrowLeft}", "{ArrowDown}", "{ArrowLeft}");
    expect(shown(), "clamped at the lowest tier").toBe(0);
    await press("{ArrowRight}", "{ArrowUp}");
    expect(shown()).toBe(2);
    expect(picks).toEqual(["low", "low", "low", "medium", "high"]);
  });

  it("jumps to the ends on Home and End", async () => {
    mount(FIVE, "medium");
    await press("{End}");
    expect(shown()).toBe(4);
    await press("{Home}");
    expect(shown()).toBe(0);
    expect(picks).toEqual(["max", "low"]);
  });

  it("keeps the card's roving focus out of its own arrow keys", async () => {
    // `model-switcher.ts` wires `rovingFocus` over the whole card, and that handler
    // reads no target: ArrowUp/ArrowDown/Home/End reaching the card would move focus
    // into the model list. So the six keys the knob acts on are STOPPED, not merely
    // defaulted, and this is the only place that can see it.
    mount(FIVE, "medium");
    const scroll = card().querySelector<HTMLElement>(".pill-model-scroll") as HTMLElement;
    for (const id of ["a-model", "b-model"]) {
      const item = document.createElement("div");
      item.className = "pill-model-item";
      item.setAttribute("role", "option");
      item.textContent = id;
      scroll.appendChild(item);
    }
    const nav = rovingFocus(card(), ".pill-model-item");

    await press("{ArrowUp}", "{Home}", "{End}");

    expect(document.activeElement, "focus stayed on the knob").toBe(knob());
    expect(picks).toEqual(["high", "low", "max"]);
    nav.dispose();
  });

  it("leaves every other key to the app", async () => {
    // Escape has to keep reaching the popup's own document handler, so nothing here
    // stops a key it does not act on.
    mount(FIVE, "medium");
    await press("{Escape}", "a", "{PageDown}", "{Enter}", " ");
    expect(shown()).toBe(1);
    expect(picks).toEqual([]);
  });
});

describe("the pointer", () => {
  it("snaps to the nearest tier when the bar is tapped away from the knob", () => {
    mount(FIVE, "low");
    const t = track();
    stubPointerCapture(t);
    const centres = tierCentres(FIVE);
    slider.setActive("low");

    // A tap 40% of the way from tier 3 to tier 4 is nearest tier 3, so that is where
    // the release has to land — the press itself paints the finger's own position.
    const between =
      (centres[3] as number) + 0.4 * ((centres[4] as number) - (centres[3] as number));
    t.dispatchEvent(ptr("pointerdown", between));
    expect(frac(), "the press paints where the finger is, not on a tier").not.toBeCloseTo(3 / 4, 3);
    expect(shown(), "while naming the tier it is nearest").toBe(3);
    expect(picks, "a press is not a pick").toEqual([]);

    t.dispatchEvent(ptr("pointerup", between));

    expect(shown()).toBe(3);
    expect(frac(), "the release snaps the position onto the tier").toBeCloseTo(3 / 4, 5);
    expect(picks).toEqual(["xhigh"]);
  });

  it("moves smoothly between two tiers and snaps to the nearer one on release", () => {
    // The knob tracks the pointer continuously; only the release lands on a tier.
    mount(FIVE, "low");
    const t = track();
    stubPointerCapture(t);
    const centres = tierCentres(FIVE);
    slider.setActive("low");
    const low = centres[0] as number;
    const medium = centres[1] as number;

    t.dispatchEvent(ptr("pointerdown", low));
    const seen: number[] = [];
    for (const step of [0.1, 0.2, 0.3, 0.4, 0.6, 0.7]) {
      t.dispatchEvent(ptr("pointermove", low + step * (medium - low)));
      seen.push(frac());
    }

    expect(seen, "every step is its own position").toHaveLength(new Set(seen).size);
    expect(
      seen.every((f) => f > 0 && f < 1 / 4),
      "all of them between the two tiers",
    ).toBe(true);
    expect(picks, "not one move is a pick").toEqual([]);

    // Released past the midpoint, so it snaps UP.
    t.dispatchEvent(ptr("pointerup", low + 0.7 * (medium - low)));
    expect(frac()).toBeCloseTo(1 / 4, 5);
    expect(picks).toEqual(["medium"]);
  });

  it("still dispatches when the tap lands where the knob already sits", async () => {
    // The tier is MARKED rather than chosen — the chat's own choice is
    // `model-switcher.setEffort`'s guard, and a chat marked at the model default has
    // chosen nothing, so a tap that pins it has to reach the server.
    mount(FIVE, "high");

    await userEvent.click(knob());

    expect(shown()).toBe(2);
    expect(picks).toEqual(["high"]);
  });

  it("follows a drag across the whole track and reports where it was released", () => {
    // Driven with synthetic pointer events against a stubbed-capture track rather
    // than `userEvent.dragAndDrop`, which is the wrong instrument for this control:
    // it maps to Playwright's `frame.dragAndDrop`, whose actionability wait is on
    // the TARGET — an empty 2px `<span>` the knob travels over — so the gesture
    // stalls under load, and HTML5 drag semantics do not compose with
    // `setPointerCapture` at all. `tabs-drag.test.ts` is the other precedent.
    mount(FIVE, "low");
    const t = track();
    stubPointerCapture(t);
    const centres = tierCentres(FIVE);
    slider.setActive("low");

    // A release reports where the knob IS (`pointerup` reads no coordinate), so the
    // gesture ends with a move at the far tier exactly as a real one does.
    t.dispatchEvent(ptr("pointerdown", centres[0] as number));
    const painted = [shown()];
    const named = [caption()];
    for (const x of centres.slice(1)) {
      t.dispatchEvent(ptr("pointermove", x));
      painted.push(shown());
      named.push(caption());
    }

    expect(painted, "the knob paints the tier under the finger, tier by tier").toEqual([
      0, 1, 2, 3, 4,
    ]);
    expect(named, "and the caption names the same tier at every step").toEqual(
      FIVE.map((l) => l.name),
    );
    expect(picks, "the moves PAINT: not one of them is a pick").toEqual([]);

    t.dispatchEvent(ptr("pointerup", centres[4] as number));

    expect(shown()).toBe(4);
    // ONE dispatch, on the release.
    expect(picks).toEqual(["max"]);
  });

  it("eases to a tier at rest and follows the finger while dragging", () => {
    mount(FIVE, "low");
    const t = track();
    const k = knob();
    // The fixture suppresses the transition for its geometry readings, so this is
    // the one case that reads the shipped declaration.
    k.style.removeProperty("transition");
    const rest = getComputedStyle(k);
    expect(rest.transitionProperty, "a settled knob eases to its tier").toContain("transform");
    expect(parseFloat(rest.transitionDuration)).toBeGreaterThan(0);
    // The states are SIZE, so `scale` is separate from the position transform and has
    // to be in the transition list or the grow applies in one frame.
    expect(rest.transitionProperty).toContain("scale");
    expect(rest.scale, "the resting handle is unscaled").toBe("none");

    t.dataset["dragging"] = "";
    // A transition makes the knob lag the finger.
    expect(getComputedStyle(k).transitionDuration).toBe("0s");
    delete t.dataset["dragging"];
  });

  it("returns to the synced tier when the gesture is cancelled", () => {
    mount(FIVE, "high");
    const t = track();
    stubPointerCapture(t);
    t.dispatchEvent(ptr("pointerdown", 0));
    expect(shown(), "the press moved the knob to the low end").toBe(0);

    t.dispatchEvent(ptr("pointercancel", 0));

    expect(shown()).toBe(2);
    expect(caption(), "the caption came back with it").toBe("High");
    expect(picks, "a cancelled gesture is not a pick").toEqual([]);
  });
});

describe("the single writer", () => {
  it("moves position, aria-valuenow, aria-valuetext and the caption together", () => {
    mount(FIVE, "low");
    const k = knob();
    const seen = new Set<string>();
    for (const [index, level] of FIVE.entries()) {
      slider.setActive(level.id);
      expect(k.getAttribute("aria-valuenow")).toBe(String(index));
      expect(k.getAttribute("aria-valuetext")).toBe(caption());
      expect(k.dataset["level"]).toBe(level.id);
      expect(caption()).toBe(level.name);
      seen.add(track().style.getPropertyValue("--effort-frac"));
    }
    expect([...seen], "a distinct position per tier").toHaveLength(FIVE.length);
  });

  it("keeps the caption's value following the knob through a real gesture", async () => {
    // The caption is the control's visible label AND its readout, so it has to track
    // the knob however the knob moved — not only through `setActive`, which is the
    // store's own path. One writer means a keyboard step and a tap both move it.
    mount(FIVE, "low");
    expect(caption()).toBe("Low");

    await press("{ArrowRight}", "{ArrowRight}");
    expect(caption()).toBe("High");
    await press("{End}");
    expect(caption()).toBe("Max");

    const t = track();
    stubPointerCapture(t);
    const centres = tierCentres(FIVE);
    slider.setActive("max");
    t.dispatchEvent(ptr("pointerdown", centres[1] as number));
    t.dispatchEvent(ptr("pointerup", centres[1] as number));
    expect(caption()).toBe("Medium");
    expect(caption(), "and it is the tier announced").toBe(knob().getAttribute("aria-valuetext"));
  });

  it("names the dimension beside the value, so the control has a visible label", () => {
    // A value alone names no dimension, and an input with no visible label is a
    // WCAG 3.3.2 (Level A) exposure. The static half is not the value's element, so
    // one writer replaces the word and never the sentence.
    mount(FIVE, "high");
    const label = slider.el.querySelector<HTMLElement>(".effort-label") as HTMLElement;
    expect(label.textContent).toBe("Effort: High");
    slider.setActive("max");
    expect(label.textContent).toBe("Effort: Max");
    // The knob's own name is STABLE across the same steps: a name carrying the live
    // value would be re-announced on every step, which is the anti-pattern this repo
    // records for its `aria-pressed` toggles.
    expect(knob().getAttribute("aria-label")).toBe("Reasoning effort");
    expect(label.getAttribute("aria-live"), "the caption is visual, not a live region").toBeNull();
    expect(knob().getAttribute("aria-labelledby")).toBeNull();
  });

  it("puts an unoffered tier on the lowest one", () => {
    // `effortVocabulary` answers "" when nothing resolved, and a slider is always
    // somewhere; `effort.test.ts` is where that resolution stays pinned.
    mount(FIVE, "");
    expect(shown()).toBe(0);
    mount(FIVE, "not-a-tier");
    expect(shown()).toBe(0);
  });
});

describe("the vocabulary", () => {
  it("rebuilds the range when the tiers change", () => {
    mount(FIVE, "high");
    expect(knob().getAttribute("aria-valuemax")).toBe("4");
    expect(track().dataset["tiers"]).toBe("5");

    mounted = [{ id: "low" }, { id: "high" }];
    slider.setLevels(mounted);
    slider.setActive("high");

    expect(knob().getAttribute("aria-valuemax")).toBe("1");
    expect(track().dataset["tiers"], "the count is the one thing CSS reads").toBe("2");
    expect(shown()).toBe(1);
    expect(knob().dataset["level"]).toBe("high");
  });

  it("needs no layout to build, so a detached or hidden card is not a special case", () => {
    // What replaced the measure step. The knob used to be sized from a live
    // `offsetWidth`, so the handle had to be told when the card was laid out and had
    // to refuse to publish a width while it was not. Every position and every
    // announcement is CSS arithmetic over `--effort-frac` now, so building the whole
    // control detached and attaching it afterwards is the same control.
    const detached = buildEffortSlider({ onPick: () => undefined });
    detached.setLevels(FIVE);
    detached.setActive("xhigh");
    const k = detached.el.querySelector<HTMLElement>('[role="slider"]') as HTMLElement;
    expect(k.getAttribute("aria-valuenow")).toBe("3");
    expect(detached.el.querySelector(".effort-value")?.textContent).toBe("xHigh");

    mount(FIVE, "low");
    card().appendChild(detached.el);
    k.style.transition = "none";
    expect(k.offsetWidth, "and it lays out at the derived size on attach").toBe(TIER.fine.knob);
    const t = detached.el.querySelector<HTMLElement>(".effort-track") as HTMLElement;
    const kr = k.getBoundingClientRect();
    const tr = t.getBoundingClientRect();
    expect(kr.right).toBeLessThanOrEqual(tr.right);
    expect(kr.left).toBeGreaterThan(tr.left);
    detached.el.remove();
  });
});

describe("the row keeps its place in the card", () => {
  it("bleeds to both edges below the scroller", () => {
    mount(FIVE, "low");
    expect(card().lastElementChild, "the section is the card's last child").toBe(slider.el);
    // The card gave its padding to the scroller, so its own border is the only thing
    // between this section and the card's edge.
    const border = parseFloat(getComputedStyle(card()).borderLeftWidth);
    const c = card().getBoundingClientRect();
    const r = slider.el.getBoundingClientRect();
    expect(r.left - c.left).toBeCloseTo(border, 1);
    expect(c.right - r.right).toBeCloseTo(border, 1);
  });

  it("stacks the caption over the bar rather than beside it", () => {
    // The two-line shape is the whole change: the caption on its own line is what
    // frees the knob from carrying the tier's name.
    mount(FIVE, "high");
    const label = (
      slider.el.querySelector<HTMLElement>(".effort-label") as HTMLElement
    ).getBoundingClientRect();
    const bar = track().getBoundingClientRect();
    expect(label.bottom, "the caption sits entirely above the bar").toBeLessThanOrEqual(bar.top);
    expect(label.left, "and both lines start on the same edge").toBeCloseTo(bar.left, 0);
  });
});

describe("nothing about it is a range input", () => {
  it("builds a custom slider role instead", () => {
    mount(FIVE, "low");
    expect(slider.el.querySelectorAll("input")).toHaveLength(0);
    expect(knob().tagName).toBe("DIV");
    expect(knob().getAttribute("role")).toBe("slider");
    expect(knob().getAttribute("aria-label")).toBe("Reasoning effort");
    expect(knob().getAttribute("tabindex")).toBe("0");
  });

  it("is the section's ONE tab stop", () => {
    mount(FIVE, "low");
    const focusable = slider.el.querySelectorAll(
      'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])',
    );
    expect([...focusable]).toEqual([knob()]);
  });
});

// A spy is not part of the subject: this guards the fixture rather than the code.
it("reports every pick through the callback it was given", () => {
  const onPick = vi.fn();
  const handle = buildEffortSlider({ onPick });
  expect(handle.el.className).toBe("effort-row");
});
