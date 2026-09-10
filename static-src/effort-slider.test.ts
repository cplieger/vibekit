// ---------------------------------------------------------------------------
// The reasoning-effort slider's own geometry and gestures, measured against the
// SHIPPED cascade: every claim here is about a real box in a real card, so the
// stylesheet is mounted rather than reasoned about.
//
// The handle is driven directly (`buildEffortSlider` plus a spy `onPick`), which
// is what keeps `model-switcher.ts`'s ten-mock graph out of this file — the state
// and dispatch behaviours live there, in `model-switcher.test.ts`.
//
// The card fixture is the real one: `.pill-slot > .pill-expand-content
// .pill-model-list`, because the section is full-bleed inside a card whose padding
// moved to its scroller, and the track's width is what every tick position and
// every travel figure below is derived from. The card's `overflow: hidden` is the
// other reason it is the real one: the knob stands proud of a thin rail, so the
// card is the box that would clip it.
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

/** `--hit-floor` per pointer tier (01-tokens.css), which is the knob's own size on
 *  both axes now that it carries no text. */
const FLOOR = { fine: 24, coarse: 44 } as const;

let style: HTMLStyleElement;
let host: HTMLElement;
let picks: string[];
let slider: EffortSliderHandle;

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

function ticks(): HTMLElement[] {
  return [...slider.el.querySelectorAll<HTMLElement>(".effort-tick")];
}

/** The word the caption names the live tier with. */
function caption(): string {
  return slider.el.querySelector<HTMLElement>(".effort-value")?.textContent ?? "";
}

/** The rail's own thickness, off the `::before` that paints it. */
function railHeight(): number {
  return parseFloat(getComputedStyle(track(), "::before").blockSize);
}

/** The index the knob reports, cross-checked against the tier it paints. */
function shown(): number {
  const k = knob();
  const list = ticks().map((t) => t.dataset["level"]);
  const now = Number(k.getAttribute("aria-valuenow"));
  expect(list[now], "aria-valuenow indexes the tier the knob shows").toBe(k.dataset["level"]);
  return now;
}

async function press(...keys: readonly string[]): Promise<void> {
  knob().focus();
  for (const key of keys) {
    await userEvent.keyboard(key);
  }
}

/** The x a real finger would be at to aim the knob's centre at this tick. The
 *  ticks are placed by CSS off `--tick-frac`, so they are an oracle INDEPENDENT
 *  of `indexAt`'s own arithmetic — the case above pins that each one marks where
 *  its tier's knob lands. */
function centreX(el: HTMLElement): number {
  const r = el.getBoundingClientRect();
  return (r.left + r.right) / 2;
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
  it("is ONE hit-floor square, whatever words the vocabulary carries", () => {
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
      `${String(FLOOR.fine)}x${String(FLOOR.fine)}`,
    ]);

    // And it is the same box for a vocabulary of one very long foreign name.
    mount([{ id: "max", name: "M".repeat(120) }], "max");
    expect(knob().offsetWidth).toBe(FLOOR.fine);
    expect(knob().offsetHeight).toBe(FLOOR.fine);
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
    expect(track().clientWidth, "the rail took the width instead").toBeGreaterThan(400);
  });

  it("stands proud of the rail without the card clipping it", () => {
    // THE CLIPPING HAZARD. `.pill-model-list` declares `overflow: hidden`, so a rail
    // line sized to the rail would cut the knob off top and bottom. The line reserves
    // the knob's full height instead and the rail is the thin thing inside it.
    mount(FIVE, "high");
    const rail = railHeight();
    expect(rail, "the rail is thin").toBeLessThan(knob().offsetHeight);
    expect(rail).toBeGreaterThan(0);

    for (const level of FIVE) {
      slider.setActive(level.id);
      const k = knob().getBoundingClientRect();
      const c = card().getBoundingClientRect();
      const t = track().getBoundingClientRect();
      expect(k.top, `${level.id}: inside the card's clip box`).toBeGreaterThanOrEqual(c.top);
      expect(k.bottom, `${level.id}: inside the card's clip box`).toBeLessThanOrEqual(c.bottom);
      // The reservation is the mechanism: the line is as tall as the knob, so the
      // knob's own band never leaves it.
      expect(k.top).toBeGreaterThanOrEqual(t.top - 0.5);
      expect(k.bottom).toBeLessThanOrEqual(t.bottom + 0.5);
      // Centred on the rail, so the handle reads as sitting on the track.
      expect(Math.abs((k.top + k.bottom) / 2 - (t.top + t.bottom) / 2)).toBeLessThan(0.5);
    }
  });

  it("holds the knob inside the track at both extremes", () => {
    mount(FIVE, "low");
    for (const index of [0, FIVE.length - 1]) {
      slider.setActive(FIVE[index]?.id ?? "");
      const t = track().getBoundingClientRect();
      const k = knob().getBoundingClientRect();
      expect(k.left, `tier ${String(index)} starts inside the track`).toBeGreaterThanOrEqual(
        t.left,
      );
      expect(k.right, `tier ${String(index)} ends inside the track`).toBeLessThanOrEqual(t.right);
    }
  });

  it("puts every tick on its own tier's knob centre", () => {
    mount(FIVE, "low");
    for (const [index, tick] of ticks().entries()) {
      slider.setActive(FIVE[index]?.id ?? "");
      const k = knob().getBoundingClientRect();
      const t = tick.getBoundingClientRect();
      expect(
        Math.abs((t.left + t.right) / 2 - (k.left + k.right) / 2),
        `tick ${String(index)} marks where its knob lands`,
      ).toBeLessThan(0.5);
    }
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
    // The mechanism: the track's floor is the knob, so the travel can never invert.
    expect(parseFloat(getComputedStyle(t).minInlineSize)).toBe(k.offsetWidth);
    expect(t.clientWidth).toBeGreaterThan(k.offsetWidth);
  });

  it("clears the coarse hit floor", () => {
    document.documentElement.dataset["pointer"] = "coarse";
    mount(FIVE, "high");
    // 44px is Apple HIG's minimum target and WCAG 2.5.5's; the per-tick targets are
    // a fine-pointer bonus, so the KNOB is what has to meet it.
    expect(knob().offsetHeight).toBe(FLOOR.coarse);
    expect(knob().offsetWidth).toBe(FLOOR.coarse);
    // And the taller knob is still reserved for rather than clipped.
    const k = knob().getBoundingClientRect();
    const c = card().getBoundingClientRect();
    expect(k.top).toBeGreaterThanOrEqual(c.top);
    expect(k.bottom).toBeLessThanOrEqual(c.bottom);
  });
});

