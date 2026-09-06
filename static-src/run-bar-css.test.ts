// The run bar's geometry and its motion, measured against the real assembled
// cascade rather than reasoned about.
//
// Three claims a source read cannot make. THE MEASURE: the bar has to end on the
// same two edges as the prompt box and the dock, or the band reads as three
// different columns — and that alignment comes from four separate declarations
// agreeing, which is exactly the kind of thing that drifts. THE MECHANICAL
// PROPERTY: the bar grows the band upward and shrinks the transcript by exactly its
// own height, covering nothing (26-dock.css states it for the dock; this is the
// third region to rely on it). THE MOTION: `running` spins and `waiting` is the
// same ring standing still, which is the app's in-flight axis, and `getAnimations()`
// is the only honest reader of it.
//
// The HIT FLOOR is measured here too, because the row deliberately declares no
// `min-height` of its own: the zero-specificity floor in 61-mcp-tools.css is what
// gives it one, and a regression in that rule's coverage would otherwise only show
// up on a phone. Measuring the rendered HEIGHT against 24px is the version of that
// case which cannot fail — the row's own box is 27px, above the fine tier's 1.5rem
// floor — so what is asserted is the RESOLVED `min-height` against the token, plus
// the coarse tier, where 2.75rem is genuinely the thing deciding the target.

import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** A token's rendered length, read off a real element rather than restated. */
function lengthPx(root: HTMLElement, expr: string): number {
  const probe = document.createElement("div");
  probe.style.inlineSize = expr;
  root.appendChild(probe);
  const px = probe.getBoundingClientRect().width;
  probe.remove();
  return px;
}

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

/** The composer band as `static/index.html` builds it, with the transcript above it
 *  so the shrink is measurable. `#chat-view` is the flex column both live in. */
function mountBand(states: readonly string[]): {
  bar: HTMLUListElement;
  wrap: HTMLElement;
  box: HTMLElement;
  dock: HTMLElement;
  rows: HTMLElement[];
} {
  host = document.createElement("div");
  host.id = "chat-view";
  host.className = "view";
  // A definite height, or the flex column has nothing to divide between the
  // transcript and the bar and the shrink cannot be observed.
  host.style.blockSize = "600px";
  host.style.inlineSize = "1000px";
  host.style.display = "flex";
  host.style.flexDirection = "column";

  // The real nesting: `#messages-wrap-outer` is the FLEX CHILD that the band's
  // height is taken out of, and `#messages-wrap` is `position: absolute; inset: 0`
  // inside it, so the scroller follows the wrapper exactly. Measuring the scroller
  // measures the shrink.
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  const wrap = document.createElement("div");
  wrap.id = "messages-wrap";
  outer.appendChild(wrap);

  const form = document.createElement("form");
  form.id = "prompt-form";
  form.className = "bottom-bar";

  const dock = document.createElement("div");
  dock.id = "decision-dock";
  dock.className = "decision-dock hidden";

  const bar = document.createElement("ul");
  bar.id = "run-bar";
  bar.className = "run-bar";

  const rows = states.map((state) => {
    const li = document.createElement("li");
    li.className = "run-bar-row";
    li.dataset["state"] = state;
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "run-bar-open";
    for (const cls of [
      "run-bar-glyph",
      "run-bar-name",
      "run-bar-state",
      "run-bar-steps",
      "run-bar-clock",
    ]) {
      const span = document.createElement("span");
      span.className = cls;
      span.textContent = cls === "run-bar-name" ? "nightly sweep" : "x";
      btn.appendChild(span);
    }
    li.appendChild(btn);
    bar.appendChild(li);
    return li;
  });

  const box = document.createElement("div");
  box.id = "prompt-box";
  box.className = "prompt-box";
  const ta = document.createElement("textarea");
  ta.id = "prompt-input";
  box.appendChild(ta);

  form.append(dock, bar, box);
  host.append(outer, form);
  document.body.appendChild(host);
  return { bar, wrap, box, dock, rows };
}

afterAll(() => {
  host?.remove();
});