describe("one tier", () => {
  it("shrinks the rail to the knob, stays named, and reports a zero-width range", () => {
    mount([{ id: "high", name: "High" }], "high");
    const t = track();
    const k = knob();
    // The rail is as wide as the knob: one tier is not a choice, so drawing travel
    // would claim a range the vocabulary does not offer.
    expect(t.clientWidth).toBe(k.offsetWidth);
    expect(k.offsetWidth).toBe(FLOOR.fine);
    expect(caption(), "the caption still names the tier in force").toBe("High");
    expect(k.getAttribute("aria-valuemin")).toBe("0");
    expect(k.getAttribute("aria-valuemax")).toBe("0");
    expect(shown()).toBe(0);
    // And the knob cannot drift off its one tick, because the travel is zero.
    const kr = k.getBoundingClientRect();
    const tr = t.getBoundingClientRect();
    expect(kr.left).toBeCloseTo(tr.left, 1);
    expect(kr.right).toBeCloseTo(tr.right, 1);
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
  it("snaps to the nearest tier when a tick away from the knob is tapped", async () => {
    mount(FIVE, "low");
    const target = ticks()[3];
    expect(target, "the fixture rendered no fourth tick").toBeDefined();

    await userEvent.click(target as HTMLElement);

    expect(shown()).toBe(3);
    expect(picks).toEqual(["xhigh"]);
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
    const marks = ticks();
    expect(marks, "the fixture rendered a tick per tier to aim at").toHaveLength(FIVE.length);

    // A release reports where the knob IS (`pointerup` reads no coordinate), so the
    // gesture ends with a move at the far tick exactly as a real one does.
    t.dispatchEvent(ptr("pointerdown", centreX(marks[0] as HTMLElement)));
    const painted = [shown()];
    const named = [caption()];
    for (const mark of marks.slice(1)) {
      t.dispatchEvent(ptr("pointermove", centreX(mark)));
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

    t.dispatchEvent(ptr("pointerup", centreX(marks[4] as HTMLElement)));

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
      seen.add(k.style.getPropertyValue("--effort-frac"));
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

    await userEvent.click(ticks()[1] as HTMLElement);
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
  it("rebuilds the ticks and the range when the tiers change", () => {
    mount(FIVE, "high");
    expect(ticks()).toHaveLength(5);

    slider.setLevels([{ id: "low" }, { id: "high" }]);
    slider.setActive("high");

    expect(ticks().map((t) => t.dataset["level"])).toEqual(["low", "high"]);
    expect(knob().getAttribute("aria-valuemax")).toBe("1");
    expect(shown()).toBe(1);
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
    expect(k.offsetWidth, "and it lays out at the declared size on attach").toBe(FLOOR.fine);
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

  it("stacks the caption over the rail rather than beside it", () => {
    // The two-line shape is the whole change: the caption on its own line is what
    // frees the knob from carrying the tier's name.
    mount(FIVE, "high");
    const label = (
      slider.el.querySelector<HTMLElement>(".effort-label") as HTMLElement
    ).getBoundingClientRect();
    const rail = track().getBoundingClientRect();
    expect(label.bottom, "the caption sits entirely above the rail").toBeLessThanOrEqual(rail.top);
    expect(label.left, "and both lines start on the same edge").toBeCloseTo(rail.left, 0);
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