describe("the run bar's geometry", () => {
  it("ends on the same edges as the prompt box and the dock", () => {
    const { bar, box, dock } = mountBand(["running"]);
    dock.classList.remove("hidden");

    const barRect = bar.getBoundingClientRect();
    const boxRect = box.getBoundingClientRect();
    const dockRect = dock.getBoundingClientRect();

    expect(barRect.left).toBeCloseTo(boxRect.left, 1);
    expect(barRect.right).toBeCloseTo(boxRect.right, 1);
    expect(barRect.left).toBeCloseTo(dockRect.left, 1);
    expect(barRect.right).toBeCloseTo(dockRect.right, 1);
    host.remove();
  });

  it("shrinks the transcript by exactly its own height and covers nothing", () => {
    const { bar, wrap } = mountBand(["running", "waiting"]);
    const shown = wrap.getBoundingClientRect();
    const barRect = bar.getBoundingClientRect();
    expect(barRect.height).toBeGreaterThan(0);
    // The MARGIN box is what the band grows by: the region carries the same
    // `margin-block-end` the dock and the steer stack do, which is the gap between
    // them rather than part of the bar.
    const margin = Number.parseFloat(getComputedStyle(bar).marginBlockEnd);
    expect(margin).toBeGreaterThan(0);

    // The `.hidden` utility is `display: none !important`, so the region's whole box
    // leaves the flex column and the transcript takes the space back.
    bar.classList.add("hidden");
    const hidden = wrap.getBoundingClientRect();

    expect(hidden.height - shown.height).toBeCloseTo(barRect.height + margin, 0);
    // And while it is shown, the two boxes do not overlap: the bar is a sibling that
    // grows the band, not an overlay.
    expect(shown.bottom).toBeLessThanOrEqual(barRect.top + 0.5);
    host.remove();
  });

  it("takes its hit target from the shared floor, at whichever tier is in force", () => {
    const { rows } = mountBand(["running"]);
    const btn = rows[0]?.querySelector<HTMLElement>(".run-bar-open") ?? null;
    expect(btn).not.toBeNull();
    if (btn === null) {
      return;
    }

    // The row declares no `min-height`, so it RESOLVES 61-mcp-tools.css's
    // `:where(button, …)` token. Measured with that rule deleted the property reads
    // 0px, while the rendered height still clears 24px off the row's own 27px box —
    // which is why the height is not what this asserts on the fine tier.
    expect(Number.parseFloat(getComputedStyle(btn).minHeight)).toBeCloseTo(
      lengthPx(host, "var(--hit-floor)"),
      1,
    );

    // The coarse tier is where the floor BINDS: 2.75rem is above the row's own box,
    // so it is the thing deciding the rendered target. The tier is the pointer rather
    // than a width (01-tokens.css), so `data-pointer` is how a test reaches it.
    const root = document.documentElement;
    const had = root.getAttribute("data-pointer");
    root.setAttribute("data-pointer", "coarse");
    try {
      const floor = lengthPx(host, "var(--hit-floor)");
      expect(floor, "the coarse tier's floor is above the row's own height").toBeGreaterThan(27);
      expect(btn.getBoundingClientRect().height).toBeCloseTo(floor, 1);
    } finally {
      if (had === null) {
        root.removeAttribute("data-pointer");
      } else {
        root.setAttribute("data-pointer", had);
      }
    }
    host.remove();
  });
});

describe("the run bar's state column", () => {
  it("spins the running ring at the shared period and holds the waiting one still", () => {
    const { rows } = mountBand(["running", "waiting"]);
    const [running, waiting] = rows;
    const glyph = (row: HTMLElement | undefined): Element | null =>
      row?.querySelector(".run-bar-glyph") ?? null;

    const spinning = glyph(running)?.getAnimations({ subtree: true }) ?? [];
    expect(spinning.length, "the running ring animates").toBe(1);
    const anim = spinning[0];
    expect(anim === undefined ? "" : (anim as CSSAnimation).animationName).toBe("vk-spin");
    const timing = anim?.effect?.getComputedTiming();
    expect(timing?.duration).toBe(600);

    expect(glyph(waiting)?.getAnimations({ subtree: true }).length).toBe(0);
    host.remove();
  });

  it("draws both in-flight rings at the dot token's size", () => {
    const { rows } = mountBand(["running", "waiting"]);
    for (const row of rows) {
      const glyph = row.querySelector(".run-bar-glyph");
      expect(glyph).not.toBeNull();
      if (glyph === null) {
        continue;
      }
      const ring = getComputedStyle(glyph, "::before");
      const dot = lengthPx(host, "var(--dot-size)");
      expect(Number.parseFloat(ring.inlineSize)).toBeCloseTo(dot, 1);
      expect(Number.parseFloat(ring.blockSize)).toBeCloseTo(dot, 1);
    }
    host.remove();
  });
});
